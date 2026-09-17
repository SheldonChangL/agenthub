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
	// Unannounceable says why this node cannot be announced, in words an owner
	// can act on, and is empty when it can be. Asked before opening, so an
	// owner is told what to change instead of being handed a window that
	// announces nothing.
	Unannounceable() string
	// Wake announces now rather than at the next tick.
	Wake()
}

// discoveryUnavailable answers the endpoints that need the local network when
// the node was started without discovery.
//
// A 409 rather than an empty answer, because "nobody is advertising" and "this
// node is not looking" are different facts and only one of them means the owner
// should keep waiting. The message names the flags, since the fix is a restart
// with more arguments.
func (s *Server) discoveryUnavailable(w http.ResponseWriter) bool {
	if s.candidates != nil && s.announcer != nil {
		return false
	}
	writeError(w, http.StatusConflict, "DISCOVERY_DISABLED",
		"this node is not listening on the local network, so it can neither advertise "+
			"nor see anyone advertising. Start it with -discover, and with -allow-lan and a "+
			"-peer-listen address on the local network so there is an address to announce; "+
			"pairing by hand or with `ah pair request <host:port>` works either way")
	return true
}

// windowUnavailable answers when this node has no pairing window at all.
//
// The window is not discovery. It is the node-level state that says this owner
// is currently willing to be asked, and the exchange in pair.go needs exactly
// that and nothing else: two machines that cannot hear each other's
// announcements pair by one owner typing the other's address. Requiring
// -discover to open it made the exchange unusable in the case it was written
// for. Announcing over mDNS is the part discovery owns, and it is reported
// separately below.
func (s *Server) windowUnavailable(w http.ResponseWriter) bool {
	if s.pairing != nil {
		return false
	}
	writeError(w, http.StatusConflict, "DISCOVERY_DISABLED",
		"this node was started without pairing mode, so there is no window to open; "+
			"pair by hand with `ah pair` instead")
	return true
}

// pairingState reports whether this machine is currently advertising.
func (s *Server) pairingState(w http.ResponseWriter, _ *http.Request) {
	if s.windowUnavailable(w) {
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
	if s.windowUnavailable(w) {
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
	// Bounded on both sides before the multiplication, rather than after. A
	// duration is nanoseconds in an int64, so a magnitude above about 9.2e9
	// seconds wraps, and the wrapped value lands inside the allowed range in
	// both directions: 18446744104 came back as a thirty-second window, and
	// -18446744043 as a thirty-second window too, while math.MinInt64 came back
	// as exactly zero — which Open reads as "no preference" and answers with
	// its default. Every one of those was accepted as what the owner asked for.
	//
	// A negative window is refused here rather than left to Open's lower bound:
	// Open never sees the number, only the duration it wrapped into.
	switch {
	case input.Seconds < 0:
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", pairing.ErrWindowTooShort.Error())
		return
	case int64(input.Seconds) > int64(pairing.MaxWindow/time.Second):
		writeError(w, http.StatusBadRequest, "INVALID_REQUEST", pairing.ErrWindowTooLong.Error())
		return
	}
	// A window that cannot be announced is still a window: the other machine can
	// be given this node's address to type. What the owner must not be left with
	// is a window they believe is advertising when it is not, so the state this
	// answers with carries a notice saying so, with the address to type.
	//
	// It used to be refused here. That was right while being found on the
	// network was the only way to pair, and wrong once an address could be
	// typed instead.
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
	// spend most of a short window silent while the UI counted down. There is
	// nothing to wake on a node without discovery, and that is not an error.
	if s.announcer != nil {
		s.announcer.Wake()
	}
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
	if s.windowUnavailable(w) {
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
	if s.discoveryUnavailable(w) {
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
	// displayName travels with the announce status because it is part of it:
	// this is the string a stranger on the segment reads. A UI that warns
	// about what is being broadcast has to be able to re-read it, and the
	// name changes when the node restarts with a different -display-name.
	// A node without discovery announces nothing and has no announcer to ask.
	// The zero status is the true answer — no announceable addresses, nothing
	// attempted — and the notice below says why, so "not announcing" is never
	// left to be guessed at.
	var announcing pairing.Status
	if s.announcer != nil {
		announcing = s.announcer.Status()
	}
	body := map[string]any{
		"open":        state.Open,
		"announcing":  announcing,
		"displayName": s.node.DisplayName,
		// Where the name came from, because the two have different remedies:
		// a name read from the machine is changed by renaming the machine or
		// passing the flag, a chosen one only by passing the flag. A UI that
		// states the wrong one sends its reader to the wrong place.
		"nameIsChosen": s.node.NameIsChosen,
	}
	if state.Open {
		body["openedAt"] = state.OpenedAt
		body["expiresAt"] = state.ExpiresAt
		// Subtracted from the clock the expiry was computed against. Reading
		// time.Now() here instead would make the countdown disagree with the
		// expiry beside it whenever the two clocks are not the same one.
		body["remainingSeconds"] = int(state.Remaining(s.pairing.Now()).Seconds())
	}
	if notice := s.announceNotice(); notice != "" {
		body["notice"] = notice
	}
	writeJSON(w, status, body)
}

// announceNotice says why this node is not advertising, and what to do instead.
//
// Empty when it is advertising: a notice that is always there is one nobody
// reads. Otherwise it names the other machine's way in — the address to type —
// because on a node without discovery that is the whole remedy, and an owner
// staring at "open" with nothing appearing on the other machine needs to be
// told which half is missing.
func (s *Server) announceNotice() string {
	reason := "discovery is off (this node was started without -discover)"
	if s.announcer != nil {
		reason = s.announcer.Unannounceable()
		if reason == "" {
			return ""
		}
	}
	notice := "this node is not announcing itself over mDNS, so a window opened here will not " +
		"put it in anyone's candidate list: " + reason
	if s.peerAddress != "" {
		return notice + ". The other machine can still pair by typing this address: " +
			"`ah pair request " + s.peerAddress + "`"
	}
	return notice + ". The other machine can still pair with " +
		"`ah pair request <this machine's host:port>`, or by hand with `ah pair`"
}
