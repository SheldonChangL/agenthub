package pairing

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// testClock is read by whatever is under test, sometimes from another
// goroutine — the announce loop reads the mode's clock while a test advances
// it — so it is guarded rather than a bare variable.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *testClock) Add(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now.Add(d)
}

func newTestMode() (*Mode, *testClock) {
	clock := &testClock{now: time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)}
	m := NewMode()
	m.now = clock.Now
	return m, clock
}

// Announcing tells everyone on the network that this machine runs AgentHub and
// which key it holds. That has to be off until someone asks for it.
func TestPairingModeIsClosedUntilOpened(t *testing.T) {
	m, _ := newTestMode()
	state := m.State()
	if state.Open {
		t.Error("a new node is advertising before anyone asked")
	}
	if !state.OpenedAt.IsZero() || !state.ExpiresAt.IsZero() {
		t.Errorf("a closed window has times on it: %+v", state)
	}
	if m.IsOpen() {
		t.Error("IsOpen disagrees with State")
	}
}

// The window closes itself. Nothing has to remember, and a lid closed in a cafe
// does not leave a machine advertising in the office tomorrow.
func TestTheWindowClosesItself(t *testing.T) {
	m, clock := newTestMode()
	state, err := m.Open(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Open || state.ExpiresAt != clock.Add(time.Minute) {
		t.Fatalf("state = %+v", state)
	}

	clock.Advance(59 * time.Second)
	if !m.IsOpen() {
		t.Fatal("closed a second early")
	}
	if remaining := m.State().Remaining(clock.Now()); remaining != time.Second {
		t.Errorf("Remaining() = %v, want 1s", remaining)
	}

	clock.Advance(2 * time.Second)
	if m.IsOpen() {
		t.Error("still advertising after the window ended")
	}
	if state := m.State(); !state.ExpiresAt.IsZero() {
		t.Errorf("an expired window still reports an expiry: %+v", state)
	}
	if remaining := m.State().Remaining(clock.Now()); remaining != 0 {
		t.Errorf("Remaining() = %v on a closed window", remaining)
	}
}

// Pressing the button twice must not hold the window open for twice as long:
// "open for five minutes" means the same thing either way.
func TestOpeningAnOpenWindowReplacesItsExpiry(t *testing.T) {
	m, clock := newTestMode()
	if _, err := m.Open(5 * time.Minute); err != nil {
		t.Fatal(err)
	}
	openedAt := m.State().OpenedAt

	clock.Advance(time.Minute)
	state, err := m.Open(5 * time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if want := clock.Add(5 * time.Minute); state.ExpiresAt != want {
		t.Errorf("expiry = %v, want %v; the second open added to the first", state.ExpiresAt, want)
	}
	// And it is still the same window, so a UI does not restart its own idea of
	// when this began.
	if state.OpenedAt != openedAt {
		t.Errorf("openedAt moved from %v to %v", openedAt, state.OpenedAt)
	}
}

// An owner who asks for an hour is told the answer is fifteen minutes, rather
// than given fifteen and left believing they have an hour.
func TestAWindowOutsideTheBoundsIsRefusedNotClamped(t *testing.T) {
	m, _ := newTestMode()
	if _, err := m.Open(time.Hour); !errors.Is(err, ErrWindowTooLong) {
		t.Errorf("Open(1h) = %v, want ErrWindowTooLong", err)
	}
	if _, err := m.Open(time.Second); !errors.Is(err, ErrWindowTooShort) {
		t.Errorf("Open(1s) = %v, want ErrWindowTooShort", err)
	}
	if m.IsOpen() {
		t.Error("a refused window opened anyway")
	}
	// The bounds themselves are usable.
	if _, err := m.Open(MaxWindow); err != nil {
		t.Errorf("Open(MaxWindow) = %v", err)
	}
	if _, err := m.Open(MinWindow); err != nil {
		t.Errorf("Open(MinWindow) = %v", err)
	}
}

// Asking for no particular duration is the common case, and it has to be a
// window someone can actually use.
func TestOpeningWithoutADurationUsesTheDefault(t *testing.T) {
	m, clock := newTestMode()
	state, err := m.Open(0)
	if err != nil {
		t.Fatal(err)
	}
	if want := clock.Add(DefaultWindow); state.ExpiresAt != want {
		t.Errorf("expiry = %v, want %v", state.ExpiresAt, want)
	}
	if DefaultWindow > MaxWindow || DefaultWindow < MinWindow {
		t.Errorf("the default window %v is outside the bounds it is checked against", DefaultWindow)
	}
}

// Closing is what a caller does when it does not know or care what the state
// was — a lid closing, a process shutting down, a second click on the button.
func TestClosingIsIdempotent(t *testing.T) {
	m, _ := newTestMode()
	if state := m.Close(); state.Open {
		t.Error("closing a closed window reported it open")
	}
	if _, err := m.Open(time.Minute); err != nil {
		t.Fatal(err)
	}
	if state := m.Close(); state.Open {
		t.Error("Close did not close")
	}
	if state := m.Close(); state.Open {
		t.Error("closing twice reopened it")
	}
}

// An HTTP handler opens and closes the window while the announce loop reads it.
func TestConcurrentUse(t *testing.T) {
	m := NewMode()
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for i := 0; i < 500; i++ {
				_, _ = m.Open(time.Minute)
				_ = m.IsOpen()
				_ = m.State()
				m.Close()
			}
		}()
	}
	wait.Wait()
}

// The window is measured, not read off a wall clock. Converting to UTC before
// comparing would strip the monotonic reading, and an NTP correction while a
// window was open would then shorten or lengthen it — a step backwards holding
// this machine advertising past the moment the owner was shown.
func TestTheWindowIsMeasuredOnTheMonotonicClock(t *testing.T) {
	m := NewMode()
	if _, err := m.Open(time.Minute); err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	openedAt, expiresAt := m.openedAt, m.expiresAt
	m.mu.Unlock()
	// Round(0) is how a time is stripped of its monotonic reading, so a value
	// that carries one differs from its own stripped copy.
	for name, stored := range map[string]time.Time{"openedAt": openedAt, "expiresAt": expiresAt} {
		if stored.Round(0).Equal(stored) && stored.Round(0) == stored {
			t.Errorf("%s has no monotonic reading, so the window runs on the wall clock", name)
		}
	}
	// What is presented carries none of it: a monotonic reading means nothing
	// to whoever reads the JSON, and time.Time marshals the wall clock anyway.
	state := m.State()
	if state.ExpiresAt.Round(0) != state.ExpiresAt || state.OpenedAt.Round(0) != state.OpenedAt {
		t.Error("the presented state carries a monotonic reading")
	}
	if state.ExpiresAt.Location() != time.UTC || state.OpenedAt.Location() != time.UTC {
		t.Errorf("the presented state is not in UTC: %+v", state)
	}
}
