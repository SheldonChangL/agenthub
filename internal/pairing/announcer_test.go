package pairing

import (
	"context"
	"errors"
	"net/netip"
	"strings"
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
	a := NewAnnouncer(mode, "224.0.0.251:5353", "node_local0000000000", "agenthub-test",
		Endpoint{
			Port:      7463,
			Addresses: func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("192.168.1.9")} },
		},
		discovery.Offer{DisplayName: "laptop", Platform: "linux/amd64",
			Fingerprint: "1223 03EA 5E96 543A 2DD8 BFEA"})
	a.announce = sink.record
	// The test's address is chosen, not one this machine holds, so the live
	// interface check would refuse it for a reason that is true of the test
	// machine and irrelevant to what is under test.
	a.canAnnounceFrom = func(netip.Addr) error { return nil }
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

// Opening the window has to put a packet on the wire now, not at the next tick.
// The interval is twenty seconds and the shortest window this node allows is
// thirty, so waiting would spend most of a short window silent while the owner
// watched a countdown at the other machine.
func TestWakeAnnouncesWithoutWaitingForTheTick(t *testing.T) {
	a, mode, sink, _ := newTestAnnouncer(t)
	// An interval far longer than this test: nothing here can be explained by a
	// tick having arrived.
	a.interval = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)

	// The loop announces once at the top, before its first tick, so wait for
	// the closed-window pass to be over before opening.
	time.Sleep(20 * time.Millisecond)
	if sent := sink.count(); sent != 0 {
		t.Fatalf("%d announcements before the window opened", sent)
	}
	if _, err := mode.Open(time.Minute); err != nil {
		t.Fatal(err)
	}
	a.Wake()

	deadline := time.Now().Add(2 * time.Second)
	for sink.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if sent := sink.count(); sent == 0 {
		t.Fatal("opening the window announced nothing until the next tick, an interval away")
	}
	// Waking twice before the loop reads the first is one announcement, not
	// two: a caller that presses the button repeatedly must not amplify.
	a.Wake()
	a.Wake()
	time.Sleep(20 * time.Millisecond)
	if sent := sink.count(); sent > 3 {
		t.Errorf("%d announcements after two extra wakes; a wake is not a queue", sent)
	}
}

// "Open" and "announcing" are different facts, and the gap between them is
// where the one failure an owner cannot see lives: a node whose peer listener
// is on loopback opens a window, announces nothing, and looks fine from here
// while the other machine waits for a candidate that never arrives.
func TestStatusSaysWhetherAnythingIsActuallyBeingAnnounced(t *testing.T) {
	a, mode, _, _ := newTestAnnouncer(t)
	if status := a.Status(); status.Addresses != 1 {
		t.Errorf("announceableAddresses = %d on a node with one address", status.Addresses)
	}
	if reason := a.Unannounceable(); reason != "" {
		t.Errorf("a node with an address says it cannot announce: %q", reason)
	}
	if status := a.Status(); !status.LastAttempt.IsZero() || !status.LastSuccess.IsZero() {
		t.Errorf("a loop that has not run reports having tried: %+v", status)
	}

	// A node with nothing to announce says so, and says why.
	a.addresses = func() []netip.Addr { return nil }
	if a.Unannounceable() == "" {
		t.Error("a node with no address gave no reason, which the API reads as permission")
	}
	if status := a.Status(); status.Addresses != 0 {
		t.Errorf("announceableAddresses = %d with no address", status.Addresses)
	}
	if _, err := mode.Open(time.Minute); err != nil {
		t.Fatal(err)
	}
	a.announceIfOpen(context.Background())
	status := a.Status()
	if status.LastAttempt.IsZero() {
		t.Error("an attempt that announced nothing was not recorded as an attempt")
	}
	if !status.LastSuccess.IsZero() {
		t.Error("an attempt that announced nothing was recorded as a success")
	}
	if status.LastError == "" {
		t.Error("nothing is being announced and the status gives no reason")
	}

	// Then the address comes back. The success is recorded and the reason clears.
	a.addresses = func() []netip.Addr { return []netip.Addr{netip.MustParseAddr("192.168.1.9")} }
	a.announceIfOpen(context.Background())
	if status := a.Status(); status.LastSuccess.IsZero() || status.LastError != "" {
		t.Errorf("a successful announcement left the status at %+v", status)
	}

	// And a failure afterwards keeps the last success, so an owner can see that
	// this was working until something changed.
	succeeded := a.Status().LastSuccess
	a.announce = func(context.Context, string, string, string, int, []netip.Addr, discovery.Offer) error {
		return errTestAnnounce
	}
	a.announceIfOpen(context.Background())
	status = a.Status()
	if !status.LastSuccess.Equal(succeeded) {
		t.Errorf("lastAnnouncedAt moved from %v to %v on a failure", succeeded, status.LastSuccess)
	}
	if status.LastError != errTestAnnounce.Error() {
		t.Errorf("lastError = %q, want the failure from the send", status.LastError)
	}
}

var errTestAnnounce = errors.New("the network is down")

// A tick and a cancellation ready at the same moment must not send one more
// packet: the process was told to stop advertising, and select picks at random
// between two ready cases.
func TestACancelledLoopDoesNotAnnounceOnceMore(t *testing.T) {
	a, mode, sink, _ := newTestAnnouncer(t)
	if _, err := mode.Open(time.Minute); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Both the wake and the tick are ready before Run is entered.
	a.Wake()
	a.interval = time.Nanosecond
	a.Run(ctx)
	if sent := sink.count(); sent != 0 {
		t.Errorf("%d announcements from a loop that was cancelled before it started", sent)
	}
}

// The interval is measured from the announcement, not from the ticker's own
// schedule. A wake landing just before a pending tick would otherwise send two
// packets moments apart, which is what the interval exists to prevent — seen on
// a live pair as seven seconds between them.
//
// Timing-based, so the margins are wide: the wake lands at roughly nine tenths
// of an interval, and the check covers the third of an interval after it, in
// which the un-reset ticker would certainly have fired.
func TestTheIntervalIsMeasuredFromTheAnnouncementNotTheTick(t *testing.T) {
	a, mode, sink, _ := newTestAnnouncer(t)
	const interval = 600 * time.Millisecond
	a.interval = interval
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go a.Run(ctx)

	// Let the loop settle with the window closed, so a tick is imminent and
	// nothing has been announced yet.
	time.Sleep(interval * 9 / 10)
	if sent := sink.count(); sent != 0 {
		t.Fatalf("%d announcements before the window opened", sent)
	}
	if _, err := mode.Open(time.Minute); err != nil {
		t.Fatal(err)
	}
	a.Wake()

	// The wake's own announcement.
	deadline := time.Now().Add(2 * time.Second)
	for sink.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if sink.count() == 0 {
		t.Fatal("the wake announced nothing")
	}
	// The tick that was pending when the wake arrived must have been pushed
	// out, not merely delayed by a few milliseconds.
	time.Sleep(interval / 3)
	if sent := sink.count(); sent != 1 {
		t.Errorf("%d announcements within a third of an interval after the wake, want 1; "+
			"the pending tick was not pushed out", sent)
	}
}

// The API asks this one question before opening a window, so an empty answer is
// read as permission. It must never be empty for a node that cannot announce —
// including the case the type does not enforce: no address and no reason
// recorded at startup.
func TestNoReasonIsNeverMistakenForPermission(t *testing.T) {
	a, _, _, _ := newTestAnnouncer(t)
	// An endpoint with no address and nothing said about why. PeerEndpoint does
	// not produce this, but the Announcer is what the API asks, and it must not
	// depend on a promise made elsewhere.
	a.addresses = func() []netip.Addr { return nil }
	a.unannounceable = ""
	if a.Unannounceable() == "" {
		t.Error("a node with no address and no recorded reason answered as if it could announce")
	}
	if status := a.Status(); status.LastError == "" {
		t.Error("the status gives no reason either, so nothing would explain the silence")
	}

	// The recorded reason is preferred, because it names the configuration.
	a.unannounceable = "the peer listener is on loopback"
	if got := a.Unannounceable(); got != "the peer listener is on loopback" {
		t.Errorf("Unannounceable() = %q, want the recorded reason", got)
	}
	if got := a.Status().LastError; got != "the peer listener is on loopback" {
		t.Errorf("status LastError = %q, want the recorded reason", got)
	}
}

// An address can outlive the interface's ability to carry a multicast packet —
// a point-to-point or WireGuard interface never had it. Answered when the
// window is asked for, not from what was true at startup, or every announcement
// fails while the window says open.
func TestAnAddressThatCannotSendMulticastIsRefusedAtTheWindow(t *testing.T) {
	a, _, _, _ := newTestAnnouncer(t)
	a.canAnnounceFrom = func(netip.Addr) error {
		return errors.New("no interface on this machine holds 10.8.0.2 and can send multicast")
	}
	reason := a.Unannounceable()
	if reason == "" {
		t.Fatal("an address nothing can send from was treated as announceable")
	}
	if !strings.Contains(reason, "multicast") {
		t.Errorf("Unannounceable() = %q, want the machine's own reason", reason)
	}
	// And the recorded startup reason does not mask it: the address is there,
	// so the configuration was fine and something changed since.
	a.unannounceable = "the peer listener is on loopback"
	if got := a.Unannounceable(); got == "the peer listener is on loopback" {
		t.Error("a live failure was reported as the startup configuration")
	}
}
