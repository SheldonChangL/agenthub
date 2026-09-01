package discovery

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"agenthub.local/agenthub/internal/nodeconfig"
)

// fakeResolver records what discovery tried to write, so a test can assert on
// the attempt rather than only on the outcome.
type fakeResolver struct {
	trusted  []string
	stored   map[string]string
	writes   int
	failWith error
}

func newResolver(trusted ...string) *fakeResolver {
	return &fakeResolver{trusted: trusted, stored: map[string]string{}}
}

func (r *fakeResolver) TrustedNodeIDs(context.Context) ([]string, error) { return r.trusted, nil }

func (r *fakeResolver) SetNodeAddress(_ context.Context, nodeID, address string) error {
	r.writes++
	if r.failWith != nil {
		return r.failWith
	}
	r.stored[nodeID] = address
	return nil
}

func anyAddress(string) error { return nil }

const (
	pairedNode   = "node_paired000000000"
	unpairedNode = "node_stranger0000000"
)

// TestDiscoveryCannotCreateTrust is the property the whole package rests on.
//
// mDNS carries no authentication: anything on the network can claim any node id
// at any address. What makes that survivable is that discovery may only fill in
// an address for a node the owner already paired with. If it could add nodes,
// whatever shouts loudest on the network would decide who this node believes.
func TestDiscoveryCannotCreateTrust(t *testing.T) {
	resolver := newResolver(pairedNode)
	browser := NewBrowser(resolver, anyAddress)

	applied, err := browser.Apply(context.Background(), Announcement{
		NodeID: unpairedNode, Address: "192.0.2.10:7463",
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if applied {
		t.Fatal("an unpaired node's announcement was applied")
	}
	if resolver.writes != 0 {
		t.Fatalf("the registry was written to %d times for an unpaired node", resolver.writes)
	}
}

func TestAPairedNodesAddressIsRecorded(t *testing.T) {
	resolver := newResolver(pairedNode)
	browser := NewBrowser(resolver, anyAddress)

	applied, err := browser.Apply(context.Background(), Announcement{
		NodeID: pairedNode, Address: "192.0.2.10:7463",
	})
	if err != nil || !applied {
		t.Fatalf("Apply() = %v, %v", applied, err)
	}
	if got := resolver.stored[pairedNode]; got != "192.0.2.10:7463" {
		t.Fatalf("stored address = %q", got)
	}
}

// TestARepeatedAnnouncementIsNotRewritten keeps a peer announcing every few
// seconds from writing to the database every few seconds.
func TestARepeatedAnnouncementIsNotRewritten(t *testing.T) {
	resolver := newResolver(pairedNode)
	browser := NewBrowser(resolver, anyAddress)
	announcement := Announcement{NodeID: pairedNode, Address: "192.0.2.10:7463"}

	for range 5 {
		if _, err := browser.Apply(context.Background(), announcement); err != nil {
			t.Fatal(err)
		}
	}
	if resolver.writes != 1 {
		t.Fatalf("writes = %d; an unchanged address must be written once", resolver.writes)
	}

	// A genuinely new address must still get through.
	if _, err := browser.Apply(context.Background(), Announcement{
		NodeID: pairedNode, Address: "192.0.2.11:7463",
	}); err != nil {
		t.Fatal(err)
	}
	if resolver.writes != 2 {
		t.Fatalf("writes = %d; a changed address must be recorded", resolver.writes)
	}
}

// TestAnAddressTheTransportWouldRefuseIsNotStored keeps discovery and delivery
// applying the same rule, so a located peer is one that can actually receive.
func TestAnAddressTheTransportWouldRefuseIsNotStored(t *testing.T) {
	resolver := newResolver(pairedNode)
	browser := NewBrowser(resolver, nodeconfig.ValidateLoopback)

	applied, err := browser.Apply(context.Background(), Announcement{
		NodeID: pairedNode, Address: "192.0.2.10:7463",
	})
	if err != nil {
		t.Fatal(err)
	}
	if applied || resolver.writes != 0 {
		t.Fatalf("a routable address was stored while the policy refuses it (applied=%v writes=%d)",
			applied, resolver.writes)
	}
}

func TestEmptyAnnouncementsAreIgnored(t *testing.T) {
	resolver := newResolver(pairedNode)
	browser := NewBrowser(resolver, anyAddress)
	for _, announcement := range []Announcement{
		{NodeID: "", Address: "192.0.2.10:7463"},
		{NodeID: pairedNode, Address: ""},
		{},
	} {
		if applied, err := browser.Apply(context.Background(), announcement); err != nil || applied {
			t.Fatalf("Apply(%#v) = %v, %v", announcement, applied, err)
		}
	}
	if resolver.writes != 0 {
		t.Fatalf("writes = %d", resolver.writes)
	}
}

func TestAStoreFailureIsReported(t *testing.T) {
	resolver := newResolver(pairedNode)
	resolver.failWith = errors.New("database is closed")
	browser := NewBrowser(resolver, anyAddress)

	if _, err := browser.Apply(context.Background(), Announcement{
		NodeID: pairedNode, Address: "192.0.2.10:7463",
	}); err == nil {
		t.Fatal("a failed write was reported as success")
	}
}

// TestAnnouncementsSurviveTheWire builds a real packet and parses it back,
// so the two halves are checked against each other rather than against a
// hand-written fixture that could drift from both.
func TestAnnouncementsSurviveTheWire(t *testing.T) {
	packet, err := buildAnnouncement(pairedNode, "agenthub-test", 7463, []netip.Addr{
		netip.MustParseAddr("192.0.2.10"),
		netip.MustParseAddr("2001:db8::1"),
	})
	if err != nil {
		t.Fatalf("buildAnnouncement() error = %v", err)
	}

	announcements := ParseAnnouncements(packet)
	if len(announcements) != 2 {
		t.Fatalf("announcements = %#v; want one per address", announcements)
	}
	addresses := map[string]bool{}
	for _, announcement := range announcements {
		if announcement.NodeID != pairedNode {
			t.Errorf("node id = %q; want the announced id", announcement.NodeID)
		}
		addresses[announcement.Address] = true
	}
	for _, want := range []string{"192.0.2.10:7463", "[2001:db8::1]:7463"} {
		if !addresses[want] {
			t.Errorf("missing %q in %v", want, addresses)
		}
	}
}

// TestHostilePacketsProduceNothing feeds the parser the shapes an attacker
// controls. None may panic, and none may yield an announcement.
func TestHostilePacketsProduceNothing(t *testing.T) {
	valid, err := buildAnnouncement(pairedNode, "agenthub-test", 7463,
		[]netip.Addr{netip.MustParseAddr("192.0.2.10")})
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string][]byte{
		"empty":                        {},
		"one byte":                     {0x00},
		"header only":                  valid[:12],
		"truncated mid-record":         valid[:len(valid)-5],
		"truncated by one byte":        valid[:len(valid)-1],
		"random bytes":                 []byte("this is not a DNS packet at all, not even close"),
		"all zeroes":                   make([]byte, 512),
		"all ones":                     bytesRepeat(0xff, 512),
		"header claiming many answers": append(append([]byte{}, 0x00, 0x00, 0x84, 0x00, 0x00, 0x00, 0xff, 0xff, 0x00, 0x00, 0x00, 0x00), make([]byte, 4)...),
	}
	for name, packet := range cases {
		t.Run(name, func(t *testing.T) {
			// The assertion is that this returns rather than panics or hangs.
			got := ParseAnnouncements(packet)
			for _, announcement := range got {
				if announcement.NodeID != "" && announcement.Address != "" {
					t.Fatalf("a hostile packet produced a usable announcement: %#v", announcement)
				}
			}
		})
	}
}

// TestAPacketWithoutANodeIDYieldsNothing covers a well-formed service record
// that simply does not identify itself: another protocol on the same group.
func TestAPacketWithoutANodeIDYieldsNothing(t *testing.T) {
	packet, err := buildAnnouncement("", "agenthub-test", 7463,
		[]netip.Addr{netip.MustParseAddr("192.0.2.10")})
	if err != nil {
		t.Fatal(err)
	}
	if got := ParseAnnouncements(packet); len(got) != 0 {
		t.Fatalf("announcements = %#v; a record with no node id must yield nothing", got)
	}
}

func TestBuildAnnouncementRefusesAnImpossiblePort(t *testing.T) {
	for _, port := range []int{0, -1, 65536, 1 << 20} {
		if _, err := buildAnnouncement(pairedNode, "agenthub-test", port, nil); err == nil {
			t.Errorf("buildAnnouncement(port=%d) succeeded", port)
		}
	}
}

func bytesRepeat(value byte, count int) []byte {
	out := make([]byte, count)
	for i := range out {
		out[i] = value
	}
	return out
}
