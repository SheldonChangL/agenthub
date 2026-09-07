package api

import (
	"errors"
	"log"
	"net/http"
	"time"

	"agenthub.local/agenthub/internal/pairing"
)

// pairingUnavailable answers the endpoints when the node was started without
// discovery.
//
// A 409 rather than an empty answer, because "nobody is advertising" and "this
// node is not looking" are different facts and only one of them means the owner
// should keep waiting. The message names the flag, since the fix is a restart
// with one more argument.
func (s *Server) pairingUnavailable(w http.ResponseWriter) bool {
	if s.pairing != nil && s.candidates != nil {
		return false
	}
	writeError(w, http.StatusConflict, "DISCOVERY_DISABLED",
		"this node is not listening on the local network, so it can neither advertise "+
			"nor see anyone advertising. Start it with -discover to use pairing mode; "+
			"pairing by hand with `ah pair` works either way")
	return true
}

// pairingState reports whether this machine is currently advertising.
func (s *Server) pairingState(w http.ResponseWriter, _ *http.Request) {
	if s.pairingUnavailable(w) {
		return
	}
	s.writePairingState(w, http.StatusOK)
}

// openPairing starts the window.
//
// The body is optional: a caller that does not care how long gets the default.
// A window outside the bounds is refused rather than clamped, so an owner who
// asked for an hour is told the answer is fifteen minutes instead of being
// given fifteen and believing they have an hour.
func (s *Server) openPairing(w http.ResponseWriter, r *http.Request) {
	if s.pairingUnavailable(w) {
		return
	}
	var input struct {
		// Seconds rather than a duration string: this is a JSON API, and a
		// number needs no parser on either side.
		Seconds int `json:"seconds"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &input); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
			return
		}
	}
	if input.Seconds < 0 {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", "seconds must not be negative")
		return
	}
	state, err := s.pairing.Open(time.Duration(input.Seconds) * time.Second)
	switch {
	case errors.Is(err, pairing.ErrWindowTooLong), errors.Is(err, pairing.ErrWindowTooShort):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	case err != nil:
		writeInternalError(w, "PAIRING_FAILED", "could not open pairing mode", err)
		return
	}
	// Logged because it is a change to what this machine says about itself on
	// the network, and whoever reads the log may not be who made the request.
	log.Printf("pairing mode open until %s", state.ExpiresAt.Format(time.RFC3339))
	s.writePairingState(w, http.StatusOK)
}

// closePairing stops advertising now. Idempotent, so a caller that does not
// know the current state can simply close.
func (s *Server) closePairing(w http.ResponseWriter, _ *http.Request) {
	if s.pairingUnavailable(w) {
		return
	}
	if s.pairing.State().Open {
		log.Print("pairing mode closed")
	}
	s.pairing.Close()
	s.writePairingState(w, http.StatusOK)
}

// pairingCandidates lists the machines currently advertising.
//
// Every field here is a claim by whoever sent the packet. Nothing in this list
// is trusted, appearing in it grants nothing, and the fingerprint shown is the
// one announced — which is a hint for finding the right row, never evidence.
// What settles identity is comparing the fingerprint of the key that arrives in
// the handshake, on both machines.
func (s *Server) pairingCandidates(w http.ResponseWriter, _ *http.Request) {
	if s.pairingUnavailable(w) {
		return
	}
	listed := s.candidates.List()
	rows := make([]candidateView, 0, len(listed))
	for _, candidate := range listed {
		rows = append(rows, candidateView{
			NodeID:      candidate.NodeID,
			Address:     candidate.Address,
			DisplayName: candidate.DisplayName,
			Platform:    candidate.Platform,
			Fingerprint: candidate.Fingerprint,
			FirstSeen:   candidate.FirstSeen,
			LastSeen:    candidate.LastSeen,
			Duplicate:   candidate.Duplicate,
			Contested:   candidate.Contested,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"candidates": rows,
		"pairing":    s.pairing.State(),
		// Said rather than left to be inferred from a short list: a full list
		// is one an attacker can produce, and the owner needs to know that the
		// machine they are looking for may be missing for that reason.
		"full":   s.candidates.Full(),
		"notice": candidateNotice,
	})
}

// candidateView is one advertising machine, as the owner sees it.
type candidateView struct {
	NodeID      string    `json:"nodeId"`
	Address     string    `json:"address"`
	DisplayName string    `json:"displayName,omitempty"`
	Platform    string    `json:"platform,omitempty"`
	Fingerprint string    `json:"fingerprint"`
	FirstSeen   time.Time `json:"firstSeen"`
	LastSeen    time.Time `json:"lastSeen"`
	// Duplicate: another row claims this display name or this fingerprint.
	Duplicate bool `json:"duplicate,omitempty"`
	// Contested: something else has announced different details under this node
	// id since it was first seen.
	Contested bool `json:"contested,omitempty"`
}

const candidateNotice = "Every field here was chosen by whoever sent the packet, on a network " +
	"anyone can write to. Nothing in this list has been verified and appearing in it grants nothing. " +
	"The fingerprint shown is the one announced: use it to find the right machine, never as proof " +
	"of which machine it is. What settles that is comparing the fingerprint on both machines when " +
	"pairing."

func (s *Server) writePairingState(w http.ResponseWriter, status int) {
	state := s.pairing.State()
	body := map[string]any{"open": state.Open}
	if state.Open {
		body["openedAt"] = state.OpenedAt
		body["expiresAt"] = state.ExpiresAt
		body["remainingSeconds"] = int(state.Remaining(time.Now().UTC()).Seconds())
	}
	writeJSON(w, status, body)
}
