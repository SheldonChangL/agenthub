package api

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"time"

	"agenthub.local/agenthub/internal/address"
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
		if !errors.Is(err, registry.ErrNotFound) {
			// The trust store could not be read. That is this node having a bad
			// moment, not a decision about the sender, and answering 403 would
			// tell a legitimate peer it is no longer trusted.
			writeInternalError(w, "TRUST_UNAVAILABLE", "could not check the sender", err)
			return
		}
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

	// The fingerprint travels with the message into a woken turn: it is the
	// one thing about a sender a person can check out of band, and a woken
	// agent has nobody present to ask for it.
	s.storeIncoming(w, r, envelope.NodeID, peer.Fingerprint, payload)
}

// storeIncoming writes an authenticated message to the local inbox and answers.
//
// A storage failure answers 5xx, not a refusal. The distinction is the whole
// difference between "this owner decided no" and "this machine is having a bad
// moment": the sender settles a refusal permanently and retries a failure, so
// reporting a locked database as a refusal silently destroys somebody's
// message. An earlier version of this did exactly that, while its own doc
// comment described the distinction it was not making.
//
// Every refusal that is a decision reads the same. A sender that could tell "no
// such session" from "that session declines messages" — or from "that id is
// taken" — could map this node by addressing guesses at it.
func (s *Server) storeIncoming(w http.ResponseWriter, r *http.Request, senderNodeID,
	senderFingerprint string, payload protocol.MessagePayload) {
	const refusal = "the addressed session does not accept messages from this node"

	stored, err := s.store.StoreIncomingMessage(r.Context(), model.Message{
		ID:                payload.MessageID,
		To:                payload.To,
		From:              qualifiedSender(senderNodeID, payload.From),
		DestinationNodeID: s.node.ID,
		Body:              payload.Body,
		CreatedAt:         time.Now().UTC(),
		// Taken from the sender. It can lie, and only downwards is useful to
		// it — which buys one more hop before the per-pair limit ends the
		// exchange regardless.
		WakeHops: payload.WakeHops,
	})
	switch {
	case err == nil && stored:
		log.Printf("queued a message from %q for %q", senderNodeID, payload.To)
		// The peer half of the wake path. Only on a fresh store: a redelivery
		// of something already held must not start a second turn, which is the
		// one way a sender could wake an agent as often as it liked without
		// passing any limit — every retry would be a new wake.
		s.considerWake(model.Message{
			ID: payload.MessageID, To: payload.To,
			From:              qualifiedSender(senderNodeID, payload.From),
			DestinationNodeID: s.node.ID, Body: payload.Body, WakeHops: payload.WakeHops,
		}, senderNodeID, senderFingerprint)
		writeJSON(w, http.StatusOK, protocol.AckPayload{
			MessageID: payload.MessageID, Status: protocol.AckQueued,
		})
	case err == nil:
		// Already held, from the same sender to the same session. A lost ack
		// must not cost the reader a second copy.
		writeJSON(w, http.StatusOK, protocol.AckPayload{
			MessageID: payload.MessageID, Status: protocol.AckDuplicate,
		})
	case errors.Is(err, registry.ErrInboxFull):
		// Not a decision and not a failure: a condition that clears when the
		// owner reads. Answering 503 leaves the message queued at the sender,
		// so a full inbox delays a message rather than destroying it — which is
		// the outcome this bound exists to prevent, not to cause.
		log.Printf("inbox full, deferring a message from %q: %v", senderNodeID, err)
		w.Header().Set("Retry-After", "300")
		writeError(w, http.StatusServiceUnavailable, "INBOX_FULL",
			"the addressed session's inbox is full; try again later")
	case errors.Is(err, registry.ErrNotFound), errors.Is(err, registry.ErrInvalidSession):
		// A decision, and the same one however it was reached.
		writeJSON(w, http.StatusOK, protocol.AckPayload{
			MessageID: payload.MessageID, Status: protocol.AckRefused, Reason: refusal,
		})
	default:
		// Not a decision. Answering 5xx leaves the message queued at the sender,
		// which is what a transient failure calls for.
		writeInternalError(w, "MESSAGE_STORE_FAILED", "could not store the message", err)
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
	parsed, err := address.ParseAddress(claimed, senderNodeID)
	if err != nil {
		return senderNodeID
	}
	return senderNodeID + model.SessionIDSeparator + parsed.SessionID
}

// queueForPeer records a message addressed to a session on another node.
//
// It answers 202 Accepted, not 201 Created, and the distinction is the whole
// contract: the message is queued here and nothing else has happened. The peer
// may be asleep. Answering as though it had arrived would make `ah send`
// success mean something it cannot know.
func (s *Server) queueForPeer(w http.ResponseWriter, r *http.Request, destination address.Address,
	from, senderSessionID, body string,
) {
	hops, chain := s.hopsFor(r.Context(), senderSessionID)
	queued, err := s.store.QueueOutbound(r.Context(), registry.OutboundMessage{
		DestinationNodeID: destination.NodeID,
		To:                destination.SessionID,
		From:              from,
		Body:              body,
		WakeHops:          hops,
	})
	switch {
	case err == nil:
		s.claimChain(r.Context(), chain)
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

// outboundSummary is one row of the outbound list.
//
// Spelled out rather than encoding registry.OutboundMessage directly, because
// the list deliberately carries no bodies and a struct with an empty `body`
// field would read as a message whose body was empty. The fields are otherwise
// the ones GET /v1/outbound/{id} answers with, under the same names.
type outboundSummary struct {
	ID                string    `json:"id"`
	DestinationNodeID string    `json:"destinationNodeId"`
	To                string    `json:"to"`
	From              string    `json:"from,omitempty"`
	State             string    `json:"state"`
	Attempts          int       `json:"attempts"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
	LastError         string    `json:"lastError,omitempty"`
	WakeHops          int       `json:"wakeHops,omitempty"`
}

// outboundList answers with what this node has queued for peers, newest first.
//
// `ah send` answers "queued" and nothing more, by design, so without a list the
// only way to find out what became of a message is to still have its id. An
// owner who has closed that terminal — or who is looking at a window rather
// than a terminal — had no way to ask at all.
func (s *Server) outboundList(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 || parsed > 200 {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "limit must be between 1 and 200")
			return
		}
		limit = parsed
	}
	after, err := registry.ParseOutboundCursor(r.URL.Query().Get("after"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST",
			"after is not a cursor this node issued; pass the `next` value from the previous page, or omit it to start over")
		return
	}
	messages, err := s.store.ListOutbound(r.Context(), limit, after)
	if err != nil {
		writeInternalError(w, "REGISTRY_ERROR", "registry unavailable", err)
		return
	}
	rows := make([]outboundSummary, 0, len(messages))
	for _, message := range messages {
		rows = append(rows, outboundSummary{
			ID: message.ID, DestinationNodeID: message.DestinationNodeID, To: message.To,
			From: message.From, State: string(message.State), Attempts: message.Attempts,
			CreatedAt: message.CreatedAt, UpdatedAt: message.UpdatedAt,
			LastError: message.LastError, WakeHops: message.WakeHops,
		})
	}
	page := map[string]any{"messages": rows}
	// A full page may not be the last; `next` says where the following one
	// begins. Absent on a short page, which is the end.
	if len(messages) == limit {
		page["next"] = registry.OutboundCursorAfter(messages[len(messages)-1]).String()
	}
	writeJSON(w, http.StatusOK, page)
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

// hopsFor is how far along an automatic exchange a message from this session
// is, and the wake it inherited that from.
//
// Reconstructed from the wake trail rather than told to us: nothing links an
// agent's decision to send to the message that woke it, because the agent
// calls agent_send like any other caller and no provider says why. So a send
// shortly after a wake is treated as caused by it.
//
// Wrong in both directions and deliberately so. A person typing immediately
// after a wake has their message counted as a hop, which costs them nothing
// but an earlier stop; an agent that thinks for longer than the window resets
// to zero, which is why the per-pair limit and not this is what ends a
// two-machine loop. Hops are for the cycle a pair limit cannot see.
//
// The claim is not spent here. It is spent by claimChain, once the message is
// stored — eagerly, a woken agent could zero its own chain by addressing one
// throwaway message at a session that does not exist.
func (s *Server) hopsFor(ctx context.Context, senderSessionID string) (int, string) {
	if senderSessionID == "" {
		return 0, ""
	}
	hops, chain, err := s.store.PeekWakeChain(ctx, senderSessionID, time.Now().UTC())
	if err != nil {
		// Not fatal to the send. Failing a message because the trail could not
		// be read would turn a working outbox into a broken one over a count
		// that only ever stops things early.
		log.Printf("wake: cannot read the hop count for %q: %v", senderSessionID, err)
		return 0, ""
	}
	return hops, chain
}

// claimChain spends the wake a stored message inherited from.
func (s *Server) claimChain(ctx context.Context, chain string) {
	if chain == "" {
		return
	}
	if err := s.store.ClaimWakeChain(ctx, chain); err != nil {
		// The chain stays unclaimed, so the next message from this session
		// inherits it too. Over-counting stops an exchange early, which is the
		// direction to fail in.
		log.Printf("wake: cannot claim the chain of %s: %v", chain, err)
	}
}
