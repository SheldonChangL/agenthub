package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
		// The label and the hop count travel too. Neither was asserted, and
		// dropping SenderLabel from the view the handler builds passed every
		// test here and the contract test both: the node id and the
		// fingerprint survive, so the agent would learn which machine sent a
		// stranger's words and not which session.
		// Qualified with the node id the envelope proved, not the one the
		// sender claimed: that substitution is what stops a peer naming
		// another machine as the origin of its own words.
		if want := peerNodeID + "/claude:theirs"; got.SenderLabel != want {
			t.Errorf("sender label = %q, want %q", got.SenderLabel, want)
		}
		if got.Hops != 0 {
			t.Errorf("hops = %d, want 0 for a message that has not been relayed", got.Hops)
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

	// And it is recorded as a wake that finished, not merely reserved.
	//
	// Waiting for the detail, not reading the row straight away: ReserveWake
	// inserts it as woken before the driver runs, so an immediate read says
	// woken whatever the drive did. Asserting on it passed with the handler's
	// acknowledgement deleted — the drive then failed on its handoff, five
	// seconds after this test had already finished looking.
	var event registry.WakeEvent
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		events, err := store.ListWakes(ctx, session, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) == 1 && events[0].Detail != "" {
			event = events[0]
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if event.Outcome != registry.WakeWoken {
		t.Fatalf("outcome = %q (%s), want %q", event.Outcome, event.Detail, registry.WakeWoken)
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

// deadSink is a socket nobody is reading and nobody will.
type deadSink struct{}

func (deadSink) Write([]byte) (int, error) { return 0, errors.New("write: broken pipe") }

// bufferedDeadConnection is what production puts between a handler and a dead
// socket, which a bare failing ResponseWriter is not.
//
// net/http wraps the connection in a 2048-byte bufio.Writer
// (bufferBeforeChunkingSize, net/http/server.go), so a body under that size is
// written to memory and Encode returns nil however dead the connection is. The
// failure appears when the buffer is flushed.
//
// Measured against a real http.Server with WriteTimeout 1s, the client's
// connection closed and the deadline already past: a 600-byte body gave
// Encode err = <nil> and Flush err = "i/o timeout"; a 5000-byte body, over the
// buffer, gave the error from Encode. An ordinary message is the first case —
// wake.Notice alone is most of 600 bytes.
//
// A real socket cannot be used here: closing it cancels the request context
// too, and the handler's select then answers that instead, so the write is
// never attempted. What has to be isolated is a write that fails while the
// request is still live.
type bufferedDeadConnection struct {
	header http.Header
	code   int
	buffer *bufio.Writer
}

func newBufferedDeadConnection() *bufferedDeadConnection {
	return &bufferedDeadConnection{buffer: bufio.NewWriterSize(deadSink{}, 2048)}
}

func (d *bufferedDeadConnection) Header() http.Header {
	if d.header == nil {
		d.header = http.Header{}
	}
	return d.header
}
func (d *bufferedDeadConnection) WriteHeader(code int)        { d.code = code }
func (d *bufferedDeadConnection) Write(p []byte) (int, error) { return d.buffer.Write(p) }
func (d *bufferedDeadConnection) FlushError() error           { return d.buffer.Flush() }

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
// The first version of this test used a ResponseWriter that failed on its
// first Write, and certified a fix that did nothing at the size a message
// actually is. See bufferedDeadConnection for why.
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
		owner.ServeHTTP(newBufferedDeadConnection(), request)
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
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		events, err := store.ListWakes(ctx, session, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(events) == 1 {
			outcome, detail = events[0].Outcome, events[0].Detail
			if outcome != registry.WakeWoken {
				break
			}
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

// The wait the node reports is the wait the node held.
//
// The header and the timer come off the same variable, and nothing spanned
// them: the api tests asserted the header was present, and the client test
// drove a stub that hardcoded its own value. Reporting half of what was held
// passed both packages.
//
// It matters because an under-report ratchets. WakeStreamWait returns what was
// asked for whenever it fits, so the client adopts the short value, asks for
// it, is told half of that, and walks down to the one-second floor — a
// connection and a held goroutine per second per session, for as long as the
// agent runs. An over-report self-corrects against the clamp; this does not.
func TestTheNodeReportsTheWaitItActuallyHeld(t *testing.T) {
	const writeTimeout = 5 * time.Second
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

	base := "http://" + listener.Addr().String() + "/v1/sessions/" + session + "/wake-stream"
	client := &http.Client{Timeout: writeTimeout + 10*time.Second}

	poll := func(asked string) (time.Duration, time.Duration) {
		t.Helper()
		started := time.Now()
		response, err := client.Get(base + "?wait=" + asked)
		if err != nil {
			t.Fatalf("a quiet poll asking for %s: %v", asked, err)
		}
		defer func() { _ = response.Body.Close() }()
		if response.StatusCode != http.StatusNoContent {
			t.Fatalf("a quiet poll answered %d", response.StatusCode)
		}
		reported, err := time.ParseDuration(response.Header.Get("Agenthub-Wake-Wait"))
		if err != nil {
			t.Fatalf("the node did not say how long it held: %v", err)
		}
		return reported, time.Since(started)
	}

	reported, held := poll("2s")
	if drift := reported - held; drift > 700*time.Millisecond || drift < -700*time.Millisecond {
		t.Errorf("the node held the poll for %s and reported %s; the client sets its next "+
			"request from what it is told", held, reported)
	}

	// And what it reports is a fixed point: fed back, it comes out unchanged.
	// A value the node shortens every round walks down to the floor.
	again, _ := poll(reported.String())
	if again != reported {
		t.Errorf("reported %s, and asking for that was answered %s; the wait ratchets down "+
			"to %s and the agent polls once a second for ever", reported, again,
			MinWakeStreamWait)
	}
}

// A small ordinary response is sent with a length, not chunked.
//
// The wake stream's 200 needs a flush, because something else has already
// recorded that those bytes went out. Routing every response through the same
// helper gave the flush to all forty-odd endpoints on both listeners and moved
// every one of them to chunked encoding — a change nothing in this tree reads,
// which is why nothing failed and why putting it back passes without this.
//
// "Small" is load-bearing and was not, in the first version of this test. A
// length only survives while the body fits net/http's 2048-byte buffer, so
// this asks /v1/node — one identity, fixed size — rather than a listing.
// Measured on /v1/sessions, which the first version used: 1 session 428 bytes
// with a length, and 6 sessions 2152 bytes chunked, with no flush anywhere.
// Its comment claimed a property of every ordinary response and had one of a
// fixture, and the day it fired on a node with six sessions it would have
// blamed the flush.
func TestASmallOrdinaryResponseIsSentWithALength(t *testing.T) {
	_, handler, _, _ := streamingSurfaces(t)

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	response, err := server.Client().Get(server.URL + "/v1/node")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("asking for this node answered %d", response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) >= 2048 {
		t.Fatalf("this node's identity is %d bytes, past the buffer that makes the "+
			"framing meaningful; this test needs a smaller endpoint", len(body))
	}
	if response.ContentLength < 0 {
		t.Errorf("a %d-byte response has no length and arrived as %v; the flush belongs "+
			"to the one handler that has to know its bytes went out",
			len(body), response.TransferEncoding)
	}
}

// One wake fits inside the context it runs under, with room for the settle.
//
// considerWake gives the whole thing wakeTimeout, and the settle that writes
// the outcome runs on that same context — so if the driver can still be
// waiting when it expires, the settle is a no-op that only logs, and the row
// stays at the reservation's "woken" for ever. That is the permanent
// mis-attribution three rounds of this PR went into removing, arrived at from
// the other end.
//
// The node's own test pins AckWait above the write deadline it waits on. That
// inequality is one-sided, and on its own it points the wrong way: raising
// ownerWriteTimeout to two minutes makes it fail and instruct you to raise
// AckWait past two minutes, which is exactly this bound broken. Both are
// needed, and this is the one with the sharp edge.
func TestOneWakeFitsInsideTheContextItRunsUnder(t *testing.T) {
	// The settle is one indexed UPDATE on a local file. Ten seconds is far
	// more than it takes and small enough to leave the bound meaningful.
	const settleRoom = 10 * time.Second
	if longest := wake.Handoff + wake.AckWait; longest+settleRoom > wakeTimeout {
		t.Errorf("a drive can take %s (%s handing over, %s waiting to be told it went "+
			"out) inside a %s context; the settle that follows it would run on an "+
			"expired one and the wake would stay recorded as woken",
			longest, wake.Handoff, wake.AckWait, wakeTimeout)
	}
}
