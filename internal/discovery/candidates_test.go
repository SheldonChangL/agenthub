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

// observeOne drives the unexported path the way a caller with no source
// address would. Production callers always have one — ObserveAll takes it —
// so this exists for the cases where the source is not what is being tested.
func observeOne(c *Candidates, announcement Announcement) (bool, error) {
	return c.observe(context.Background(), netip.Addr{}, announcement)
}

const localTestNode = "node_thismachine0000"

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
	c := NewCandidates(localTestNode, probe.isPaired, func(string) error { return nil })
	c.now = func() time.Time { return clock }
	return c, probe, &clock
}

// A candidate list is a list of claims. What it must never do is change what
// this node trusts — the reason discovery has never been allowed to add rows.
func TestObservingACandidateNeverConsultsOrChangesTrustBeyondAsking(t *testing.T) {
	c, probe, _ := newTestCandidates(t)
	changed, err := observeOne(c, offering("node_stranger00000000", "192.168.1.9:7463", "their laptop"))
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
	changed, err := observeOne(c, Announcement{
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
			changed, err := observeOne(c, offering(id, "192.168.1.9:7463", "hostile"))
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
	if changed, err := observeOne(c,
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
	c := NewCandidates(localTestNode, probe.isPaired, func(address string) error {
		if strings.HasPrefix(address, "8.8.") {
			return errors.New("not a private address")
		}
		return nil
	})
	if _, err := observeOne(c,
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

// A multi-homed node announces every address it has in one packet, and only one
// of them is the one the datagram came from. Choosing a record before checking
// the source drops the node entirely — which is every dual-stack machine.
func TestAMultiHomedNodeIsListedAtTheAddressItAnnouncedFrom(t *testing.T) {
	c, _, _ := newTestCandidates(t)
	source := netip.MustParseAddr("192.168.1.9")
	// The matching record is not first, which is the case that fails if the
	// reduction picks before it checks.
	if _, err := c.ObserveAll(context.Background(), source, []Announcement{
		offering("node_multi0000000000", "[2001:db8::1]:7463", "laptop"),
		offering("node_multi0000000000", "192.168.1.20:7463", "laptop"),
		offering("node_multi0000000000", "192.168.1.9:7463", "laptop"),
	}); err != nil {
		t.Fatal(err)
	}
	listed := c.List()
	if len(listed) != 1 {
		t.Fatalf("list = %+v; a multi-homed node is one candidate", listed)
	}
	if listed[0].Address != "192.168.1.9:7463" {
		t.Errorf("address = %q, want the one the packet came from", listed[0].Address)
	}
}

// A dual-stack socket reports an IPv4 sender as ::ffff:a.b.c.d while the A
// record parses as a.b.c.d, and a link-local source carries a zone an AAAA
// record cannot. Comparing them literally refuses both.
func TestTheSourceComparisonHandlesMappedAndZonedAddresses(t *testing.T) {
	for name, c := range map[string]struct {
		announced string
		source    string
	}{
		"an IPv4 sender on a dual-stack socket": {"192.168.1.9:7463", "::ffff:192.168.1.9"},
		"a link-local source with a zone":       {"[fe80::1]:7463", "fe80::1%en0"},
		"an ordinary IPv4 pair":                 {"192.168.1.9:7463", "192.168.1.9"},
	} {
		t.Run(name, func(t *testing.T) {
			source, err := netip.ParseAddr(c.source)
			if err != nil {
				t.Fatal(err)
			}
			if !announcedFrom(c.announced, source) {
				t.Errorf("announcedFrom(%q, %q) = false; a legitimate node was excluded", c.announced, c.source)
			}
		})
	}
	// And a genuine mismatch is still a mismatch.
	if announcedFrom("192.168.1.20:7463", netip.MustParseAddr("192.168.1.9")) {
		t.Error("an address that is not the source was accepted")
	}
}

// A listed candidate's paired status was checked when it was inserted, and
// pairing with one calls Forget. Asking again on every packet is a database
// read per packet per row.
func TestARefreshDoesNotAskTheTrustStoreAgain(t *testing.T) {
	c, probe, clock := newTestCandidates(t)
	announcement := offering("node_steady000000000", "192.168.1.9:7463", "steady")
	if _, err := observeOne(c, announcement); err != nil {
		t.Fatal(err)
	}
	if probe.reads != 1 {
		t.Fatalf("the first sighting cost %d reads, want 1", probe.reads)
	}
	for i := 0; i < 50; i++ {
		*clock = clock.Add(time.Second)
		if _, err := observeOne(c, announcement); err != nil {
			t.Fatal(err)
		}
	}
	if probe.reads != 1 {
		t.Errorf("50 refreshes cost %d trust reads in total, want 1", probe.reads)
	}
}

// Nothing announces that it has stopped offering, so the only thing that
// removes a row is time.
func TestACandidateStopsBeingListedWhenItGoesQuiet(t *testing.T) {
	c, _, clock := newTestCandidates(t)
	if _, err := observeOne(c,
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

// Anyone on the group can read a candidate's id — every node broadcasts it — so
// a forger can send offers under one. If the newest packet won, the row the
// owner is looking at would point at the forger and the owner would click the
// name they recognise.
func TestAForgerCannotRewriteTheRowSomeoneIsAboutToClick(t *testing.T) {
	c, _, clock := newTestCandidates(t)
	genuine := offering("node_genuine00000000", "192.168.1.9:7463", "sheldon's laptop")
	if _, err := observeOne(c, genuine); err != nil {
		t.Fatal(err)
	}
	forged := genuine
	forged.Address = "192.168.1.66:7463"
	forged.Fingerprint = "DEAD BEEF DEAD BEEF DEAD BEEF"

	// The genuine node keeps announcing, as a node in pairing mode does, and the
	// forger interleaves.
	changes := 0
	for i := 0; i < 60; i++ {
		*clock = clock.Add(10 * time.Second)
		if _, err := observeOne(c, genuine); err != nil {
			t.Fatal(err)
		}
		changed, err := observeOne(c, forged)
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
	// The disagreement is shown once, not swallowed and not logged per packet.
	if !listed[0].Contested {
		t.Error("something else claimed this id and the row does not say so")
	}
	if changes != 1 {
		t.Errorf("%d changes from 60 forged packets; want exactly one, when the row became contested", changes)
	}
}

// The other half of the contest check, and the one that matters most: the same
// address, a different key. A node re-keyed, or someone answering at the same
// place with their own key — either way the fingerprint a person is about to
// compare has changed under them.
func TestADifferentKeyAtTheSameAddressIsContested(t *testing.T) {
	c, _, clock := newTestCandidates(t)
	first := offering("node_rekeyed00000000", "192.168.1.9:7463", "laptop")
	if _, err := observeOne(c, first); err != nil {
		t.Fatal(err)
	}
	rekeyed := first
	rekeyed.Fingerprint = "DEAD BEEF DEAD BEEF DEAD BEEF"
	*clock = clock.Add(time.Second)
	changed, err := observeOne(c, rekeyed)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("a new key under a listed id was not reported as a change")
	}
	listed := c.List()
	if len(listed) != 1 {
		t.Fatalf("list = %+v", listed)
	}
	if !listed[0].Contested {
		t.Error("a different key at the same address is not contested")
	}
	if listed[0].Fingerprint != first.Fingerprint {
		t.Errorf("fingerprint = %q; the row moved to the new key", listed[0].Fingerprint)
	}
}

// A claim over the address family a row is not pinned to is deliberately not
// flagged: at this layer it cannot be told from the same node being dual-stack,
// and flagging it would mark every dual-stack machine. Pinned here so that
// changing it is a decision rather than an accident.
func TestAClaimOverTheOtherFamilyIsKnowinglyNotFlagged(t *testing.T) {
	c, _, clock := newTestCandidates(t)
	victim := offering("node_victim000000000", "192.168.1.9:7463", "sheldon's laptop")
	if _, err := c.ObserveAll(context.Background(), netip.MustParseAddr("192.168.1.9"),
		[]Announcement{victim}); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Second)
	// Same id, same name, same announced fingerprint, other family.
	claimant := offering("node_victim000000000", "[fe80::66]:7463", "sheldon's laptop")
	if _, err := c.ObserveAll(context.Background(), netip.MustParseAddr("fe80::66%en0"),
		[]Announcement{claimant}); err != nil {
		t.Fatal(err)
	}
	listed := c.List()
	if len(listed) != 1 {
		t.Fatalf("list = %+v", listed)
	}
	if listed[0].Contested {
		t.Error("the cross-family case is flagged; that marks every dual-stack machine")
	}
	// What does hold: the row still names the address it was pinned to, so the
	// claimant did not redirect it.
	if listed[0].Address != "192.168.1.9:7463" {
		t.Errorf("address = %q; a cross-family claimant redirected the row", listed[0].Address)
	}
}

// The three outcomes, stated as a table rather than inferred from two
// predicates — a port change used to land in the branch for a different family.
func TestHowTwoAddressesRelate(t *testing.T) {
	for name, c := range map[string]struct {
		announced, existing string
		want                addressRelation
	}{
		"identical":                    {"192.168.1.9:7463", "192.168.1.9:7463", addressSame},
		"a v4-mapped form of the same": {"[::ffff:192.168.1.9]:7463", "192.168.1.9:7463", addressSame},
		"a zoned form of the same":     {"[fe80::1%en0]:7463", "[fe80::1]:7463", addressSame},
		"another host, same family":    {"192.168.1.40:7463", "192.168.1.9:7463", addressConflicts},
		"another port, same host":      {"192.168.1.9:7464", "192.168.1.9:7463", addressConflicts},
		"the other family":             {"[fe80::1]:7463", "192.168.1.9:7463", addressOtherFamily},
		"unparseable and different":    {"not-an-address", "192.168.1.9:7463", addressConflicts},
		"unparseable and identical":    {"not-an-address", "not-an-address", addressSame},
	} {
		t.Run(name, func(t *testing.T) {
			if got := relateAddresses(c.announced, c.existing); got != c.want {
				t.Errorf("relateAddresses(%q, %q) = %v, want %v", c.announced, c.existing, got, c.want)
			}
		})
	}
}

// A forger must not be able to inherit a departed node's row. Refreshing on
// any packet under the id does exactly that, and the row would still carry the
// address and fingerprint the owner recognises long after that machine left.
//
// What a forger can always do is announce an id nobody is using — including one
// that has expired — and be listed as the unverified claim every candidate is.
// That is not something this list can prevent and not what it is for: the
// fingerprint comparison in the handshake is. What it must prevent is a forged
// packet inheriting a row someone already trusts the look of.
func TestAForgerCannotInheritADepartedNodesRow(t *testing.T) {
	c, _, clock := newTestCandidates(t)
	start := *clock
	genuine := offering("node_gone00000000000", "192.168.1.9:7463", "went home")
	if _, err := observeOne(c, genuine); err != nil {
		t.Fatal(err)
	}
	forged := genuine
	forged.Address = "192.168.1.66:7463"
	forged.Fingerprint = "DEAD BEEF DEAD BEEF DEAD BEEF"
	for i := 0; i < 30; i++ {
		*clock = clock.Add(10 * time.Second)
		if _, err := observeOne(c, forged); err != nil {
			t.Fatal(err)
		}
	}
	listed := c.List()
	for _, candidate := range listed {
		if candidate.Address == genuine.Address || candidate.Fingerprint == genuine.Fingerprint {
			t.Errorf("the departed node's details are still listed %v later: %+v", 300*time.Second, candidate)
		}
	}
	// Whatever is listed now is the forger's own claim rather than an inherited
	// row: its first sighting is after the original one expired.
	for _, candidate := range listed {
		if !candidate.FirstSeen.After(start) {
			t.Errorf("a row survived from before the expiry: %+v", candidate)
		}
	}
}

// A node that genuinely moves — a new DHCP lease, wifi to ethernet — has to
// come back. Its own packets must not keep the stale row young, or it can never
// be paired with again.
func TestANodeThatMovesReappearsAtItsNewAddress(t *testing.T) {
	c, _, clock := newTestCandidates(t)
	before := offering("node_mover0000000000", "192.168.1.9:7463", "laptop")
	if _, err := observeOne(c, before); err != nil {
		t.Fatal(err)
	}
	after := before
	after.Address = "192.168.1.40:7463"
	for i := 0; i < 20; i++ {
		*clock = clock.Add(10 * time.Second)
		if _, err := observeOne(c, after); err != nil {
			t.Fatal(err)
		}
	}
	listed := c.List()
	if len(listed) != 1 {
		t.Fatalf("list = %+v; the mover should be listed exactly once", listed)
	}
	if listed[0].Address != after.Address {
		t.Errorf("address = %q, want %q; the stale row never expired", listed[0].Address, after.Address)
	}
}

// An attacker who claims a victim's id before the victim opens pairing mode
// must not silently own the row. Ids are public, so first-writer-wins is as
// forgeable as last-writer-wins; what matters is that the person is told.
func TestClaimingAnIDFirstDoesNotSilentlyOwnTheRow(t *testing.T) {
	c, _, clock := newTestCandidates(t)
	attacker := offering("node_victim000000000", "192.168.1.66:7463", "sheldon's laptop")
	if _, err := observeOne(c, attacker); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(5 * time.Second)
	victim := offering("node_victim000000000", "192.168.1.9:7463", "sheldon's laptop")
	if _, err := observeOne(c, victim); err != nil {
		t.Fatal(err)
	}
	listed := c.List()
	if len(listed) != 1 {
		t.Fatalf("list = %+v", listed)
	}
	if !listed[0].Contested {
		t.Error("two machines claimed this id and the row does not say so")
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
	if _, err := observeOne(c, wanted); err != nil {
		t.Fatal(err)
	}
	full := 0
	for i := 0; i < MaxCandidates*4; i++ {
		_, err := observeOne(c,
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
		if _, err := observeOne(c, wanted); err != nil {
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
	if changed, _ := observeOne(c, announcement); !changed {
		t.Fatal("the first sighting was not a change")
	}
	for i := 0; i < 10; i++ {
		*clock = clock.Add(time.Second)
		if changed, _ := observeOne(c, announcement); changed {
			t.Fatal("a re-announcement was reported as a change")
		}
	}
	// Including one that renames itself: that is the forgery case, not a rename.
	announcement.DisplayName = "renamed"
	if changed, _ := observeOne(c, announcement); changed {
		t.Error("a changed label was accepted as a change to a listed row")
	}
}

// Two rows claiming one fingerprint means at least one is lying, and a person
// needs to see that rather than pick.
func TestACloneIsMarkedRatherThanGuessedAt(t *testing.T) {
	c, _, clock := newTestCandidates(t)
	genuine := offering("node_genuine00000000", "192.168.1.9:7463", "sheldon's laptop")
	if _, err := observeOne(c, genuine); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Second)
	// The same name, a different fingerprint: only the name half can catch it.
	clone := offering("node_clone0000000000", "192.168.1.66:7463", "sheldon's laptop")
	clone.Fingerprint = "DEAD BEEF DEAD BEEF DEAD BEEF"
	if _, err := observeOne(c, clone); err != nil {
		t.Fatal(err)
	}
	listed := c.List()
	if len(listed) != 2 {
		t.Fatalf("list = %+v", listed)
	}
	for _, candidate := range listed {
		if !candidate.Duplicate {
			t.Errorf("%s shares its display name with another row and is not marked", candidate.NodeID)
		}
	}
}

// The other half: a different name, the same fingerprint. This is the one that
// matters most — two rows cannot honestly claim one key.
func TestASharedFingerprintIsMarked(t *testing.T) {
	c, _, clock := newTestCandidates(t)
	genuine := offering("node_genuine00000000", "192.168.1.9:7463", "sheldon's laptop")
	if _, err := observeOne(c, genuine); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Second)
	// A different name, and the fingerprint written the other way, which is how
	// an impersonator would dodge a comparison by bytes.
	clone := offering("node_clone0000000000", "192.168.1.66:7463", "build server")
	clone.Fingerprint = "122303ea5e96543a2dd8bfea"
	if _, err := observeOne(c, clone); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range c.List() {
		if !candidate.Duplicate {
			t.Errorf("%s claims a fingerprint another row claims and is not marked", candidate.NodeID)
		}
	}
}

// A row on its own is not a duplicate, and neither are two rows that share
// nothing — including two with no display name at all.
func TestDistinctRowsAreNotMarked(t *testing.T) {
	c, _, clock := newTestCandidates(t)
	for i, id := range []string{"node_first0000000000", "node_second000000000"} {
		*clock = clock.Add(time.Second)
		announcement := offering(id, "192.168.1.9:7463", "")
		announcement.Fingerprint = fmt.Sprintf("AAAA BBBB CCCC DDDD EEEE %04d", i)
		if _, err := observeOne(c, announcement); err != nil {
			t.Fatal(err)
		}
	}
	for _, candidate := range c.List() {
		if candidate.Duplicate {
			t.Errorf("%s shares nothing with another row and is marked: %+v", candidate.NodeID, candidate)
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
		if _, err := observeOne(c, offering(id, "192.168.1.9:7463", id)); err != nil {
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
	if _, err := observeOne(c, offering(want[0], "192.168.1.9:7463", want[0])); err != nil {
		t.Fatal(err)
	}
	assertOrder("after a refresh")
}

// Pairing with a candidate makes it a peer; leaving it listed invites pairing
// with it twice.
func TestForgettingACandidateRemovesIt(t *testing.T) {
	c, _, _ := newTestCandidates(t)
	if _, err := observeOne(c,
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
	c := NewCandidates(localTestNode,
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
				_, _ = observeOne(c, offering(id, "192.168.1.9:7463", "burst"))
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
	c := NewCandidates(localTestNode,
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
				_, _ = observeOne(c, offering(id, "192.168.1.9:7463", "racer"))
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

// A dual-stack node announces on both multicast groups, and each datagram
// reduces to the address matching its own source. That is one node, not a
// contested id — and if it read as one, every dual-stack machine would be
// marked the moment the IPv6 group is listened on.
func TestADualStackNodeDoesNotContestItself(t *testing.T) {
	c, _, clock := newTestCandidates(t)
	records := []Announcement{
		offering("node_dual00000000000", "192.168.1.9:7463", "laptop"),
		offering("node_dual00000000000", "[fe80::1]:7463", "laptop"),
	}
	if _, err := c.ObserveAll(context.Background(), netip.MustParseAddr("192.168.1.9"), records); err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(time.Second)
	if _, err := c.ObserveAll(context.Background(), netip.MustParseAddr("fe80::1%en0"), records); err != nil {
		t.Fatal(err)
	}
	listed := c.List()
	if len(listed) != 1 {
		t.Fatalf("list = %+v; one node is one row", listed)
	}
	if listed[0].Contested {
		t.Error("a node announcing on both address families contested itself")
	}
	// The row stays on the family it was first seen on. The other family's
	// packets are neither a conflict nor a reason to move it: whichever is
	// shown has to be one the owner can reach, and swapping under them as
	// packets arrive is how a row points somewhere else between looking and
	// clicking.
	if listed[0].Address != "192.168.1.9:7463" {
		t.Errorf("address = %q; the row moved to the other family", listed[0].Address)
	}
	// A row lives on the family it was first seen on. Packets from the other
	// family neither conflict with it nor keep it alive: if the node stops
	// answering on the family the row names, the row has to expire rather than
	// be held open by traffic pointing somewhere the owner cannot reach.
	for i := 0; i < 10; i++ {
		*clock = clock.Add(CandidateTTL / 4)
		if _, err := c.ObserveAll(context.Background(), netip.MustParseAddr("fe80::1%en0"), records); err != nil {
			t.Fatal(err)
		}
	}
	remaining := c.List()
	if len(remaining) != 1 {
		t.Fatalf("list = %+v; the node is still announcing, so it is still one row", remaining)
	}
	// The v4 row expired and the node reappeared at what it is now reachable
	// at. What must not happen is the v4 address being held open by v6 traffic,
	// leaving the owner an address they cannot connect to.
	if remaining[0].Address != "[fe80::1]:7463" {
		t.Errorf("address = %q; a row was kept alive by packets from the other family", remaining[0].Address)
	}
	if remaining[0].Contested {
		t.Error("reappearing on the other family after expiry was treated as a conflict")
	}

	// And a genuine move within one family still contests.
	moved := offering("node_dual00000000000", "[fe80::2]:7463", "laptop")
	if _, err := c.ObserveAll(context.Background(), netip.MustParseAddr("fe80::2%en0"), []Announcement{moved}); err != nil {
		t.Fatal(err)
	}
	if !c.List()[0].Contested {
		t.Error("an address change within one family was not contested")
	}
}

// The paired check is skipped for a row that is already listed. If the row goes
// away between that decision and the insert — pairing calls Forget, and expiry
// is on a timer — inserting would list a peer this owner has already paired
// with, and refreshes never ask again.
func TestARowVanishingMidObserveDoesNotInsertUnchecked(t *testing.T) {
	probe := &trustProbe{paired: map[string]struct{}{"node_racing000000000": {}}}
	c := NewCandidates(localTestNode, probe.isPaired, func(string) error { return nil })
	clock := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	c.now = func() time.Time { return clock }

	announcement := offering("node_racing000000000", "192.168.1.9:7463", "racer")
	// Seed the row directly, as an earlier packet would have, then arrange for
	// it to be gone by the second lock section.
	c.seen[announcement.NodeID] = Candidate{
		NodeID: announcement.NodeID, Address: announcement.Address,
		Fingerprint: announcement.Fingerprint,
		FirstSeen:   clock, LastSeen: clock,
	}
	calls := 0
	c.now = func() time.Time {
		calls++
		if calls > 1 {
			// By the second expire() the row has aged out.
			return clock.Add(CandidateTTL * 2)
		}
		return clock
	}
	changed, err := observeOne(c, announcement)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("a row was inserted after vanishing mid-observe")
	}
	if len(c.List()) != 0 {
		t.Errorf("a paired node was listed without a trust read: %+v", c.List())
	}
}

// A caller holding a packet always has its source, so an invalid one is a bug
// rather than a case to tolerate — tolerating it disables the anti-spray check.
func TestObserveAllRequiresASource(t *testing.T) {
	c, _, _ := newTestCandidates(t)
	if _, err := c.ObserveAll(context.Background(), netip.Addr{}, []Announcement{
		offering("node_any00000000000", "192.168.1.9:7463", "any"),
	}); err == nil {
		t.Error("observed a packet with no source address")
	}
}

// A fingerprint reaches this type canonical whichever way the announcement was
// built, or comparison between rows means nothing.
func TestAFingerprintThatIsNotOneIsNotACandidate(t *testing.T) {
	c, _, _ := newTestCandidates(t)
	for name, fingerprint := range map[string]string{
		"a letter O for a zero": "1223 O3EA 5E96 543A 2DD8 BFEA",
		"prose":                 "trust me",
		"too short":             "1223 03EA",
	} {
		t.Run(name, func(t *testing.T) {
			announcement := offering("node_badfp0000000000", "192.168.1.9:7463", "hostile")
			announcement.Fingerprint = fingerprint
			if _, err := observeOne(c, announcement); err != nil {
				t.Fatal(err)
			}
			if len(c.List()) != 0 {
				t.Errorf("listed with a fingerprint that is not one: %+v", c.List())
			}
		})
	}
	// And one written the other way is the same fingerprint, so it is one row.
	first := offering("node_spelled00000000", "192.168.1.9:7463", "laptop")
	first.Fingerprint = "1223 03EA 5E96 543A 2DD8 BFEA"
	second := first
	second.Fingerprint = "122303ea5e96543a2dd8bfea"
	if _, err := observeOne(c, first); err != nil {
		t.Fatal(err)
	}
	if _, err := observeOne(c, second); err != nil {
		t.Fatal(err)
	}
	listed := c.List()
	if len(listed) != 1 || listed[0].Contested {
		t.Errorf("two spellings of one fingerprint were treated as a conflict: %+v", listed)
	}
}

// A node's own announcements come back on the loopback of the group it sends
// to. It must not offer to pair with itself: that row can only waste the time
// of whoever is reading the list.
func TestANodeIsNotItsOwnCandidate(t *testing.T) {
	c, probe, _ := newTestCandidates(t)
	changed, err := observeOne(c, offering(localTestNode, "192.168.1.9:7463", "this machine"))
	if err != nil || changed {
		t.Fatalf("Observe() = %v, %v", changed, err)
	}
	if listed := c.List(); len(listed) != 0 {
		t.Errorf("this node listed itself: %+v", listed)
	}
	// And it costs nothing to reject: this is the one id guaranteed to be
	// announcing whenever the list is being filled.
	if probe.reads != 0 {
		t.Errorf("rejecting our own announcement cost %d trust reads", probe.reads)
	}
}
