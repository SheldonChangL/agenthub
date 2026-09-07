package pairing

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/discovery"
)

type recorder struct {
	mu     sync.Mutex
	offers []discovery.Offer
	addrs  [][]netip.Addr
}

func (r *recorder) record(_ context.Context, _, _, _ string, _ int, addresses []netip.Addr, offer discovery.Offer) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.offers = append(r.offers, offer)
	r.addrs = append(r.addrs, addresses)
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.offers)
}

func newTestAnnouncer(t *testing.T) (*Announcer, *Mode, *recorder, *testClock) {
	t.Helper()
	mode, clock := newTestMode()
	sink := &recorder{}
	a := NewAnnouncer(mode, "224.0.0.251:5353", "node_local0000000000", "agenthub-test", 7463,
		func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("192.168.1.9")} },
		discovery.Offer{DisplayName: "laptop", Platform: "linux/amd64",
			Fingerprint: "1223 03EA 5E96 543A 2DD8 BFEA"})
	a.announce = sink.record
	a.interval = time.Millisecond
	return a, mode, sink, clock
}

// The whole point of the window: a node that has not been asked to advertise
// says nothing at all. This is the assertion #61 asks to be provable by packet
// capture, made where it can be asserted deterministically.
func TestNothingIsAnnouncedWhileTheWindowIsClosed(t *testing.T) {
	a, _, sink, _ := newTestAnnouncer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	time.Sleep(30 * time.Millisecond)
	cancel()
	if sent := sink.count(); sent != 0 {
		t.Errorf("%d announcements were sent with the window closed", sent)
	}
}

// And an open one announces, carrying the fingerprint rather than the key.
func TestAnOpenWindowAnnounces(t *testing.T) {
	a, mode, sink, _ := newTestAnnouncer(t)
	if _, err := mode.Open(time.Minute); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	deadline := time.Now().Add(2 * time.Second)
	for sink.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if sink.count() < 2 {
		t.Fatalf("an open window sent %d announcements", sink.count())
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if sink.offers[0].Fingerprint == "" {
		t.Error("announced without a fingerprint, so no listener would treat it as an offer")
	}
	if len(sink.addrs[0]) == 0 {
		t.Error("announced without an address")
	}
}

// The window closing has to stop the announcements, and it has to stop them by
// itself — nothing calls the announcer when a window expires.
func TestAnnouncementsStopWhenTheWindowExpires(t *testing.T) {
	a, mode, sink, clock := newTestAnnouncer(t)
	if _, err := mode.Open(time.Minute); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)

	deadline := time.Now().Add(2 * time.Second)
	for sink.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if sink.count() == 0 {
		t.Fatal("nothing was announced while the window was open")
	}

	// The clock passes the expiry; nothing tells the announcer.
	clock.Advance(2 * time.Minute)
	time.Sleep(20 * time.Millisecond)
	after := sink.count()
	time.Sleep(30 * time.Millisecond)
	cancel()
	if grew := sink.count() - after; grew != 0 {
		t.Errorf("%d announcements were sent after the window expired", grew)
	}
}

// A machine with no address would be listed as a candidate nobody can reach.
func TestNothingIsAnnouncedWithoutAnAddress(t *testing.T) {
	a, mode, sink, _ := newTestAnnouncer(t)
	a.addresses = func() []netip.Addr { return nil }
	if _, err := mode.Open(time.Minute); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)
	time.Sleep(30 * time.Millisecond)
	cancel()
	if sent := sink.count(); sent != 0 {
		t.Errorf("%d announcements were sent with no address to announce", sent)
	}
}

// The interval has to be short enough that a listener does not see a machine
// flicker: a candidate is dropped CandidateTTL after its last packet, and mDNS
// is UDP on a network that drops some.
func TestTheIntervalLeavesRoomForLostPackets(t *testing.T) {
	if AnnounceInterval*3 > discovery.CandidateTTL {
		t.Errorf("announcing every %v against a %v TTL leaves no room for a lost packet",
			AnnounceInterval, discovery.CandidateTTL)
	}
}
