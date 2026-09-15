package codexdriver

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrThreadBusy is returned when a thread already has a turn in flight and as
// many messages queued behind it as this node will hold.
//
// The message is not lost by this: nothing about waking consumes an inbox
// entry, so a refused wake leaves the message exactly where a wake that never
// happened would — readable, with an audit row saying why it did not start a
// turn of its own.
var ErrThreadBusy = errors.New("the thread already has a turn running and a full queue behind it")

// ErrTurnCoalesced is returned when app-server answered turn/start with the id
// of a turn this lane had already recorded.
//
// Measured against codex-cli 0.153.4: a turn/start into a busy thread does not
// open a second turn and does not fail. The input is appended to the running
// turn's queue, and when several land together they are merged into one model
// call that answers only the last of them — one of three probe messages got no
// reply at all. So a repeated turn id means this message may have been
// swallowed, and saying so is the only honest thing to record.
//
// What it catches is this node's own turn, let go of too early: the backstop
// expiring on a turn that never reported completing, or WaitForTurn failing
// while the turn was still running. The lane is free again, the turn is not
// over, and the next message goes into it — which is precisely the case the
// lane exists to prevent and cannot prevent once the thread has been released
// on a guess.
//
// A turn started outside this node — the owner's own Codex window driving the
// same thread — is invisible to it, and saying otherwise would be claiming a
// reach this check does not have: app-server answers with an id this lane
// never recorded, so it differs from lastTurn and reports nothing. The same
// blindness follows a turn/start whose answer was lost, for the same reason:
// an id that was never seen cannot be seen to repeat. Holding the thread, not
// this error, is what covers that case.
var ErrTurnCoalesced = errors.New("app-server merged this message into a turn that was already running")

// lane is one Codex thread's turn-at-a-time discipline.
//
// Whoever holds the lane owns the thread's next turn, from before turn/start
// until the server's turn/completed for it. Waiters are served in arrival
// order: two messages from one peer arriving together should reach the agent
// in the order they were sent, and a plain channel hands the slot to whichever
// goroutine the runtime picks.
type lane struct {
	mu    sync.Mutex
	busy  bool
	queue []chan struct{}
	// lastTurn is the turn id app-server gave for the previous turn on this
	// thread, which is how a merged turn is recognised.
	lastTurn string
}

// enter takes the lane, waiting behind anyone already queued.
//
// The wait is bounded twice over — by the caller's context and by maxWait —
// because the thing being waited for is a model turn, which can run for as
// long as the model runs. An unbounded wait would let a slow turn accumulate
// one blocked goroutine per message arriving behind it for as long as the turn
// lasted; depth caps how many may queue, and these cap how long each stays.
func (l *lane) enter(ctx context.Context, depth int, maxWait time.Duration) error {
	l.mu.Lock()
	if !l.busy {
		l.busy = true
		l.mu.Unlock()
		return nil
	}
	if len(l.queue) >= depth {
		l.mu.Unlock()
		return fmt.Errorf("%w (%d waiting)", ErrThreadBusy, depth)
	}
	ticket := make(chan struct{})
	l.queue = append(l.queue, ticket)
	l.mu.Unlock()

	timer := time.NewTimer(maxWait)
	defer timer.Stop()
	select {
	case <-ticket:
		return nil
	case <-ctx.Done():
		l.abandon(ticket)
		return fmt.Errorf("waiting for the turn in flight to finish: %w", ctx.Err())
	case <-timer.C:
		l.abandon(ticket)
		return fmt.Errorf("waited %s for the turn in flight to finish: %w", maxWait, ErrThreadBusy)
	}
}

// abandon gives up a queued place.
func (l *lane) abandon(ticket chan struct{}) {
	l.mu.Lock()
	for i, queued := range l.queue {
		if queued == ticket {
			l.queue = append(l.queue[:i], l.queue[i+1:]...)
			l.mu.Unlock()
			return
		}
	}
	l.mu.Unlock()
	// Not in the queue any more means the lane was handed over in the moment
	// this caller gave up. It is ours now whether we wanted it or not, so pass
	// it on rather than leave a thread held by nobody for ever.
	l.leave()
}

// leave hands the lane to the next waiter, or marks the thread free.
func (l *lane) leave() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.queue) > 0 {
		next := l.queue[0]
		l.queue = l.queue[1:]
		close(next)
		return
	}
	l.busy = false
}

// recordTurn stores the turn id app-server gave and reports whether it is the
// one the previous message got.
func (l *lane) recordTurn(turnID string) (repeated bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	repeated = turnID != "" && turnID == l.lastTurn
	if turnID != "" {
		l.lastTurn = turnID
	}
	return repeated
}
