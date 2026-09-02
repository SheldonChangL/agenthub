package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
)

// acceptingLocalSession stores a session on this node that takes messages.
func acceptingLocalSession(t *testing.T, store *registry.Registry, handler http.Handler, id string) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	provider, providerSessionID, _ := strings.Cut(id, ":")
	if _, err := store.UpsertSession(ctx, model.Session{
		ID: id, Provider: model.Provider(provider), ProviderSessionID: providerSessionID,
		Management: model.Managed, Status: model.StatusIdle, StatusSource: "test",
		LastSeenAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	allow := perform(t, handler, http.MethodPut, "/v1/sessions/"+id+"/audience",
		map[string]any{"mode": "none", "acceptMessages": true})
	if allow.Code != http.StatusOK {
		t.Fatalf("opt in = %d %s", allow.Code, allow.Body.String())
	}
	return id
}

// messageEnvelope builds what a paired peer would put on the wire.
func (s sender) messageEnvelope(t *testing.T, recipient, messageID, to, from, body string) protocol.Envelope {
	t.Helper()
	envelope, err := protocol.NewMessageEnvelope(s.nodeID, recipient, protocol.MessagePayload{
		MessageID: messageID, To: to, From: from, Body: body, SentAt: time.Now().UTC(),
	}, s.signer)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func decodeAck(t *testing.T, body []byte) protocol.AckPayload {
	t.Helper()
	var ack protocol.AckPayload
	if err := json.Unmarshal(body, &ack); err != nil {
		t.Fatalf("decode ack: %v (body %s)", err, body)
	}
	return ack
}

// TestAMessageFromAPairedPeerIsQueued is issue #16's first acceptance item.
func TestAMessageFromAPairedPeerIsQueued(t *testing.T) {
	store, owner, peers := testSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	session := acceptingLocalSession(t, store, owner, "codex:inbox-target")

	envelope := peer.messageEnvelope(t, testNodeID, "msg_from_peer_1", session, "codex:sender", "review the schema")
	response := perform(t, peers, http.MethodPost, "/v1/messages", envelope)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if ack := decodeAck(t, response.Body.Bytes()); ack.Status != protocol.AckQueued {
		t.Fatalf("ack = %+v; want queued", ack)
	}

	inbox := perform(t, owner, http.MethodGet, "/v1/inbox/"+session, nil)
	if inbox.Code != http.StatusOK {
		t.Fatalf("inbox = %d %s", inbox.Code, inbox.Body.String())
	}
	if body := inbox.Body.String(); !strings.Contains(body, "review the schema") {
		t.Fatalf("inbox = %s; the message is not there", body)
	}
	// The stored sender must name the node the envelope was actually signed by,
	// not whatever the payload claimed.
	if body := inbox.Body.String(); !strings.Contains(body, peerNodeID) {
		t.Fatalf("inbox = %s; the sender is not attributed to the sending node", body)
	}
}

// TestAnUnpairedSenderGetsNothing keeps the message endpoint to the same rule
// as the heartbeat endpoint: a stranger learns nothing, including whether the
// addressed session exists.
func TestAnUnpairedSenderGetsNothing(t *testing.T) {
	store, owner, peers := testSurfaces(t)
	session := acceptingLocalSession(t, store, owner, "codex:inbox-target")
	stranger := newSender(t, peerNodeID)

	envelope := stranger.messageEnvelope(t, testNodeID, "msg_stranger", session, "", "hello")
	response := perform(t, peers, http.MethodPost, "/v1/messages", envelope)
	if response.Code != http.StatusForbidden {
		t.Fatalf("response = %d %s; an unpaired sender must be refused", response.Code, response.Body.String())
	}
	unpaired := response.Body.String()

	// A paired sender with the wrong key must be refused identically.
	stranger.pairWith(t, owner)
	impostor := newSender(t, peerNodeID)
	forged := impostor.messageEnvelope(t, testNodeID, "msg_forged", session, "", "hello")
	forgedResponse := perform(t, peers, http.MethodPost, "/v1/messages", forged)
	if forgedResponse.Code != http.StatusForbidden {
		t.Fatalf("a forged signature was accepted: %d", forgedResponse.Code)
	}
	if forgedResponse.Body.String() != unpaired {
		t.Errorf("refusals differ and can be used to probe the trust store:\n unpaired: %s\n forged:   %s",
			unpaired, forgedResponse.Body.String())
	}

	inbox := perform(t, owner, http.MethodGet, "/v1/inbox/"+session, nil)
	if strings.Contains(inbox.Body.String(), "hello") {
		t.Fatal("a refused message reached the inbox")
	}
}

// TestARefusedSessionAnswersClearly is issue #16's third acceptance item, and
// the reason every refusal shares one sentence: a sender that could tell "no
// such session" from "that session declines messages" could map the recipient's
// sessions by guessing addresses.
func TestARefusedSessionAnswersClearly(t *testing.T) {
	store, owner, peers := testSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)

	// A session that exists but has not opted in.
	ctx := context.Background()
	now := time.Now().UTC()
	if _, err := store.UpsertSession(ctx, model.Session{
		ID: "codex:declines", Provider: model.ProviderCodex, ProviderSessionID: "declines",
		Management: model.Managed, Status: model.StatusIdle, StatusSource: "test",
		LastSeenAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	declining := peer.messageEnvelope(t, testNodeID, "msg_declined", "codex:declines", "", "hello")
	declined := perform(t, peers, http.MethodPost, "/v1/messages", declining)
	if declined.Code != http.StatusOK {
		t.Fatalf("response = %d %s", declined.Code, declined.Body.String())
	}
	declinedAck := decodeAck(t, declined.Body.Bytes())
	if declinedAck.Status != protocol.AckRefused {
		t.Fatalf("ack = %+v; want refused", declinedAck)
	}
	if declinedAck.Reason == "" {
		t.Error("a refusal came back without a reason")
	}

	// A session that does not exist at all must answer identically.
	missing := peer.messageEnvelope(t, testNodeID, "msg_missing", "codex:not-here", "", "hello")
	missingResponse := perform(t, peers, http.MethodPost, "/v1/messages", missing)
	missingAck := decodeAck(t, missingResponse.Body.Bytes())
	if missingAck.Status != protocol.AckRefused {
		t.Fatalf("ack = %+v; want refused", missingAck)
	}
	if missingAck.Reason != declinedAck.Reason {
		t.Errorf("refusals differ and map the recipient's sessions:\n declines: %q\n missing:  %q",
			declinedAck.Reason, missingAck.Reason)
	}
}

// TestARedeliveryDoesNotDuplicate covers the case a lost ack creates. The
// sender retries; the reader must not end up with two of the same message.
func TestARedeliveryDoesNotDuplicate(t *testing.T) {
	store, owner, peers := testSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	session := acceptingLocalSession(t, store, owner, "codex:inbox-target")

	envelope := peer.messageEnvelope(t, testNodeID, "msg_repeat", session, "", "only once")
	first := perform(t, peers, http.MethodPost, "/v1/messages", envelope)
	if ack := decodeAck(t, first.Body.Bytes()); ack.Status != protocol.AckQueued {
		t.Fatalf("first ack = %+v", ack)
	}
	second := perform(t, peers, http.MethodPost, "/v1/messages", envelope)
	if ack := decodeAck(t, second.Body.Bytes()); ack.Status != protocol.AckDuplicate {
		t.Fatalf("second ack = %+v; want duplicate", ack)
	}

	messages, err := store.Inbox(context.Background(), session, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("inbox holds %d copies; a redelivery must not duplicate", len(messages))
	}
}

// TestSendToAPeerQueuesRatherThanDelivers is issue #16's second acceptance
// item. `ah send` succeeding must not be read as "delivered" or "read".
func TestSendToAPeerQueuesRatherThanDelivers(t *testing.T) {
	_, owner, _ := testSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)

	response := perform(t, owner, http.MethodPost, "/v1/messages", map[string]string{
		"to": peerNodeID + "/codex:their-session", "body": "look at this",
	})
	if response.Code != http.StatusAccepted {
		t.Fatalf("response = %d %s; want 202 Accepted, not a created-and-delivered 201",
			response.Code, response.Body.String())
	}
	var queued struct {
		ID    string `json:"id"`
		State string `json:"state"`
		Note  string `json:"note"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &queued); err != nil {
		t.Fatal(err)
	}
	if queued.State != string(registry.OutboundPending) {
		t.Errorf("state = %q; a freshly queued message is pending", queued.State)
	}
	if queued.Note == "" {
		t.Error("the response does not say that queued is not delivered")
	}

	status := perform(t, owner, http.MethodGet, "/v1/outbound/"+queued.ID, nil)
	if status.Code != http.StatusOK {
		t.Fatalf("status = %d %s", status.Code, status.Body.String())
	}
	if !strings.Contains(status.Body.String(), string(registry.OutboundPending)) {
		t.Errorf("status = %s; want it still pending", status.Body.String())
	}
}

// TestSendToAnUnpairedNodeIsRefused keeps the queue from filling with messages
// that can never be delivered.
func TestSendToAnUnpairedNodeIsRefused(t *testing.T) {
	_, owner, _ := testSurfaces(t)
	response := perform(t, owner, http.MethodPost, "/v1/messages", map[string]string{
		"to": "node_neverpaired00000/codex:whatever", "body": "hello",
	})
	if response.Code != http.StatusNotFound {
		t.Fatalf("response = %d %s; want 404", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "UNKNOWN_NODE") {
		t.Errorf("body = %s; want UNKNOWN_NODE", response.Body.String())
	}
}

// TestALocalSendStillBehavesAsBefore keeps the change from altering the case
// that already worked.
func TestALocalSendStillBehavesAsBefore(t *testing.T) {
	store, owner, _ := testSurfaces(t)
	session := acceptingLocalSession(t, store, owner, "codex:inbox-target")

	response := perform(t, owner, http.MethodPost, "/v1/messages", map[string]string{
		"to": session, "from": "claude:source", "body": "local note",
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("response = %d %s; a local send must still answer 201", response.Code, response.Body.String())
	}
	inbox := perform(t, owner, http.MethodGet, "/v1/inbox/"+session, nil)
	if !strings.Contains(inbox.Body.String(), "local note") {
		t.Fatalf("inbox = %s", inbox.Body.String())
	}
}

// TestAnUnreadableTrustStoreAnswersRetriably covers the first thing that breaks
// when a recipient's database is unavailable: the trust lookup.
//
// It used to answer 403 "not a trusted node", which tells a legitimate paired
// peer it is no longer paired. The database being unreadable is this node
// having a bad moment, not a decision about the sender, so it answers 5xx and
// the sender leaves the message queued.
//
// The storage failure further down the same handler is covered at the registry
// layer by TestATransientStoreFailureIsNotARefusal — closing the database here
// cannot reach it, because the trust lookup fails first.
func TestAnUnreadableTrustStoreAnswersRetriably(t *testing.T) {
	store, owner, peers := testSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	session := acceptingLocalSession(t, store, owner, "codex:inbox-target")

	// Take the database away, which is what a lock or a full disk looks like
	// from the handler's point of view.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	envelope := peer.messageEnvelope(t, testNodeID, "msg_during_failure", session, "", "important")
	response := perform(t, peers, http.MethodPost, "/v1/messages", envelope)
	if response.Code == http.StatusOK {
		var ack protocol.AckPayload
		_ = json.Unmarshal(response.Body.Bytes(), &ack)
		t.Fatalf("a storage failure answered 200 %s; the sender would settle this permanently and lose the message",
			ack.Status)
	}
	if response.Code < 500 {
		t.Fatalf("response = %d %s; a transient failure must be retriable", response.Code, response.Body.String())
	}
}

// TestATakenIDIsRefusedLikeAnythingElse closes an oracle over the inbox's id
// namespace: an unscoped duplicate check would tell a peer whether an id exists
// anywhere here, including ids from local sends and from other peers.
func TestATakenIDIsRefusedLikeAnythingElse(t *testing.T) {
	store, owner, peers := testSurfaces(t)
	first := newSender(t, "node_peeraaaaaaaaaaaa")
	first.pairWith(t, owner)
	second := newSender(t, "node_peerbbbbbbbbbbbb")
	second.pairWith(t, owner)
	session := acceptingLocalSession(t, store, owner, "codex:inbox-target")

	taken := first.messageEnvelope(t, testNodeID, "msg_contested", session, "", "mine")
	if response := perform(t, peers, http.MethodPost, "/v1/messages", taken); response.Code != http.StatusOK {
		t.Fatalf("first message = %d %s", response.Code, response.Body.String())
	}

	// A different peer reusing that id.
	collision := second.messageEnvelope(t, testNodeID, "msg_contested", session, "", "theirs")
	collisionResponse := perform(t, peers, http.MethodPost, "/v1/messages", collision)
	collisionAck := decodeAck(t, collisionResponse.Body.Bytes())
	if collisionAck.Status != protocol.AckRefused {
		t.Fatalf("ack = %+v; want refused", collisionAck)
	}

	// And a session that simply declines, from the same peer.
	declining := acceptingLocalSession(t, store, owner, "codex:other")
	if err := store.SetAudience(context.Background(), declining, model.Audience{Mode: model.AudienceNone}); err != nil {
		t.Fatal(err)
	}
	declined := second.messageEnvelope(t, testNodeID, "msg_fresh", declining, "", "hello")
	declinedAck := decodeAck(t, perform(t, peers, http.MethodPost, "/v1/messages", declined).Body.Bytes())
	if declinedAck.Status != protocol.AckRefused {
		t.Fatalf("ack = %+v; want refused", declinedAck)
	}

	if collisionAck.Reason != declinedAck.Reason {
		t.Errorf("a taken id is distinguishable from a declining session:\n taken:    %q\n declined: %q",
			collisionAck.Reason, declinedAck.Reason)
	}
	// The first peer's message must be untouched.
	inbox, err := store.Inbox(context.Background(), session, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 || inbox[0].Body != "mine" {
		t.Fatalf("inbox = %#v; the second peer overwrote or duplicated the first", inbox)
	}
}

// TestAForgedSenderLabelCannotNameAnotherNode pins attribution against a peer
// that lies about where a message came from.
func TestAForgedSenderLabelCannotNameAnotherNode(t *testing.T) {
	store, owner, peers := testSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	session := acceptingLocalSession(t, store, owner, "codex:inbox-target")

	// The peer claims the message came from a session on a different node.
	forged := peer.messageEnvelope(t, testNodeID, "msg_forged_from", session,
		"node_someoneelse0000/claude:their-session", "trust me")
	if response := perform(t, peers, http.MethodPost, "/v1/messages", forged); response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}

	inbox, err := store.Inbox(context.Background(), session, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 {
		t.Fatalf("inbox = %#v", inbox)
	}
	from := inbox[0].From
	if strings.Contains(from, "node_someoneelse0000") {
		t.Fatalf("from = %q; a peer named another node as the sender", from)
	}
	if !strings.HasPrefix(from, peerNodeID) {
		t.Fatalf("from = %q; want it attributed to the node that signed the envelope", from)
	}
}

// TestAFullInboxDefersTheSenderRatherThanLosingTheMessage is the reason this
// answers 503 rather than a refusal.
//
// A refusal is settled permanently by the sender, so reporting a full inbox as
// one would destroy the message — the exact failure the bound exists to
// prevent. 503 leaves it queued, and the sender retries once the owner has read
// and cleared.
func TestAFullInboxDefersTheSenderRatherThanLosingTheMessage(t *testing.T) {
	store, owner, peers := testSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	session := acceptingLocalSession(t, store, owner, "codex:inbox-target")

	ctx := context.Background()
	for i := range registry.MaxInboxMessages {
		if _, err := store.StoreIncomingMessage(ctx, model.Message{
			ID: fmt.Sprintf("msg_fill_%d", i), To: session, From: peerNodeID,
			DestinationNodeID: testNodeID, Body: "filler",
		}); err != nil {
			t.Fatalf("filling at %d: %v", i, err)
		}
	}

	envelope := peer.messageEnvelope(t, testNodeID, "msg_deferred", session, "", "please keep me")
	response := perform(t, peers, http.MethodPost, "/v1/messages", envelope)

	if response.Code == http.StatusOK {
		ack := decodeAck(t, response.Body.Bytes())
		t.Fatalf("a full inbox answered 200 %s; the sender would settle this and lose the message", ack.Status)
	}
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("response = %d %s; want 503", response.Code, response.Body.String())
	}
	if retry := response.Header().Get("Retry-After"); retry == "" {
		t.Error("a deferral did not say when to come back")
	}

	// After the owner clears, the same message goes in.
	cleared := perform(t, owner, http.MethodDelete, "/v1/inbox/"+session, nil)
	if cleared.Code != http.StatusOK {
		t.Fatalf("clear = %d %s", cleared.Code, cleared.Body.String())
	}
	retried := perform(t, peers, http.MethodPost, "/v1/messages", envelope)
	if retried.Code != http.StatusOK {
		t.Fatalf("after clearing = %d %s", retried.Code, retried.Body.String())
	}
	if ack := decodeAck(t, retried.Body.Bytes()); ack.Status != protocol.AckQueued {
		t.Fatalf("ack = %+v; want queued", ack)
	}
}

// TestTheInboxReportsHowFullItIs keeps a filling session visible before senders
// start backing up.
func TestTheInboxReportsHowFullItIs(t *testing.T) {
	store, owner, peers := testSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	session := acceptingLocalSession(t, store, owner, "codex:inbox-target")

	envelope := peer.messageEnvelope(t, testNodeID, "msg_one", session, "", "hello")
	if response := perform(t, peers, http.MethodPost, "/v1/messages", envelope); response.Code != http.StatusOK {
		t.Fatalf("send = %d", response.Code)
	}

	response := perform(t, owner, http.MethodGet, "/v1/inbox/"+session, nil)
	var view struct {
		Held     int  `json:"held"`
		Capacity int  `json:"capacity"`
		Full     bool `json:"full"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Held != 1 {
		t.Errorf("held = %d; want 1", view.Held)
	}
	if view.Capacity != registry.MaxInboxMessages {
		t.Errorf("capacity = %d; want %d", view.Capacity, registry.MaxInboxMessages)
	}
	if view.Full {
		t.Error("an inbox with one message reports itself full")
	}
}

// TestDeletingOneMessageLeavesTheRest covers the finer control the owner needs
// when only some of an inbox has been dealt with.
func TestDeletingOneMessageLeavesTheRest(t *testing.T) {
	store, owner, peers := testSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	session := acceptingLocalSession(t, store, owner, "codex:inbox-target")

	for _, id := range []string{"msg_a", "msg_b"} {
		envelope := peer.messageEnvelope(t, testNodeID, id, session, "", "body "+id)
		if response := perform(t, peers, http.MethodPost, "/v1/messages", envelope); response.Code != http.StatusOK {
			t.Fatalf("send %s = %d", id, response.Code)
		}
	}

	deleted := perform(t, owner, http.MethodDelete, "/v1/inbox/"+session+"/msg_a", nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete = %d %s", deleted.Code, deleted.Body.String())
	}
	remaining, err := store.Inbox(context.Background(), session, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].ID != "msg_b" {
		t.Fatalf("remaining = %#v; want only msg_b", remaining)
	}

	// Deleting something that is not there says so rather than pretending.
	missing := perform(t, owner, http.MethodDelete, "/v1/inbox/"+session+"/msg_absent", nil)
	if missing.Code != http.StatusNotFound {
		t.Errorf("deleting an absent message = %d; want 404", missing.Code)
	}
}

// A message on the owner's API has nothing behind its `from` — no signature, no
// envelope. Letting it name another node would put an unverifiable claim in a
// local inbox, where a reader looks the fingerprint up by node id and would find
// the real one belonging to the node that was named.
func TestALocalSenderCannotClaimAnotherNode(t *testing.T) {
	store, owner := testServer(t)
	id := seedSession(t, store, "target")
	if response := perform(t, owner, http.MethodPut, "/v1/sessions/"+id+"/audience",
		map[string]any{"mode": "none", "acceptMessages": true}); response.Code != http.StatusOK {
		t.Fatalf("opt in = %d %s", response.Code, response.Body.String())
	}

	response := perform(t, owner, http.MethodPost, "/v1/messages", map[string]string{
		"to": id, "from": peerNodeID + "/claude:x", "body": "hello",
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("response = %d %s; a local caller must not claim another node",
			response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), "verify") {
		t.Errorf("the refusal does not say why: %s", response.Body.String())
	}

	// A local sender label is still fine.
	ok := perform(t, owner, http.MethodPost, "/v1/messages", map[string]string{
		"to": id, "from": id, "body": "hello",
	})
	if ok.Code != http.StatusOK && ok.Code != http.StatusAccepted && ok.Code != http.StatusCreated {
		t.Errorf("a local from was refused: %d %s", ok.Code, ok.Body.String())
	}
}

// The whole sender-rendering fix rests on this storage shape: a peer that names
// no sending session is stored as the bare proven node id, not as an empty
// string and not as anything the sender chose.
func TestASenderNamingNoSessionIsStoredAsItsProvenNodeID(t *testing.T) {
	if got := qualifiedSender(peerNodeID, ""); got != peerNodeID {
		t.Errorf("qualifiedSender(%q, \"\") = %q, want the bare node id", peerNodeID, got)
	}
	// A claimed session is kept, qualified by the proven node.
	if got := qualifiedSender(peerNodeID, "claude:theirs"); got != peerNodeID+"/claude:theirs" {
		t.Errorf("qualifiedSender with a claim = %q", got)
	}
	// A claim naming another node is ignored: only the proven id is used.
	if got := qualifiedSender(peerNodeID, "node_other00000000000/claude:x"); got != peerNodeID+"/claude:x" {
		t.Errorf("a cross-node claim survived: %q", got)
	}
}

// A locally queued message stores its sender qualified by this node.
//
// A bare session id is also a valid node id, so a reader holding one cannot tell
// whether the message was queued here or sent by a peer that chose a
// session-shaped id. Qualifying it removes the ambiguity at the source rather
// than asking every reader to guess.
func TestALocallyQueuedSenderIsQualifiedByThisNode(t *testing.T) {
	store, handler := testServer(t)
	id := seedSession(t, store, "local-from")
	if response := perform(t, handler, http.MethodPut, "/v1/sessions/"+id+"/audience",
		map[string]any{"mode": "none", "acceptMessages": true}); response.Code != http.StatusOK {
		t.Fatalf("opt in = %d %s", response.Code, response.Body.String())
	}

	created := perform(t, handler, http.MethodPost, "/v1/messages",
		map[string]string{"to": id, "from": id, "body": "hello"})
	if created.Code != http.StatusCreated {
		t.Fatalf("send = %d %s", created.Code, created.Body.String())
	}

	inbox := perform(t, handler, http.MethodGet, "/v1/inbox/"+id, nil)
	if inbox.Code != http.StatusOK {
		t.Fatalf("inbox = %d %s", inbox.Code, inbox.Body.String())
	}
	var decoded struct {
		Messages []struct {
			From string `json:"from"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(inbox.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Messages) != 1 {
		t.Fatalf("want 1 message, got %d", len(decoded.Messages))
	}
	if !strings.Contains(decoded.Messages[0].From, "/") {
		t.Errorf("from = %q; a bare session id cannot be told from a node id", decoded.Messages[0].From)
	}
	if !strings.HasSuffix(decoded.Messages[0].From, "/"+id) {
		t.Errorf("from = %q, want it to end with /%s", decoded.Messages[0].From, id)
	}
}
