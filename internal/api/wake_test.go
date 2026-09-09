package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
)

// recordingWaker remembers what it was asked about.
//
// The gate runs on its own goroutine now — a wake can outlast the sender's
// delivery timeout, and holding the ack for it would make a working delivery
// look like a failure — so a test has to wait for it rather than read
// immediately after the response.
type recordingWaker struct {
	mu      sync.Mutex
	seen    []model.Message
	from    []string
	arrived chan struct{}
}

func newRecordingWaker() *recordingWaker {
	return &recordingWaker{arrived: make(chan struct{}, 64)}
}

func (w *recordingWaker) Consider(_ context.Context, message model.Message, fingerprint string) {
	w.mu.Lock()
	w.seen = append(w.seen, message)
	w.from = append(w.from, fingerprint)
	w.mu.Unlock()
	w.arrived <- struct{}{}
}

func (w *recordingWaker) messages() []model.Message {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]model.Message(nil), w.seen...)
}

// await waits for n messages to reach the gate, and fails rather than hanging.
func (w *recordingWaker) await(t *testing.T, n int) {
	t.Helper()
	for range n {
		select {
		case <-w.arrived:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d messages reached the gate", len(w.messages()), n)
		}
	}
}

// quiet fails if anything reaches the gate in the time given.
func (w *recordingWaker) quiet(t *testing.T, within time.Duration) {
	t.Helper()
	select {
	case <-w.arrived:
		t.Fatalf("the gate was asked about %+v", w.messages())
	case <-time.After(within):
	}
}

func wakingSurfaces(t *testing.T) (*registry.Registry, http.Handler, http.Handler, *recordingWaker) {
	t.Helper()
	ctx := context.Background()
	store, err := registry.Open(ctx, filepath.Join(t.TempDir(), "agenthub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	node := model.NodeIdentity{ID: testNodeID, DisplayName: "test", Platform: "test"}
	heartbeats := protocol.NewHeartbeatBuilder(store, node, apiTestSigner{})
	waker := newRecordingWaker()
	server := NewServer(store, nil, heartbeats, node, WithWaker(waker))
	return store, server.Handler(), server.PeerHandler(), waker
}

// Both ways a message becomes durable ask the gate.
//
// There are two independent writes into the inbox — a peer's delivery and a
// local send — in two files, and a wake wired to one of them looks entirely
// correct from the other. The peer path is the one that gets attention because
// it is where the danger is; the local path is the one a developer tests with,
// so a gap there is a wake that works in every demo and never in the field, or
// the reverse.
func TestBothDeliveryPathsAskTheWakeGate(t *testing.T) {
	store, owner, peers, waker := wakingSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	session := acceptingLocalSession(t, store, owner, "codex:wake-target")

	// 1. A peer's delivery.
	envelope := peer.messageEnvelope(t, testNodeID, "msg_from_peer", session, "codex:theirs", "from the peer")
	response := perform(t, peers, http.MethodPost, "/v1/messages", envelope)
	if response.Code != http.StatusOK {
		t.Fatalf("peer delivery = %d %s", response.Code, response.Body.String())
	}

	// 2. A local send to a session on this same node.
	local := perform(t, owner, http.MethodPost, "/v1/messages",
		map[string]string{"to": session, "body": "from this machine"})
	if local.Code != http.StatusCreated {
		t.Fatalf("local send = %d %s", local.Code, local.Body.String())
	}

	waker.await(t, 2)
	seen := waker.messages()
	if len(seen) != 2 {
		t.Fatalf("the gate was asked about %d messages, want both paths: %+v", len(seen), seen)
	}
	byBody := map[string]model.Message{}
	for _, message := range seen {
		byBody[message.Body] = message
	}
	fromPeer, ok := byBody["from the peer"]
	if !ok {
		t.Error("a peer's delivery did not reach the gate")
	}
	if fromPeer.ID != "msg_from_peer" || fromPeer.To != session {
		t.Errorf("the peer's message reached the gate as %+v", fromPeer)
	}
	if _, ok := byBody["from this machine"]; !ok {
		t.Error("a local send did not reach the gate")
	}
}

// A redelivery must not wake anything a second time.
//
// A sender that never sees an ack retries, and every retry is the same message.
// If each one started a turn, a peer could wake an agent as often as it liked
// without passing any limit — every attempt would look like a first arrival.
func TestARedeliveryDoesNotWakeASecondTime(t *testing.T) {
	store, owner, peers, waker := wakingSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	session := acceptingLocalSession(t, store, owner, "codex:wake-target")

	envelope := peer.messageEnvelope(t, testNodeID, "msg_retried", session, "codex:theirs", "sent twice")
	for attempt := range 3 {
		response := perform(t, peers, http.MethodPost, "/v1/messages", envelope)
		if response.Code != http.StatusOK {
			t.Fatalf("attempt %d = %d %s", attempt, response.Code, response.Body.String())
		}
	}
	waker.await(t, 1)
	waker.quiet(t, 500*time.Millisecond)
	if seen := waker.messages(); len(seen) != 1 {
		t.Errorf("three deliveries of one message woke the gate %d times", len(seen))
	}
}

// The hop count and the sender's fingerprint reach the gate.
//
// The count is what stops a cycle; the fingerprint is the one thing about a
// sender a person can check out of band, and a woken agent has nobody present
// to ask for it. Either dropped in transit is a defence that reads as present
// and is not.
func TestTheGateSeesTheHopCountAndTheFingerprint(t *testing.T) {
	store, owner, peers, waker := wakingSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	session := acceptingLocalSession(t, store, owner, "codex:wake-target")

	envelope, err := protocol.NewMessageEnvelope(peerNodeID, testNodeID, protocol.MessagePayload{
		MessageID: "msg_hops", To: session, From: "codex:theirs", Body: "two hops in",
		SentAt: time.Now().UTC(), WakeHops: 2,
	}, peer.signer)
	if err != nil {
		t.Fatal(err)
	}
	response := perform(t, peers, http.MethodPost, "/v1/messages", envelope)
	if response.Code != http.StatusOK {
		t.Fatalf("delivery = %d %s", response.Code, response.Body.String())
	}

	waker.await(t, 1)
	seen := waker.messages()
	if len(seen) != 1 {
		t.Fatalf("the gate saw %d messages", len(seen))
	}
	if seen[0].WakeHops != 2 {
		t.Errorf("the gate saw %d hops, want the 2 the sender declared", seen[0].WakeHops)
	}
	waker.mu.Lock()
	fingerprint := waker.from[0]
	waker.mu.Unlock()
	if fingerprint == "" {
		t.Error("the gate saw no fingerprint, so a woken agent cannot say who sent this")
	}
}

// A node with no gate stores messages exactly as before. Waking is something a
// node gains, not something its inbox depends on.
func TestANodeWithNoGateStillDelivers(t *testing.T) {
	store, owner, peers := testSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	session := acceptingLocalSession(t, store, owner, "codex:no-gate")

	envelope := peer.messageEnvelope(t, testNodeID, "msg_no_gate", session, "codex:theirs", "still delivered")
	if response := perform(t, peers, http.MethodPost, "/v1/messages", envelope); response.Code != http.StatusOK {
		t.Fatalf("delivery = %d %s", response.Code, response.Body.String())
	}
	held, err := store.CountInbox(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if held != 1 {
		t.Errorf("the inbox holds %d messages on a node with no wake gate", held)
	}
}

// A reply sent after a wake carries the chain forward.
//
// Without this the hop count is dead: it travels, it is stored, it is checked,
// and nothing ever sets it above zero. That was true for four commits, and the
// limit read as present while doing nothing at all.
func TestAReplyAfterAWakeCarriesTheChainForward(t *testing.T) {
	ctx := context.Background()
	store, owner, peers, waker := wakingSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	session := acceptingLocalSession(t, store, owner, "codex:woken")
	other := acceptingLocalSession(t, store, owner, "codex:neighbour")

	// A peer wakes the session, two hops into an exchange.
	envelope, err := protocol.NewMessageEnvelope(peerNodeID, testNodeID, protocol.MessagePayload{
		MessageID: "msg_in", To: session, From: "codex:theirs", Body: "your turn",
		SentAt: time.Now().UTC(), WakeHops: 2,
	}, peer.signer)
	if err != nil {
		t.Fatal(err)
	}
	if response := perform(t, peers, http.MethodPost, "/v1/messages", envelope); response.Code != http.StatusOK {
		t.Fatalf("delivery = %d %s", response.Code, response.Body.String())
	}
	waker.await(t, 1)
	if seen := waker.messages(); len(seen) != 1 || seen[0].WakeHops != 2 {
		t.Fatalf("the gate saw %+v", seen)
	}
	// The gate is a stub here, so record the wake the way a real one would.
	if _, err := store.ReserveWake(ctx, registry.WakeEvent{
		MessageID: "msg_in", SourceSession: peerNodeID + "/codex:theirs",
		DestinationSession: session, Hops: 2,
	}, registry.DefaultWakeLimits()); err != nil {
		t.Fatal(err)
	}

	// The agent answers. Its reply is one hop further along.
	reply := perform(t, owner, http.MethodPost, "/v1/messages",
		map[string]string{"to": other, "from": session, "body": "answering"})
	if reply.Code != http.StatusCreated {
		t.Fatalf("reply = %d %s", reply.Code, reply.Body.String())
	}
	waker.await(t, 1)
	seen := waker.messages()
	answered := seen[len(seen)-1]
	if answered.WakeHops != 3 {
		t.Errorf("the reply carries %d hops, want 3: the message that woke it was at 2",
			answered.WakeHops)
	}

	// A session nobody woke sends at zero, so a person typing is not counted
	// into somebody else's exchange.
	fresh := perform(t, owner, http.MethodPost, "/v1/messages",
		map[string]string{"to": session, "from": other, "body": "unprompted"})
	if fresh.Code != http.StatusCreated {
		t.Fatalf("fresh send = %d %s", fresh.Code, fresh.Body.String())
	}
	waker.await(t, 1)
	seen = waker.messages()
	if last := seen[len(seen)-1]; last.WakeHops != 0 {
		t.Errorf("a send from a session nobody woke carries %d hops", last.WakeHops)
	}
}

// The trail is readable, and it is the owner's alone.
//
// Untested until now: deleting the route registration passed the whole suite,
// which would have shipped a feature whose entire purpose is to let an owner
// see what moved their agent, with no way to see it.
func TestTheWakeTrailIsReadableAndOwnerOnly(t *testing.T) {
	ctx := context.Background()
	store, owner, peers, _ := wakingSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	session := acceptingLocalSession(t, store, owner, "codex:watched")
	if _, err := store.RecordWake(ctx, registry.WakeEvent{
		MessageID: "msg_1", SourceNodeID: peerNodeID, SourceSession: peerNodeID + "/codex:theirs",
		DestinationSession: session, Hops: 1, Outcome: registry.WakeRefusedPair,
		Detail: "3 in the last 10m0s",
	}); err != nil {
		t.Fatal(err)
	}

	response := perform(t, owner, http.MethodGet, "/v1/wakes", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /v1/wakes = %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Wakes  []registry.WakeEvent `json:"wakes"`
		Limits map[string]any       `json:"limits"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Wakes) != 1 || body.Wakes[0].Outcome != registry.WakeRefusedPair {
		t.Fatalf("wakes = %+v", body.Wakes)
	}
	// A refusal is only legible beside the rule that produced it.
	for _, key := range []string{"hops", "pair", "pairWindow", "session", "node"} {
		if _, ok := body.Limits[key]; !ok {
			t.Errorf("the limits do not name %q, so a refusal cannot be judged", key)
		}
	}

	// Scoped to one session, and a limit that is not a number is refused
	// rather than silently ignored.
	scoped := perform(t, owner, http.MethodGet, "/v1/wakes?session="+session, nil)
	if scoped.Code != http.StatusOK {
		t.Errorf("scoped read = %d %s", scoped.Code, scoped.Body.String())
	}
	if bad := perform(t, owner, http.MethodGet, "/v1/wakes?limit=0", nil); bad.Code != http.StatusBadRequest {
		t.Errorf("limit=0 = %d, want 400", bad.Code)
	}

	// And it is not on the peer surface. The trail names which peers made this
	// machine move, which is exactly what a peer must not be able to read.
	if fromPeer := perform(t, peers, http.MethodGet, "/v1/wakes", nil); fromPeer.Code == http.StatusOK {
		t.Errorf("a peer read the wake trail: %d %s", fromPeer.Code, fromPeer.Body.String())
	}
}
