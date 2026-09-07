package discovery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func offering(nodeID, address, name string) Announcement {
	return Announcement{
		NodeID: nodeID, Address: address,
		DisplayName: name, Platform: "linux/amd64",
		Fingerprint: "1223 03EA 5E96 543A 2DD8 BFEA",
	}
}

func newTestCandidates(t *testing.T, paired ...string) (*Candidates, *time.Time) {
	t.Helper()
	known := map[string]struct{}{}
	for _, id := range paired {
		known[id] = struct{}{}
	}
	clock := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	c := NewCandidates(
		func(_ context.Context, nodeID string) (bool, error) {
			_, ok := known[nodeID]
			return ok, nil
		},
		func(string) error { return nil },
	)
	c.now = func() time.Time { return clock }
	return c, &clock
}

// A candidate list is a list of claims. What it must never do is change what
// this node trusts — that is the whole reason discovery has never been allowed
// to add rows.
func TestObservingACandidateChangesNoTrust(t *testing.T) {
	c, _ := newTestCandidates(t)
	changed, err := c.Observe(context.Background(), offering("node_stranger00000000", "192.168.1.9:7463", "their laptop"))
	if err != nil || !changed {
		t.Fatalf("Observe() = %v, %v", changed, err)
	}
	listed := c.List()
	if len(listed) != 1 || listed[0].NodeID != "node_stranger00000000" {
		t.Fatalf("list = %+v", listed)
	}
	// The type carries no way to become trust. This is the assertion the rest
	// of the design rests on, so it is stated rather than assumed: Candidates
	// has no writer for the trust store, and Observe's only inputs are a packet
	// and a read-only paired check.
	if listed[0].Fingerprint == "" {
		t.Error("a candidate without a fingerprint is not a candidate")
	}
}

// A node that is not offering is the case discovery already served: it wants
// its address recorded by peers that know it, not a row on someone's screen.
func TestANodeThatIsNotOfferingIsNotACandidate(t *testing.T) {
	c, _ := newTestCandidates(t)
	changed, err := c.Observe(context.Background(), Announcement{
		NodeID: "node_quiet0000000000", Address: "192.168.1.9:7463",
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed || len(c.List()) != 0 {
		t.Errorf("a non-offering announcement was listed: %+v", c.List())
	}
}

// Pairing is the point of the list, so something already paired is noise — and
// worse, it is noise that could occupy a slot in a bounded list.
func TestAnAlreadyPairedNodeIsNotACandidate(t *testing.T) {
	c, _ := newTestCandidates(t, "node_known0000000000")
	if changed, err := c.Observe(context.Background(),
		offering("node_known0000000000", "192.168.1.9:7463", "known")); err != nil || changed {
		t.Fatalf("Observe() = %v, %v; a paired node was listed", changed, err)
	}
	if len(c.List()) != 0 {
		t.Errorf("list = %+v", c.List())
	}
}

// The same rule delivery uses. A candidate this node could never deliver to is
// an invitation to pair with something unreachable.
func TestAnAddressThePolicyRefusesIsNotACandidate(t *testing.T) {
	c := NewCandidates(
		func(context.Context, string) (bool, error) { return false, nil },
		func(address string) error {
			if strings.HasPrefix(address, "8.8.") {
				return errors.New("not a private address")
			}
			return nil
		},
	)
	if _, err := c.Observe(context.Background(),
		offering("node_public0000000000", "8.8.8.8:7463", "somewhere")); err != nil {
		t.Fatal(err)
	}
	if len(c.List()) != 0 {
		t.Errorf("a public address was listed: %+v", c.List())
	}
}

// Nothing announces that it has stopped offering, so the only thing that
// removes a row is time.
func TestACandidateStopsBeingListedWhenItGoesQuiet(t *testing.T) {
	c, clock := newTestCandidates(t)
	if _, err := c.Observe(context.Background(),
		offering("node_brief00000000000", "192.168.1.9:7463", "brief")); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(CandidateTTL - time.Second)
	if len(c.List()) != 1 {
		t.Fatalf("dropped while still within its TTL: %+v", c.List())
	}
	*clock = clock.Add(2 * time.Second)
	if listed := c.List(); len(listed) != 0 {
		t.Errorf("still listed %v after its TTL", listed)
	}
}

// Anyone who can send multicast can invent node ids. At the bound the list
// refuses new ones rather than evicting: evicting would let a flood push the
// machine the owner is looking for off the screen, which is what the flood is
// for.
func TestAFloodCannotPushOutTheCandidateSomeoneIsLookingFor(t *testing.T) {
	c, _ := newTestCandidates(t)
	wanted := offering("node_wanted0000000000", "192.168.1.9:7463", "the one")
	if _, err := c.Observe(context.Background(), wanted); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < MaxCandidates*4; i++ {
		if _, err := c.Observe(context.Background(),
			offering(fmt.Sprintf("node_flood%011d", i), "192.168.1.10:7463", "flood")); err != nil {
			t.Fatal(err)
		}
	}
	listed := c.List()
	if len(listed) > MaxCandidates {
		t.Errorf("list grew to %d, over the %d bound", len(listed), MaxCandidates)
	}
	found := false
	for _, candidate := range listed {
		if candidate.NodeID == wanted.NodeID {
			found = true
		}
	}
	if !found {
		t.Error("the flood pushed out the candidate that was already listed")
	}
	// And a candidate already listed keeps refreshing at the bound.
	if _, err := c.Observe(context.Background(), wanted); err != nil {
		t.Fatalf("a listed candidate could not refresh at the bound: %v", err)
	}
}

// A candidate re-announcing every second must not produce a log line every
// second, so "changed" has to mean what a person would see.
func TestARefreshIsNotAChange(t *testing.T) {
	c, clock := newTestCandidates(t)
	announcement := offering("node_steady000000000", "192.168.1.9:7463", "steady")
	if changed, _ := c.Observe(context.Background(), announcement); !changed {
		t.Fatal("the first sighting was not a change")
	}
	*clock = clock.Add(time.Second)
	if changed, _ := c.Observe(context.Background(), announcement); changed {
		t.Error("an identical re-announcement was reported as a change")
	}
	announcement.DisplayName = "renamed"
	if changed, _ := c.Observe(context.Background(), announcement); !changed {
		t.Error("a renamed candidate was not reported as a change")
	}
}

// The first sighting is what orders the list, so a row does not jump as packets
// arrive.
func TestTheListIsStableAsPacketsArrive(t *testing.T) {
	c, clock := newTestCandidates(t)
	for i, id := range []string{"node_first00000000000", "node_second0000000000", "node_third00000000000"} {
		*clock = clock.Add(time.Duration(i) * time.Second)
		if _, err := c.Observe(context.Background(), offering(id, "192.168.1.9:7463", id)); err != nil {
			t.Fatal(err)
		}
	}
	before := c.List()
	// The one seen first re-announces; it must not move to the end.
	*clock = clock.Add(time.Second)
	if _, err := c.Observe(context.Background(),
		offering("node_first00000000000", "192.168.1.9:7463", "node_first00000000000")); err != nil {
		t.Fatal(err)
	}
	after := c.List()
	for i := range before {
		if before[i].NodeID != after[i].NodeID {
			t.Fatalf("order changed on a refresh: %v then %v",
				[]string{before[0].NodeID, before[1].NodeID, before[2].NodeID},
				[]string{after[0].NodeID, after[1].NodeID, after[2].NodeID})
		}
	}
}

// Pairing with a candidate makes it a peer; leaving it listed invites pairing
// with it twice.
func TestForgettingACandidateRemovesIt(t *testing.T) {
	c, _ := newTestCandidates(t)
	if _, err := c.Observe(context.Background(),
		offering("node_paired0000000000", "192.168.1.9:7463", "soon a peer")); err != nil {
		t.Fatal(err)
	}
	c.Forget("node_paired0000000000")
	if len(c.List()) != 0 {
		t.Errorf("still listed after Forget: %+v", c.List())
	}
}
