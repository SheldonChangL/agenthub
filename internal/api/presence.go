package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"agenthub.local/agenthub/internal/identity"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
)

// maxHeartbeatBody bounds what an unauthenticated caller can make this node
// read before anything has been verified. A heartbeat carries session
// summaries, not transcripts, so a megabyte is already generous.
const maxHeartbeatBody = 1 << 20

// receiveHeartbeat accepts one peer's presence snapshot.
//
// This is the first endpoint that exists to be called by another machine, so
// the order of the checks is the contract, not an implementation detail:
//
//  1. Decode the envelope. Nothing about it is believed yet.
//  2. Look the sender up in the trust store. An unpaired node is refused here,
//     before anything is read from its payload and before this node says
//     anything about itself. That is issue #14's "an unpaired node gets no
//     session data" — it also gets no confirmation of what this node knows.
//  3. Verify the signature and the recipient together, with the key the owner
//     already trusts for that node ID. Never with a key from the envelope.
//  4. Only then look at the payload.
//
// Every refusal answers with the same shape and no detail about which check
// failed. A caller that is not paired must not be able to tell "unknown node"
// from "bad signature" from "wrong recipient": that difference is a probe
// oracle for whether this owner has paired with a given node ID.
func (s *Server) receiveHeartbeat(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, maxHeartbeatBody)
	var envelope protocol.Envelope
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "envelope is not readable")
		return
	}

	if envelope.Type != protocol.TypeNodeHeartbeat {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "envelope is not a heartbeat")
		return
	}

	// refuse answers everything a caller who has not proven itself may learn.
	refuse := func(reason string, err error) {
		log.Printf("heartbeat refused from %q: %s: %v", envelope.NodeID, reason, err)
		writeError(w, http.StatusForbidden, "HEARTBEAT_REFUSED", "heartbeat was not accepted")
	}

	peer, err := s.store.TrustedNode(r.Context(), envelope.NodeID)
	if err != nil {
		if !errors.Is(err, registry.ErrNotFound) {
			// Reading the trust store failed. Refusing would tell a paired peer
			// it is no longer paired, which is not what happened.
			writeInternalError(w, "TRUST_UNAVAILABLE", "could not check the sender", err)
			return
		}
		refuse("sender is not a trusted node", err)
		return
	}
	publicKey, err := identity.DecodePublicKey(peer.PublicKey)
	if err != nil {
		// The trust store holds a key this build cannot parse. That is this
		// node's own damage, not the sender's, but the answer is the same.
		refuse("stored public key is unusable", err)
		return
	}
	if err := envelope.VerifyDirected(publicKey, envelope.NodeID, s.node.ID); err != nil {
		refuse("envelope is not authentically addressed to this node", err)
		return
	}

	var payload protocol.HeartbeatPayload
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		refuse("payload is not a heartbeat payload", err)
		return
	}

	// Checked before it is stored, and again by listPeers on the way out for
	// rows a build without this check already wrote. What is inside a snapshot
	// reaches an agent's reasoning through agent_list with no notice and no
	// attribution, and the desktop reads the same rows — a check in any one
	// reader protects only that reader.
	if err := protocol.ValidateIncomingPayload(envelope.NodeID, payload); err != nil {
		refuse("payload describes sessions this node will not store", err)
		return
	}

	now := time.Now().UTC()
	snapshot := registry.PeerSnapshot{
		NodeID:   envelope.NodeID,
		Sequence: payload.Sequence,
		// Clamped: a peer choosing its own expiry could otherwise declare itself
		// online indefinitely with one heartbeat.
		ExpiresAt: protocol.ClampExpiry(payload.ExpiresAt, now),
		Payload:   envelope.Payload,
	}
	err = s.store.StorePeerSnapshot(r.Context(), snapshot, now)
	switch {
	case err == nil:
		if markErr := s.store.MarkNodeSeen(r.Context(), envelope.NodeID, now); markErr != nil {
			log.Printf("could not record last-seen for %q: %v", envelope.NodeID, markErr)
		}
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, registry.ErrStaleSnapshot):
		// The sender is authentic; this delivery is a replay or arrived out of
		// order. Saying so is safe — it is only reachable after a valid
		// signature — and a sender that is genuinely behind needs to know.
		writeError(w, http.StatusConflict, "STALE_HEARTBEAT",
			"a heartbeat with this sequence or a later one has already been accepted")
	case errors.Is(err, registry.ErrSnapshotExpired):
		writeError(w, http.StatusConflict, "EXPIRED_HEARTBEAT", "the heartbeat had already expired on arrival")
	case errors.Is(err, registry.ErrInvalidSession):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
	default:
		writeInternalError(w, "HEARTBEAT_STORE_FAILED", "could not store the heartbeat", err)
	}
}

// listPeers reports what this node currently believes about its paired peers.
//
// An expired snapshot is reported as offline rather than dropped: "this peer
// went quiet at 10:04" is what the owner needs, and deleting the row would make
// a peer that stopped sending indistinguishable from one that was never paired.
func (s *Server) listPeers(w http.ResponseWriter, r *http.Request) {
	snapshots, err := s.store.ListPeerSnapshots(r.Context())
	if err != nil {
		writeInternalError(w, "PEERS_FAILED", "could not read peer presence", err)
		return
	}
	byNode := make(map[string]registry.PeerSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		byNode[snapshot.NodeID] = snapshot
	}

	trusted, err := s.store.TrustedNodes(r.Context())
	if err != nil {
		writeInternalError(w, "PEERS_FAILED", "could not read the trust store", err)
		return
	}

	now := time.Now().UTC()
	type peerView struct {
		NodeID      string                    `json:"nodeId"`
		DisplayName string                    `json:"displayName"`
		Online      bool                      `json:"online"`
		Sequence    uint64                    `json:"sequence,omitempty"`
		ReceivedAt  *time.Time                `json:"receivedAt,omitempty"`
		ExpiresAt   *time.Time                `json:"expiresAt,omitempty"`
		Sessions    []protocol.SessionSummary `json:"sessions"`
		// SessionsWithheld distinguishes "this peer published nothing" from
		// "this node refused what it published". Without it the two render the
		// same, and the second is the one an owner needs to act on.
		SessionsWithheld bool `json:"sessionsWithheld,omitempty"`
	}

	peers := make([]peerView, 0, len(trusted))
	for _, node := range trusted {
		view := peerView{NodeID: node.NodeID, DisplayName: node.DisplayName, Sessions: []protocol.SessionSummary{}}
		snapshot, held := byNode[node.NodeID]
		if held {
			received, expires := snapshot.ReceivedAt, snapshot.ExpiresAt
			view.Sequence, view.ReceivedAt, view.ExpiresAt = snapshot.Sequence, &received, &expires
			if !snapshot.Expired(now) {
				view.Online = true
				var payload protocol.HeartbeatPayload
				if err := json.Unmarshal(snapshot.Payload, &payload); err != nil {
					writeInternalError(w, "PEERS_FAILED", "could not read peer presence",
						fmt.Errorf("stored snapshot for %q is unreadable: %w", node.NodeID, err))
					return
				}
				// The same check the receiving edge applies, applied again on
				// the way out. A snapshot stored by a build that predates that
				// check is still in the database after an upgrade, and this is
				// the layer that hands it to the desktop — where a session
				// attributed to a third node the owner has also paired with is
				// read as that node's, and a person makes a trust decision on
				// it.
				//
				// The whole snapshot goes, not the offending row: a peer that
				// has started claiming other people's sessions is not a source
				// whose remaining rows are worth serving, which is the same
				// judgement `Peers()` makes in agenthub-mcp.
				//
				// The peer stays listed and online rather than disappearing: it
				// is reachable, and its next heartbeat replaces this. What it
				// does not do is look like a peer that published nothing —
				// sessionsWithheld says which of the two this is. This is not
				// what the receiving edge does with the same content: there, a
				// refusal leaves the previous valid snapshot in place, and here
				// there is nothing to fall back to.
				if err := protocol.ValidateIncomingPayload(node.NodeID, payload); err != nil {
					view.SessionsWithheld = true
					s.noteUnservableSnapshot(node.NodeID, snapshot.Sequence, err)
				} else if payload.Sessions != nil {
					view.Sessions = payload.Sessions
				}
			}
		}
		peers = append(peers, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"peers": peers})
}

// noteUnservableSnapshot logs a stored snapshot this node will not serve, once
// per peer per sequence.
//
// Without the bookkeeping every `agent_list` call — which reads this endpoint —
// would write the line again for as long as the row is the newest one held and
// unexpired. That is the case that produces it: an expired snapshot is skipped
// before this is reached, and a row stored by a build that did not clamp expiry
// can sit unexpired indefinitely.
func (s *Server) noteUnservableSnapshot(nodeID string, sequence uint64, reason error) {
	s.refusedMu.Lock()
	defer s.refusedMu.Unlock()
	if last, seen := s.refused[nodeID]; seen && last == sequence {
		return
	}
	if s.refused == nil {
		s.refused = make(map[string]uint64)
	}
	s.refused[nodeID] = sequence
	log.Printf("peers: serving no sessions for %q: stored snapshot %d %v", nodeID, sequence, reason)
}

// setNodeAddress records where a paired peer answers.
//
// The address is validated as host:port and refused if it is not one this build
// will deliver to. Storing an address the publisher would skip would leave the
// owner looking at a configured peer that never receives anything, with the
// reason buried in a log line.
func (s *Server) setNodeAddress(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Address string `json:"address"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	address := strings.TrimSpace(input.Address)
	if address != "" {
		if _, _, err := net.SplitHostPort(address); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST",
				"address must be host:port")
			return
		}
		if err := s.deliveryPolicy(address); err != nil {
			writeError(w, http.StatusBadRequest, "ADDRESS_NOT_ALLOWED", err.Error())
			return
		}
	}
	if err := s.store.SetNodeAddress(r.Context(), r.PathValue("id"), address); err != nil {
		writeRegistryError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// answerChallenge proves this node holds the key its identity advertises.
//
// The answer is what lets a sender confirm it is talking to the peer it thinks
// it is before handing over any session metadata. Address discovery is an
// untrusted input — anything on the network can claim to be at an address — and
// comparing public keys would prove nothing, because a public key is public.
// Only signing over a nonce the challenger chose proves possession.
//
// This endpoint answers anyone, and deliberately so. It reveals nothing that
// GET /v1/node does not already publish, and refusing strangers would mean
// deciding who a caller is before they have proven anything, which is the
// problem this endpoint exists to solve. What makes that safe is the domain
// separation in protocol.ChallengeBytes: signing bytes a stranger chose is a
// signing oracle, and the challenge prefix is what stops it from being a useful
// one — no answer can ever be presented as an envelope signature.
func (s *Server) answerChallenge(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Nonce            string `json:"nonce"`
		ChallengerNodeID string `json:"challengerNodeId"`
	}
	if err := decodeJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	nonce, err := base64.StdEncoding.DecodeString(input.Nonce)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "nonce is not base64")
		return
	}
	answer, err := s.heartbeats.AnswerChallenge(s.node.ID, input.ChallengerNodeID, nonce)
	if err != nil {
		if errors.Is(err, protocol.ErrChallengeRefused) {
			writeError(w, http.StatusBadRequest, "CHALLENGE_REFUSED", err.Error())
			return
		}
		writeInternalError(w, "CHALLENGE_FAILED", "could not answer the challenge", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"nodeId":    s.node.ID,
		"signature": answer,
	})
}
