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

// The incoming list is something a stranger can fill, so it is bounded — and
// the bound counts only what is still waiting, or a flood would keep the list
// shut for as long as the decided rows are kept.
func TestPendingRequestsAreBounded(t *testing.T) {
	c := &clock{at: time.Now()}
	requests := pairing.NewRequestsWithClock(c.now)
	for i := range pairing.MaxPending {
		id := fmt.Sprintf("pair_%d", i)
		if err := requests.Add(pending(id, fmt.Sprintf("node_%d", i), c.at, pairing.Incoming)); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	err := requests.Add(pending("pair_extra", "node_extra", c.at, pairing.Incoming))
	if !errors.Is(err, pairing.ErrTooManyRequests) {
		t.Fatalf("request past the cap = %v, want ErrTooManyRequests", err)
	}
	// An outgoing request is this owner's own and is counted separately.
	if err := requests.Add(pending("pair_mine", "node_mine", c.at, pairing.Outgoing)); err != nil {
		t.Fatalf("outgoing request refused because incoming ones were full: %v", err)
	}
	// Deciding one makes room again.
	if _, err := requests.Settle("pair_0", pairing.StatePending, pairing.StateRejected, pairing.ReasonDeclined); err != nil {
		t.Fatal(err)
	}
	if err := requests.Add(pending("pair_extra", "node_extra", c.at, pairing.Incoming)); err != nil {
		t.Fatalf("request after room was made = %v, want it accepted", err)
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
