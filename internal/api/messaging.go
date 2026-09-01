package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"agenthub.local/agenthub/internal/identity"
	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
)

// maxMessageEnvelope bounds what an unauthenticated caller can make this node
// read before anything has been verified.
const maxMessageEnvelope = 1 << 20

// receiveMessage accepts one message for a session on this node.
//
// The checks run in the same order as receiveHeartbeat's, and for the same
// reason: an unpaired sender is refused before its payload is read, and every
// refusal before that point answers identically so the endpoint cannot be used
// to find out who this owner has paired with.
//
// What arrives here is different from a heartbeat in one way that matters. A
// heartbeat carries metadata this node observed; a message carries what a
// person wrote. It is queued for the owner to read and nothing injects it into
// a provider — the boundary in docs/multinode-plan.md, unchanged by this
// endpoint existing.
func (s *Server) receiveMessage(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, maxMessageEnvelope)
	var envelope protocol.Envelope
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "envelope is not readable")
		return
	}
	if envelope.Type != protocol.TypeAgentMessage {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "envelope is not a message")
		return
	}

	refuse := func(reason string, err error) {
		log.Printf("message refused from %q: %s: %v", envelope.NodeID, reason, err)
		writeError(w, http.StatusForbidden, "MESSAGE_REFUSED", "message was not accepted")
	}

	peer, err := s.store.TrustedNode(r.Context(), envelope.NodeID)
	if err != nil {
		refuse("sender is not a trusted node", err)
		return
	}
	publicKey, err := identity.DecodePublicKey(peer.PublicKey)
	if err != nil {
		refuse("stored public key is unusable", err)
		return
	}
	if err := envelope.VerifyDirected(publicKey, envelope.NodeID, s.node.ID); err != nil {
		refuse("envelope is not authentically addressed to this node", err)
		return
	}

	var payload protocol.MessagePayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		refuse("payload is not a message", err)
		return
	}
	if err := payload.Validate(); err != nil {
		// The sender is authentic, so saying what is wrong with its payload is
		// safe and lets it stop sending something this node will never take.
		writeJSON(w, http.StatusOK, protocol.AckPayload{
			MessageID: payload.MessageID, Status: protocol.AckRefused, Reason: err.Error(),
		})
		return
	}

	ack := s.storeIncoming(r, envelope.NodeID, payload)
	writeJSON(w, http.StatusOK, ack)
}

// storeIncoming writes an authenticated message to the local inbox.
//
// Every refusal answers with the same reason text. A sender that could tell
// "no such session" from "that session declines messages" could map the
// recipient's sessions by sending messages to guessed addresses, which is the
// same oracle the heartbeat endpoint avoids at a different layer.
func (s *Server) storeIncoming(r *http.Request, senderNodeID string, payload protocol.MessagePayload) protocol.AckPayload {
	const refusal = "the addressed session does not accept messages from this node"

	// A redelivery must not become a second copy. The sender's id is carried
	// through to the stored row so this is answerable without a second table.
	existing, err := s.store.MessageByID(r.Context(), payload.MessageID)
	switch {
	case err == nil:
		if existing.From == qualifiedSender(senderNodeID, payload.From) && existing.To == payload.To {
			return protocol.AckPayload{MessageID: payload.MessageID, Status: protocol.AckDuplicate}
		}
		// The id is taken by a different message. Refusing is the safe answer:
		// accepting would either overwrite something or duplicate an id.
		return protocol.AckPayload{
			MessageID: payload.MessageID, Status: protocol.AckRefused,
			Reason: "that message id is already in use here",
		}
	case errors.Is(err, registry.ErrNotFound):
		// The normal path.
	default:
		log.Printf("could not check for a duplicate message: %v", err)
		return protocol.AckPayload{
			MessageID: payload.MessageID, Status: protocol.AckRefused, Reason: "this node could not store the message",
		}
	}

	stored, err := s.store.CreateMessage(r.Context(), model.Message{
		ID:                payload.MessageID,
		To:                payload.To,
		From:              qualifiedSender(senderNodeID, payload.From),
		DestinationNodeID: s.node.ID,
		Body:              payload.Body,
		CreatedAt:         time.Now().UTC(),
	})
	switch {
	case err == nil:
		log.Printf("queued a message from %q for %q", senderNodeID, stored.To)
		return protocol.AckPayload{MessageID: payload.MessageID, Status: protocol.AckQueued}
	case errors.Is(err, registry.ErrNotFound), errors.Is(err, registry.ErrInvalidSession):
		// Both mean "not going to happen": the session is absent, or its owner
		// has not opted it in. One answer for both, so neither can be told from
		// the other.
		return protocol.AckPayload{MessageID: payload.MessageID, Status: protocol.AckRefused, Reason: refusal}
	default:
		log.Printf("could not queue a message from %q: %v", senderNodeID, err)
		return protocol.AckPayload{
			MessageID: payload.MessageID, Status: protocol.AckRefused, Reason: "this node could not store the message",
		}
	}
}

// qualifiedSender labels a stored message with the node it actually came from.
//
// The sender's own From field is a label it chose, so it is only ever used for
// the session part. The node part comes from the verified envelope, which is
// the only thing here that was proven.
func qualifiedSender(senderNodeID, claimed string) string {
	if claimed == "" {
		return senderNodeID
	}
	address, err := protocol.ParseAddress(claimed, senderNodeID)
	if err != nil {
		return senderNodeID
	}
	return senderNodeID + model.SessionIDSeparator + address.SessionID
}

// queueForPeer records a message addressed to a session on another node.
//
// It answers 202 Accepted, not 201 Created, and the distinction is the whole
// contract: the message is queued here and nothing else has happened. The peer
// may be asleep. Answering as though it had arrived would make `ah send`
// success mean something it cannot know.
func (s *Server) queueForPeer(w http.ResponseWriter, r *http.Request, destination protocol.Address, from, body string) {
	queued, err := s.store.QueueOutbound(r.Context(), registry.OutboundMessage{
		DestinationNodeID: destination.NodeID,
		To:                destination.SessionID,
		From:              from,
		Body:              body,
	})
	switch {
	case err == nil:
		writeJSON(w, http.StatusAccepted, map[string]any{
			"id":                queued.ID,
			"destinationNodeId": queued.DestinationNodeID,
			"to":                destination.NodeID + model.SessionIDSeparator + destination.SessionID,
			"state":             string(queued.State),
			"queuedAt":          queued.CreatedAt,
			// Said plainly in the response, because the status code alone is
			// easy to read as "sent".
			"note": "queued for delivery; this does not mean it has been delivered or read",
		})
	case errors.Is(err, registry.ErrNotFound):
		writeError(w, http.StatusNotFound, "UNKNOWN_NODE",
			"node "+destination.NodeID+" is not paired with this node")
	case errors.Is(err, registry.ErrInvalidSession):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
	default:
		writeInternalError(w, "REGISTRY_ERROR", "could not queue the message", err)
	}
}

// outboundStatus reports what happened to a queued message.
//
// `ah send` deliberately cannot tell an owner whether a message arrived, so
// there has to be somewhere to find out afterwards.
func (s *Server) outboundStatus(w http.ResponseWriter, r *http.Request) {
	message, err := s.store.OutboundFor(r.Context(), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, registry.ErrOutboundNotFound) {
			writeError(w, http.StatusNotFound, "UNKNOWN_MESSAGE", err.Error())
			return
		}
		writeInternalError(w, "REGISTRY_ERROR", "could not read the message", err)
		return
	}
	writeJSON(w, http.StatusOK, message)
}
