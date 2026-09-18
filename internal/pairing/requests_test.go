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

// A full incoming list is refused, and only a sender that already has a row
// waiting here can make room — by displacing its own oldest one.
//
// Displacing the oldest row overall looked kinder to the owner and was not: it
// handed whoever sent the newest request the power to evict whoever sent the
// oldest, and that is exactly what a flooder does.
func TestAFullIncomingListDisplacesOnlyWithinOneSource(t *testing.T) {
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

	// A stranger arriving at a full list is told the list is full, rather than
	// being handed somebody else's place in it.
	stranger := pending("pair_stranger", "node_stranger", c.at, pairing.Incoming)
	stranger.SourceHost = "192.168.1.42"
	if err := requests.Add(stranger); !errors.Is(err, pairing.ErrTooManyRequests) {
		t.Fatalf("a new address at a full list = %v, want ErrTooManyRequests", err)
	}
	if row, _ := requests.Get("pair_0"); row.State != pairing.StatePending {
		t.Fatalf("the oldest row was evicted by a refused request: %q", row.State)
	}

	// The machine that already has a row may retry: it loses its own place,
	// nobody else's.
	retry := pending("pair_retry", "node_retry", c.at, pairing.Incoming)
	retry.SourceHost = "10.0.0.3"
	retry.ExpiresAt = c.at.Add(pairing.RequestTTL)
	if err := requests.Add(retry); err != nil {
		t.Fatalf("a retry from an address already in the list: %v", err)
	}
	if row, _ := requests.Get("pair_retry"); row.State != pairing.StatePending {
		t.Fatalf("the retry is %q", row.State)
	}
	// The displaced row says why: it did not run out of time, something filled
	// the list.
	displaced, ok := requests.Get("pair_3")
	if !ok {
		t.Fatal("the displaced request vanished; the owner has to be able to tell what happened")
	}
	if displaced.State != pairing.StateExpired || displaced.Reason != pairing.ReasonDisplaced {
		t.Fatalf("displaced request = %q/%q, want expired/displaced", displaced.State, displaced.Reason)
	}
	// And nothing from any other address was touched.
	if row, _ := requests.Get("pair_0"); row.State != pairing.StatePending {
		t.Fatalf("another address's row was displaced by one arrival: %q", row.State)
	}
}

// The reviewer's probe: a flooder rotating source addresses cannot evict the
// owner's own request.
//
// Six addresses were enough to fill the list under the old rule, and every
// request after that pushed out whatever was oldest — the owner's real machine
// among them, which then showed as displaced during the window they had opened
// to pair it.
func TestAFloodRotatingSourcesCannotEvictTheOwnersRequest(t *testing.T) {
	c := &clock{at: time.Now()}
	requests := pairing.NewRequestsWithClock(c.now)

	legit := pending("pair_legit", "node_legit", c.at, pairing.Incoming)
	legit.SourceHost = "10.0.0.9"
	if err := requests.Add(legit); err != nil {
		t.Fatalf("the owner's own request: %v", err)
	}
	c.at = c.at.Add(time.Second)

	for i := range 40 {
		row := pending(fmt.Sprintf("pair_flood%d", i), fmt.Sprintf("node_flood%d", i), c.at, pairing.Incoming)
		row.SourceHost = fmt.Sprintf("fd00::%d", i)
		// Refusals are the point: what must not happen is one of these
		// succeeding at the owner's expense.
		_ = requests.Add(row)
		c.at = c.at.Add(time.Second)
	}

	row, ok := requests.Get("pair_legit")
	if !ok {
		t.Fatal("the owner's request was forgotten entirely")
	}
	if row.State != pairing.StatePending {
		t.Fatalf("the owner's request is %q/%q after a flood from rotating addresses, want it "+
			"still pending", row.State, row.Reason)
	}
}

// The retained table is bounded too, and a flooder can only crowd out its own
// history.
//
// MaxPending and MaxPendingPerSource count undecided rows only. Displacement is
// the leak: once the pending list is full, a source that still holds a row
// there gives up its own oldest to make room for its next one, and the
// displaced row was then kept for retainDecided with nothing counting it. That
// loop has no end — the source's pending count comes straight back to where it
// was — so one address at the boundary left a decided row behind on every
// request it sent, at the 120/min the peer limiter allows.
//
// Both shapes are asserted, because the per-source bound alone does not bound
// the total and the total bound alone would let one address push out everybody
// else's answers. setUp fills the pending list and returns the source addresses
// that hold a row in it and can therefore displace.
func TestRetainedRequestsAreBounded(t *testing.T) {
	cases := map[string]struct {
		floodSources int // how many of the boundary sources go on sending
		each         int // requests each of them sends after the list is full
	}{
		"one source at the boundary":   {floodSources: 1, each: 500},
		"every source at the boundary": {floodSources: pairing.MaxPending, each: 40},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &clock{at: time.Now()}
			requests := pairing.NewRequestsWithClock(c.now)
			add := func(id, node, source string) error {
				row := pending(id, node, c.at, pairing.Incoming)
				row.SourceHost = source
				err := requests.Add(row)
				// Well inside RequestTTL, so nothing below is bounded by the
				// clock — only by the caps under test.
				c.at = c.at.Add(time.Millisecond)
				return err
			}

			// The owner's own machine asks first, from an address the flood
			// never uses. It stays undecided, so no bound may touch it.
			if err := add("pair_owner", "node_owner", "192.168.1.9"); err != nil {
				t.Fatalf("the owner's own request: %v", err)
			}

			// Fill the rest of the pending list, one row per source, so every
			// flooder below is a source that can displace.
			for i := range pairing.MaxPending - 1 {
				if err := add(fmt.Sprintf("pair_seed%d", i), fmt.Sprintf("node_seed%d", i),
					fmt.Sprintf("10.0.0.%d", i)); err != nil {
					t.Fatalf("seeding row %d: %v", i, err)
				}
			}

			// Now the loop that used to grow the table without end.
			displaced := 0
			for j := range tc.each {
				for i := range tc.floodSources {
					if err := add(fmt.Sprintf("pair_f%d_%d", i, j), fmt.Sprintf("node_f%d_%d", i, j),
						fmt.Sprintf("10.0.0.%d", i)); err == nil {
						displaced++
					}
				}
			}
			if displaced < pairing.MaxRetained {
				t.Fatalf("only %d requests got in, which never reaches the bound of %d — the "+
					"test stopped exercising what it is for", displaced, pairing.MaxRetained)
			}

			rows := requests.List()
			if len(rows) > pairing.MaxRetained {
				t.Errorf("%d rows retained after %d accepted requests, want at most MaxRetained=%d",
					len(rows), displaced, pairing.MaxRetained)
			}

			perSource := map[string]int{}
			for _, row := range rows {
				if row.Decided() && row.SourceHost != "" {
					perSource[row.SourceHost]++
				}
			}
			for source, kept := range perSource {
				if kept > pairing.MaxRetainedPerSource {
					t.Errorf("%s kept %d decided rows, want at most MaxRetainedPerSource=%d",
						source, kept, pairing.MaxRetainedPerSource)
				}
			}

			// The bound must never be paid for by something still waiting for
			// the owner: that is the eviction MaxPendingPerSource exists to
			// deny, and a cap that reintroduced it would be worse than no cap.
			row, ok := requests.Get("pair_owner")
			if !ok {
				t.Fatal("the owner's pending request was dropped to make room for a flood's history")
			}
			if row.State != pairing.StatePending {
				t.Fatalf("the owner's request is %q/%q, want it still pending", row.State, row.Reason)
			}
		})
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

// SettleFrom is the one atomic decision point: it takes the transition, the
// state it came from and the row's own change together.
//
// The three parts are asserted together because the callers depend on them
// being one step. The approval records "this request wrote the trust" in the
// same instant as becoming approved, since a refusal reading between those two
// facts revoked nothing and left a refused key trusted; the refusal learns
// whether it was approved from the transition itself, since the answer read
// beforehand is about a row the node may already have left.
func TestSettleFromTakesTheTransitionAndTheRowTogether(t *testing.T) {
	c := &clock{at: time.Now()}
	requests := pairing.NewRequestsWithClock(c.now)
	if err := requests.Add(pending("pair_1", "node_a", c.at, pairing.Incoming)); err != nil {
		t.Fatal(err)
	}

	settled, previous, err := requests.SettleFrom("pair_1",
		[]pairing.RequestState{pairing.StatePending}, pairing.StateApproved, "",
		func(row *pairing.Request) { row.TrustedByRequest = true })
	if err != nil {
		t.Fatal(err)
	}
	if previous != pairing.StatePending {
		t.Errorf("previous = %q, want %q", previous, pairing.StatePending)
	}
	if settled.State != pairing.StateApproved || !settled.TrustedByRequest {
		t.Errorf("the returned row does not carry both halves: %+v", settled)
	}
	if stored, _ := requests.Get("pair_1"); stored.State != pairing.StateApproved ||
		!stored.TrustedByRequest {
		t.Errorf("the stored row does not carry both halves: %+v", stored)
	}

	// The refusal accepts either state and reports which one it found, which is
	// what decides whether a trust row is revoked.
	refused, previous, err := requests.SettleFrom("pair_1",
		[]pairing.RequestState{pairing.StatePending, pairing.StateApproved},
		pairing.StateRejected, pairing.ReasonDeclined, nil)
	if err != nil {
		t.Fatal(err)
	}
	if previous != pairing.StateApproved {
		t.Errorf("previous = %q, want %q: a refusal reading this would revoke nothing",
			previous, pairing.StateApproved)
	}
	if refused.State != pairing.StateRejected || refused.Reason != pairing.ReasonDeclined {
		t.Errorf("the refusal did not land: %+v", refused)
	}

	// And a transition that is not the one waiting changes nothing at all.
	_, previous, err = requests.SettleFrom("pair_1",
		[]pairing.RequestState{pairing.StatePending}, pairing.StateApproved, "",
		func(row *pairing.Request) { row.TrustedByRequest = false })
	if !errors.Is(err, pairing.ErrWrongState) {
		t.Fatalf("settling a refused request = %v, want ErrWrongState", err)
	}
	if previous != pairing.StateRejected {
		t.Errorf("the refusal reports %q as the state it found", previous)
	}
	stored, _ := requests.Get("pair_1")
	if stored.State != pairing.StateRejected || !stored.TrustedByRequest {
		t.Errorf("a refused transition changed the row anyway: %+v", stored)
	}
}
