package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"sort"
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

	messages, err := store.Inbox(context.Background(), session, 10, registry.InboxStart)
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
	store, owner, _ := testSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	from := openOutbound(t, store, owner, "sender")

	response := perform(t, owner, http.MethodPost, "/v1/messages", map[string]string{
		"to": peerNodeID + "/codex:their-session", "from": from, "body": "look at this",
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
	store, owner, _ := testSurfaces(t)
	from := openOutbound(t, store, owner, "sender")
	response := perform(t, owner, http.MethodPost, "/v1/messages", map[string]string{
		"to": "node_neverpaired00000/codex:whatever", "from": from, "body": "hello",
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
	inbox, err := store.Inbox(context.Background(), session, 10, registry.InboxStart)
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

	inbox, err := store.Inbox(context.Background(), session, 10, registry.InboxStart)
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
	remaining, err := store.Inbox(context.Background(), session, 10, registry.InboxStart)
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
	// Exact: asserting only that it contains "/" would pass for a value
	// qualified by some other node, which is the thing being ruled out.
	if want := testNodeID + "/" + id; decoded.Messages[0].From != want {
		t.Errorf("from = %q, want %q", decoded.Messages[0].From, want)
	}
}

// openOutbound seeds a local session and opens its outbound gate, returning the
// id a caller passes as `from`.
func openOutbound(t *testing.T, store *registry.Registry, handler http.Handler, name string) string {
	t.Helper()
	id := seedSession(t, store, name)
	if response := perform(t, handler, http.MethodPut, "/v1/sessions/"+id+"/audience",
		map[string]any{"mode": "none", "allowOutbound": true}); response.Code != http.StatusOK {
		t.Fatalf("open outbound for %s = %d %s", id, response.Code, response.Body.String())
	}
	return id
}

// The gate was checked only in agenthub-mcp, a client of this node. Any local
// process could post here directly and bypass it — and this node cannot tell the
// owner's CLI from an agent talked into calling it. So a message that leaves the
// machine must be attributed to a local session, and that session's gate
// decides.
func TestAMessageLeavingTheMachineNeedsAnOpenGate(t *testing.T) {
	store, owner, _ := testSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	remote := peerNodeID + "/codex:theirs"

	t.Run("no sender named is refused", func(t *testing.T) {
		response := perform(t, owner, http.MethodPost, "/v1/messages",
			map[string]string{"to": remote, "body": "hello"})
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "OUTBOUND_NEEDS_SENDER") {
			t.Fatalf("response = %d %s; want 400 OUTBOUND_NEEDS_SENDER", response.Code, response.Body.String())
		}
		if !strings.Contains(response.Body.String(), "--from") {
			t.Errorf("the refusal does not say how to fix it: %s", response.Body.String())
		}
	})

	t.Run("a sender with the gate closed is refused", func(t *testing.T) {
		closed := seedSession(t, store, "closed-gate")
		response := perform(t, owner, http.MethodPost, "/v1/messages",
			map[string]string{"to": remote, "from": closed, "body": "hello"})
		if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "OUTBOUND_CLOSED") {
			t.Fatalf("response = %d %s; want 403 OUTBOUND_CLOSED", response.Code, response.Body.String())
		}
		for _, want := range []string{"--outbound", "another machine"} {
			if !strings.Contains(response.Body.String(), want) {
				t.Errorf("the refusal does not mention %q: %s", want, response.Body.String())
			}
		}
	})

	t.Run("a sender with the gate open is queued", func(t *testing.T) {
		open := openOutbound(t, store, owner, "open-gate")
		response := perform(t, owner, http.MethodPost, "/v1/messages",
			map[string]string{"to": remote, "from": open, "body": "hello"})
		if response.Code != http.StatusAccepted {
			t.Fatalf("response = %d %s; want 202", response.Code, response.Body.String())
		}
	})

	t.Run("a sender qualified with another node is refused even with its gate open", func(t *testing.T) {
		// Otherwise the gate decides on a session the caller has just said is
		// elsewhere, and the outbound record names a third node as the sender.
		open := openOutbound(t, store, owner, "open-gate-elsewhere")
		response := perform(t, owner, http.MethodPost, "/v1/messages",
			map[string]string{"to": remote, "from": "node_other00000000000/" + open, "body": "hello"})
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "verify") {
			t.Fatalf("response = %d %s; want 400 saying the claim cannot be verified",
				response.Code, response.Body.String())
		}
	})

	t.Run("a sender this node has no session for is refused by name", func(t *testing.T) {
		response := perform(t, owner, http.MethodPost, "/v1/messages",
			map[string]string{"to": remote, "from": "claude:nobody-here", "body": "hello"})
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), "OUTBOUND_SENDER_UNKNOWN") {
			t.Fatalf("response = %d %s; want 404 OUTBOUND_SENDER_UNKNOWN", response.Code, response.Body.String())
		}
		if !strings.Contains(response.Body.String(), "from: ") || !strings.Contains(response.Body.String(), "nobody-here") {
			t.Errorf("the refusal does not say which name it is about: %s", response.Body.String())
		}
	})

	t.Run("a local destination is not gated", func(t *testing.T) {
		// Nothing leaves the machine, so the gate does not apply — and the
		// sender need not be named at all, as before.
		local := seedSession(t, store, "local-target")
		if response := perform(t, owner, http.MethodPut, "/v1/sessions/"+local+"/audience",
			map[string]any{"mode": "none", "acceptMessages": true}); response.Code != http.StatusOK {
			t.Fatal(response.Body.String())
		}
		response := perform(t, owner, http.MethodPost, "/v1/messages",
			map[string]string{"to": local, "body": "hello"})
		if response.Code != http.StatusCreated {
			t.Fatalf("a local send was gated: %d %s", response.Code, response.Body.String())
		}
	})
}

// Pages chain by the cursor the previous one handed out. Every message is seen
// once across the pages, and a cursor this node did not issue is refused.
func TestTheInboxPagesWithNext(t *testing.T) {
	store, owner := testServer(t)
	id := seedSession(t, store, "paged")
	if response := perform(t, owner, http.MethodPut, "/v1/sessions/"+id+"/audience",
		map[string]any{"mode": "none", "acceptMessages": true}); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	for i := 0; i < 12; i++ {
		if response := perform(t, owner, http.MethodPost, "/v1/messages",
			map[string]string{"to": id, "body": fmt.Sprintf("m%02d", i)}); response.Code != http.StatusCreated {
			t.Fatalf("send %d = %d %s", i, response.Code, response.Body.String())
		}
	}
	type page struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
		Next string `json:"next"`
	}
	seen := map[string]bool{}
	after := ""
	pages := 0
	for {
		response := perform(t, owner, http.MethodGet, "/v1/inbox/"+id+"?limit=5&after="+url.QueryEscape(after), nil)
		if response.Code != http.StatusOK {
			t.Fatalf("page = %d %s", response.Code, response.Body.String())
		}
		var p page
		if err := json.Unmarshal(response.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		pages++
		for _, m := range p.Messages {
			if seen[m.ID] {
				t.Errorf("message %s appeared twice", m.ID)
			}
			seen[m.ID] = true
		}
		if p.Next == "" {
			break
		}
		after = p.Next
		if pages > 5 {
			t.Fatal("paging did not end")
		}
	}
	if len(seen) != 12 || pages != 3 {
		t.Errorf("saw %d messages over %d pages; want 12 over 3", len(seen), pages)
	}
	bad := perform(t, owner, http.MethodGet, "/v1/inbox/"+id+"?after=garbage", nil)
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "cursor") {
		t.Errorf("garbage cursor = %d %s; want 400 naming the cursor", bad.Code, bad.Body.String())
	}
}

// A page boundary that falls on a message whose id a peer chose must still be
// crossable. The wire refuses an awkward id now, but a row already in a
// database was written before that check, and the node must not answer its own
// cursor with a 400 — that is the failure this change exists to remove, and it
// is cheaper to reach with small messages than with large ones.
func TestAPageBoundaryOnAPeerChosenIDIsCrossable(t *testing.T) {
	store, owner := testServer(t)
	id := seedSession(t, store, "awkward-boundary")
	if response := perform(t, owner, http.MethodPut, "/v1/sessions/"+id+"/audience",
		map[string]any{"mode": "none", "acceptMessages": true}); response.Code != http.StatusOK {
		t.Fatal(response.Body.String())
	}
	// Straight into the store, the way an older build stored what a peer sent.
	for i, messageID := range []string{"msg with space", "msg_\ttab", "msg_é"} {
		if _, err := store.CreateMessage(context.Background(), model.Message{
			ID: messageID, To: id, From: "codex:sender", DestinationNodeID: testNodeID,
			Body: fmt.Sprintf("m%d", i),
		}); err != nil {
			t.Fatalf("seed %q: %v", messageID, err)
		}
	}
	var page struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
		Next string `json:"next"`
	}
	first := perform(t, owner, http.MethodGet, "/v1/inbox/"+id+"?limit=1", nil)
	if first.Code != http.StatusOK {
		t.Fatalf("page 1 = %d %s", first.Code, first.Body.String())
	}
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Next == "" {
		t.Fatal("no cursor was issued for a full page")
	}
	second := perform(t, owner, http.MethodGet, "/v1/inbox/"+id+"?limit=1&after="+url.QueryEscape(page.Next), nil)
	if second.Code != http.StatusOK {
		t.Fatalf("the node refused a cursor it issued: %d %s", second.Code, second.Body.String())
	}
	if err := json.Unmarshal(second.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 || page.Messages[0].ID == "msg with space" {
		t.Errorf("page 2 = %+v; want the message after the first", page.Messages)
	}
}

// queueForPeerViaAPI sends one message to a peer through the owner API and
// returns the id it was queued under.
func queueForPeerViaAPI(t *testing.T, owner http.Handler, from, body string) string {
	t.Helper()
	response := perform(t, owner, http.MethodPost, "/v1/messages", map[string]string{
		"to": peerNodeID + "/codex:their-session", "from": from, "body": body,
	})
	if response.Code != http.StatusAccepted {
		t.Fatalf("queue = %d %s", response.Code, response.Body.String())
	}
	var queued struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &queued); err != nil {
		t.Fatal(err)
	}
	// Two in the same millisecond tie, and the tie-break is a random id — so a
	// test asserting an order would be asserting nothing.
	time.Sleep(2 * time.Millisecond)
	return queued.ID
}

type outboundPage struct {
	Messages []struct {
		ID                string `json:"id"`
		DestinationNodeID string `json:"destinationNodeId"`
		To                string `json:"to"`
		From              string `json:"from"`
		State             string `json:"state"`
		Attempts          int    `json:"attempts"`
		LastError         string `json:"lastError"`
		Body              string `json:"body"`
		CreatedAt         string `json:"createdAt"`
		WakeHops          int    `json:"wakeHops"`
	} `json:"messages"`
	Next string `json:"next"`
}

func readOutboundPage(t *testing.T, owner http.Handler, query string) outboundPage {
	t.Helper()
	response := perform(t, owner, http.MethodGet, "/v1/outbound"+query, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /v1/outbound%s = %d %s", query, response.Code, response.Body.String())
	}
	var page outboundPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode %s: %v", response.Body.String(), err)
	}
	return page
}

// TestOutboundListAnswersNewestFirst is the endpoint the desktop reads. `ah
// send` answers "queued" and nothing else, so an owner without the id — the
// terminal is closed, or an agent sent it — had no way to ask what happened.
func TestOutboundListAnswersNewestFirst(t *testing.T) {
	store, owner, _ := testSurfaces(t)
	sender := newSender(t, peerNodeID)
	sender.pairWith(t, owner)
	from := openOutbound(t, store, owner, "sender")

	first := queueForPeerViaAPI(t, owner, from, "one")
	second := queueForPeerViaAPI(t, owner, from, "two")

	page := readOutboundPage(t, owner, "")
	if len(page.Messages) != 2 {
		t.Fatalf("listed %d rows; want 2", len(page.Messages))
	}
	if page.Messages[0].ID != second || page.Messages[1].ID != first {
		t.Fatalf("ids = %q, %q; want the newest first",
			page.Messages[0].ID, page.Messages[1].ID)
	}
	row := page.Messages[0]
	if row.State != string(registry.OutboundPending) || row.To != "codex:their-session" {
		t.Errorf("row = %#v; want the same fields the single lookup answers with", row)
	}
	// The destination node travels with the row: `to` is the session half
	// alone, so without it a reader cannot say which machine a message is
	// stuck on — which is the question an owner opens this list with.
	if row.DestinationNodeID != peerNodeID {
		t.Errorf("destinationNodeId = %q; want %q", row.DestinationNodeID, peerNodeID)
	}
	if row.CreatedAt == "" {
		t.Error("a row carries no time, so nothing can say when it was sent")
	}
	// No bodies. A page of fifty 32KB bodies is both more than this question
	// needs and enough to pass a reader's response cap — which is how the
	// answer to "what is jamming my outbox" becomes unreadable exactly when it
	// is wanted.
	if strings.Contains(bodyOf(t, owner, "/v1/outbound"), `"body"`) {
		t.Error("the listing carries message bodies")
	}
	// And the settled state travels: a refusal is the outcome the owner most
	// needs to see, and it is only ever visible here.
	if err := store.MarkOutbound(context.Background(), second, registry.OutboundRefused,
		"the addressed session does not accept messages"); err != nil {
		t.Fatal(err)
	}
	page = readOutboundPage(t, owner, "")
	if page.Messages[0].State != string(registry.OutboundRefused) ||
		page.Messages[0].Attempts != 1 ||
		!strings.Contains(page.Messages[0].LastError, "does not accept") {
		t.Fatalf("refused row = %#v; want the state, the attempt and the reason", page.Messages[0])
	}
}

// bodyOf reads a path as a string, for the assertions about what a body does
// not contain.
func bodyOf(t *testing.T, owner http.Handler, path string) string {
	t.Helper()
	return perform(t, owner, http.MethodGet, path, nil).Body.String()
}

// TestOutboundListPagesAndBoundsItself covers the three ways a caller can ask
// for the wrong thing.
func TestOutboundListPagesAndBoundsItself(t *testing.T) {
	store, owner, _ := testSurfaces(t)
	sender := newSender(t, peerNodeID)
	sender.pairWith(t, owner)
	from := openOutbound(t, store, owner, "sender")

	// Empty is an empty list, not an error and not a missing field.
	empty := readOutboundPage(t, owner, "")
	if len(empty.Messages) != 0 || empty.Next != "" {
		t.Fatalf("empty node answered %#v", empty)
	}
	if !strings.Contains(bodyOf(t, owner, "/v1/outbound"), `"messages"`) {
		t.Error("an empty answer omits the messages key, so a client cannot tell it from a failure")
	}

	oldest := queueForPeerViaAPI(t, owner, from, "one")
	middle := queueForPeerViaAPI(t, owner, from, "two")
	newest := queueForPeerViaAPI(t, owner, from, "three")

	page := readOutboundPage(t, owner, "?limit=2")
	if len(page.Messages) != 2 || page.Messages[0].ID != newest || page.Messages[1].ID != middle {
		t.Fatalf("first page = %#v", page.Messages)
	}
	if page.Next == "" {
		t.Fatal("a full page carries no cursor, so the rest is unreachable")
	}
	next := readOutboundPage(t, owner, "?limit=2&after="+url.QueryEscape(page.Next))
	if len(next.Messages) != 1 || next.Messages[0].ID != oldest {
		t.Fatalf("second page = %#v; want the oldest alone, nothing repeated", next.Messages)
	}
	if next.Next != "" {
		t.Error("a short page carries a cursor, which reads as more to come")
	}

	for _, bad := range []string{"?limit=0", "?limit=201", "?limit=x"} {
		if code := perform(t, owner, http.MethodGet, "/v1/outbound"+bad, nil).Code; code != http.StatusBadRequest {
			t.Errorf("GET /v1/outbound%s = %d; want 400", bad, code)
		}
	}
	// And the ends of the range are inside it. Only the refusals were covered,
	// so a bound written one step too tight would have refused a caller asking
	// for exactly one row, or for a full page, and nothing would have said so.
	for _, good := range []string{"?limit=1", "?limit=200"} {
		if code := perform(t, owner, http.MethodGet, "/v1/outbound"+good, nil).Code; code != http.StatusOK {
			t.Errorf("GET /v1/outbound%s = %d; want 200, the limit is inclusive", good, code)
		}
	}
	// A cursor this node did not issue is refused rather than silently read as
	// the start, which would answer a page the caller has already seen.
	if code := perform(t, owner, http.MethodGet, "/v1/outbound?after=nonsense", nil).Code; code != http.StatusBadRequest {
		t.Errorf("a forged cursor = %d; want 400", code)
	}
}

// TestOutboundListNarrowsToOneSession is the parameter the desktop needs. The
// window is opened from one session's row, but the list is the whole node's,
// so without this the front end filters client-side and pages for rows it
// throws away.
func TestOutboundListNarrowsToOneSession(t *testing.T) {
	store, owner, _ := testSurfaces(t)
	sender := newSender(t, peerNodeID)
	sender.pairWith(t, owner)
	mine := openOutbound(t, store, owner, "mine")
	theirs := openOutbound(t, store, owner, "theirs")

	first := queueForPeerViaAPI(t, owner, mine, "one")
	queueForPeerViaAPI(t, owner, theirs, "not mine")
	second := queueForPeerViaAPI(t, owner, mine, "two")

	page := readOutboundPage(t, owner, "?session="+url.QueryEscape(mine))
	if len(page.Messages) != 2 {
		t.Fatalf("listed %d rows for one session; want its 2: %#v", len(page.Messages), page.Messages)
	}
	if page.Messages[0].ID != second || page.Messages[1].ID != first {
		t.Fatalf("ids = %q, %q; want this session's two, newest first",
			page.Messages[0].ID, page.Messages[1].ID)
	}
	// The qualified form of the same address is the same session, as it is
	// everywhere else a session is addressed.
	qualified := readOutboundPage(t, owner, "?session="+url.QueryEscape(testNodeID+"/"+mine))
	if len(qualified.Messages) != 2 {
		t.Errorf("the qualified address listed %d rows; want the same 2", len(qualified.Messages))
	}

	// Paging under the filter walks the filtered list, not the node's.
	firstPage := readOutboundPage(t, owner, "?limit=1&session="+url.QueryEscape(mine))
	if len(firstPage.Messages) != 1 || firstPage.Messages[0].ID != second || firstPage.Next == "" {
		t.Fatalf("first filtered page = %#v (next %q)", firstPage.Messages, firstPage.Next)
	}
	next := readOutboundPage(t, owner,
		"?limit=1&session="+url.QueryEscape(mine)+"&after="+url.QueryEscape(firstPage.Next))
	if len(next.Messages) != 1 || next.Messages[0].ID != first {
		t.Fatalf("second filtered page = %#v; want this session's older row, not the other session's",
			next.Messages)
	}
	end := readOutboundPage(t, owner,
		"?limit=1&session="+url.QueryEscape(mine)+"&after="+url.QueryEscape(next.Next))
	if len(end.Messages) != 0 {
		t.Fatalf("page past the end of the filtered list = %#v; want nothing", end.Messages)
	}
}

// TestOutboundListRefusesASessionThatIsNotLocal keeps the filter from
// answering an empty page for an address this node cannot own. An empty list
// reads as "this session sent nothing", which is a different fact.
func TestOutboundListRefusesASessionThatIsNotLocal(t *testing.T) {
	_, owner, _ := testSurfaces(t)

	remote := perform(t, owner, http.MethodGet,
		"/v1/outbound?session="+url.QueryEscape("node_somewhere_else/codex:theirs"), nil)
	if remote.Code != http.StatusNotFound || !strings.Contains(remote.Body.String(), "UNKNOWN_NODE") {
		t.Fatalf("another node's session = %d %s; want 404 UNKNOWN_NODE, the answer /v1/wakes gives",
			remote.Code, remote.Body.String())
	}
	malformed := perform(t, owner, http.MethodGet, "/v1/outbound?session=nonsense", nil)
	if malformed.Code != http.StatusBadRequest {
		t.Fatalf("a malformed session = %d %s; want 400", malformed.Code, malformed.Body.String())
	}
}

// TestABlankSessionFilterIsRefusedNotIgnored covers the value that used to be
// trimmed away: a caller that asked to narrow the list was handed the whole
// node's, which reads as the session's own traffic and is not. The two
// listings answer alike, because they are the same mistake.
func TestABlankSessionFilterIsRefusedNotIgnored(t *testing.T) {
	_, owner, _ := testSurfaces(t)

	for _, path := range []string{
		"/v1/outbound?session=",
		"/v1/outbound?session=%20%20",
		"/v1/wakes?session=",
		"/v1/wakes?session=%20%20",
	} {
		response := perform(t, owner, http.MethodGet, path, nil)
		if response.Code != http.StatusBadRequest ||
			!strings.Contains(response.Body.String(), "INVALID_REQUEST") {
			t.Errorf("GET %s = %d %s; want 400 INVALID_REQUEST rather than the whole node's list",
				path, response.Code, response.Body.String())
		}
	}
	// Absent still means everything: nobody asked to narrow anything.
	for _, path := range []string{"/v1/outbound", "/v1/wakes"} {
		if code := perform(t, owner, http.MethodGet, path, nil).Code; code != http.StatusOK {
			t.Errorf("GET %s = %d; want 200 when no filter was asked for", path, code)
		}
	}
}

// TestASessionFilterGivenTwiceIsRefused covers the query the CLI already
// refuses on its own side: `--session` twice is an error there, so `?session=`
// twice cannot quietly keep the first value and drop the second. Two different
// sessions asked for is not a listing anyone can read.
func TestASessionFilterGivenTwiceIsRefused(t *testing.T) {
	_, owner, _ := testSurfaces(t)

	for _, path := range []string{
		"/v1/outbound?session=codex:a&session=codex:b",
		"/v1/wakes?session=codex:a&session=codex:b",
	} {
		response := perform(t, owner, http.MethodGet, path, nil)
		if response.Code != http.StatusBadRequest ||
			!strings.Contains(response.Body.String(), "INVALID_REQUEST") ||
			!strings.Contains(response.Body.String(), "more than once") {
			t.Errorf("GET %s = %d %s; want 400 INVALID_REQUEST naming the repeat",
				path, response.Code, response.Body.String())
		}
	}
}

// TestTheListRowCarriesEveryFieldTheSingleLookupDoes pins the promise
// outboundSummary makes in its own comment. The summary is written out by
// hand, so a field added to registry.OutboundMessage reaches GET
// /v1/outbound/{id} and silently never reaches the list — and nothing else in
// the suite notices, because every existing assertion names the fields it
// already knows.
func TestTheListRowCarriesEveryFieldTheSingleLookupDoes(t *testing.T) {
	store, owner, _ := testSurfaces(t)
	sender := newSender(t, peerNodeID)
	sender.pairWith(t, owner)
	from := openOutbound(t, store, owner, "sender")

	// Every omitempty field filled, or the comparison would pass by both sides
	// omitting the same key. WakeHops is set here rather than earned through a
	// wake because what is under test is the shape of the answer.
	queued, err := store.QueueOutbound(context.Background(), registry.OutboundMessage{
		DestinationNodeID: peerNodeID,
		To:                "codex:their-session",
		From:              testNodeID + "/" + from,
		Body:              "a body the list does not carry",
		WakeHops:          2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkOutbound(context.Background(), queued.ID, registry.OutboundRefused,
		"the addressed session does not accept messages"); err != nil {
		t.Fatal(err)
	}

	single := perform(t, owner, http.MethodGet, "/v1/outbound/"+queued.ID, nil)
	if single.Code != http.StatusOK {
		t.Fatalf("GET /v1/outbound/%s = %d %s", queued.ID, single.Code, single.Body.String())
	}
	var one map[string]json.RawMessage
	if err := json.Unmarshal(single.Body.Bytes(), &one); err != nil {
		t.Fatal(err)
	}
	listed := perform(t, owner, http.MethodGet, "/v1/outbound", nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("GET /v1/outbound = %d %s", listed.Code, listed.Body.String())
	}
	var page struct {
		Messages []map[string]json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != 1 {
		t.Fatalf("listed %d rows; want the one queued", len(page.Messages))
	}

	// The list deliberately carries no body, and only that.
	if _, present := page.Messages[0]["body"]; present {
		t.Error("the list row carries a body")
	}
	// The values travel too, not only the keys: wakeHops is how far an
	// automatic exchange has gone, and a row that always reads zero would say
	// the opposite of what happened.
	typed := readOutboundPage(t, owner, "")
	if typed.Messages[0].WakeHops != 2 || typed.Messages[0].DestinationNodeID != peerNodeID {
		t.Errorf("row = %#v; want wakeHops 2 and the destination node", typed.Messages[0])
	}

	row := keysOf(page.Messages[0])
	row = append(row, "body")
	sort.Strings(row)
	lookup := keysOf(one)
	sort.Strings(lookup)
	if !slices.Equal(row, lookup) {
		t.Errorf("the list row and the single lookup answer with different fields:\n"+
			" list + body: %v\n lookup:      %v\n"+
			"a field added to registry.OutboundMessage has to be added to outboundSummary too",
			row, lookup)
	}
}

// keysOf is the top-level field set of one JSON object.
func keysOf(object map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	return keys
}
