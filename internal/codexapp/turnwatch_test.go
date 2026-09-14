package codexapp

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"
)

// turnCompleted is the frame app-server sends when a turn ends, in the shape
// it actually sends it — threadId beside a turn object, verified against
// codex-cli 0.153.4's TurnCompletedNotification and against a recorded
// session.
func turnCompleted(threadID, turnID string) map[string]any {
	return map[string]any{
		"method": "turn/completed",
		"params": map[string]any{
			"threadId": threadID,
			"turn":     map[string]any{"id": turnID, "items": []any{}},
		},
	}
}

// A caller waiting on a turn is released by the turn's own completion, and by
// nothing else.
func TestWaitForTurnReturnsWhenThatTurnCompletes(t *testing.T) {
	server := newFakeServer(t)
	client := NewClient(server.transport)
	t.Cleanup(func() { _ = client.Close() })

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done <- client.WaitForTurn(ctx, "turn-wanted")
	}()

	// Another thread's turn ending says nothing about this one.
	server.send(t, turnCompleted("thread-other", "turn-other"))
	select {
	case err := <-done:
		t.Fatalf("a different turn's completion released the waiter: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	server.send(t, turnCompleted("thread-1", "turn-wanted"))
	if err := <-done; err != nil {
		t.Fatalf("WaitForTurn() error = %v", err)
	}
}

// A completion that arrives before anyone waits on it still releases the wait.
//
// The response to turn/start and the turn's completion come off one reader
// goroutine with nothing ordering them, so the caller can learn the turn id
// after the turn is already over. A tracker that only signalled live waiters
// would park that caller for ever — and it holds a thread's turn slot while it
// waits, so the session would never be woken again.
func TestACompletionSeenBeforeTheWaitStillReleasesIt(t *testing.T) {
	var tracker turnTracker
	tracker.complete("turn-early")
	select {
	case <-tracker.wait("turn-early").ready:
	default:
		t.Fatal("a turn that completed before anyone waited on it left the waiter parked; " +
			"nothing will ever send that completion again")
	}
}

// A connection that dies under a turn releases everyone waiting on it.
//
// Nobody will ever send that turn's completion now. A waiter left parked on it
// is a thread held for the life of the process by a turn that is not running.
func TestWaitForTurnEndsWhenTheConnectionDoes(t *testing.T) {
	server := newFakeServer(t)
	client := NewClient(server.transport)

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done <- client.WaitForTurn(ctx, "turn-1")
	}()
	time.Sleep(20 * time.Millisecond)
	server.hangUp()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a dead connection reported the turn as completed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a waiter outlived the connection it was waiting on")
	}
}

// Watching the turn stream does not depend on anyone having asked for
// notifications.
//
// A turn's completion is the client's own business — it is what frees the
// thread — and the supervisor that builds these clients sets no OnNotify.
func TestTurnsAreWatchedWithoutAnOnNotify(t *testing.T) {
	server := newFakeServer(t)
	client := NewClientWithOptions(server.transport, Options{})
	t.Cleanup(func() { _ = client.Close() })

	server.send(t, turnCompleted("thread-1", "turn-1"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.WaitForTurn(ctx, "turn-1"); err != nil {
		t.Fatalf("with no OnNotify set, the completion was not seen: %v", err)
	}
}

// A caller's own OnNotify still sees the stream.
func TestOnNotifyStillSeesTurnCompletions(t *testing.T) {
	server := newFakeServer(t)
	seen := make(chan string, 4)
	client := NewClientWithOptions(server.transport, Options{
		OnNotify: func(method string, _ json.RawMessage) { seen <- method },
	})
	t.Cleanup(func() { _ = client.Close() })

	server.send(t, turnCompleted("thread-1", "turn-1"))
	select {
	case method := <-seen:
		if method != "turn/completed" {
			t.Errorf("OnNotify saw %q", method)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("OnNotify never saw the notification the client also watches")
	}
}

// The memory of finished turns is bounded, so a connection that lives for days
// does not grow one entry per turn for ever.
func TestFinishedTurnsAreForgottenEventually(t *testing.T) {
	var tracker turnTracker
	for turn := 0; turn < turnsRemembered*3; turn++ {
		tracker.complete(turnID(turn))
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if len(tracker.finished) > turnsRemembered || len(tracker.order) > turnsRemembered {
		t.Errorf("remembered %d turns, want at most %d", len(tracker.finished), turnsRemembered)
	}
	if _, still := tracker.finished[turnID(turnsRemembered*3-1)]; !still {
		t.Error("the most recent turn was forgotten")
	}
	if _, gone := tracker.finished[turnID(0)]; gone {
		t.Error("the oldest turn is still remembered")
	}
}

func turnID(n int) string { return "turn-" + strconv.Itoa(n) }

// waiterCount reports how many turns have somebody waiting on them.
func (t *turnTracker) waiterCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.waiters)
}

// A wait that gives up is forgotten.
//
// The two ways out of WaitForTurn that are not a completion — the caller's
// context ending, the connection dying — have as their premise that the
// completion is never coming, so complete() will never remove these entries.
// Before this was handled, 500 timed-out waits left 500 channels in the map
// for the life of the connection, and a busy thread produces one of those per
// message it refuses. The bound on finished turns does not cover it: that
// caps `finished` and `order`, which is the other half of the tracker.
func TestGivingUpOnATurnForgetsTheWait(t *testing.T) {
	server := newFakeServer(t)
	client := NewClient(server.transport)
	t.Cleanup(func() { _ = client.Close() })

	const abandoned = 50
	for turn := 0; turn < abandoned; turn++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Millisecond)
		err := client.WaitForTurn(ctx, turnID(turn))
		cancel()
		if err == nil {
			t.Fatalf("waiting for turn %d reported a completion nobody sent", turn)
		}
	}
	if got := client.turns.waiterCount(); got != 0 {
		t.Errorf("%d waits gave up and left %d waiters behind; nothing will ever remove "+
			"them, so this grows for as long as the connection lives", abandoned, got)
	}
}

// A wait released by the connection dying is forgotten too.
//
// Same premise, the other exit: nobody will send that turn's completion now,
// so nothing else would ever take the entry out.
func TestAWaitEndedByADeadConnectionIsForgotten(t *testing.T) {
	server := newFakeServer(t)
	client := NewClient(server.transport)

	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done <- client.WaitForTurn(ctx, "turn-1")
	}()
	// Waiting is registered before the connection dies, so what is being
	// measured is the waiter being removed rather than never being added.
	deadline := time.Now().Add(2 * time.Second)
	for client.turns.waiterCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := client.turns.waiterCount(); got != 1 {
		t.Fatalf("%d waiters registered before the connection died, want 1", got)
	}
	server.hangUp()
	if err := <-done; err == nil {
		t.Fatal("a dead connection reported the turn as completed")
	}
	if got := client.turns.waiterCount(); got != 0 {
		t.Errorf("the connection died and %d waiters stayed in the map", got)
	}
}

// One caller giving up does not strand another waiting on the same turn.
//
// Waiters for one turn id share a channel, so forgetting the entry as soon as
// the first of them walks away would leave the rest for complete() to find
// nothing to close — released only by their own deadlines, each holding a
// thread's turn slot until then.
func TestGivingUpDoesNotStrandAnotherWaiterOnTheSameTurn(t *testing.T) {
	var tracker turnTracker
	staying := tracker.wait("turn-1")
	leaving := tracker.wait("turn-1")
	tracker.abandon("turn-1", leaving)

	if got := tracker.waiterCount(); got != 1 {
		t.Fatalf("%d waiters left after one of two gave up, want the one still waiting", got)
	}
	tracker.complete("turn-1")
	select {
	case <-staying.ready:
	default:
		t.Fatal("the turn completed and the caller still waiting on it was never released")
	}
}
