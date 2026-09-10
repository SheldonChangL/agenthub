package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
	"agenthub.local/agenthub/internal/wake"
)

// streamingSurfaces builds a node whose gate can reach a Claude session
// through the same driver the endpoint subscribes to.
func streamingSurfaces(t *testing.T, extra ...Option) (*registry.Registry, http.Handler, http.Handler, *wake.ChannelDriver) {
	t.Helper()
	ctx := context.Background()
	store, err := registry.Open(ctx, t.TempDir()+"/agenthub.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	node := model.NodeIdentity{ID: testNodeID, DisplayName: "test", Platform: "test"}
	heartbeats := protocol.NewHeartbeatBuilder(store, node, apiTestSigner{})
	channels := wake.NewChannelDriver()
	gate := wake.New(store, registry.DefaultWakeLimits(), channels)
	options := append([]Option{WithWaker(gate), WithChannelSubscriber(channels)}, extra...)
	server := NewServer(store, nil, heartbeats, node, options...)
	return store, server.Handler(), server.PeerHandler(), channels
}

// A quiet poll ends without an error, and the subscriber comes back.
func TestAQuietWakeStreamAnswersNoContent(t *testing.T) {
	store, owner, _, _ := streamingSurfaces(t)
	session := acceptingLocalSession(t, store, owner, "claude:listening")

	response := perform(t, owner, http.MethodGet,
		"/v1/sessions/"+session+"/wake-stream?wait=1s", nil)
	if response.Code != http.StatusNoContent {
		t.Fatalf("a quiet poll = %d %s", response.Code, response.Body.String())
	}
}

// A message for a woken session reaches whoever is waiting, with the notice
// the node chose rather than one the subscriber composed.
func TestAWakeReachesTheWaitingSubscriber(t *testing.T) {
	ctx := context.Background()
	store, owner, peers, _ := streamingSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	session := acceptingLocalSession(t, store, owner, "claude:listening")
	if err := store.SetAudience(ctx, session, model.Audience{
		Mode: model.AudienceAllPaired, AcceptMessages: true, AutoWake: true,
	}); err != nil {
		t.Fatal(err)
	}

	// The subscriber is waiting before the message arrives, which is the
	// ordinary case: the agent's MCP server polls continuously.
	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := httptest.NewRequest(http.MethodGet,
			"/v1/sessions/"+session+"/wake-stream?wait=20s", nil)
		recorder := httptest.NewRecorder()
		owner.ServeHTTP(recorder, request)
		answered <- recorder
	}()
	// Give the poll time to register before the message lands.
	waitFor(t, func() bool {
		response := perform(t, owner, http.MethodGet, "/v1/wakes", nil)
		return response.Code == http.StatusOK
	})
	time.Sleep(100 * time.Millisecond)

	envelope := peer.messageEnvelope(t, testNodeID, "msg_channel", session, "claude:theirs",
		"look at the build")
	if response := perform(t, peers, http.MethodPost, "/v1/messages", envelope); response.Code != http.StatusOK {
		t.Fatalf("delivery = %d %s", response.Code, response.Body.String())
	}

	select {
	case recorder := <-answered:
		if recorder.Code != http.StatusOK {
			t.Fatalf("the poll answered %d %s", recorder.Code, recorder.Body.String())
		}
		var got channelView
		if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.MessageID != "msg_channel" || got.Body != "look at the build" {
			t.Errorf("the subscriber received %+v", got)
		}
		if got.SenderNodeID != peerNodeID {
			t.Errorf("sender node = %q, want the one the envelope proved", got.SenderNodeID)
		}
		if got.Fingerprint == "" {
			t.Error("no fingerprint reached the subscriber, and nobody is present to ask for one")
		}
		// The words an agent is told about a message are the node's to choose.
		// A subscriber composing its own would drift from the inbox's and the
		// Codex path's.
		if got.Notice != wake.Notice {
			t.Errorf("notice = %q", got.Notice)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the message never reached the subscriber")
	}

	// And it is recorded as a wake, not merely delivered.
	events, err := store.ListWakes(ctx, session, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Outcome != registry.WakeWoken {
		t.Fatalf("wake events = %+v", events)
	}
}

// With nobody listening the wake fails and the message stays in the inbox.
//
// This is #57's acceptance item, and the reason the direction is inverted: a
// session with no subscriber has no agent running, and that is the only way
// this node can tell.
func TestWithNobodyListeningTheMessageStaysInTheInbox(t *testing.T) {
	ctx := context.Background()
	store, owner, peers, _ := streamingSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	session := acceptingLocalSession(t, store, owner, "claude:absent")
	if err := store.SetAudience(ctx, session, model.Audience{
		Mode: model.AudienceAllPaired, AcceptMessages: true, AutoWake: true,
	}); err != nil {
		t.Fatal(err)
	}

	envelope := peer.messageEnvelope(t, testNodeID, "msg_unheard", session, "claude:theirs", "hello")
	if response := perform(t, peers, http.MethodPost, "/v1/messages", envelope); response.Code != http.StatusOK {
		t.Fatalf("delivery = %d %s", response.Code, response.Body.String())
	}

	waitFor(t, func() bool {
		events, err := store.ListWakes(ctx, session, 10)
		return err == nil && len(events) == 1
	})
	events, err := store.ListWakes(ctx, session, 10)
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Outcome != registry.WakeFailed {
		t.Errorf("outcome = %q, want %q: nobody was listening", events[0].Outcome, registry.WakeFailed)
	}
	// The message is still there for whenever an agent does start.
	held, err := store.CountInbox(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	if held != 1 {
		t.Errorf("the inbox holds %d messages after a wake nobody took", held)
	}
}

// A second subscriber for one session sends the first away rather than leaving
// two polls racing for the same messages.
func TestADisplacedSubscriberIsToldToStop(t *testing.T) {
	store, owner, _, _ := streamingSurfaces(t)
	session := acceptingLocalSession(t, store, owner, "claude:listening")

	answered := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		request := httptest.NewRequest(http.MethodGet,
			"/v1/sessions/"+session+"/wake-stream?wait=20s", nil)
		recorder := httptest.NewRecorder()
		owner.ServeHTTP(recorder, request)
		answered <- recorder
	}()
	time.Sleep(200 * time.Millisecond)

	// A second server starts for the same session.
	go func() {
		_ = perform(t, owner, http.MethodGet, "/v1/sessions/"+session+"/wake-stream?wait=1s", nil)
	}()

	select {
	case recorder := <-answered:
		if recorder.Code != http.StatusConflict {
			t.Errorf("the displaced poll answered %d %s, want 409 so it stops rather than "+
				"polling back and displacing the live one in turn",
				recorder.Code, recorder.Body.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the displaced poll was left hanging until its own deadline")
	}
}

// A node with no wake support says so rather than hanging a subscriber that
// would never be told anything.
func TestANodeWithoutWakeSupportRefusesTheStream(t *testing.T) {
	store, owner := testServer(t)
	session := acceptingLocalSession(t, store, owner, "claude:listening")
	response := perform(t, owner, http.MethodGet, "/v1/sessions/"+session+"/wake-stream", nil)
	if response.Code != http.StatusNotFound {
		t.Errorf("a node without wake support answered %d %s", response.Code, response.Body.String())
	}
}

func waitFor(t *testing.T, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("timed out waiting")
}

// A quiet poll answers through a real http.Server carrying the node's own
// timeouts.
//
// This is the seam nothing crossed. Every other test here calls the handler
// directly, where no write deadline exists — and the shipped node set both the
// deadline and the default poll to thirty seconds. Go arms the write deadline
// when the request header is read, so the deadline always won: every quiet
// poll was cut with nothing written, the subscriber backed off, and the
// session spent most of its life unregistered. Measured against the binaries
// before this test existed: wait=29s answered, wait=30s gave an empty reply.
func TestAQuietPollAnswersThroughARealServerWithProductionTimeouts(t *testing.T) {
	const writeTimeout = 4 * time.Second
	store, handler, _, _ := streamingSurfaces(t, WithWriteTimeout(writeTimeout))
	session := acceptingLocalSession(t, store, handler, "claude:listening")

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      writeTimeout,
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	base := "http://" + listener.Addr().String()
	client := &http.Client{Timeout: writeTimeout + 10*time.Second}

	// Asking for longer than the deadline must not produce a poll that cannot
	// answer: the node shortens it and says so.
	response, err := client.Get(base + "/v1/sessions/" + session + "/wake-stream?wait=5m")
	if err != nil {
		t.Fatalf("a quiet poll over a real connection: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("a quiet poll answered %d", response.StatusCode)
	}
	used, err := time.ParseDuration(response.Header.Get("Agenthub-Wake-Wait"))
	if err != nil {
		t.Fatalf("the node did not say how long it waited: %v", err)
	}
	if used >= writeTimeout {
		t.Errorf("the node held for %s against a %s write deadline; a poll that long is "+
			"one that can never answer", used, writeTimeout)
	}
}

// The wait a caller asks for is bounded on both sides.
func TestTheWakeStreamRefusesAWaitOutsideItsRange(t *testing.T) {
	store, owner, _, _ := streamingSurfaces(t)
	session := acceptingLocalSession(t, store, owner, "claude:listening")
	for _, value := range []string{"0s", "500ms", "6m", "-2s", "soon"} {
		response := perform(t, owner, http.MethodGet,
			"/v1/sessions/"+session+"/wake-stream?wait="+value, nil)
		if response.Code != http.StatusBadRequest {
			t.Errorf("wait=%s answered %d, want 400", value, response.Code)
		}
	}
}

// A session this node does not hold is refused rather than held open.
//
// localSession only parses the address, so any string of the right shape used
// to buy a goroutine and a held connection. The endpoint is reachable by every
// process on this machine.
func TestTheWakeStreamRefusesASessionThisNodeDoesNotHold(t *testing.T) {
	_, owner, _, _ := streamingSurfaces(t)
	response := perform(t, owner, http.MethodGet,
		"/v1/sessions/claude:never-heard-of-it/wake-stream?wait=1s", nil)
	if response.Code != http.StatusNotFound {
		t.Errorf("an invented session answered %d, want 404", response.Code)
	}
}

// Held polls are bounded.
func TestTheWakeStreamRefusesMoreThanItsShareOfSubscribers(t *testing.T) {
	store, owner, _, channels := streamingSurfaces(t)
	session := acceptingLocalSession(t, store, owner, "claude:listening")
	// Fill the register directly: what the endpoint checks is how many are
	// held, not who holds them.
	held := make([]wake.Subscription, 0, MaxWakeSubscribers)
	for i := range MaxWakeSubscribers {
		held = append(held, channels.Subscribe(fmt.Sprintf("claude:filler-%d", i)))
	}
	t.Cleanup(func() {
		for _, subscription := range held {
			subscription.Close()
		}
	})

	response := perform(t, owner, http.MethodGet,
		"/v1/sessions/"+session+"/wake-stream?wait=1s", nil)
	if response.Code != http.StatusServiceUnavailable {
		t.Errorf("the %dth subscriber answered %d, want 503",
			MaxWakeSubscribers+1, response.Code)
	}
}

// Draining ends a held poll, so a shutdown finishes inside its budget.
//
// http.Server.Shutdown waits for handlers to return and does not cancel their
// contexts, so one held poll kept the node alive to its own deadline — the
// shutdown then reported a timeout and the peer listener was never closed.
func TestDrainingEndsAHeldPoll(t *testing.T) {
	ctx := context.Background()
	store, err := registry.Open(ctx, t.TempDir()+"/agenthub.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	node := model.NodeIdentity{ID: testNodeID, DisplayName: "test", Platform: "test"}
	channels := wake.NewChannelDriver()
	server := NewServer(store, nil, protocol.NewHeartbeatBuilder(store, node, apiTestSigner{}),
		node, WithChannelSubscriber(channels))
	handler := server.Handler()
	session := acceptingLocalSession(t, store, handler, "claude:listening")

	answered := make(chan int, 1)
	go func() {
		request := httptest.NewRequest(http.MethodGet,
			"/v1/sessions/"+session+"/wake-stream?wait=5m", nil)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		answered <- recorder.Code
	}()
	time.Sleep(200 * time.Millisecond)

	started := time.Now()
	server.Drain()
	select {
	case code := <-answered:
		if code != http.StatusServiceUnavailable {
			t.Errorf("a drained poll answered %d, want 503", code)
		}
		if elapsed := time.Since(started); elapsed > 5*time.Second {
			t.Errorf("the poll took %s to notice the drain", elapsed)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("draining did not end the held poll; every shutdown would time out")
	}
	// And draining twice is not a panic.
	server.Drain()
}

// The wait a node settles on is one it would accept back.
//
// It is reported to the client and returned as the next request's, so a value
// outside the range the same handler enforces would be answered 400 — for a
// number this node chose, on every poll, with nothing to recover from it. The
// clamp was against the write deadline only, so a long deadline produced a
// wait past the upper bound and a very short one produced a wait at or past
// the deadline it was meant to stay under.
func TestTheWaitANodeSettlesOnIsOneItWouldAccept(t *testing.T) {
	for name, writeTimeout := range map[string]time.Duration{
		"unset":      0,
		"very short": time.Second,
		"short":      4 * time.Second,
		"the node's": 60 * time.Second,
		"very long":  10 * time.Minute,
		"absurd":     24 * time.Hour,
	} {
		server := NewServer(nil, nil, nil, model.NodeIdentity{}, WithWriteTimeout(writeTimeout))
		settled := server.WakeStreamWait(0)
		if settled < MinWakeStreamWait || settled > MaxWakeStreamWait {
			t.Errorf("with a %s deadline the node settles on %s, outside the %s..%s it "+
				"accepts — the client would be answered 400 for the node's own number",
				name, settled, MinWakeStreamWait, MaxWakeStreamWait)
		}
		// And a poll must still be able to answer, wherever the deadline is.
		if writeTimeout > MinWakeStreamWait && settled >= writeTimeout {
			t.Errorf("with a %s deadline the node holds for %s, so the connection is cut "+
				"before the response", name, settled)
		}
		// A caller asking for something inside the range gets it, unless the
		// deadline forces a shorter one.
		if asked := server.WakeStreamWait(2 * time.Second); asked > settled || asked <= 0 {
			t.Errorf("with a %s deadline, asking for 2s gave %s", name, asked)
		}
	}
}

// deadConnection is a ResponseWriter whose body never reaches anyone, which is
// what a client that has gone away looks like from inside a handler.
type deadConnection struct {
	header http.Header
	code   int
}

func (d *deadConnection) Header() http.Header {
	if d.header == nil {
		d.header = http.Header{}
	}
	return d.header
}
func (d *deadConnection) WriteHeader(code int) { d.code = code }
func (d *deadConnection) Write([]byte) (int, error) {
	return 0, errors.New("write: broken pipe")
}

// A wake whose response never reaches the subscriber is recorded as failed.
//
// The handler receiving the envelope is what makes Drive return, and Drive
// returning nil is what leaves the wake settled as woken. But the receive
// happens before the write, and the write can fail — the agent's MCP server
// exits while a message is being handed to it, which is every time its session
// ends. The message was then recorded as delivered to an agent that never saw
// it, and stayed that way: only a settled failure stops counting against the
// limits, so one broken pipe spent one of three wakes per pair per ten minutes
// permanently.
//
// Both halves were covered — Drive hands over, the handler writes — and this
// is the seam between them.
func TestAWakeWhoseResponseNeverArrivesIsNotRecordedAsWoken(t *testing.T) {
	ctx := context.Background()
	store, owner, peers, _ := streamingSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	session := acceptingLocalSession(t, store, owner, "claude:goingaway")
	if err := store.SetAudience(ctx, session, model.Audience{
		Mode: model.AudienceAllPaired, AcceptMessages: true, AutoWake: true,
	}); err != nil {
		t.Fatal(err)
	}

	polled := make(chan struct{})
	go func() {
		defer close(polled)
		request := httptest.NewRequest(http.MethodGet,
			"/v1/sessions/"+session+"/wake-stream?wait=20s", nil)
		owner.ServeHTTP(&deadConnection{}, request)
	}()
	waitFor(t, func() bool {
		response := perform(t, owner, http.MethodGet, "/v1/wakes", nil)
		return response.Code == http.StatusOK
	})
	time.Sleep(100 * time.Millisecond)

	envelope := peer.messageEnvelope(t, testNodeID, "msg_lost", session, "claude:theirs",
		"look at the build")
	if response := perform(t, peers, http.MethodPost, "/v1/messages", envelope); response.Code != http.StatusOK {
		t.Fatalf("delivery = %d %s", response.Code, response.Body.String())
	}
	select {
	case <-polled:
	case <-time.After(10 * time.Second):
		t.Fatal("the poll never returned")
	}

	// The row is inserted when the wake is reserved and settled after the
	// drive returns, so this waits for it to settle rather than reading it
	// mid-flight — and keeps what it last saw, so a wake that stays woken says
	// so instead of reporting a timeout.
	var outcome registry.WakeOutcome
	var detail string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		events, err := store.ListWakes(ctx, session, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 {
			time.Sleep(20 * time.Millisecond)
			continue
		}
		outcome, detail = events[0].Outcome, events[0].Detail
		if outcome != registry.WakeWoken {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if outcome != registry.WakeFailed {
		t.Errorf("outcome = %q (%s), want %q: the response never reached the subscriber",
			outcome, detail, registry.WakeFailed)
	}
	// And the message is still there, so the next poll gets it.
	held, err := store.CountInbox(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	if held != 1 {
		t.Errorf("the inbox holds %d messages after a wake that was never delivered", held)
	}
}
