package api

import (
	"context"
	"encoding/json"
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
func streamingSurfaces(t *testing.T) (*registry.Registry, http.Handler, http.Handler, *wake.ChannelDriver) {
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
	server := NewServer(store, nil, heartbeats, node,
		WithWaker(gate), WithChannelSubscriber(channels))
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
