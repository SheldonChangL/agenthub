package discovery

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
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

// trustReads counts how often the trust store was asked, which is the property
// ApplyAll was written to protect and this list has to protect too.
type trustProbe struct {
	paired map[string]struct{}
	reads  int
}

func (p *trustProbe) isPaired(_ context.Context, nodeID string) (bool, error) {
	p.reads++
	_, ok := p.paired[nodeID]
	return ok, nil
}

func newTestCandidates(t *testing.T, paired ...string) (*Candidates, *trustProbe, *time.Time) {
	t.Helper()
	probe := &trustProbe{paired: map[string]struct{}{}}
	for _, id := range paired {
		probe.paired[id] = struct{}{}
	}
	clock := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	c := NewCandidates(probe.isPaired, func(string) error { return nil })
	c.now = func() time.Time { return clock }
	return c, probe, &clock
}

// A candidate list is a list of claims. What it must never do is change what
// this node trusts — the reason discovery has never been allowed to add rows.
func TestObservingACandidateNeverConsultsOrChangesTrustBeyondAsking(t *testing.T) {
	c, probe, _ := newTestCandidates(t)
	changed, err := c.Observe(context.Background(), offering("node_stranger00000000", "192.168.1.9:7463", "their laptop"))
	if err != nil || !changed {
		t.Fatalf("Observe() = %v, %v", changed, err)
	}
	// The only interaction with the trust store is a read, and the type has no
	// writer for it at all: NewCandidates takes a paired check and an address
	// policy, and nothing else.
	if probe.reads != 1 {
		t.Errorf("trust reads = %d, want exactly one", probe.reads)
	}
	listed := c.List()
	if len(listed) != 1 || listed[0].NodeID != "node_stranger00000000" {
		t.Fatalf("list = %+v", listed)
	}
}

// A node that is not offering is the case discovery already served: it wants
// its address recorded by peers that know it, not a row on someone's screen.
func TestANodeThatIsNotOfferingIsNotACandidate(t *testing.T) {
	c, probe, _ := newTestCandidates(t)
	changed, err := c.Observe(context.Background(), Announcement{
		NodeID: "node_quiet0000000000", Address: "192.168.1.9:7463",
	})
	if err != nil {
		t.Fatal(err)
	}
	if changed || len(c.List()) != 0 {
		t.Errorf("a non-offering announcement was listed: %+v", c.List())
	}
	if probe.reads != 0 {
		t.Errorf("a non-offer cost %d trust reads", probe.reads)
	}
}

// The id is the map key and a field on a person's screen. Before this list it
// was inert — it could only fail to match the trust store — so nothing checked
// it.
func TestAnIDThatIsNotAnIDIsNotACandidate(t *testing.T) {
	for name, id := range map[string]string{
		"a newline":      "node_one000000000000\nnode_two000000000000",
		"trailing space": "node_spaced000000000 ",
		"too short":      "node_x",
		"too long":       "node_" + strings.Repeat("a", 200),
		"non-ASCII":      "node_lookalike00000а",
		"empty":          "",
	} {
		t.Run(name, func(t *testing.T) {
			c, probe, _ := newTestCandidates(t)
			changed, err := c.Observe(context.Background(), offering(id, "192.168.1.9:7463", "hostile"))
			if err != nil || changed {
				t.Fatalf("Observe() = %v, %v", changed, err)
			}
			if len(c.List()) != 0 {
				t.Errorf("listed: %+v", c.List())
			}
			if probe.reads != 0 {
				t.Errorf("a malformed id cost %d trust reads", probe.reads)
			}
		})
	}
}

// Pairing is the point of the list, so something already paired is noise — and
// worse, noise that could occupy a slot in a bounded list.
func TestAnAlreadyPairedNodeIsNotACandidate(t *testing.T) {
	c, _, _ := newTestCandidates(t, "node_known0000000000")
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
	probe := &trustProbe{paired: map[string]struct{}{}}
	c := NewCandidates(probe.isPaired, func(address string) error {
		if strings.HasPrefix(address, "8.8.") {
			return errors.New("not a private address")
		}
		return nil
	})
	if _, err := c.Observe(context.Background(),
		offering("node_public000000000", "8.8.8.8:7463", "somewhere")); err != nil {
		t.Fatal(err)
	}
	if len(c.List()) != 0 {
		t.Errorf("a public address was listed: %+v", c.List())
	}
	if probe.reads != 0 {
		t.Errorf("a refused address cost %d trust reads", probe.reads)
	}
}

// An offer has to come from where it says it is. Without this one host sprays
// the list with candidates that all resolve to itself, and the owner picks one.
func TestAnOfferMustComeFromWhereItSaysItIs(t *testing.T) {
	c, _, _ := newTestCandidates(t)
	source := netip.MustParseAddr("192.168.1.9")
	if _, err := c.ObserveAll(context.Background(), source, []Announcement{
		offering("node_honest0000000000", "192.168.1.9:7463", "honest"),
		offering("node_liar0000000000000", "192.168.1.50:7463", "claims to be elsewhere"),
	}); err != nil {
		t.Fatal(err)
	}
	listed := c.List()
	if len(listed) != 1 || listed[0].NodeID != "node_honest0000000000" {
		t.Fatalf("list = %+v; only the one announcing its own address belongs", listed)
	}
}

// Nothing announces that it has stopped offering, so the only thing that
// removes a row is time.
func TestACandidateStopsBeingListedWhenItGoesQuiet(t *testing.T) {
	c, _, clock := newTestCandidates(t)
	if _, err := c.Observe(context.Background(),
		offering("node_brief0000000000", "192.168.1.9:7463", "brief")); err != nil {
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

// Anyone on the group can read a candidate's id and then send offers under it.
// If the newest packet won, an attacker would rewrite the row the owner is
// looking at to point at themselves, and the owner would click the name they
// recognise.
func TestAForgerCannotRewriteTheRowSomeoneIsAboutToClick(t *testing.T) {
	c, _, clock := newTestCandidates(t)
	genuine := offering("node_genuine00000000", "192.168.1.9:7463", "sheldon's laptop")
	if _, err := c.Observe(context.Background(), genuine); err != nil {
		t.Fatal(err)
	}

	forged := genuine
	forged.Address = "192.168.1.66:7463"
	forged.Fingerprint = "DEAD BEEF DEAD BEEF DEAD BEEF"
	changes := 0
	for i := 0; i < 100; i++ {
		*clock = clock.Add(time.Second)
		changed, err := c.Observe(context.Background(), forged)
		if err != nil {
			t.Fatal(err)
		}
		if changed {
			changes++
		}
	}
	listed := c.List()
	if len(listed) != 1 {
		t.Fatalf("list = %+v", listed)
	}
	if listed[0].Address != "192.168.1.9:7463" {
		t.Errorf("address = %q; the forger redirected the row", listed[0].Address)
	}
	if listed[0].Fingerprint != genuine.Fingerprint {
		t.Errorf("fingerprint = %q; the forger replaced what a person compares", listed[0].Fingerprint)
	}
	if changes != 0 {
		t.Errorf("%d forged packets each counted as a change; that is a log line per packet", changes)
	}
}

// One datagram can carry hundreds of address records for a few node ids. A
// trust read per record is the amplification ApplyAll exists to prevent.
func TestOneTrustReadPerNodePerPacket(t *testing.T) {
	c, probe, _ := newTestCandidates(t)
	source := netip.MustParseAddr("192.168.1.9")
	packet := make([]Announcement, 0, 600)
	for i := 0; i < 600; i++ {
		// One node id, many address records, as a multi-homed host produces.
		packet = append(packet, offering("node_multihomed00000", "192.168.1.9:7463", "many addresses"))
	}
	if _, err := c.ObserveAll(context.Background(), source, packet); err != nil {
		t.Fatal(err)
	}
	if probe.reads != 1 {
		t.Errorf("600 records for one node cost %d trust reads, want 1", probe.reads)
	}
	if len(c.List()) != 1 {
		t.Errorf("one node became %d rows", len(c.List()))
	}
}

// Anyone who can send multicast can invent node ids. At the bound the list
// refuses new ones rather than evicting: evicting is what a flood is for.
func TestAFloodCannotPushOutTheCandidateSomeoneIsLookingFor(t *testing.T) {
	c, _, clock := newTestCandidates(t)
	wanted := offering("node_wanted000000000", "192.168.1.9:7463", "the one")
	if _, err := c.Observe(context.Background(), wanted); err != nil {
		t.Fatal(err)
	}
	full := 0
	for i := 0; i < MaxCandidates*4; i++ {
		_, err := c.Observe(context.Background(),
			offering(fmt.Sprintf("node_flood%011d", i), "192.168.1.10:7463", "flood"))
		if errors.Is(err, ErrCandidatesFull) {
			full++
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	listed := c.List()
	if len(listed) > MaxCandidates {
		t.Errorf("list grew to %d, over the %d bound", len(listed), MaxCandidates)
	}
	if full == 0 {
		t.Error("the list filled without ever saying so")
	}
	if !c.Full() {
		t.Error("Full() does not report a full list")
	}
	found := false
	for _, candidate := range listed {
		if candidate.NodeID == wanted.NodeID {
			found = true
		}
	}
	if !found {
		t.Fatal("the flood pushed out the candidate that was already listed")
	}

	// And it survives the flood: the flood expires, the refreshed one does not.
	for i := 0; i < 3; i++ {
		*clock = clock.Add(CandidateTTL / 2)
		if _, err := c.Observe(context.Background(), wanted); err != nil {
			t.Fatalf("a listed candidate could not refresh at the bound: %v", err)
		}
	}
	surviving := c.List()
	if len(surviving) != 1 || surviving[0].NodeID != wanted.NodeID {
		t.Errorf("after the flood expired: %+v; want only the refreshed one", surviving)
	}
}

// A candidate re-announcing every second must not produce a log line every
// second, so "changed" has to mean what a person would see — and after the
// first sighting nothing about a row changes, by design.
func TestOnlyAFirstSightingIsAChange(t *testing.T) {
	c, _, clock := newTestCandidates(t)
	announcement := offering("node_steady000000000", "192.168.1.9:7463", "steady")
	if changed, _ := c.Observe(context.Background(), announcement); !changed {
		t.Fatal("the first sighting was not a change")
	}
	for i := 0; i < 10; i++ {
		*clock = clock.Add(time.Second)
		if changed, _ := c.Observe(context.Background(), announcement); changed {
			t.Fatal("a re-announcement was reported as a change")
		}
	}
	// Including one that renames itself: that is the forgery case, not a rename.
	announcement.DisplayName = "renamed"
	if changed, _ := c.Observe(context.Background(), announcement); changed {
		t.Error("a changed label was accepted as a change to a listed row")
	}
}

// Two rows claiming one fingerprint means at least one is lying, and a person
// needs to see that rather than pick.
func TestACloneIsMarkedRatherThanGuessedAt(t *testing.T) {
	c, _, clock := newTestCandidates(t)
	genuine := offering("node_genuine00000000", "192.168.1.9:7463", "sheldon's laptop")
	if _, err := c.Observe(context.Background(), genuine); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Second)
	clone := offering("node_clone0000000000", "192.168.1.66:7463", "sheldon's laptop")
	if _, err := c.Observe(context.Background(), clone); err != nil {
		t.Fatal(err)
	}
	listed := c.List()
	if len(listed) != 2 {
		t.Fatalf("list = %+v", listed)
	}
	for _, candidate := range listed {
		if !candidate.Duplicate {
			t.Errorf("%s shares its name and fingerprint with another row and is not marked", candidate.NodeID)
		}
	}
}

// The first sighting orders the list, so a row does not jump as packets arrive.
func TestTheListIsStableAsPacketsArrive(t *testing.T) {
	c, _, clock := newTestCandidates(t)
	want := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("node_ordered%09d", i)
		want = append(want, id)
		*clock = clock.Add(time.Second)
		if _, err := c.Observe(context.Background(), offering(id, "192.168.1.9:7463", id)); err != nil {
			t.Fatal(err)
		}
	}
	assertOrder := func(when string) {
		t.Helper()
		listed := c.List()
		if len(listed) != len(want) {
			t.Fatalf("%s: %d rows, want %d", when, len(listed), len(want))
		}
		for i, candidate := range listed {
			if candidate.NodeID != want[i] {
				t.Fatalf("%s: position %d is %s, want %s", when, i, candidate.NodeID, want[i])
			}
		}
	}
	assertOrder("after the first sightings")
	// The one seen first re-announces; it must not move to the end.
	*clock = clock.Add(time.Second)
	if _, err := c.Observe(context.Background(), offering(want[0], "192.168.1.9:7463", want[0])); err != nil {
		t.Fatal(err)
	}
	assertOrder("after a refresh")
}

// Pairing with a candidate makes it a peer; leaving it listed invites pairing
// with it twice.
func TestForgettingACandidateRemovesIt(t *testing.T) {
	c, _, _ := newTestCandidates(t)
	if _, err := c.Observe(context.Background(),
		offering("node_paired000000000", "192.168.1.9:7463", "soon a peer")); err != nil {
		t.Fatal(err)
	}
	c.Forget("node_paired000000000")
	if len(c.List()) != 0 {
		t.Errorf("still listed after Forget: %+v", c.List())
	}
}

// The bound is checked twice: once before the trust read and once after
// re-taking the lock. The second is what holds when several packets for
// different new nodes are in flight at once, which is the normal case on a busy
// group — without it the list can overshoot by however many were racing.
func TestTheBoundHoldsWhenNewCandidatesArriveAtOnce(t *testing.T) {
	c := NewCandidates(
		func(context.Context, string) (bool, error) {
			// A trust store is a database; the window this opens is the point.
			time.Sleep(time.Microsecond)
			return false, nil
		},
		func(string) error { return nil },
	)
	var wait sync.WaitGroup
	for worker := 0; worker < 16; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for i := 0; i < 40; i++ {
				id := fmt.Sprintf("node_burst%03d%08d", worker, i)
				_, _ = c.Observe(context.Background(), offering(id, "192.168.1.9:7463", "burst"))
			}
		}(worker)
	}
	wait.Wait()
	if listed := len(c.List()); listed > MaxCandidates {
		t.Errorf("the list holds %d, over the %d bound: the check after the trust read did not hold", listed, MaxCandidates)
	}
}

// The list is read by an HTTP handler while packets arrive on a UDP socket.
func TestConcurrentUse(t *testing.T) {
	c := NewCandidates(
		func(context.Context, string) (bool, error) { return false, nil },
		func(string) error { return nil },
	)
	var wait sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for i := 0; i < 200; i++ {
				id := fmt.Sprintf("node_race%03d%08d", worker, i%20)
				_, _ = c.Observe(context.Background(), offering(id, "192.168.1.9:7463", "racer"))
				_ = c.List()
				_ = c.Full()
				c.Forget(id)
			}
		}(worker)
	}
	wait.Wait()
	if len(c.List()) > MaxCandidates {
		t.Errorf("the bound did not hold under concurrency: %d", len(c.List()))
	}
}
