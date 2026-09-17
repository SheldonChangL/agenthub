package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"agenthub.local/agenthub/internal/id"
	"agenthub.local/agenthub/internal/identity"
	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/pairing"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
	"agenthub.local/agenthub/internal/transport"
)

// The pairing exchange, in one place because the two halves only make sense
// together. docs/decisions/004-pairing-exchange.md is the long form.
//
// A machine that wants to be trusted (A) posts a signed pair.request to the
// machine that decides (B), and then polls for the answer. B never dials A: on
// the networks this exists for — a cable between two laptops, two subnets, a
// firewall that lets one direction out — the reverse connection is exactly what
// does not work, and needing it was how the hand-copied public key survived.
//
// Nothing here creates trust by itself. The exchange moves keys; a person
// comparing six groups of hex on two screens is what decides they belong to the
// machines they name. Every fingerprint shown on either side is derived from
// the key that side actually received, never read from a field a peer filled
// in — a substituted key that carried the fingerprint of the key it replaced
// would otherwise pass the only check there is.

// pairNotice is what the owner is told beside a request, on both sides.
const pairNotice = "Compare the fingerprint below with the one shown on the other machine, " +
	"looking at both screens. They are the same two values in the same order on each. " +
	"If they differ in any group, reject: something is between the two machines. " +
	"Nothing is trusted until the owner of each machine confirms."

// pairRequestView is one exchange as the owner sees it.
type pairRequestView struct {
	pairing.Request
	// Notice repeats what the owner is being asked to do, per row, because a
	// UI shows one row at a time and a banner somewhere else is not an
	// instruction attached to the decision.
	Notice string `json:"notice"`
}

func view(request pairing.Request) pairRequestView {
	return pairRequestView{Request: request, Notice: pairNotice}
}

// pairExchangeUnavailable answers when this build has no pairing exchange
// wired, which is every server a test builds without it.
func (s *Server) pairExchangeUnavailable(w http.ResponseWriter) bool {
	if s.pairRequests != nil && s.pairDialer != nil {
		return false
	}
	writeError(w, http.StatusConflict, "PAIRING_EXCHANGE_DISABLED",
		"this node was not started with the pairing exchange; pair by hand with `ah pair` instead")
	return true
}

// windowOpen reports whether this node is currently willing to be asked.
//
// The window is the consent. Without it the peer endpoint stores nothing and
// says so: a node that is not pairing must not accumulate a list of strangers
// for its owner to find later.
func (s *Server) windowOpen() bool {
	return s.pairing != nil && s.pairing.IsOpen()
}

// syncWindow lets a closed window expire the requests it collected.
func (s *Server) syncWindow() {
	if s.pairRequests == nil {
		return
	}
	if !s.windowOpen() {
		s.pairRequests.ExpirePending(pairing.Incoming)
	}
}

// receivePairRequest is the peer-facing endpoint: another machine asking to be
// trusted here.
//
// Like POST /v1/challenge it answers a caller that is not in the trust store,
// because the trust store is what this exchange exists to write. What bounds it
// is the pairing window, the rate limiter every peer route sits behind, and
// MaxPending.
func (s *Server) receivePairRequest(w http.ResponseWriter, r *http.Request) {
	if s.pairRequests == nil {
		writeError(w, http.StatusForbidden, "PAIRING_CLOSED",
			"this node is not accepting pairing requests")
		return
	}
	s.syncWindow()
	if !s.windowOpen() {
		// Nothing is stored. A request that arrived while nobody was pairing is
		// not a decision to hold for later; it is a stranger knocking.
		writeError(w, http.StatusForbidden, "PAIRING_CLOSED",
			"this node is not in pairing mode. Its owner has to open a pairing window "+
				"(`ah pairing on`) before it will accept a pairing request")
		return
	}

	var envelope protocol.Envelope
	if err := decodeJSON(r, &envelope); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if envelope.Type != protocol.TypePairRequest {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST",
			fmt.Sprintf("this endpoint takes a %s envelope, not %q", protocol.TypePairRequest, envelope.Type))
		return
	}
	descriptor, public, err := protocol.ReadPairRequest(envelope)
	if err != nil {
		writeError(w, http.StatusBadRequest, "PAIR_REQUEST_REFUSED", err.Error())
		return
	}
	if descriptor.NodeID == s.node.ID {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "a node cannot pair with itself")
		return
	}

	requestID, err := id.New("pair_")
	if err != nil {
		writeInternalError(w, "PAIRING_FAILED", "could not start a pairing request", err)
		return
	}
	now := s.pairRequests.Now()
	// The request cannot outlive the window that consented to it, and it cannot
	// outlive its own five minutes either. Whichever ends first ends it.
	expires := now.Add(pairing.RequestTTL)
	if state := s.pairing.State(); state.Open && state.ExpiresAt.Before(expires) {
		expires = state.ExpiresAt
	}
	request := pairing.Request{
		ID:          requestID,
		Direction:   pairing.Incoming,
		NodeID:      descriptor.NodeID,
		DisplayName: displayNameOr(descriptor.DisplayName, descriptor.NodeID),
		Platform:    descriptor.Platform,
		PublicKey:   identity.EncodePublicKey(public),
		// Derived here, from the key that actually arrived. The fingerprint the
		// requester wrote into its own descriptor is not read at all.
		Fingerprint:      identity.Fingerprint(public),
		LocalFingerprint: s.node.Fingerprint,
		Address:          s.acceptablePeerAddress(protocol.PairAddress(envelope)),
		State:            pairing.StatePending,
		CreatedAt:        now.UTC(),
		ExpiresAt:        expires.UTC(),
	}
	switch err := s.pairRequests.Add(request); {
	case errors.Is(err, pairing.ErrTooManyRequests):
		writeError(w, http.StatusTooManyRequests, "PAIRING_BUSY", err.Error())
		return
	case errors.Is(err, pairing.ErrDuplicateRequest):
		writeError(w, http.StatusConflict, "PAIRING_DUPLICATE", err.Error())
		return
	case err != nil:
		writeInternalError(w, "PAIRING_FAILED", "could not record the pairing request", err)
		return
	}
	log.Printf("pairing request %s from node %s (fingerprint %s) is waiting for this owner",
		requestID, descriptor.NodeID, request.Fingerprint)

	// The reply carries this node's descriptor so the requester can derive and
	// show a fingerprint for the machine it is actually talking to — and check
	// that the key in it is the key that terminated the TLS connection.
	writeJSON(w, http.StatusAccepted, map[string]any{
		"requestId": requestID,
		"node":      s.heartbeats.Descriptor(),
		"expiresAt": request.ExpiresAt,
		"notice":    pairNotice,
	})
}

// pollPairRequest is the peer-facing answer to "what became of my request".
//
// The request id is the capability: it is 128 random bits chosen by this node
// and known only to the requester, and everything it reveals — this node's
// descriptor, an approval or a refusal — is either already public or is an
// answer the owner just gave to that request. The alternative, authenticating
// the poll, would need a trust relationship that does not exist yet.
func (s *Server) pollPairRequest(w http.ResponseWriter, r *http.Request) {
	if s.pairRequests == nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such pairing request")
		return
	}
	s.syncWindow()
	request, ok := s.pairRequests.Get(r.PathValue("id"))
	if !ok || request.Direction != pairing.Incoming {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such pairing request")
		return
	}
	body := map[string]any{"requestId": request.ID, "state": string(request.State)}
	if request.Reason != "" {
		body["reason"] = request.Reason
	}
	switch request.State {
	case pairing.StateApproved:
		envelope, err := s.heartbeats.BuildPairApprove(time.Now(), request.NodeID, request.ID)
		if err != nil {
			writeInternalError(w, "PAIRING_FAILED", "could not sign the approval", err)
			return
		}
		body["envelope"] = envelope
	case pairing.StateRejected, pairing.StateExpired:
		envelope, err := s.heartbeats.BuildPairReject(time.Now(), request.NodeID, request.ID, request.Reason)
		if err != nil {
			writeInternalError(w, "PAIRING_FAILED", "could not sign the refusal", err)
			return
		}
		body["envelope"] = envelope
	}
	writeJSON(w, http.StatusOK, body)
}

// startPairRequest is the owner-facing endpoint on the requesting machine.
//
// It takes an address and nothing else that matters: no key, no fingerprint, no
// node id. That is the point of the issue this implements — the owner types
// where the other machine is, not what it is.
func (s *Server) startPairRequest(w http.ResponseWriter, r *http.Request) {
	if s.pairExchangeUnavailable(w) {
		return
	}
	var input struct {
		Address string `json:"address"`
		// Name renames the peer locally. The other machine's own display name
		// is whatever it was started with, and an owner pairing two laptops
		// called "MacBook Pro" needs to be able to tell them apart here.
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	address := strings.TrimSpace(input.Address)
	if _, _, err := net.SplitHostPort(address); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST",
			"address must be host:port, as in 192.168.1.42:7463")
		return
	}
	if err := s.deliveryPolicy(address); err != nil {
		writeError(w, http.StatusBadRequest, "ADDRESS_NOT_ALLOWED", err.Error())
		return
	}

	envelope, err := s.heartbeats.BuildPairRequest(time.Now(), s.peerAddress)
	if err != nil {
		writeInternalError(w, "PAIRING_FAILED", "could not sign the pairing request", err)
		return
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		writeInternalError(w, "PAIRING_FAILED", "could not encode the pairing request", err)
		return
	}
	reply, err := s.pairDialer.Post(r.Context(), address, "/v1/pair/requests", body, nil)
	if err != nil {
		writeError(w, http.StatusBadGateway, "PEER_UNREACHABLE",
			fmt.Sprintf("could not reach %s: %s", address, err))
		return
	}
	if !s.handlePairReplyStatus(w, address, reply) {
		return
	}

	var answer struct {
		RequestID string                  `json:"requestId"`
		Node      protocol.NodeDescriptor `json:"node"`
	}
	if err := json.Unmarshal(reply.Body, &answer); err != nil {
		writeError(w, http.StatusBadGateway, "PEER_UNREADABLE",
			fmt.Sprintf("%s answered the pairing request with something unreadable", address))
		return
	}
	if err := model.ValidateNodeID(answer.Node.NodeID); err != nil || answer.RequestID == "" {
		writeError(w, http.StatusBadGateway, "PEER_UNREADABLE",
			fmt.Sprintf("%s answered without a usable node id or request id", address))
		return
	}
	if answer.Node.NodeID == s.node.ID {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "a node cannot pair with itself")
		return
	}
	public, err := identity.DecodePublicKey(answer.Node.PublicKey)
	if err != nil {
		writeError(w, http.StatusBadGateway, "PEER_UNREADABLE",
			fmt.Sprintf("%s answered with a key that is not an Ed25519 public key", address))
		return
	}
	// The key that terminated the TLS connection and the key in the descriptor
	// have to be one key. If they are not, something in the middle completed
	// the handshake with its own key and passed on the real machine's
	// descriptor — the exact attack the fingerprint comparison is the last line
	// against, caught here without needing the owner to catch it. Abandon the
	// exchange rather than show anything: there is nothing to compare, because
	// neither key is known to be anyone's.
	if !public.Equal(reply.PresentedKey) {
		writeError(w, http.StatusBadGateway, "PEER_KEY_MISMATCH",
			fmt.Sprintf("%s completed the TLS connection with one key and described itself with "+
				"another. Something is relaying this connection. Nothing has been paired", address))
		return
	}

	now := s.pairRequests.Now()
	request := pairing.Request{
		ID:               answer.RequestID,
		Direction:        pairing.Outgoing,
		NodeID:           answer.Node.NodeID,
		DisplayName:      displayNameOr(strings.TrimSpace(input.Name), displayNameOr(answer.Node.DisplayName, answer.Node.NodeID)),
		Platform:         answer.Node.Platform,
		PublicKey:        identity.EncodePublicKey(public),
		Fingerprint:      identity.Fingerprint(public),
		LocalFingerprint: s.node.Fingerprint,
		Address:          address,
		State:            pairing.StatePending,
		CreatedAt:        now.UTC(),
		ExpiresAt:        now.Add(pairing.RequestTTL).UTC(),
	}
	if err := s.pairRequests.Add(request); err != nil {
		writeError(w, http.StatusConflict, "PAIRING_BUSY", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, view(request))
}

// handlePairReplyStatus turns the far side's status code into an answer the
// owner can act on, and reports whether the exchange may continue.
func (s *Server) handlePairReplyStatus(w http.ResponseWriter, address string, reply transport.Reply) bool {
	switch reply.Status {
	case http.StatusAccepted:
		return true
	case http.StatusNotFound:
		// An older node serves /v1/challenge and /v1/heartbeat on this listener
		// and nothing else, so a 404 here is a version answer, not a routing
		// one. Said in those words because "404" sends an owner looking for a
		// typo in an address that is correct.
		writeError(w, http.StatusConflict, "PEER_TOO_OLD",
			fmt.Sprintf("%s answered, but does not know how to pair this way: it is running a build "+
				"from before the pairing exchange existed. Upgrade AgentHub on that machine, or pair "+
				"by hand on both with `ah pair`", address))
	case http.StatusForbidden:
		writeError(w, http.StatusConflict, "PEER_PAIRING_CLOSED",
			fmt.Sprintf("%s is not in pairing mode. Run `ah pairing on` there, then try again", address))
	case http.StatusTooManyRequests:
		writeError(w, http.StatusConflict, "PEER_PAIRING_BUSY",
			fmt.Sprintf("%s already has as many pairing requests waiting as it will hold", address))
	case http.StatusConflict:
		writeError(w, http.StatusConflict, "PEER_PAIRING_DUPLICATE",
			fmt.Sprintf("%s already has a pairing request from this node waiting for its owner", address))
	default:
		writeError(w, http.StatusBadGateway, "PEER_REFUSED",
			fmt.Sprintf("%s refused the pairing request (%d): %s",
				address, reply.Status, peerMessage(reply.Body)))
	}
	return false
}

// listPairRequests answers the owner's "what am I being asked, and what am I
// waiting for".
//
// Reading also refreshes: every outgoing request still pending is polled at the
// far side first. The poll belongs to the node rather than to a background
// loop, because this is the only moment the answer is wanted, and a loop would
// keep dialling an address for five minutes after the owner stopped caring.
func (s *Server) listPairRequests(w http.ResponseWriter, r *http.Request) {
	if s.pairExchangeUnavailable(w) {
		return
	}
	s.syncWindow()
	for _, request := range s.pairRequests.List() {
		if request.Direction == pairing.Outgoing && request.State == pairing.StatePending {
			s.refreshOutgoing(r.Context(), request)
		}
	}
	rows := make([]pairRequestView, 0)
	for _, request := range s.pairRequests.List() {
		rows = append(rows, view(request))
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": rows, "notice": pairNotice})
}

// refreshOutgoing asks the far side what became of one request.
//
// Every answer is verified against the key this exchange recorded at its first
// connection. An approval signed by anything else is not this machine's
// approval, and a middle that could produce one would not need the exchange.
func (s *Server) refreshOutgoing(ctx context.Context, request pairing.Request) {
	public, err := identity.DecodePublicKey(request.PublicKey)
	if err != nil {
		return
	}
	reply, err := s.pairDialer.Get(ctx, request.Address, "/v1/pair/requests/"+request.ID, public)
	if err != nil {
		// Unreachable for now. Left pending: the window has not run out, and an
		// owner who unplugged a cable should not have to start again.
		return
	}
	if reply.Status == http.StatusNotFound {
		// The far side has forgotten it — restarted, or its window closed long
		// enough ago that the row was swept. There is nothing left to confirm.
		_, _ = s.pairRequests.Settle(request.ID, pairing.StatePending, pairing.StateExpired, pairing.ReasonExpired)
		return
	}
	if reply.Status != http.StatusOK {
		return
	}
	var answer struct {
		State    string            `json:"state"`
		Reason   string            `json:"reason"`
		Envelope protocol.Envelope `json:"envelope"`
	}
	if err := json.Unmarshal(reply.Body, &answer); err != nil {
		return
	}
	switch answer.State {
	case string(pairing.StateApproved):
		if err := s.checkPairAnswer(answer.Envelope, protocol.TypePairApprove, request, public); err != nil {
			log.Printf("pairing request %s: discarding an approval from %s: %s", request.ID, request.Address, err)
			return
		}
		_, _ = s.pairRequests.Settle(request.ID, pairing.StatePending, pairing.StateAwaitingConfirm, "")
	case string(pairing.StateRejected), string(pairing.StateExpired):
		if err := s.checkPairAnswer(answer.Envelope, protocol.TypePairReject, request, public); err != nil {
			log.Printf("pairing request %s: discarding a refusal from %s: %s", request.ID, request.Address, err)
			return
		}
		state := pairing.StateRejected
		if answer.State == string(pairing.StateExpired) {
			state = pairing.StateExpired
		}
		_, _ = s.pairRequests.Settle(request.ID, pairing.StatePending, state,
			displayNameOr(answer.Reason, pairing.ReasonDeclined))
	}
}

// checkPairAnswer verifies that an answer really is this peer's, about this
// request, and addressed to this node.
func (s *Server) checkPairAnswer(
	envelope protocol.Envelope, wantType string, request pairing.Request, public ed25519.PublicKey,
) error {
	if envelope.Type != wantType {
		return fmt.Errorf("envelope is a %q, expected %q", envelope.Type, wantType)
	}
	if err := envelope.VerifyDirected(public, request.NodeID, s.node.ID); err != nil {
		return err
	}
	switch wantType {
	case protocol.TypePairApprove:
		payload, err := protocol.DecodePayload[protocol.PairApprovePayload](envelope)
		if err != nil {
			return err
		}
		if payload.RequestID != request.ID {
			return fmt.Errorf("approval names request %q, not %q", payload.RequestID, request.ID)
		}
		// The approval carries the peer's descriptor a second time. It must be
		// the same key the connection was pinned to; a different one would mean
		// the fingerprint the owner is about to compare is not the key that
		// would be stored.
		if payload.Node.PublicKey != request.PublicKey {
			return errors.New("approval carries a different key than this exchange recorded")
		}
	case protocol.TypePairReject:
		payload, err := protocol.DecodePayload[protocol.PairRejectPayload](envelope)
		if err != nil {
			return err
		}
		if payload.RequestID != request.ID {
			return fmt.Errorf("refusal names request %q, not %q", payload.RequestID, request.ID)
		}
	}
	return nil
}

// approvePairRequest is the receiving owner saying the fingerprints match.
//
// Owner surface only. A peer that could reach this would be approving itself,
// which is the whole exchange defeated, and that is why PeerHandler and Handler
// are separate muxes rather than one with a check inside.
func (s *Server) approvePairRequest(w http.ResponseWriter, r *http.Request) {
	if s.pairExchangeUnavailable(w) {
		return
	}
	s.syncWindow()
	request, ok := s.pairRequests.Get(r.PathValue("id"))
	if !ok || request.Direction != pairing.Incoming {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such incoming pairing request")
		return
	}
	// The state is moved first, so two clicks cannot write the trust store
	// twice, and put back if the write fails.
	settled, err := s.pairRequests.Settle(request.ID, pairing.StatePending, pairing.StateApproved, "")
	if err != nil {
		writePairStateError(w, request, err)
		return
	}
	if err := s.trustFromRequest(r.Context(), settled); err != nil {
		_, _ = s.pairRequests.Settle(request.ID, pairing.StateApproved, pairing.StatePending, "")
		writeRegistryError(w, err)
		return
	}
	log.Printf("paired with node %s (fingerprint %s) after its request %s was approved",
		settled.NodeID, settled.Fingerprint, settled.ID)
	writeJSON(w, http.StatusOK, view(settled))
}

// confirmPairRequest is the requesting owner saying the fingerprints match.
//
// Both owners confirm, and neither confirmation stands in for the other. An
// approval on the far side writes that machine's trust store, not this one's:
// the person here has still compared nothing at that point, and a design where
// they had not would let one screen decide for two.
func (s *Server) confirmPairRequest(w http.ResponseWriter, r *http.Request) {
	if s.pairExchangeUnavailable(w) {
		return
	}
	request, ok := s.pairRequests.Get(r.PathValue("id"))
	if !ok || request.Direction != pairing.Outgoing {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such outgoing pairing request")
		return
	}
	settled, err := s.pairRequests.Settle(request.ID, pairing.StateAwaitingConfirm, pairing.StateApproved, "")
	if err != nil {
		writePairStateError(w, request, err)
		return
	}
	if err := s.trustFromRequest(r.Context(), settled); err != nil {
		_, _ = s.pairRequests.Settle(request.ID, pairing.StateApproved, pairing.StateAwaitingConfirm, "")
		writeRegistryError(w, err)
		return
	}
	log.Printf("paired with node %s (fingerprint %s) after confirming request %s",
		settled.NodeID, settled.Fingerprint, settled.ID)
	writeJSON(w, http.StatusOK, view(settled))
}

// rejectPairRequest refuses one, in either direction.
//
// On the receiving side the refusal is collected by the requester's next poll,
// so "they said no" and "it ran out" are different things on both screens. On
// the requesting side nothing is sent: the far side is waiting for its own
// owner, and it will expire on its own. Telling it would be a courtesy paid by
// dialling a machine this owner has just decided not to trust.
func (s *Server) rejectPairRequest(w http.ResponseWriter, r *http.Request) {
	if s.pairExchangeUnavailable(w) {
		return
	}
	s.syncWindow()
	request, ok := s.pairRequests.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such pairing request")
		return
	}
	from := pairing.StatePending
	if request.State == pairing.StateAwaitingConfirm {
		from = pairing.StateAwaitingConfirm
	}
	settled, err := s.pairRequests.Settle(request.ID, from, pairing.StateRejected, pairing.ReasonDeclined)
	if err != nil {
		writePairStateError(w, request, err)
		return
	}
	writeJSON(w, http.StatusOK, view(settled))
}

// trustFromRequest writes one side of the pairing into the trust store.
//
// Identity only. Nothing here touches session_audience: pairing says who a node
// is, and what it may see is a separate decision the owner makes per session.
// A pairing that published anything would make the safe default depend on
// nobody having written the code.
func (s *Server) trustFromRequest(ctx context.Context, request pairing.Request) error {
	node := registry.TrustedNode{
		NodeID:      request.NodeID,
		DisplayName: request.DisplayName,
		Platform:    request.Platform,
		PublicKey:   request.PublicKey,
		Fingerprint: request.Fingerprint,
	}
	if err := s.store.TrustNode(ctx, node); err != nil {
		return err
	}
	if address := s.acceptablePeerAddress(request.Address); address != "" {
		if err := s.store.SetNodeAddress(ctx, request.NodeID, address); err != nil {
			return err
		}
	}
	// Paired machines are no longer candidates to pair with, exactly as the
	// manual route does.
	if s.candidates != nil {
		s.candidates.Forget(request.NodeID)
	}
	return nil
}

// acceptablePeerAddress returns an address this node would actually deliver to,
// or empty.
//
// An address that does not pass is dropped rather than refused: it is not a
// trust decision, and a pairing that failed because the other machine offered
// an address this build will not send to would be a confusing way to say "no
// address recorded".
func (s *Server) acceptablePeerAddress(address string) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return ""
	}
	if _, _, err := net.SplitHostPort(address); err != nil {
		return ""
	}
	if s.deliveryPolicy(address) != nil {
		return ""
	}
	return address
}

// writePairStateError says what the request is doing instead of what was asked.
func writePairStateError(w http.ResponseWriter, request pairing.Request, err error) {
	if errors.Is(err, pairing.ErrNoSuchRequest) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such pairing request")
		return
	}
	writeError(w, http.StatusConflict, "PAIRING_STATE",
		fmt.Sprintf("pairing request %s is %s: %s", request.ID, request.State, err))
}

// peerMessage pulls the message out of a peer's error body, falling back to the
// raw bytes so an owner is never shown nothing at all.
func peerMessage(body []byte) string {
	var decoded struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &decoded); err == nil && decoded.Error.Message != "" {
		return decoded.Error.Message
	}
	trimmed := strings.TrimSpace(string(body))
	if len(trimmed) > 200 {
		return trimmed[:200] + "…"
	}
	return trimmed
}

func displayNameOr(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
