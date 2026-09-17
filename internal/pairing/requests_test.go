package pairing_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/pairing"
)

// clock is a hand-wound time source, so the expiry boundary can be put exactly
// where the test wants it rather than waited for.
type clock struct{ at time.Time }

func (c *clock) now() time.Time { return c.at }

func pending(id, nodeID string, now time.Time, direction pairing.Direction) pairing.Request {
	return pairing.Request{
		ID: id, Direction: direction, NodeID: nodeID,
		DisplayName: nodeID, Platform: "test",
		PublicKey: "key", Fingerprint: "AAAA BBBB CCCC DDDD EEEE FFFF",
		State:     pairing.StatePending,
		CreatedAt: now, ExpiresAt: now.Add(pairing.RequestTTL),
	}
}

// A request that nobody answered stops being answerable. Nothing has to notice
// the moment: the next read is what applies the clock.
func TestRequestsExpireOnTheClock(t *testing.T) {
	c := &clock{at: time.Now()}
	requests := pairing.NewRequestsWithClock(c.now)
	if err := requests.Add(pending("pair_1", "node_a", c.at, pairing.Incoming)); err != nil {
		t.Fatal(err)
	}

	c.at = c.at.Add(pairing.RequestTTL - time.Second)
	if row, _ := requests.Get("pair_1"); row.State != pairing.StatePending {
		t.Fatalf("state one second before expiry = %q, want pending", row.State)
	}
	c.at = c.at.Add(time.Second)
	row, ok := requests.Get("pair_1")
	if !ok {
		t.Fatal("an expired request disappeared; the owner has to be able to tell it ran out")
	}
	if row.State != pairing.StateExpired || row.Reason != pairing.ReasonExpired {
		t.Fatalf("state at expiry = %q/%q, want expired/expired", row.State, row.Reason)
	}
	// And an expired request cannot be approved after the fact.
	if _, err := requests.Settle("pair_1", pairing.StatePending, pairing.StateApproved, ""); !errors.Is(err, pairing.ErrWrongState) {
		t.Fatalf("settling an expired request = %v, want ErrWrongState", err)
	}
}

// Closing the window is the owner withdrawing consent, and it must reach the
// requests the window collected.
func TestClosingTheWindowExpiresWhatItCollected(t *testing.T) {
	c := &clock{at: time.Now()}
	requests := pairing.NewRequestsWithClock(c.now)
	if err := requests.Add(pending("pair_in", "node_a", c.at, pairing.Incoming)); err != nil {
		t.Fatal(err)
	}
	if err := requests.Add(pending("pair_out", "node_b", c.at, pairing.Outgoing)); err != nil {
		t.Fatal(err)
	}

	requests.ExpirePending(pairing.Incoming)

	if row, _ := requests.Get("pair_in"); row.State != pairing.StateExpired {
		t.Fatalf("incoming request = %q, want expired", row.State)
	}
	// Outgoing requests are this owner's own, made deliberately, and are not
	// what the window consents to. Closing it must not cancel them.
	if row, _ := requests.Get("pair_out"); row.State != pairing.StatePending {
		t.Fatalf("outgoing request = %q, want it left alone", row.State)
	}
}

// The outgoing list is this owner's own and is bounded outright: sixteen of
// them means the owner asked for sixteen, and cancelling one silently would be
// this node deciding which of their pairings to abandon.
func TestPendingOutgoingRequestsAreBounded(t *testing.T) {
	c := &clock{at: time.Now()}
	requests := pairing.NewRequestsWithClock(c.now)
	for i := range pairing.MaxPending {
		id := fmt.Sprintf("pair_%d", i)
		if err := requests.Add(pending(id, fmt.Sprintf("node_%d", i), c.at, pairing.Outgoing)); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	err := requests.Add(pending("pair_extra", "node_extra", c.at, pairing.Outgoing))
	if !errors.Is(err, pairing.ErrTooManyRequests) {
		t.Fatalf("request past the cap = %v, want ErrTooManyRequests", err)
	}
	// An incoming request is counted separately.
	if err := requests.Add(pending("pair_theirs", "node_theirs", c.at, pairing.Incoming)); err != nil {
		t.Fatalf("incoming request refused because outgoing ones were full: %v", err)
	}
	// Deciding one makes room again.
	if _, err := requests.Settle("pair_0", pairing.StatePending, pairing.StateRejected, pairing.ReasonDeclined); err != nil {
		t.Fatal(err)
	}
	if err := requests.Add(pending("pair_extra", "node_extra", c.at, pairing.Outgoing)); err != nil {
		t.Fatalf("request after room was made = %v, want it accepted", err)
	}
}

// One address may not hold the incoming list open. Node ids are the sender's to
// choose, so a bound keyed on them bounds nothing: sixteen fresh ones from one
// machine filled the list, and the owner's real machine was refused during the
// very window they had opened to pair it.
func TestOneSourceCannotHoldManyPendingRequests(t *testing.T) {
	c := &clock{at: time.Now()}
	requests := pairing.NewRequestsWithClock(c.now)
	for i := range pairing.MaxPendingPerSource {
		row := pending(fmt.Sprintf("pair_flood%d", i), fmt.Sprintf("node_flood%d", i), c.at, pairing.Incoming)
		row.SourceHost = "10.0.0.9"
		if err := requests.Add(row); err != nil {
			t.Fatalf("request %d from one address: %v", i, err)
		}
	}
	extra := pending("pair_flood_extra", "node_flood_extra", c.at, pairing.Incoming)
	extra.SourceHost = "10.0.0.9"
	if err := requests.Add(extra); !errors.Is(err, pairing.ErrTooManyFromSource) {
		t.Fatalf("a fourth request from one address = %v, want ErrTooManyFromSource", err)
	}
	// Another machine is unaffected: the bound is per address, not global.
	other := pending("pair_real", "node_real", c.at, pairing.Incoming)
	other.SourceHost = "10.0.0.10"
	if err := requests.Add(other); err != nil {
		t.Fatalf("a different address was refused: %v", err)
	}
}

// A full incoming list makes room by displacing the oldest thing still waiting.
// The owner is standing at two machines with a window open; their own machine's
// request landing matters more than an older unanswered row.
func TestAFullIncomingListDisplacesTheOldest(t *testing.T) {
	c := &clock{at: time.Now()}
	requests := pairing.NewRequestsWithClock(c.now)
	for i := range pairing.MaxPending {
		row := pending(fmt.Sprintf("pair_%d", i), fmt.Sprintf("node_%d", i), c.at, pairing.Incoming)
		row.SourceHost = fmt.Sprintf("10.0.0.%d", i)
		if err := requests.Add(row); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		c.at = c.at.Add(time.Second)
	}

	real := pending("pair_real", "node_real", c.at, pairing.Incoming)
	real.SourceHost = "192.168.1.42"
	real.ExpiresAt = c.at.Add(pairing.RequestTTL)
	if err := requests.Add(real); err != nil {
		t.Fatalf("the real machine was turned away by a full list: %v", err)
	}
	if row, _ := requests.Get("pair_real"); row.State != pairing.StatePending {
		t.Fatalf("the real machine's request is %q", row.State)
	}
	// The oldest is gone, and says why: it did not run out of time, something
	// filled the list.
	displaced, ok := requests.Get("pair_0")
	if !ok {
		t.Fatal("the displaced request vanished; the owner has to be able to tell what happened")
	}
	if displaced.State != pairing.StateExpired || displaced.Reason != pairing.ReasonDisplaced {
		t.Fatalf("displaced request = %q/%q, want expired/displaced", displaced.State, displaced.Reason)
	}
	// And nothing else was touched.
	if row, _ := requests.Get("pair_1"); row.State != pairing.StatePending {
		t.Fatalf("a second row was displaced by one arrival: %q", row.State)
	}
}

// One machine, one row to compare. A second request from the same node would
// put two identical fingerprints in front of the owner, only one of which is
// the one the other machine is waiting on.
func TestASecondRequestFromTheSameNodeIsRefused(t *testing.T) {
	c := &clock{at: time.Now()}
	requests := pairing.NewRequestsWithClock(c.now)
	if err := requests.Add(pending("pair_1", "node_a", c.at, pairing.Incoming)); err != nil {
		t.Fatal(err)
	}
	if err := requests.Add(pending("pair_2", "node_a", c.at, pairing.Incoming)); !errors.Is(err, pairing.ErrDuplicateRequest) {
		t.Fatalf("second request from the same node = %v, want ErrDuplicateRequest", err)
	}
	// Once the first is decided, the node may ask again.
	if _, err := requests.Settle("pair_1", pairing.StatePending, pairing.StateRejected, pairing.ReasonDeclined); err != nil {
		t.Fatal(err)
	}
	if err := requests.Add(pending("pair_2", "node_a", c.at, pairing.Incoming)); err != nil {
		t.Fatalf("request after a refusal = %v, want it accepted", err)
	}
}

// A decided request is kept for a while, because "rejected" and "expired" are
// answers, and then forgotten, because nothing should accumulate for the life
// of the process.
func TestDecidedRequestsAreKeptThenForgotten(t *testing.T) {
	c := &clock{at: time.Now()}
	requests := pairing.NewRequestsWithClock(c.now)
	if err := requests.Add(pending("pair_1", "node_a", c.at, pairing.Incoming)); err != nil {
		t.Fatal(err)
	}
	if _, err := requests.Settle("pair_1", pairing.StatePending, pairing.StateRejected, pairing.ReasonDeclined); err != nil {
		t.Fatal(err)
	}
	c.at = c.at.Add(pairing.RequestTTL)
	if _, ok := requests.Get("pair_1"); !ok {
		t.Fatal("a refusal was forgotten while the other side could still be asking")
	}
	c.at = c.at.Add(11 * time.Minute)
	if _, ok := requests.Get("pair_1"); ok {
		t.Fatal("a long-decided request is still held")
	}
	if rows := requests.List(); len(rows) != 0 {
		t.Fatalf("list still holds %d rows", len(rows))
	}
}
