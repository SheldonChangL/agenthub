package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	nodes   []string
	arrived chan struct{}
	// before runs at the top of Consider, so a test can hold the gate open
	// and watch what the request does meanwhile.
	before func()
}

func newRecordingWaker() *recordingWaker {
	return &recordingWaker{arrived: make(chan struct{}, 64)}
}

func (w *recordingWaker) Consider(_ context.Context, message model.Message, nodeID, fingerprint string) {
	if w.before != nil {
		w.before()
	}
	w.mu.Lock()
	w.seen = append(w.seen, message)
	w.from = append(w.from, fingerprint)
	w.nodes = append(w.nodes, nodeID)
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

	// And the wake is spent: the agent's next message is its own doing, not a
	// second inheritance of the same wake. Without this a person typing five
	// minutes later has their own message carried at the agent's count and
	// refused at the far end, on a trail they cannot read.
	again := perform(t, owner, http.MethodPost, "/v1/messages",
		map[string]string{"to": other, "from": session, "body": "and again"})
	if again.Code != http.StatusCreated {
		t.Fatalf("second reply = %d %s", again.Code, again.Body.String())
	}
	waker.await(t, 1)
	seen = waker.messages()
	if last := seen[len(seen)-1]; last.WakeHops != 0 {
		t.Errorf("a second message inherited the same wake and carries %d hops", last.WakeHops)
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

// A peer that omits `from` is still counted as that peer.
//
// The composition is what was wrong, and both halves were separately correct:
// an empty `from` is stored as a bare node id, and PairKey reads a node id off
// the label only when the label has a slash in it. So a peer that simply left
// the field out was bucketed as "local" — a second bucket worth another three
// wakes, and an audit row naming a peer's wake as this machine's own.
func TestAPeerThatOmitsItsSenderLabelIsStillCountedAsThatPeer(t *testing.T) {
	store, owner, peers, waker := wakingSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	session := acceptingLocalSession(t, store, owner, "codex:wake-target")

	envelope, err := protocol.NewMessageEnvelope(peerNodeID, testNodeID, protocol.MessagePayload{
		MessageID: "msg_anon", To: session, Body: "no sender named",
		SentAt: time.Now().UTC(),
	}, peer.signer)
	if err != nil {
		t.Fatal(err)
	}
	if response := perform(t, peers, http.MethodPost, "/v1/messages", envelope); response.Code != http.StatusOK {
		t.Fatalf("delivery = %d %s", response.Code, response.Body.String())
	}
	waker.await(t, 1)

	waker.mu.Lock()
	nodeID := waker.nodes[0]
	waker.mu.Unlock()
	if nodeID != peerNodeID {
		t.Fatalf("the gate saw sender node %q, want the one the envelope proved", nodeID)
	}
	// And that is what the bucket is keyed on.
	event := registry.WakeEvent{SourceNodeID: nodeID, SourceSession: peerNodeID}
	if got := event.PairKey(); got != "node:"+peerNodeID {
		t.Errorf("a peer with no sender label lands in bucket %q, not its own", got)
	}
}

// The hop count reaches the wire, not only the local inbox.
//
// Every other assertion about it is on the local delivery path or inside the
// registry. The leg that crosses machines is the only leg the hop count exists
// for — a loop between two nodes — and replacing the outbound count with zero
// passed everything. That is the same defect this branch already fixed once on
// the other half.
func TestTheHopCountReachesTheOutboundQueue(t *testing.T) {
	ctx := context.Background()
	store, owner, peers, waker := wakingSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	session := acceptingLocalSession(t, store, owner, "codex:woken")
	if err := store.SetAudience(ctx, session, model.Audience{
		Mode: model.AudienceAllPaired, AcceptMessages: true, AllowOutbound: true, AutoWake: true,
	}); err != nil {
		t.Fatal(err)
	}

	// A peer wakes it, one hop in.
	envelope, err := protocol.NewMessageEnvelope(peerNodeID, testNodeID, protocol.MessagePayload{
		MessageID: "msg_in", To: session, From: "codex:theirs", Body: "your turn",
		SentAt: time.Now().UTC(), WakeHops: 1,
	}, peer.signer)
	if err != nil {
		t.Fatal(err)
	}
	if response := perform(t, peers, http.MethodPost, "/v1/messages", envelope); response.Code != http.StatusOK {
		t.Fatalf("delivery = %d %s", response.Code, response.Body.String())
	}
	waker.await(t, 1)
	if _, err := store.ReserveWake(ctx, registry.WakeEvent{
		MessageID: "msg_in", SourceNodeID: peerNodeID, DestinationSession: session, Hops: 1,
	}, registry.DefaultWakeLimits()); err != nil {
		t.Fatal(err)
	}

	// The agent answers the peer. That message leaves this machine.
	reply := perform(t, owner, http.MethodPost, "/v1/messages", map[string]string{
		"to": peerNodeID + "/codex:theirs", "from": session, "body": "answering",
	})
	if reply.Code != http.StatusAccepted {
		t.Fatalf("reply = %d %s", reply.Code, reply.Body.String())
	}

	queued, err := store.PendingOutbound(ctx, peerNodeID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 {
		t.Fatalf("the queue holds %d messages", len(queued))
	}
	if queued[0].WakeHops != 2 {
		t.Errorf("the message crossing to the peer carries %d hops, want 2: it answers one "+
			"that arrived at 1, and a loop between two nodes is what the count is for",
			queued[0].WakeHops)
	}

	// And spent, on this path too. The two paths claim in different files, and
	// only the failed direction was asserted on either.
	second := perform(t, owner, http.MethodPost, "/v1/messages", map[string]string{
		"to": peerNodeID + "/codex:theirs", "from": session, "body": "and again",
	})
	if second.Code != http.StatusAccepted {
		t.Fatalf("second reply = %d %s", second.Code, second.Body.String())
	}
	queued, err = store.PendingOutbound(ctx, peerNodeID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 2 {
		t.Fatalf("the queue holds %d messages", len(queued))
	}
	// By body, not by index: both were queued in the same millisecond and the
	// listing breaks that tie on a random id, so queued[1] is a coin flip.
	byBody := map[string]int{}
	for _, message := range queued {
		byBody[message.Body] = message.WakeHops
	}
	if byBody["answering"] != 2 {
		t.Errorf("the first reply carries %d hops, want 2", byBody["answering"])
	}
	if byBody["and again"] != 0 {
		t.Errorf("a second message inherited the same wake and carries %d hops",
			byBody["and again"])
	}
}

// A send that failed does not spend the chain.
//
// The claim used to happen inside the argument list, before anything was
// stored, so a woken agent could zero its own chain with one throwaway message
// addressed at a session that does not exist: the send is refused, the claim
// is gone, and its real reply then goes out at zero hops.
func TestAFailedSendDoesNotSpendTheChain(t *testing.T) {
	ctx := context.Background()
	store, owner, _, waker := wakingSurfaces(t)
	session := acceptingLocalSession(t, store, owner, "codex:woken")
	other := acceptingLocalSession(t, store, owner, "codex:neighbour")
	if _, err := store.ReserveWake(ctx, registry.WakeEvent{
		MessageID: "msg_in", SourceNodeID: peerNodeID, DestinationSession: session, Hops: 2,
	}, registry.DefaultWakeLimits()); err != nil {
		t.Fatal(err)
	}

	// One throwaway, at a session that is not here.
	refused := perform(t, owner, http.MethodPost, "/v1/messages",
		map[string]string{"to": "codex:not-a-session", "from": session, "body": "nowhere"})
	if refused.Code < 400 {
		t.Fatalf("the throwaway was accepted: %d %s", refused.Code, refused.Body.String())
	}

	// The real reply still carries the chain.
	reply := perform(t, owner, http.MethodPost, "/v1/messages",
		map[string]string{"to": other, "from": session, "body": "answering"})
	if reply.Code != http.StatusCreated {
		t.Fatalf("reply = %d %s", reply.Code, reply.Body.String())
	}
	waker.await(t, 1)
	seen := waker.messages()
	if last := seen[len(seen)-1]; last.WakeHops != 3 {
		t.Errorf("the reply carries %d hops, want 3; a refused send spent the chain",
			last.WakeHops)
	}
}

// And the same on the leg that crosses machines.
//
// The local path and the peer path claim the chain in two different files, and
// moving the claim back above QueueOutbound passed everything: a woken agent
// could still zero its own chain with one throwaway, on the only leg a hop
// count exists for.
func TestAFailedSendDoesNotSpendTheChainOnTheOutboundLeg(t *testing.T) {
	ctx := context.Background()
	store, owner, _, _ := wakingSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	session := acceptingLocalSession(t, store, owner, "codex:woken")
	if err := store.SetAudience(ctx, session, model.Audience{
		Mode: model.AudienceAllPaired, AcceptMessages: true, AllowOutbound: true, AutoWake: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveWake(ctx, registry.WakeEvent{
		MessageID: "msg_in", SourceNodeID: peerNodeID, DestinationSession: session, Hops: 1,
	}, registry.DefaultWakeLimits()); err != nil {
		t.Fatal(err)
	}

	// One throwaway, at a node this machine has never paired with.
	refused := perform(t, owner, http.MethodPost, "/v1/messages", map[string]string{
		"to": "node_stranger00000000000/codex:nobody", "from": session, "body": "nowhere",
	})
	if refused.Code < 400 {
		t.Fatalf("the throwaway was accepted: %d %s", refused.Code, refused.Body.String())
	}

	// The real reply, back to the peer that woke it.
	reply := perform(t, owner, http.MethodPost, "/v1/messages", map[string]string{
		"to": peerNodeID + "/codex:theirs", "from": session, "body": "answering",
	})
	if reply.Code != http.StatusAccepted {
		t.Fatalf("reply = %d %s", reply.Code, reply.Body.String())
	}
	queued, err := store.PendingOutbound(ctx, peerNodeID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(queued) != 1 {
		t.Fatalf("the queue holds %d messages", len(queued))
	}
	if queued[0].WakeHops != 2 {
		t.Errorf("the message crossing to the peer carries %d hops, want 2; a refused send "+
			"spent the chain", queued[0].WakeHops)
	}
}

// The wake does not run inline: a slow provider must not hold the ack.
//
// A peer waits on the ack under its own ten-second delivery timeout, and a
// cold start spawns an app-server and resumes a thread before the turn is even
// sent. Run inline, the sender gives up and retries into a duplicate.
func TestTheAckDoesNotWaitForTheWake(t *testing.T) {
	store, owner, peers, waker := wakingSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	session := acceptingLocalSession(t, store, owner, "codex:slow")

	// The gate blocks until released, standing in for a cold start.
	release := make(chan struct{})
	waker.before = func() { <-release }

	// Built on this goroutine: both helpers can t.Fatal, and doing that from a
	// goroutine other than the test's is undefined.
	envelope := peer.messageEnvelope(t, testNodeID, "msg_slow", session, "codex:theirs", "hello")
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	answered := make(chan int, 1)
	go func() {
		request := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		peers.ServeHTTP(recorder, request)
		answered <- recorder.Code
	}()

	select {
	case code := <-answered:
		if code != http.StatusOK {
			t.Fatalf("delivery = %d", code)
		}
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the ack waited for the wake; a slow provider would time the sender out")
	}
	close(release)
	waker.await(t, 1)
}

// The switch can be opened and closed through the API, and closing it stops
// the waking.
//
// The only way anyone turns this on is `ah audience … --auto-wake` or the
// desktop checkbox, and both route through this endpoint. Two of the three
// hops were unpinned — the API's audienceInput field and the CLI's flag — so
// the switch could be silently disconnected from the thing it governs while
// the registry tests, which set the flag directly, stayed green.
func TestTheWakeSwitchTravelsThroughTheAudienceEndpoint(t *testing.T) {
	store, owner, _, _ := wakingSurfaces(t)
	session := acceptingLocalSession(t, store, owner, "codex:switched")

	set := func(t *testing.T, autoWake bool) {
		t.Helper()
		response := perform(t, owner, http.MethodPut, "/v1/sessions/"+session+"/audience",
			map[string]any{"mode": "none", "acceptMessages": true, "autoWake": autoWake})
		if response.Code != http.StatusOK {
			t.Fatalf("PUT audience = %d %s", response.Code, response.Body.String())
		}
		read := perform(t, owner, http.MethodGet, "/v1/sessions/"+session+"/audience", nil)
		var body struct {
			AutoWake bool `json:"autoWake"`
		}
		if err := json.Unmarshal(read.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.AutoWake != autoWake {
			t.Fatalf("the endpoint reports autoWake %v after setting it %v", body.AutoWake, autoWake)
		}
	}

	set(t, true)
	stored, err := store.GetAudience(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.AutoWake {
		t.Fatal("the endpoint accepted the switch and the store did not receive it")
	}

	// Closing it again is the action an owner takes when a peer misbehaves,
	// and it has to reach the store too.
	set(t, false)
	stored, err = store.GetAudience(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AutoWake {
		t.Error("closing the switch did not reach the store")
	}
	if !stored.AcceptMessages {
		t.Error("closing the wake switch also closed the inbox")
	}
}
