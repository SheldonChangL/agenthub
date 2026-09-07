package discovery

import (
	"context"
	"net/netip"
	"testing"
)

// The source address is the one field in a packet the sender did not choose,
// which is why the candidate layer compares a claimed address against it. If
// the wrong address reached the handler — or the zero value — that check would
// be comparing an announcement to nothing, and a forger could claim any
// address it liked. The read loop is one line away from doing exactly that, so
// the source has to be pinned rather than assumed.
func TestThePacketsSourceAddressReachesEveryHandler(t *testing.T) {
	from := netip.MustParseAddrPort("192.168.4.7:5353")
	packet, err := buildAnnouncement("node_sourcecheck0000", "agenthub-test", 7463,
		[]netip.Addr{netip.MustParseAddr("192.168.4.7")}, Offer{})
	if err != nil {
		t.Fatal(err)
	}

	var seen []netip.Addr
	record := func(_ context.Context, source netip.Addr, announcements []Announcement) {
		if len(announcements) == 0 {
			t.Error("a handler was called with nothing to handle")
		}
		seen = append(seen, source)
	}
	dispatch(context.Background(), from, packet, []PacketHandler{record, record})

	// Both handlers, because a packet is two things at once and only one of
	// them is checked against the source.
	if len(seen) != 2 {
		t.Fatalf("handlers called %d times, want 2", len(seen))
	}
	for i, source := range seen {
		if source != from.Addr() {
			t.Errorf("handler %d saw source %v, want %v", i, source, from.Addr())
		}
	}

	// And a packet with nothing in it reaches nobody, so a handler never has to
	// decide what an empty call means.
	seen = nil
	dispatch(context.Background(), from, []byte("not a dns message"), []PacketHandler{record})
	if len(seen) != 0 {
		t.Errorf("a packet with no announcements still called %d handler(s)", len(seen))
	}
}
