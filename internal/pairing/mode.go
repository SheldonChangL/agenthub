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
	return NewModeWithClock(time.Now)
}

// NewModeWithClock is NewMode against a clock the caller supplies.
//
// Exported so a test elsewhere can put the boundary where it wants it rather
// than sleeping up to it, and so a caller that must present a countdown reads
// the same clock the expiry was computed from — see Now.
func NewModeWithClock(now func() time.Time) *Mode {
	return &Mode{now: now}
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
	// Not .UTC() here: that strips the monotonic reading, which would leave the
	// window's length at the mercy of the wall clock. An NTP correction while a
	// window is open would then shorten or lengthen it, and a step backwards
	// would hold this machine advertising past the moment the owner was shown.
	// Converted to UTC in state(), where it is being read rather than compared.
	now := m.now()
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
	return m.state(m.now())
}

// State reports the window, having first let the clock close it.
func (m *Mode) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state(m.now())
}

// Open reports whether the window is open, which is the question the announce
// loop asks on every tick.
func (m *Mode) IsOpen() bool {
	return m.State().Open
}

// Now is the clock this window is measured against, for a caller computing how
// much of it is left. Exported so that answer comes from the same clock as the
// expiry it is subtracted from, rather than from time.Now() beside it.
func (m *Mode) Now() time.Time {
	return m.now()
}

// state answers from the stored times without changing them. An expired window
// is reported as closed and its times are left alone rather than cleared: they
// are never read while closed, and a read that quietly writes is one a caller
// has to think about.
func (m *Mode) state(now time.Time) State {
	if !m.isOpen(now) {
		// A closed window has no expiry. Reporting one invites a reader to
		// subtract two times and show a negative countdown.
		return State{}
	}
	// Presented in UTC. The stored values carry a monotonic reading, which is
	// what measures the window but means nothing to whoever reads the JSON.
	return State{Open: true, OpenedAt: m.openedAt.UTC(), ExpiresAt: m.expiresAt.UTC()}
}

func (m *Mode) isOpen(now time.Time) bool {
	return !m.expiresAt.IsZero() && now.Before(m.expiresAt)
}
