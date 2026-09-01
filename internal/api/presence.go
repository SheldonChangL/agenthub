package api

import (
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

	now := time.Now().UTC()
	snapshot := registry.PeerSnapshot{
		NodeID:    envelope.NodeID,
		Sequence:  payload.Sequence,
		ExpiresAt: payload.ExpiresAt,
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
				if payload.Sessions != nil {
					view.Sessions = payload.Sessions
				}
			}
		}
		peers = append(peers, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"peers": peers})
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
