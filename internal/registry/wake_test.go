package registry

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
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

	// Two sources aimed at one agent, with the pair limit set far out of the
	// way so this is the session limit being measured and not that one. They
	// share a bucket now — the key is the node — which is exactly why the
	// session limit has to exist separately: it is what stops one machine
	// driving one agent all night.
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

// A message that never left this machine still has a bucket.
//
// It used to have none: the pair check was skipped whenever the sender label
// was empty, and hops are zero for an unattributed send, so two sessions here
// could answer each other with neither loop mechanism applying. One shared
// "local" bucket is a real bound on that, and it is not the empty key, which
// would mean "count every source" — the session limit wearing the pair
// limit's name.
func TestLocalTrafficSharesOneBucketRatherThanNone(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	limits := DefaultWakeLimits()
	limits.Pair = 2

	for i := range limits.Pair {
		event := wakeEvent("", "claude:mine")
		event.SourceNodeID = ""
		event.MessageID = fmt.Sprintf("msg_%d", i)
		stored, err := store.ReserveWake(ctx, event, limits)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Outcome != WakeWoken {
			t.Fatalf("local wake %d was refused as %q", i, stored.Outcome)
		}
		if stored.PairKey() != "local" {
			t.Fatalf("local traffic was bucketed as %q", stored.PairKey())
		}
	}
	over := wakeEvent("", "claude:mine")
	over.SourceNodeID = ""
	over.MessageID = "msg_over"
	stopped, err := store.ReserveWake(ctx, over, limits)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Outcome != WakeRefusedPair {
		t.Errorf("local sends are unlimited: %q", stopped.Outcome)
	}
}

// The pair limit counts by the node the signature proves, not the label the
// sender writes.
//
// Keying on the label made the limit decorative. A measured run took one
// peer from 3 wakes to 12 against the same agent by varying `from` and
// nothing else — every message minted a fresh bucket. The node id is the only
// part of a sender's claim this node can check.
func TestThePairLimitCountsByTheVerifiedNodeNotTheChosenLabel(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	limits := DefaultWakeLimits()

	for i := range limits.Pair {
		event := wakeEvent(fmt.Sprintf("node_peer0000000000000/codex:alias-%d", i), "claude:mine")
		event.SourceNodeID = "node_peer0000000000000"
		event.MessageID = fmt.Sprintf("msg_%d", i)
		stored, err := store.ReserveWake(ctx, event, limits)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Outcome != WakeWoken {
			t.Fatalf("wake %d refused as %q before the limit", i, stored.Outcome)
		}
	}

	// A label it has never used before, from the same machine.
	fresh := wakeEvent("node_peer0000000000000/codex:brand-new-alias", "claude:mine")
	fresh.SourceNodeID = "node_peer0000000000000"
	fresh.MessageID = "msg_alias"
	stopped, err := store.ReserveWake(ctx, fresh, limits)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Outcome != WakeRefusedPair {
		t.Errorf("a new sender label bought another wake: %q — the limit is keyed on "+
			"something the sender chooses", stopped.Outcome)
	}

	// A genuinely different machine is a different bucket.
	other := wakeEvent("node_other0000000000000/codex:theirs", "claude:mine")
	other.SourceNodeID = "node_other0000000000000"
	other.MessageID = "msg_other"
	permitted, err := store.ReserveWake(ctx, other, limits)
	if err != nil {
		t.Fatal(err)
	}
	if permitted.Outcome != WakeWoken {
		t.Errorf("a different node was charged against another node's bucket: %q", permitted.Outcome)
	}
}

// A wake is claimed by at most one message.
//
// Without that, one wake tainted every send from that session for fifteen
// minutes: a person typing five minutes later had their own message carried at
// the agent's hop count, and it could be refused at the far end with a
// hop-limit refusal they had no way to see — the refusal lands on the
// receiver's trail, which the sender cannot read.
func TestOneWakeIsInheritedByOneMessageOnly(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	now := time.Now().UTC()

	event := wakeEvent("node_peer0000000000000/codex:theirs", "claude:mine")
	event.SourceNodeID = "node_peer0000000000000"
	event.Hops = 2
	if _, err := store.ReserveWake(ctx, event, DefaultWakeLimits()); err != nil {
		t.Fatal(err)
	}

	first, chain, err := store.PeekWakeChain(ctx, "claude:mine", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ClaimWakeChain(ctx, chain); err != nil {
		t.Fatal(err)
	}
	if first != 3 {
		t.Errorf("the reply carries %d hops, want one more than the wake's 2", first)
	}
	// Anything the session sends afterwards is its own doing until it is woken
	// again.
	second, _, err := store.PeekWakeChain(ctx, "claude:mine", now)
	if err != nil {
		t.Fatal(err)
	}
	if second != 0 {
		t.Errorf("a second message inherited the same wake and carries %d hops", second)
	}

	// Being woken again arms it again.
	next := wakeEvent("node_peer0000000000000/codex:theirs", "claude:mine")
	next.SourceNodeID = "node_peer0000000000000"
	next.MessageID, next.Hops = "msg_again", 1
	if _, err := store.ReserveWake(ctx, next, DefaultWakeLimits()); err != nil {
		t.Fatal(err)
	}
	again, _, err := store.PeekWakeChain(ctx, "claude:mine", now)
	if err != nil {
		t.Fatal(err)
	}
	if again != 2 {
		t.Errorf("after a second wake the reply carries %d hops, want 2", again)
	}
}

// Two wakes in the same millisecond are told apart by insert order.
//
// Ids are random hex, so ordering by them picks arbitrarily between them —
// measured returning 4 from a pair recorded at 0 and 3, which is a message
// stopped at the far end for a chain that never happened.
func TestWakesInTheSameMillisecondAreOrderedByWhatHappened(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	sameMoment := time.Now().UTC().Truncate(time.Millisecond)

	// Ids that descend as insert order ascends, so ordering by id gives the
	// exact opposite answer and the mutant dies every run. Six random-hex rows
	// would still be a one-in-six pin — a test that mostly reports the bug is
	// a test that sometimes reports the opposite.
	for i := range 6 {
		event := wakeEvent("node_peer0000000000000/codex:theirs", "claude:mine")
		event.SourceNodeID = "node_peer0000000000000"
		event.ID = fmt.Sprintf("wake_%06d", 6-i)
		event.MessageID = fmt.Sprintf("msg_%d", i)
		event.Hops, event.At = i, sameMoment
		event.Outcome = WakeWoken
		if _, err := store.RecordWake(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	// The last insert is the latest wake, so its 5 is the one to inherit.
	got, _, err := store.PeekWakeChain(ctx, "claude:mine", sameMoment)
	if err != nil {
		t.Fatal(err)
	}
	if got != 6 {
		t.Errorf("hops = %d, want 6 from the wake that was recorded last", got)
	}
}

// A wake trail written by an earlier build gains its new columns.
//
// CREATE TABLE IF NOT EXISTS does not revisit a table that exists, and neither
// does CREATE INDEX — so such a database opened without complaint and then
// failed every wake with "no such column: pair_key", permanently, with only a
// log line to say so. It failed closed, which is the right direction and not a
// reason to leave it.
func TestAWakeTrailFromAnEarlierBuildGainsItsColumns(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agenthub.db")
	store := openRegistryAt(t, path)
	event := wakeEvent("node_peer0000000000000/codex:theirs", "claude:mine")
	event.SourceNodeID = "node_peer0000000000000"
	event.Hops = 1
	if _, err := store.ReserveWake(ctx, event, DefaultWakeLimits()); err != nil {
		t.Fatal(err)
	}
	// The old shape, index included: the index is what CREATE INDEX IF NOT
	// EXISTS will decline to replace, so reproducing it is the point.
	if _, err := store.db.ExecContext(ctx, `
DROP INDEX idx_wake_events_pair;
ALTER TABLE wake_events DROP COLUMN pair_key;
ALTER TABLE wake_events DROP COLUMN chain_used;
CREATE INDEX idx_wake_events_pair
    ON wake_events(source_session, destination_session, at_ms DESC);`); err != nil {
		t.Fatalf("could not reproduce the older schema: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openRegistryAt(t, path)
	// Everything that touches those columns has to work, not merely the open.
	if _, err := reopened.ListWakes(ctx, "", 10); err != nil {
		t.Errorf("ListWakes on an upgraded trail: %v", err)
	}
	if _, _, err := reopened.PeekWakeChain(ctx, "claude:mine", time.Now().UTC()); err != nil {
		t.Errorf("PeekWakeChain on an upgraded trail: %v", err)
	}
	next := wakeEvent("node_peer0000000000000/codex:theirs", "claude:mine")
	next.SourceNodeID = "node_peer0000000000000"
	next.MessageID = "msg_after"
	stored, err := reopened.ReserveWake(ctx, next, DefaultWakeLimits())
	if err != nil {
		t.Fatalf("ReserveWake on an upgraded trail: %v", err)
	}
	if stored.Outcome != WakeWoken {
		t.Errorf("the first wake after an upgrade was refused as %q", stored.Outcome)
	}
	// The rows that predate the column read as local, which is the default and
	// is wrong for a peer — but they are history, not a bucket anyone is
	// counting into now.
	if stored.PairKey() != "node:node_peer0000000000000" {
		t.Errorf("a new wake after the upgrade is bucketed as %q", stored.PairKey())
	}

	// And the index covers what is now counted. Left over the old column, every
	// reservation on this node would scan the table for the rest of its life.
	var plan string
	if err := reopened.db.QueryRowContext(ctx, `
EXPLAIN QUERY PLAN SELECT count(*) FROM wake_events
WHERE outcome = ? AND at_ms >= ? AND pair_key = ? AND destination_session = ?`,
		string(WakeWoken), 0, "node:node_peer0000000000000", "claude:mine").Scan(
		new(int), new(int), new(int), &plan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "idx_wake_events_pair") {
		t.Errorf("the pair count does not use the pair index after an upgrade: %s", plan)
	}

	// And the constraints match a fresh database. An upgraded schema whose
	// CHECKs differ from a new one is two schemas wearing one version number,
	// and the difference only shows up as a bad row somebody has to explain
	// later.
	if _, err := reopened.db.ExecContext(ctx,
		`UPDATE wake_events SET chain_used = 7`); err == nil {
		t.Error("chain_used accepted 7 after an upgrade; the fresh schema's CHECK is missing")
	}
	if _, err := reopened.db.ExecContext(ctx,
		`UPDATE wake_events SET chain_used = 1`); err != nil {
		t.Errorf("chain_used refused a legitimate value after an upgrade: %v", err)
	}
}

// When more than one limit applies, the owner is told the narrowest.
//
// "This peer has been talking too fast" names something they can act on: they
// know which machine, and they can revoke it. "This node is busy" names a
// symptom and points nowhere. Both are true when both limits are exceeded, so
// which is reported is a choice.
//
// An earlier version of this test was deleted when the pair key moved to the
// node id, because half of it asserted something that keying made untrue. The
// half that was still true went with it, and reversing the check order passed
// everything for two commits.
func TestTheReasonGivenIsTheNarrowestThatApplies(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	limits := DefaultWakeLimits()
	limits.Pair, limits.Session, limits.Node = 1, 1, 1

	first := wakeEvent("node_peer0000000000000/codex:theirs", "claude:mine")
	first.SourceNodeID = "node_peer0000000000000"
	stored, err := store.ReserveWake(ctx, first, limits)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Outcome != WakeWoken {
		t.Fatalf("the first wake was refused as %q", stored.Outcome)
	}

	// Over all three at once: same peer, same destination, same node.
	over := wakeEvent("node_peer0000000000000/codex:theirs", "claude:mine")
	over.SourceNodeID = "node_peer0000000000000"
	over.MessageID = "msg_over"
	stopped, err := store.ReserveWake(ctx, over, limits)
	if err != nil {
		t.Fatal(err)
	}
	if stopped.Outcome != WakeRefusedPair {
		t.Errorf("outcome = %q; with every limit exceeded the owner should be told the pair "+
			"one, the only one that names what to change", stopped.Outcome)
	}

	// A different machine, a different session: both narrow limits are
	// untouched, so the widest one is the true answer.
	elsewhere := wakeEvent("node_other0000000000000/codex:theirs", "claude:elsewhere")
	elsewhere.SourceNodeID = "node_other0000000000000"
	elsewhere.MessageID = "msg_elsewhere"
	nodeStopped, err := store.ReserveWake(ctx, elsewhere, limits)
	if err != nil {
		t.Fatal(err)
	}
	if nodeStopped.Outcome != WakeRefusedNode {
		t.Errorf("outcome = %q, want %q: this peer and this session are both untouched",
			nodeStopped.Outcome, WakeRefusedNode)
	}
}

// The trail reads newest first, including within one millisecond.
//
// "Newest first" is the whole contract of the listing — an owner arrives
// asking what just happened — and ids are random hex, so ordering by them
// answers arbitrarily for two rows recorded together.
func TestTheTrailReadsNewestFirstWithinAMillisecond(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	sameMoment := time.Now().UTC().Truncate(time.Millisecond)

	for i := range 6 {
		event := wakeEvent("node_peer0000000000000/codex:theirs", "claude:mine")
		event.SourceNodeID = "node_peer0000000000000"
		event.MessageID = fmt.Sprintf("msg_%d", i)
		event.Hops, event.At, event.Outcome = i, sameMoment, WakeWoken
		if _, err := store.RecordWake(ctx, event); err != nil {
			t.Fatal(err)
		}
	}

	listed, err := store.ListWakes(ctx, "claude:mine", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 6 {
		t.Fatalf("the trail holds %d rows", len(listed))
	}
	for i, event := range listed {
		want := 5 - i
		if event.Hops != want {
			t.Fatalf("row %d is the wake recorded %s, not the %s: the listing is ordered by "+
				"a random id, so rows written in the same millisecond come back arbitrarily",
				i, ordinal(event.Hops), ordinal(want))
		}
	}
}

func ordinal(n int) string { return fmt.Sprintf("#%d", n+1) }

// The hop count a message was stored with is the one that comes back.
//
// Three places write it — the peer path, the local path, and the send queue —
// and nothing asserted any of them. Zeroing any write, or dropping the column
// from either read, left the whole repository green. It is what the gate reads
// when it decides whether an exchange has gone far enough, so a count that
// does not survive storage is a limit that never fires.
func TestAStoredMessageKeepsItsHopCount(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := acceptingSession(t, store)

	created, err := store.CreateMessage(ctx, model.Message{
		To: session.ID, From: "codex:mine", DestinationNodeID: testNodeID,
		Body: "local", WakeHops: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.WakeHops != 2 {
		t.Errorf("CreateMessage returned %d hops", created.WakeHops)
	}

	stored, err := store.StoreIncomingMessage(ctx, model.Message{
		ID: "msg_from_peer", To: session.ID, From: "node_peer0000000000000/codex:theirs",
		DestinationNodeID: testNodeID, Body: "from a peer", WakeHops: 3,
	})
	if err != nil || !stored {
		t.Fatalf("StoreIncomingMessage() = %v, %v", stored, err)
	}

	// Read back both ways: the inbox listing and the by-id lookup.
	held, err := store.Inbox(ctx, session.ID, 10, InboxCursor{})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]int{}
	for _, message := range held {
		byID[message.ID] = message.WakeHops
	}
	if byID[created.ID] != 2 {
		t.Errorf("the local message reads back at %d hops, want 2", byID[created.ID])
	}
	if byID["msg_from_peer"] != 3 {
		t.Errorf("the peer's message reads back at %d hops, want 3", byID["msg_from_peer"])
	}
	one, err := store.MessageByID(ctx, "msg_from_peer")
	if err != nil {
		t.Fatal(err)
	}
	if one.WakeHops != 3 {
		t.Errorf("MessageByID gives %d hops", one.WakeHops)
	}
}
