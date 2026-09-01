package api

import (
	"context"
	"encoding/json"
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
