package registry

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func wakeEvent(source, destination string) WakeEvent {
	return WakeEvent{
		MessageID: "msg_" + source + destination, SourceNodeID: "node_peer0000000000000",
		SourceSession: source, DestinationSession: destination,
	}
}

// Two machines that both wake automatically answer each other until somebody
// notices. The pair limit is what ends that, and it is the only one of the
// three a third node cannot be used to route around.
func TestThePairLimitStopsAnExchange(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	limits := DefaultWakeLimits()

	for attempt := 1; attempt <= limits.Pair; attempt++ {
		event := wakeEvent("claude:theirs", "claude:mine")
		event.MessageID = fmt.Sprintf("msg_%d", attempt)
		stored, err := store.ReserveWake(ctx, event, limits)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Outcome != WakeWoken {
			t.Fatalf("wake %d of %d was refused as %q", attempt, limits.Pair, stored.Outcome)
		}
	}

	over := wakeEvent("claude:theirs", "claude:mine")
	over.MessageID = "msg_over"
	stopped, err := store.ReserveWake(ctx, over, limits)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Outcome != WakeRefusedPair {
		t.Errorf("the %dth wake in the window gave %q, want %q",
			limits.Pair+1, stopped.Outcome, WakeRefusedPair)
	}
	// Refused, and written down. A limit that swallows what it stopped leaves
	// an owner unable to tell "nothing arrived" from "something was held back".
	if stopped.Detail == "" {
		t.Error("the refusal says nothing about why")
	}
	listed, err := store.ListWakes(ctx, "claude:mine", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != limits.Pair+1 {
		t.Errorf("the trail holds %d events, want every attempt including the refusal", len(listed))
	}

	// The other direction is its own pair, and is not already spent. A limit
	// that counted both directions together would cut an exchange off halfway
	// through its first round trip.
	back := wakeEvent("claude:mine", "claude:theirs")
	back.MessageID = "msg_back"
	returned, err := store.ReserveWake(ctx, back, limits)
	if err != nil {
		t.Fatal(err)
	}
	if returned.Outcome != WakeWoken {
		t.Errorf("the reverse direction was refused as %q on its first wake", returned.Outcome)
	}
}

// The count that permits a row is taken in the transaction that writes it.
//
// Counting and inserting apart is not a limit: two deliveries arriving together
// both count the same number, both find room, and both wake. This is the test
// that says the reservation is atomic, and it is the reason ReserveWake exists
// rather than a CountWakes call followed by a RecordWake.
func TestConcurrentArrivalsCannotBothTakeTheLastSlot(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	limits := DefaultWakeLimits()
	limits.Pair = 1

	const racers = 8
	var wg sync.WaitGroup
	outcomes := make([]WakeOutcome, racers)
	start := make(chan struct{})
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			event := wakeEvent("claude:theirs", "claude:mine")
			event.MessageID = fmt.Sprintf("msg_%d", i)
			<-start
			stored, err := store.ReserveWake(ctx, event, limits)
			if err != nil {
				t.Errorf("racer %d: %v", i, err)
				return
			}
			outcomes[i] = stored.Outcome
		}()
	}
	close(start)
	wg.Wait()

	woken := 0
	for _, outcome := range outcomes {
		if outcome == WakeWoken {
			woken++
		}
	}
	if woken != 1 {
		t.Errorf("%d of %d concurrent arrivals were permitted, want exactly %d",
			woken, racers, limits.Pair)
	}
}

// A provider that refused leaves no wake behind, because none happened. The
// row stays for the owner to read, and stops counting against the limit — a
// turn that never ran spent nothing.
func TestAFailedWakeStopsCountingAgainstTheLimit(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	limits := DefaultWakeLimits()
	limits.Pair = 1

	first, err := store.ReserveWake(ctx, wakeEvent("claude:theirs", "claude:mine"), limits)
	if err != nil {
		t.Fatal(err)
	}
	if first.Outcome != WakeWoken {
		t.Fatalf("the first wake was refused as %q", first.Outcome)
	}
	// While it is running it holds the slot: a second message must not start
	// another turn in the same agent.
	during := wakeEvent("claude:theirs", "claude:mine")
	during.MessageID = "msg_during"
	held, err := store.ReserveWake(ctx, during, limits)
	if err != nil {
		t.Fatal(err)
	}
	if held.Outcome != WakeRefusedPair {
		t.Errorf("a second wake was permitted while the first was still running: %q", held.Outcome)
	}

	if err := store.SettleWake(ctx, first.ID, WakeFailed, "app-server not reachable"); err != nil {
		t.Fatal(err)
	}
	after := wakeEvent("claude:theirs", "claude:mine")
	after.MessageID = "msg_after"
	retried, err := store.ReserveWake(ctx, after, limits)
	if err != nil {
		t.Fatal(err)
	}
	if retried.Outcome != WakeWoken {
		t.Errorf("a wake that failed still holds its slot: %q", retried.Outcome)
	}

	// And a settle cannot resurrect a refusal into a wake that never happened.
	if err := store.SettleWake(ctx, held.ID, WakeWoken, ""); err == nil {
		t.Error("a refused wake was settled as woken")
	}
}

// The three windows answer different questions, so a message can pass the
// narrow one and be stopped by a wider one.
func TestTheWiderLimitsCatchWhatThePairLimitCannot(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	limits := DefaultWakeLimits()
	limits.Pair, limits.Session, limits.Node = 100, 2, 3

	// Two different sources, so no pair is near its limit, all aimed at one
	// agent. Without the session limit one peer could spread itself across
	// sessions it has and drive the same agent all night.
	for i := range 2 {
		event := wakeEvent(fmt.Sprintf("claude:theirs-%d", i), "claude:mine")
		event.MessageID = fmt.Sprintf("msg_a%d", i)
		stored, err := store.ReserveWake(ctx, event, limits)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Outcome != WakeWoken {
			t.Fatalf("wake %d was refused as %q before any limit was reached", i, stored.Outcome)
		}
	}
	third := wakeEvent("claude:theirs-2", "claude:mine")
	third.MessageID = "msg_a2"
	stopped, err := store.ReserveWake(ctx, third, limits)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Outcome != WakeRefusedSession {
		t.Errorf("a third source reached the same agent: %q", stopped.Outcome)
	}

	// A different destination is a different session, so it is not already
	// spent — until the node-wide limit, which is what stops one peer using
	// the whole machine.
	other := wakeEvent("claude:theirs-3", "claude:other")
	other.MessageID = "msg_b0"
	permitted, err := store.ReserveWake(ctx, other, limits)
	if err != nil {
		t.Fatal(err)
	}
	if permitted.Outcome != WakeWoken {
		t.Fatalf("a fresh destination was refused as %q", permitted.Outcome)
	}
	last := wakeEvent("claude:theirs-4", "claude:third")
	last.MessageID = "msg_c0"
	nodeStopped, err := store.ReserveWake(ctx, last, limits)
	if err != nil {
		t.Fatal(err)
	}
	if nodeStopped.Outcome != WakeRefusedNode {
		t.Errorf("the node-wide limit did not apply: %q", nodeStopped.Outcome)
	}
}

// A window that has passed does not hold anything down.
func TestAnOldWakeFallsOutOfItsWindow(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	limits := DefaultWakeLimits()
	limits.Pair = 1

	stale := wakeEvent("claude:theirs", "claude:mine")
	stale.At = time.Now().UTC().Add(-limits.PairWindow - time.Minute)
	if _, err := store.ReserveWake(ctx, stale, limits); err != nil {
		t.Fatal(err)
	}
	fresh := wakeEvent("claude:theirs", "claude:mine")
	fresh.MessageID = "msg_fresh"
	permitted, err := store.ReserveWake(ctx, fresh, limits)
	if err != nil {
		t.Fatal(err)
	}
	if permitted.Outcome != WakeWoken {
		t.Errorf("a wake outside the window still counted: %q", permitted.Outcome)
	}
}

// A local send names no source session. That must not be read as "every
// source", which would charge one anonymous send against the pair limit of
// every peer sharing the destination.
func TestASendWithNoNamedSourceSkipsThePairLimit(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	limits := DefaultWakeLimits()
	limits.Pair = 1

	for i := range 3 {
		event := wakeEvent("", "claude:mine")
		event.MessageID = fmt.Sprintf("msg_%d", i)
		stored, err := store.ReserveWake(ctx, event, limits)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Outcome != WakeWoken {
			t.Fatalf("an unattributed send %d was refused as %q; the pair limit was applied to it",
				i, stored.Outcome)
		}
	}
}
