package api

import (
	"context"
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
type recordingWaker struct {
	mu   sync.Mutex
	seen []model.Message
	from []string
}

func (w *recordingWaker) Consider(_ context.Context, message model.Message, fingerprint string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seen = append(w.seen, message)
	w.from = append(w.from, fingerprint)
}

func (w *recordingWaker) messages() []model.Message {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]model.Message(nil), w.seen...)
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
	waker := &recordingWaker{}
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
	seen = waker.messages()
	if last := seen[len(seen)-1]; last.WakeHops != 0 {
		t.Errorf("a send from a session nobody woke carries %d hops", last.WakeHops)
	}
}
