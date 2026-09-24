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
	"net/netip"
	"strings"
	"sync"
	"time"

	"agenthub.local/agenthub/internal/id"
	"agenthub.local/agenthub/internal/identity"
	"agenthub.local/agenthub/internal/label"
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

// pairNotice is what the owner is told beside a request that still needs a
// decision, on both sides.
//
// It describes exactly what is on the screen. The earlier wording said "the
// fingerprint below" beside two fingerprints printed above it, and claimed the
// same order on both machines while each machine printed its own first — an
// instruction that does not match the screen is worse than none, because the
// person stops reading it and starts guessing.
const pairNotice = "Two fingerprints are shown: the requester (the machine that asked) first, " +
	"the receiver (the machine it asked) second — the same words that label the lines. The other " +
	"machine shows the same two values in the same order; run `ah pair pending` there to see them. " +
	"Read both screens: if any group differs, reject — something is between the two machines. " +
	"Nothing is trusted until the owner of each machine says so."

// pairFingerprintView is one machine's fingerprint, labelled well enough that a
// person looking at two screens knows which line to compare with which.
type pairFingerprintView struct {
	// Role is "requester" or "receiver" — the canonical order. Both machines
	// list the requester first, whichever machine they are, so the two screens
	// can be read line by line.
	Role string `json:"role"`
	// Machine is that machine's own display name. Its own, not a local label:
	// the name on this screen has to be the name on that one.
	Machine string `json:"machine"`
	// Whose is "this machine" or "the other machine", from here.
	Whose       string `json:"whose"`
	Fingerprint string `json:"fingerprint"`
}

// pairRequestView is one exchange as the owner sees it.
type pairRequestView struct {
	pairing.Request
	// Fingerprints is what the owner compares, requester first on both
	// machines.
	Fingerprints []pairFingerprintView `json:"fingerprints"`
	// NextStep is one sentence: what happens now, and on which machine. Named
	// per row because the answer differs per row, and a person holding two
	// terminals needs to be told which one to type in.
	NextStep string `json:"nextStep"`
	// Notice repeats what the owner is being asked to do, per row, because a
	// UI shows one row at a time and a banner somewhere else is not an
	// instruction attached to the decision. Absent on a finished row: there is
	// nothing left to compare, and repeating it there is what made owners
	// re-read a decided request looking for something to do.
	Notice string `json:"notice,omitempty"`
}

const (
	whoseThis  = "this machine"
	whoseOther = "the other machine"
)

// view is one request as this node's owner sees it.
//
// A method on the server because it needs this machine's own display name: the
// point of the labels is that each fingerprint is named by the machine that
// holds it, in the words that machine uses about itself.
func (s *Server) view(request pairing.Request) pairRequestView {
	local := pairFingerprintView{
		Machine: displayNameOr(s.node.DisplayName, s.node.ID), Whose: whoseThis,
		Fingerprint: request.LocalFingerprint,
	}
	other := pairFingerprintView{
		Machine: displayNameOr(request.DisplayName, request.NodeID), Whose: whoseOther,
		Fingerprint: request.Fingerprint,
	}
	// Canonical order: the machine that asked, then the machine it asked. On an
	// outgoing request this machine asked; on an incoming one the other did.
	first, second := local, other
	if request.Direction == pairing.Incoming {
		first, second = other, local
	}
	first.Role, second.Role = "requester", "receiver"
	row := pairRequestView{
		Request:      request,
		Fingerprints: []pairFingerprintView{first, second},
		NextStep:     pairNextStep(request, other.Machine),
	}
	if !request.Decided() {
		row.Notice = pairNotice
	}
	return row
}

// pairNextStep says what happens now and where, in one sentence.
func pairNextStep(request pairing.Request, otherName string) string {
	switch {
	case request.TrustedByRequest && request.TrustLeftInPlace != "":
		// First, and stated without naming a state, because trust can outlive
		// the decision that should have taken it back under more than one of
		// them. A refusal is the obvious one; an expiry is the one that used to
		// fall through here — the approval writes the trust row, the window
		// runs out before the transition, the sweep marks the row expired, and
		// the compensation cannot revoke. A state-specific case then left the
		// owner reading "Nothing was trusted" with the key still in the store.
		//
		// The revoke is skipped when the key stored under that node id is not
		// the key this request carried, because then the trust came from
		// somewhere else, and it can also simply fail. Saying "withdrawn" here
		// — which this sentence used to do unconditionally — would tell the
		// owner the one thing that is not true: something is still trusted, and
		// only they can decide about it. The row carries which of the two
		// happened, so this sentence states it rather than guessing.
		return fmt.Sprintf("This request ended as %s after this machine had trusted %s, and that "+
			"trust was left in place: %s. Check it with `ah nodes` and remove it yourself "+
			"with: ah revoke %s", request.State, otherName, request.TrustLeftInPlace, request.NodeID)
	case request.State == pairing.StatePending && request.Direction == pairing.Outgoing:
		// Both halves, because the requester who is told only the first half
		// stalls: the approval on the far side does not finish the pairing, and
		// nothing else on this screen says a confirm is coming.
		return fmt.Sprintf("On %s, run: ah pair approve %s — then, after they approve, compare "+
			"the fingerprints and run here: ah pair confirm %s", otherName, request.ID, request.ID)
	case request.State == pairing.StatePending:
		return fmt.Sprintf("Compare the two fingerprints, then on this machine run: "+
			"ah pair approve %s (or ah pair reject %s)", request.ID, request.ID)
	case request.State == pairing.StateAwaitingConfirm:
		return fmt.Sprintf("%s approved it. On this machine, run: ah pair confirm %s",
			otherName, request.ID)
	case request.State == pairing.StateApproved && request.Direction == pairing.Incoming:
		// The gap this sentence covers: this node cannot tell whether the other
		// owner ever confirms, so it cannot expire what it already wrote. See
		// docs/decisions/004-pairing-exchange.md.
		return fmt.Sprintf("%s is trusted here. Nothing more to do on this machine; wait for "+
			"them to confirm. If they never do, undo it with: ah revoke %s", otherName, request.NodeID)
	case request.State == pairing.StateApproved:
		return fmt.Sprintf("Done: %s is trusted here, and this machine is trusted there.", otherName)
	case request.State == pairing.StateRejected && request.TrustedByRequest:
		// This machine had already approved, and the refusal arrived after.
		// Saying only "refused" would leave the owner with no way to know a
		// trust row was written here at all, let alone that it is gone again.
		return fmt.Sprintf("Refused by %s after this machine had trusted it. The trust written "+
			"here has been withdrawn; nothing from this request is trusted on either machine.",
			otherName)
	case request.State == pairing.StateRejected:
		return "Refused. Nothing from this request is trusted on either machine."
	default:
		return fmt.Sprintf("It ran out (%s). Nothing was trusted; start again if you still want to pair.",
			displayNameOr(request.Reason, pairing.ReasonExpired))
	}
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
	claimed, claimedAlternates := protocol.PairAddresses(envelope)
	preferred := s.acceptablePeerAddress(claimed)
	alternates := s.acceptableAlternates(preferred, claimedAlternates)
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
		DisplayName: peerNameOr(descriptor.DisplayName, descriptor.NodeID),
		Platform:    label.Printable(descriptor.Platform),
		PublicKey:   identity.EncodePublicKey(public),
		// Derived here, from the key that actually arrived. The fingerprint the
		// requester wrote into its own descriptor is not read at all.
		Fingerprint:      identity.Fingerprint(public),
		LocalFingerprint: s.node.Fingerprint,
		Address:          preferred,
		Alternates:       alternates,
		SourceHost:       sourceHost(r),
		State:            pairing.StatePending,
		CreatedAt:        now.UTC(),
		ExpiresAt:        expires.UTC(),
	}
	switch err := s.pairRequests.Add(request); {
	case errors.Is(err, pairing.ErrTooManyFromSource), errors.Is(err, pairing.ErrTooManyRequests):
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
		envelope, err := s.heartbeats.BuildPairApprove(time.Now(), request.NodeID, request.ID,
			s.ownPeerAddresses())
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
	// Address and nothing else. An earlier version took a local name for the
	// peer and stored it as the peer's display name, which put a different name
	// on each screen while the whole exchange asks the owner to check that the
	// two screens agree. A node is called what it calls itself.
	var input struct {
		Address string `json:"address"`
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

	envelope, err := s.heartbeats.BuildPairRequest(time.Now(), s.ownPeerAddresses())
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
		DisplayName:      peerNameOr(answer.Node.DisplayName, answer.Node.NodeID),
		Platform:         label.Printable(answer.Node.Platform),
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
	writeJSON(w, http.StatusCreated, s.view(request))
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
		// The peer's own sentence, because 429 covers two different refusals —
		// the bound per source address (the usual one: this machine has been
		// asking) and the whole list being full — and they are undone in
		// different places. Either way the remedy is the same list.
		writeError(w, http.StatusConflict, "PEER_PAIRING_BUSY",
			fmt.Sprintf("%s will not take another pairing request right now: %s. Reject what is "+
				"waiting there or let it expire (`ah pair pending` on that machine), then try again",
				address, peerMessage(reply.Body)))
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
	s.refreshAllOutgoing(r.Context())
	// Finished rows are kept — "rejected" and "expired" are answers — but they
	// are not what the owner is looking at when they ask what is waiting. A
	// list where the one row needing a decision sat under four decided ones is
	// how an owner misses their own pairing.
	all := r.URL.Query().Get("all") == "true" || r.URL.Query().Get("all") == "1"
	rows := make([]pairRequestView, 0)
	for _, request := range s.pairRequests.List() {
		if !all && request.Decided() {
			continue
		}
		rows = append(rows, s.view(request))
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": rows, "notice": pairNotice, "all": all})
}

// pairPollTimeout bounds one refresh of one outgoing request.
//
// The dialer's own timeout is ten seconds, which is right for delivering a
// message and wrong here: this runs while an owner waits at a prompt, and the
// machines are on the same segment or not reachable at all. Sixteen pending
// rows at ten seconds each was nearly three minutes of silence.
const pairPollTimeout = 3 * time.Second

// refreshAllOutgoing polls every pending outgoing request at once.
//
// Concurrently, because these are separate machines and one that has been
// unplugged must not hold up the answer about the one that is answering.
func (s *Server) refreshAllOutgoing(ctx context.Context) {
	var waiting sync.WaitGroup
	for _, request := range s.pairRequests.List() {
		if request.Direction != pairing.Outgoing || request.State != pairing.StatePending {
			continue
		}
		waiting.Add(1)
		go func(request pairing.Request) {
			defer waiting.Done()
			polling, cancel := context.WithTimeout(ctx, pairPollTimeout)
			defer cancel()
			s.refreshOutgoing(polling, request)
		}(request)
	}
	waiting.Wait()
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
		// The approval says where else the far side answers. The address this
		// node dialled stays preferred: it is the one known to work.
		var alternates []string
		if approval, err := protocol.DecodePayload[protocol.PairApprovePayload](answer.Envelope); err == nil {
			alternates = s.acceptableAlternates(request.Address, approval.Addresses)
		}
		_, _, _ = s.pairRequests.SettleFrom(request.ID, []pairing.RequestState{pairing.StatePending},
			pairing.StateAwaitingConfirm, "", func(row *pairing.Request) { row.Alternates = alternates })
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
	// Refused here rather than by the transition below, so a row that is
	// already decided is answered without the trust store being touched at all.
	if request.State != pairing.StatePending {
		writePairStateError(w, request, "approved", pairing.ErrWrongState)
		return
	}
	// The trust row goes in first, and the decision that keeps it is taken
	// afterwards in one atomic step. The interleavings, with a refusal arriving
	// from the other owner at every point:
	//
	//   before the write — the refusal settles rejected, this approval's
	//     transition fails because the row is no longer pending, and nothing
	//     was written to take back;
	//   between the write and the transition — the refusal settles rejected
	//     and finds trustedByRequest unset, so it revokes nothing; this
	//     approval's transition then fails and it takes back the row it wrote
	//     itself. That compensation is why the write comes first: the refusal
	//     cannot revoke what it cannot see, but the approval always can;
	//   after the transition — the row says approved and trustedByRequest in
	//     the same instant, so the refusal reads both or neither, settles
	//     rejected from approved, and revokes.
	//
	// The order it replaced was settle-then-write, and its unguarded window was
	// the one that mattered: a refusal reading between the settle and the trust
	// write left the key its owner had just refused in the store with the row
	// saying nothing was trusted.
	if err := s.trustFromRequest(r.Context(), request); err != nil {
		writeRegistryError(w, err)
		return
	}
	if s.afterApproveTrustWrite != nil {
		s.afterApproveTrustWrite()
	}
	settled, _, err := s.pairRequests.SettleFrom(request.ID,
		[]pairing.RequestState{pairing.StatePending}, pairing.StateApproved, "",
		func(row *pairing.Request) { row.TrustedByRequest = true })
	if err != nil {
		s.undoApprovalThatLost(r.Context(), w, request, err)
		return
	}
	log.Printf("paired with node %s (fingerprint %s) after its request %s was approved",
		settled.NodeID, settled.Fingerprint, settled.ID)
	writeJSON(w, http.StatusOK, s.view(settled))
}

// undoApprovalThatLost takes back the trust row an approval wrote when the
// approval itself did not stand, and answers the owner with the state that beat
// it rather than with the one they asked for.
//
// The one case where nothing is taken back is another approval of this same
// request having won the transition: the trust row this one wrote is the row
// that approval now stands on, and revoking it would un-pair a machine the
// owner has just paired. Only an approval that lost to a refusal or to an
// expiry has something to undo.
func (s *Server) undoApprovalThatLost(
	ctx context.Context, w http.ResponseWriter, request pairing.Request, settleErr error,
) {
	current, ok := s.pairRequests.Get(request.ID)
	// What the owner is told this request is now. Read from the row when there
	// still is one; "gone" when the sweep took it between the transition and
	// here, because the only copy left is the pending one this call started
	// from, and answering out of it would say the request "is pending, so it
	// cannot be approved now" about a request that no longer exists.
	state := string(current.State)
	if !ok {
		// Swept between the transition and here. There is no row left to
		// attribute the trust to, so it goes, and the owner is told the request
		// is gone.
		current = request
		state = "gone"
		settleErr = pairing.ErrNoSuchRequest
	}
	if ok && current.State == pairing.StateApproved && current.TrustedByRequest {
		writePairStateError(w, current, "approved", settleErr)
		return
	}
	// TrustedByRequest is set on the copy only: the row never carried it, which
	// is exactly why the refusal that won left this trust row standing.
	written := request
	written.TrustedByRequest = true
	leftInPlace, err := s.untrustFromRequest(ctx, written)
	if err != nil {
		log.Printf("pairing request %s: the approval lost to %s and taking its trust row for %s "+
			"back failed: %v", request.ID, state, request.NodeID, err)
		leftInPlace = trustLeftByFailedRevoke
	}
	if leftInPlace == "" {
		writePairStateError(w, current, "approved", settleErr)
		return
	}
	// Something is trusted here that no decision stands behind, so the row has
	// to say so: every sentence that reads "nothing from this request is
	// trusted" would otherwise be false.
	if _, updateErr := s.pairRequests.Update(request.ID, func(row *pairing.Request) {
		row.TrustedByRequest = true
		row.TrustLeftInPlace = leftInPlace
	}); updateErr != nil {
		log.Printf("pairing request %s: %s is trusted here with nothing standing behind it, and "+
			"the row could not be marked: %v", request.ID, request.NodeID, updateErr)
	}
	writeInternalError(w, "PAIRING_FAILED", fmt.Sprintf(
		"this request was %s before the approval landed, and the trust it had written for %s "+
			"could not be taken back; remove it with `ah revoke %s`",
		state, displayNameOr(request.DisplayName, request.NodeID), request.NodeID),
		errors.New(leftInPlace))
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
	// Ask the far side first, when this request has not yet heard an answer.
	//
	// Confirming is the command the owner was told to run, and it has to work
	// when run exactly as told. Learning of the approval used to happen only in
	// the list handler, so an owner who went straight from `ah pair request` to
	// `ah pair confirm` — as its own output instructed — was answered "it is
	// pending, not awaiting-confirm" for an approval that had already been
	// given.
	if request.State == pairing.StatePending {
		polling, cancel := context.WithTimeout(r.Context(), pairPollTimeout)
		s.refreshOutgoing(polling, request)
		cancel()
		if refreshed, found := s.pairRequests.Get(request.ID); found {
			request = refreshed
		}
	}
	settled, err := s.pairRequests.Settle(request.ID, pairing.StateAwaitingConfirm, pairing.StateApproved, "")
	if err != nil {
		writePairStateError(w, request, "confirmed", err)
		return
	}
	if err := s.trustFromRequest(r.Context(), settled); err != nil {
		_, _ = s.pairRequests.Settle(request.ID, pairing.StateApproved, pairing.StateAwaitingConfirm, "")
		writeRegistryError(w, err)
		return
	}
	log.Printf("paired with node %s (fingerprint %s) after confirming request %s",
		settled.NodeID, settled.Fingerprint, settled.ID)
	writeJSON(w, http.StatusOK, s.view(settled))
}

// rejectPairRequest refuses one, in either direction.
//
// On the receiving side the refusal is collected by the requester's next poll,
// so "they said no" and "it ran out" are different things on both screens.
//
// On the requesting side it is pushed. Nothing used to be sent, on the argument
// that the far side would expire on its own — but by then it may already have
// approved, and an approval has written a trust row. An owner who refuses
// because the fingerprints did not match would have left the other machine
// trusting exactly the key they refused, for as long as nobody noticed. The
// push is directed and signed, and it reaches a machine this owner has just
// decided not to trust — which costs nothing, because that machine already has
// this node's address and key from the request itself.
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
		writePairStateError(w, request, "rejected", err)
		return
	}
	row := s.view(settled)
	if settled.Direction == pairing.Outgoing {
		if err := s.pushPairReject(r.Context(), settled); err != nil {
			// The refusal stands here whatever the other machine heard: this
			// owner has decided. What changes is what they are told to do
			// about the machine that may still be trusting them.
			log.Printf("pairing request %s: could not tell %s it was refused: %s",
				settled.ID, settled.Address, err)
			row.NextStep = fmt.Sprintf("Refused here, but %s could not be told (%s). "+
				"If its owner had already approved, ask them to run: ah revoke %s",
				settled.DisplayName, err, s.node.ID)
		}
	}
	writeJSON(w, http.StatusOK, row)
}

// pushPairReject tells the other machine that this owner refused.
//
// Pinned to the key this exchange recorded, like every other call after the
// first: a refusal delivered to a different machine than the one being refused
// would be no refusal at all.
func (s *Server) pushPairReject(ctx context.Context, request pairing.Request) error {
	if request.Address == "" {
		return errors.New("this exchange recorded no address for that machine")
	}
	public, err := identity.DecodePublicKey(request.PublicKey)
	if err != nil {
		return err
	}
	envelope, err := s.heartbeats.BuildPairReject(
		time.Now(), request.NodeID, request.ID, pairing.ReasonDeclined)
	if err != nil {
		return err
	}
	body, err := json.Marshal(envelope)
	if err != nil {
		return err
	}
	pushing, cancel := context.WithTimeout(ctx, pairPollTimeout)
	defer cancel()
	reply, err := s.pairDialer.Post(pushing, request.Address,
		"/v1/pair/requests/"+request.ID+"/reject", body, public)
	if err != nil {
		return err
	}
	if reply.Status != http.StatusOK && reply.Status != http.StatusNotFound {
		return fmt.Errorf("%s answered %d: %s", request.Address, reply.Status, peerMessage(reply.Body))
	}
	return nil
}

// receivePairReject is the peer-facing endpoint: the machine that asked to be
// trusted saying it does not want to be after all.
//
// Unauthenticated in the sense that the caller is not in the trust store, and
// authenticated in the sense that matters: the envelope must be signed by the
// key this request recorded, directed at this node, and name this request. That
// is a stricter check than the poll, because this one changes state.
//
// If this node had already approved, the trust row that approval wrote is taken
// back — but only when this request is what wrote it. A node paired earlier by
// some other route is not revoked by a refusal that names it.
func (s *Server) receivePairReject(w http.ResponseWriter, r *http.Request) {
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
	var envelope protocol.Envelope
	if err := decodeJSON(r, &envelope); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	public, err := identity.DecodePublicKey(request.PublicKey)
	if err != nil {
		writeInternalError(w, "PAIRING_FAILED", "could not read the recorded key", err)
		return
	}
	if err := s.checkPairAnswer(envelope, protocol.TypePairReject, request, public); err != nil {
		writeError(w, http.StatusForbidden, "PAIR_REJECT_REFUSED", err.Error())
		return
	}

	// Everything above read a snapshot taken before the body was decoded and
	// the signature checked, and this owner can approve inside that window. A
	// decision branched off the stale row was the bug: the refusal saw
	// `pending`, skipped the revoke, and its Settle(pending→rejected) then
	// failed into a discarded error — so the requester was told `rejected`
	// while this node kept the row approved with the trust row written.
	settled, err := s.settleReceivedReject(r.Context(), request.ID)
	switch {
	case errors.Is(err, pairing.ErrNoSuchRequest):
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such pairing request")
		return
	case errors.Is(err, pairing.ErrWrongState):
		writeError(w, http.StatusConflict, "PAIR_REJECT_REFUSED", err.Error())
		return
	case err != nil:
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"requestId": settled.ID, "state": string(settled.State)})
}

// settleReceivedReject records the refusal against whatever state the request
// is actually in now, and takes back the trust an approval wrote.
//
// The order is the point. Settle is the one atomic decision in the exchange, so
// it goes first: an approval that lands between the read and the Settle loses
// there, is seen by the re-read, and is refused properly instead of being
// papered over by a branch chosen from a row the node has already left. The
// revoke follows the state change rather than preceding it: an approval can
// still be in flight behind a row that says rejected, and it is not this side's
// to find. That approval loses its own transition and compensates itself —
// undoApprovalThatLost takes back the trust row it had already written — so
// revoking here is about the trust an approval that already won left standing,
// and nothing else.
//
// A request already refused or expired is returned as it stands: the requester
// is telling this node something it already believes, and saying so is a better
// answer than a conflict.
func (s *Server) settleReceivedReject(ctx context.Context, id string) (pairing.Request, error) {
	// Bounded rather than open: each pass can only lose to a decision some
	// other request actually took, so a second is already generous, and the
	// bound is what stops a pathological interleaving spinning here.
	for attempt := 0; attempt < 4; attempt++ {
		request, ok := s.pairRequests.Get(id)
		if !ok || request.Direction != pairing.Incoming {
			return pairing.Request{}, pairing.ErrNoSuchRequest
		}
		if request.Decided() && request.State != pairing.StateApproved {
			return request, nil
		}
		if s.afterRejectRead != nil {
			s.afterRejectRead()
		}
		// Whether this request had been approved is learnt from the transition
		// itself rather than from the read above, because that is the answer
		// that decides whether a trust row is revoked — the one destructive
		// step here — and the row read a moment ago may not be the row settled.
		settled, previous, err := s.pairRequests.SettleFrom(id,
			[]pairing.RequestState{pairing.StatePending, pairing.StateApproved},
			pairing.StateRejected, pairing.ReasonDeclined, nil)
		if errors.Is(err, pairing.ErrWrongState) {
			// Somebody moved it between the read and here. Read what it is now
			// and decide from that, rather than from what it was.
			continue
		}
		if err != nil {
			return pairing.Request{}, err
		}
		if previous != pairing.StateApproved {
			// Nothing was approved here, so there is nothing of this request's
			// in the trust store. An approval that wrote its trust row and had
			// not yet reached its transition is the one exception, and it takes
			// that row back itself when its transition fails against this one.
			log.Printf("pairing request %s from %s was withdrawn by its own owner",
				settled.ID, settled.NodeID)
			return settled, nil
		}
		leftInPlace, err := s.untrustFromRequest(ctx, settled)
		if err != nil {
			// The refusal is already recorded and the trust is still here. The
			// error is not returned in place of the row, because a row that
			// says rejected while the sentence beside it claims a withdrawal
			// is the exact lie this whole path exists to prevent: it is
			// recorded on the row instead, and the reason is logged.
			log.Printf("pairing request %s: revoking %s after its owner refused it failed: %v",
				settled.ID, settled.NodeID, err)
			leftInPlace = trustLeftByFailedRevoke
		}
		if leftInPlace != "" {
			// Recorded on the row, because the sentence the owner reads about
			// this request has to say what the node did with the trust, and
			// only this call knows.
			updated, updateErr := s.pairRequests.Update(id, func(row *pairing.Request) {
				row.TrustLeftInPlace = leftInPlace
			})
			if updateErr != nil {
				log.Printf("pairing request %s: %s is still trusted here and the row could not "+
					"be marked, so nothing may claim the trust was withdrawn: %v",
					settled.ID, settled.NodeID, updateErr)
				return pairing.Request{}, fmt.Errorf(
					"%s is still trusted here after this refusal and this node could not record "+
						"that: %w", settled.NodeID, updateErr)
			}
			settled = updated
		}
		log.Printf("pairing request %s: %s refused after this node approved it; trust %s",
			settled.ID, settled.NodeID,
			map[bool]string{true: "left in place", false: "withdrawn"}[leftInPlace != ""])
		return settled, nil
	}
	return pairing.Request{}, fmt.Errorf("%w: it kept changing while this refusal was recorded",
		pairing.ErrWrongState)
}

// untrustFromRequest takes back exactly what this request wrote.
//
// Two guards, because revoking is destructive: the row must say this request is
// what trusted that node, and the key stored under that node id must still be
// the key this request carried. Either one failing means the trust in the store
// came from somewhere else, and somewhere else is not this refusal's to undo.
//
// Reports why trust this request wrote was left standing, in the words the
// owner is shown, and empty when nothing was left: the one case is a stored key
// that is not this request's. A node with nothing stored is not "left in
// place": there is nothing there to leave.
func (s *Server) untrustFromRequest(ctx context.Context, request pairing.Request) (string, error) {
	if !request.TrustedByRequest {
		return "", nil
	}
	stored, err := s.store.TrustedNode(ctx, request.NodeID)
	if errors.Is(err, registry.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if stored.PublicKey != request.PublicKey {
		log.Printf("pairing request %s: not revoking %s, its trusted key is not the one this "+
			"request carried", request.ID, request.NodeID)
		return fmt.Sprintf("%s is trusted here under a different key from the one this request "+
			"carried, so this refusal did not remove it",
			displayNameOr(request.DisplayName, request.NodeID)), nil
	}
	return "", s.store.RevokeNode(ctx, request.NodeID)
}

// trustLeftByFailedRevoke is the other way trust outlives the refusal that
// should have taken it back: the store would not do it. Named beside the
// different-key clause because the two are what Request.TrustLeftInPlace can
// say, and the sentence the owner reads is built from whichever it holds.
const trustLeftByFailedRevoke = "this node could not remove it — its log says why"

// sourceHost is the host half of where a request actually came from.
//
// The listener's view, not anything in the body: this is the one thing about an
// incoming pairing request that its sender cannot choose freely, which is why
// the flood bound is keyed on it.
func sourceHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return host
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
	// Checked again here rather than trusted from the row: the policy is the
	// one that decides what is dialled, and the row was filled minutes ago.
	addresses := []string{}
	if address := s.acceptablePeerAddress(request.Address); address != "" {
		addresses = append(addresses, address)
	}
	addresses = append(addresses, s.acceptableAlternates(request.Address, request.Alternates)...)
	if len(addresses) > registry.MaxNodeAddresses {
		addresses = addresses[:registry.MaxNodeAddresses]
	}
	// Nothing claimed leaves whatever is recorded alone, as it always has: a
	// re-pairing that offered no address is not a decision that there is none.
	if len(addresses) > 0 {
		if err := s.store.SetNodeAddresses(ctx, request.NodeID, addresses, s.deliveryPolicy); err != nil {
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

// acceptableAlternates is the addresses a pairing payload listed that this node
// would deliver to, beside preferred (ADR-005 §4).
//
// Each is checked as acceptablePeerAddress checks one, and loopback is dropped
// as well: another machine's loopback is this machine's, so an alternate there
// could only ever reach this node itself. The preferred address and repeats are
// dropped, and what is left is cut so preferred and alternates together are at
// most MaxPairAddresses — the sender was asked for no more, and a sender that
// sends more does not get to make this node dial them.
func (s *Server) acceptableAlternates(preferred string, claimed []string) []string {
	preferred = strings.TrimSpace(preferred)
	room := protocol.MaxPairAddresses
	if s.acceptablePeerAddress(preferred) != "" {
		room--
	}
	seen := map[string]bool{preferred: true}
	var kept []string
	for _, address := range claimed {
		if len(kept) == room {
			break
		}
		address = s.acceptablePeerAddress(address)
		if address == "" || seen[address] || isLoopbackAddress(address) {
			continue
		}
		seen[address] = true
		kept = append(kept, address)
	}
	return kept
}

// isLoopbackAddress reports a host:port whose host is a loopback literal or
// the name localhost.
func isLoopbackAddress(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	parsed, err := netip.ParseAddr(host)
	return err == nil && parsed.Unmap().IsLoopback()
}

// writePairStateError says, in one sentence, what this request is and what the
// owner can do instead.
//
// One sentence deliberately. Wrapping the store's error inside the handler's
// stuttered the same fact three ways — "pairing request … is rejected: pairing
// request is not waiting for that: it is rejected, not awaiting-confirm" — and
// a person reading that has to work out which clause is the news.
func writePairStateError(w http.ResponseWriter, request pairing.Request, wanted string, err error) {
	if errors.Is(err, pairing.ErrNoSuchRequest) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such pairing request")
		return
	}
	var message string
	switch {
	case request.State == pairing.StateRejected:
		message = fmt.Sprintf("pairing request %s was refused; nothing from it is trusted. "+
			"Start again with `ah pair request <host:port>` if you still want to pair", request.ID)
	case request.State == pairing.StateExpired:
		message = fmt.Sprintf("pairing request %s ran out before both owners agreed. "+
			"Open a pairing window on the other machine and start again with "+
			"`ah pair request <host:port>`", request.ID)
	case request.State == pairing.StateApproved:
		message = fmt.Sprintf("pairing request %s is finished and %s is trusted here; "+
			"undo it with `ah revoke %s`", request.ID, request.DisplayName, request.NodeID)
	case request.State == pairing.StatePending && request.Direction == pairing.Outgoing:
		message = fmt.Sprintf("pairing request %s has not been approved on the other machine yet; "+
			"run `ah pair approve %s` there first", request.ID, request.ID)
	case request.State == pairing.StateAwaitingConfirm:
		message = fmt.Sprintf("pairing request %s is waiting for this machine to confirm; "+
			"run `ah pair confirm %s`", request.ID, request.ID)
	default:
		message = fmt.Sprintf("pairing request %s is %s, so it cannot be %s now",
			request.ID, request.State, wanted)
	}
	writeError(w, http.StatusConflict, "PAIRING_STATE", message)
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

// peerNameOr is displayNameOr for a name the other machine wrote. The name is
// printed on the fingerprint-comparison screen beside the fingerprint it
// labels, on one line, and everything ADR-004 asks of the owner is that they
// compare those lines. A name carrying a line break can therefore append a
// forged "receiver" row showing this machine's own fingerprint, with the
// requester's real one pushed to the line below; a name of forty thousand
// bytes can push the owner's own request off the screen. label.Printable is
// what the mDNS path already applies to the same fields: it refuses control
// characters, bidi overrides and anything over label.MaxLength, and returns ""
// for a name it will not vouch for, which falls back to the verified node id.
func peerNameOr(value, fallback string) string {
	return displayNameOr(label.Printable(value), fallback)
}
