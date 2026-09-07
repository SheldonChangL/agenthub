// Package pairing holds the owner's pairing window: the bounded period during
// which this node says on the local network that it is willing to pair, and
// collects the machines saying the same.
//
// Nothing here creates trust. Opening the window announces an identity and
// listens for others; it grants nothing, publishes no session, and changes no
// audience. Pairing itself remains a decision the owner makes against a
// fingerprint they compared.
package pairing

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// MaxWindow is the longest pairing window this node will hold open.
//
// Announcing tells everyone on the network that this machine runs AgentHub, and
// which key it holds. That is a trade the owner makes deliberately for as long
// as it takes to pair, and a window left open by accident should not be a
// permanent broadcast — a laptop that opened one in a cafe and closed the lid
// must not still be advertising in the office tomorrow.
const MaxWindow = 15 * time.Minute

// DefaultWindow is what opening the window without saying how long asks for.
// Long enough to walk to the other machine and read a fingerprint off it.
const DefaultWindow = 5 * time.Minute

// MinWindow refuses a window too short to use. A window of a second would
// announce once, be over before anyone looked, and read as a broken feature
// rather than a closed one.
const MinWindow = 30 * time.Second

// ErrWindowTooLong and ErrWindowTooShort are returned rather than clamped: an
// owner who asked for an hour should be told the answer is fifteen minutes, not
// given fifteen minutes and left believing they have an hour.
var (
	ErrWindowTooLong  = fmt.Errorf("a pairing window may last at most %s", MaxWindow)
	ErrWindowTooShort = fmt.Errorf("a pairing window must last at least %s", MinWindow)
)

// ErrClosed reports that pairing mode is not open.
var ErrClosed = errors.New("pairing mode is closed")

// State is what the owner is told about the window.
type State struct {
	// Open is the whole answer to "is this machine advertising".
	Open bool `json:"open"`
	// OpenedAt and ExpiresAt are absent when closed. ExpiresAt is what a UI
	// counts down, and what makes the window visibly temporary rather than a
	// setting someone has to remember to turn off.
	OpenedAt  time.Time `json:"openedAt,omitzero"`
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
}

// Remaining is how long the window has left, or zero when it is closed.
func (s State) Remaining(now time.Time) time.Duration {
	if !s.Open {
		return 0
	}
	if remaining := s.ExpiresAt.Sub(now); remaining > 0 {
		return remaining
	}
	return 0
}

// Mode is the pairing window. Closed until something opens it, and closed again
// by the clock without anything having to remember.
//
// Safe for concurrent use: an HTTP handler opens and closes it while the
// announce loop reads it.
type Mode struct {
	now func() time.Time

	mu        sync.Mutex
	openedAt  time.Time
	expiresAt time.Time
}

func NewMode() *Mode {
	return &Mode{now: time.Now}
}

// Open starts or extends the window.
//
// A duration of zero means DefaultWindow. Opening an already-open window
// replaces its expiry rather than adding to it, so "open for five minutes"
// means the same thing whether or not it was already open — an owner pressing
// the button twice must not accidentally hold it open for ten.
func (m *Mode) Open(window time.Duration) (State, error) {
	if window == 0 {
		window = DefaultWindow
	}
	if window > MaxWindow {
		return State{}, ErrWindowTooLong
	}
	if window < MinWindow {
		return State{}, ErrWindowTooShort
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now().UTC()
	if !m.isOpen(now) {
		m.openedAt = now
	}
	m.expiresAt = now.Add(window)
	return m.state(now), nil
}

// Close shuts the window now. Idempotent: closing a closed window is what a
// caller does when it does not know or care what the state was.
func (m *Mode) Close() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.openedAt = time.Time{}
	m.expiresAt = time.Time{}
	return m.state(m.now().UTC())
}

// State reports the window, having first let the clock close it.
func (m *Mode) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state(m.now().UTC())
}

// Open reports whether the window is open, which is the question the announce
// loop asks on every tick.
func (m *Mode) IsOpen() bool {
	return m.State().Open
}

func (m *Mode) state(now time.Time) State {
	if !m.isOpen(now) {
		// Cleared rather than reported as an expired window: a closed window
		// has no expiry, and leaving one behind invites a reader to subtract
		// two times and get a negative countdown.
		m.openedAt = time.Time{}
		m.expiresAt = time.Time{}
		return State{}
	}
	return State{Open: true, OpenedAt: m.openedAt, ExpiresAt: m.expiresAt}
}

func (m *Mode) isOpen(now time.Time) bool {
	return !m.expiresAt.IsZero() && now.Before(m.expiresAt)
}
