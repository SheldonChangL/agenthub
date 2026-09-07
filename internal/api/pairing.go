package api

import (
	"errors"
	"log"
	"net/http"
	"time"

	"agenthub.local/agenthub/internal/pairing"
)

// PairingAnnouncer is the announce loop as the API needs it.
//
// An interface rather than the concrete type because what the handlers need is
// three answers, and a test that has to build a live announcer to check an
// error message is a test that stops being written.
type PairingAnnouncer interface {
	// Status is what the loop last managed to do, which is not the same
	// question as whether the window is open.
	Status() pairing.Status
	// Announceable reports whether this node has any address a peer could
	// reach. Asked before opening, so an owner is told pairing cannot work here
	// instead of being handed a window that announces nothing.
	Announceable() bool
	// Wake announces now rather than at the next tick.
	Wake()
}

// pairingUnavailable answers the endpoints when the node was started without
// discovery.
//
// A 409 rather than an empty answer, because "nobody is advertising" and "this
// node is not looking" are different facts and only one of them means the owner
// should keep waiting. The message names the flags, since the fix is a restart
// with more arguments.
func (s *Server) pairingUnavailable(w http.ResponseWriter) bool {
	if s.pairing != nil && s.candidates != nil && s.announcer != nil {
		return false
	}
	writeError(w, http.StatusConflict, "DISCOVERY_DISABLED",
		"this node is not listening on the local network, so it can neither advertise "+
			"nor see anyone advertising. Start it with -discover, and with -allow-lan and a "+
			"-peer-listen address on the local network so there is an address to announce; "+
			"pairing by hand with `ah pair` works either way")
	return true
}

// pairingState reports whether this machine is currently advertising.
func (s *Server) pairingState(w http.ResponseWriter, _ *http.Request) {
	if s.pairingUnavailable(w) {
		return
	}
	s.writePairingState(w, http.StatusOK, s.pairing.State())
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
	// Bounded before the multiplication rather than after. A duration is
	// nanoseconds in an int64, so seconds above about 9.2e9 wrap: 18446744104
	// seconds came back as a thirty-second window and was accepted as if it
	// were what the owner asked for. Anything over the maximum gets the same
	// answer a slightly-too-long window gets.
	if int64(input.Seconds) > int64(pairing.MaxWindow/time.Second) {
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", pairing.ErrWindowTooLong.Error())
		return
	}
	// Refused rather than opened, because a window on a node with no address to
	// announce is a window that does nothing while saying it is open. Checked
	// here as well as reported in the state, since this is the moment an owner
	// is waiting for an answer.
	if !s.announcer.Announceable() {
		writeError(w, http.StatusConflict, "NO_ANNOUNCEABLE_ADDRESS",
			"this node has no address a peer on the local network could reach, so opening "+
				"pairing mode would announce nothing. Start it with -allow-lan and a "+
				"-peer-listen address on this machine's network address rather than loopback")
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
	// Announced now rather than at the next tick: the shortest window this node
	// allows is thirty seconds and the interval is twenty, so waiting would
	// spend most of a short window silent while the UI counted down.
	s.announcer.Wake()
	// Logged because it is a change to what this machine says about itself on
	// the network, and whoever reads the log may not be who made the request.
	log.Printf("pairing mode open until %s", state.ExpiresAt.Format(time.RFC3339))
	// Answered from the state Open returned, not from a second read: a DELETE
	// arriving between the two would have this reply say the window it just
	// opened is closed.
	s.writePairingState(w, http.StatusOK, state)
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
	s.writePairingState(w, http.StatusOK, s.pairing.Close())
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

// writePairingState answers with the window and with what the announce loop is
// actually doing.
//
// Both, because they are different facts: a window can be open on a machine
// that has nothing to announce, and an owner staring at "open" with no
// candidate appearing on the other machine needs to be able to tell which side
// the problem is on.
func (s *Server) writePairingState(w http.ResponseWriter, status int, state pairing.State) {
	body := map[string]any{"open": state.Open, "announcing": s.announcer.Status()}
	if state.Open {
		body["openedAt"] = state.OpenedAt
		body["expiresAt"] = state.ExpiresAt
		// Subtracted from the clock the expiry was computed against. Reading
		// time.Now() here instead would make the countdown disagree with the
		// expiry beside it whenever the two clocks are not the same one.
		body["remainingSeconds"] = int(state.Remaining(s.pairing.Now()).Seconds())
	}
	writeJSON(w, status, body)
}
