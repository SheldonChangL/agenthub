package discovery

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// localV4Addresses lists this machine's usable v4 addresses, one per interface,
// so the test can announce from each of them.
// These three tests hear this machine's own announcements, which needs the
// multicast loopback the receiving socket has on Unix. On Windows the option is
// receiver-side and net.ListenMulticastUDP clears it, so a node there does not
// hear itself and these would fail for a reason that says nothing about the
// code. Skipped rather than left to fail on the Windows runs in
// docs/verification.md; what they prove is checked on the platforms where a
// node can observe its own packet.
func requireSelfHeardMulticast(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Windows clears multicast loopback on the receiving socket, so a node cannot " +
			"hear its own announcement; this checks the sender by hearing it")
	}
}

func localV4Addresses(t *testing.T) []netip.Addr {
	t.Helper()
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Skipf("cannot list interfaces: %v", err)
	}
	var found []netip.Addr
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 {
			continue
		}
		// A point-to-point interface — a VPN tunnel, usually — has an address
		// but no segment a peer could answer on, and on a machine where one is
		// up it would fail these tests for a reason that is about the machine.
		if iface.Flags&net.FlagPointToPoint != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			prefix, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			parsed, ok := netip.AddrFromSlice(prefix.IP)
			if !ok {
				continue
			}
			parsed = parsed.Unmap()
			if !parsed.Is4() || parsed.IsLoopback() || parsed.IsUnspecified() {
				continue
			}
			found = append(found, parsed)
			break
		}
	}
	return found
}

// An announcement has to be heard, and heard as coming from the address it
// names — because that is the one thing the receiving side checks before it
// will list a candidate at all.
//
// This is the assertion that was missing. Announcements were sent with a nil
// local address, so the kernel chose the source from the route to the multicast
// group; a peer listener on any other interface was announced from the wrong
// address and dropped by every receiver, while the sending node recorded a
// successful announcement and reported no error. On a machine with two
// interfaces this test fails outright against that code.
func TestAnAnnouncementIsHeardFromTheAddressItNames(t *testing.T) {
	requireSelfHeardMulticast(t)
	addresses := localV4Addresses(t)
	if len(addresses) == 0 {
		t.Skip("this machine has no non-loopback IPv4 address on a multicast interface")
	}
	if len(addresses) == 1 {
		// Still worth running: it proves the source is bound rather than left
		// to the route. It cannot prove the cross-interface case.
		t.Logf("only one usable interface (%v); the cross-interface case is not covered here",
			addresses[0])
	}

	// A group of this package's own, so a real mDNS responder on the machine
	// cannot be mistaken for the node under test.
	const group = "224.0.0.251:15355"

	type packet struct {
		source        netip.Addr
		announcements []Announcement
	}
	heard := make(chan packet, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var listening sync.WaitGroup
	listening.Add(1)
	go func() {
		defer listening.Done()
		err := Listen(ctx, group, func(_ context.Context, source netip.Addr, announcements []Announcement) {
			select {
			case heard <- packet{source, announcements}:
			default:
			}
		})
		if err != nil && ctx.Err() == nil {
			t.Errorf("Listen: %v", err)
		}
	}()
	// Give the joins time to take effect before anything is sent.
	time.Sleep(200 * time.Millisecond)

	for _, address := range addresses {
		t.Run(address.String(), func(t *testing.T) {
			// Drain anything left over, so a pass cannot be another address's
			// packet arriving late.
			for len(heard) > 0 {
				<-heard
			}
			err := AnnounceOffering(ctx, group, "node_egress00000000", "agenthub-egress", 7463,
				[]netip.Addr{address}, Offer{DisplayName: "egress", Platform: "test",
					Fingerprint: "1223 03EA 5E96 543A 2DD8 BFEA"})
			if err != nil {
				t.Fatalf("AnnounceOffering from %v: %v", address, err)
			}
			deadline := time.After(3 * time.Second)
			for {
				select {
				case got := <-heard:
					if len(got.announcements) == 0 || got.announcements[0].NodeID != "node_egress00000000" {
						continue
					}
					// The receiving side compares these two and drops the offer
					// when they differ, so this equality is the whole point.
					if got.source != address {
						t.Fatalf("announced %v but the datagram came from %v; every receiver "+
							"would drop this offer", address, got.source)
					}
					announced := got.announcements[0].Address
					if want := netip.AddrPortFrom(address, 7463).String(); announced != want {
						t.Fatalf("the packet carries address %q, want %q", announced, want)
					}
					return
				case <-deadline:
					t.Fatalf("an announcement from %v was never heard, so no machine on that "+
						"interface could discover this node", address)
				}
			}
		})
	}
	cancel()
	listening.Wait()
}

// An address this machine does not have cannot be announced from, and saying so
// beats sending from whatever the route picks: that is what made a wrong
// address look like a successful announcement.
func TestAnnouncingFromAnAddressThisMachineDoesNotHaveIsAnError(t *testing.T) {
	err := AnnounceOffering(context.Background(), "224.0.0.251:15356",
		"node_absent000000000", "agenthub-absent", 7463,
		[]netip.Addr{netip.MustParseAddr("203.0.113.99")}, Offer{
			DisplayName: "absent", Platform: "test",
			Fingerprint: "1223 03EA 5E96 543A 2DD8 BFEA"})
	if err == nil {
		t.Fatal("announcing from an address this machine does not hold reported success")
	}
	if got := err.Error(); !strings.Contains(got, "203.0.113.99") {
		t.Errorf("the error does not name the address: %v", got)
	}
}

// A datagram has to leave by the interface holding the address it advertises,
// which is a different question from what source it carries.
//
// The group is joined on one interface only, so a packet that leaves by any
// other is not heard at all. On macOS this passes whether or not the multicast
// interface option is set — binding the source selects the interface there, as
// measured. It is the platforms where egress follows the route to the group
// that this guards: without the option a packet announcing an address on a
// second interface leaves by the default one, carrying a source that matches
// its own claim, and is accepted by a receiver that cannot reach the address.
func TestAnAnnouncementLeavesByTheInterfaceHoldingItsAddress(t *testing.T) {
	requireSelfHeardMulticast(t)
	addresses := localV4Addresses(t)
	if len(addresses) < 2 {
		t.Skip("needs two interfaces with IPv4 addresses; with one, every route leads there anyway")
	}
	const group = "224.0.0.251:15358"
	target, err := net.ResolveUDPAddr("udp", group)
	if err != nil {
		t.Fatal(err)
	}

	for _, address := range addresses {
		t.Run(address.String(), func(t *testing.T) {
			iface, err := interfaceHolding(address)
			if err != nil {
				t.Fatalf("interfaceHolding(%v): %v", address, err)
			}
			// Joined on this interface alone.
			listener, err := net.ListenMulticastUDP("udp4", iface, target)
			if err != nil {
				t.Skipf("cannot join %s only: %v", iface.Name, err)
			}
			defer func() { _ = listener.Close() }()

			if err := AnnounceOffering(context.Background(), group,
				"node_iface000000000", "agenthub-iface", 7463, []netip.Addr{address},
				Offer{DisplayName: "iface", Platform: "test",
					Fingerprint: "1223 03EA 5E96 543A 2DD8 BFEA"}); err != nil {
				t.Fatalf("AnnounceOffering from %v: %v", address, err)
			}

			_ = listener.SetReadDeadline(time.Now().Add(2 * time.Second))
			buffer := make([]byte, maxPacket)
			read, from, err := listener.ReadFromUDPAddrPort(buffer)
			if err != nil {
				t.Fatalf("an announcement for %v never reached a join on %s, the interface that "+
					"holds it: %v", address, iface.Name, err)
			}
			if from.Addr().Unmap() != address {
				t.Errorf("the datagram came from %v, want %v", from.Addr(), address)
			}
			if got := ParseAnnouncements(buffer[:read]); len(got) == 0 {
				t.Error("the packet heard on the right interface carried no announcement")
			}
		})
	}
}

// An announcement carries exactly one IPv4 address, because that address is
// both what it is sent from and what the receiver checks it against. Anything
// else has to be refused at the call rather than sent the old way: a nil-local
// dial here is the bug this file exists for, reported as a success.
func TestAnAnnouncementRefusesWhatItCannotSendCorrectly(t *testing.T) {
	// Held addresses, so the count is what refuses these and not the interface
	// lookup behind it. With addresses the machine does not have, every case
	// failed for the wrong reason and the count check was unpinned: changing
	// `len(addresses) != 1` to `== 0` passed the whole suite.
	held := localV4Addresses(t)
	if len(held) == 0 {
		t.Skip("this machine has no usable IPv4 address to announce from")
	}
	two := []netip.Addr{held[0], held[0]}
	if len(held) > 1 {
		two = []netip.Addr{held[0], held[1]}
	}

	for name, testCase := range map[string]struct {
		addresses []netip.Addr
		wantInErr string
	}{
		"none": {nil, "exactly one"},
		"two":  {two, "exactly one"},
		// v6 cannot be announced at all: the group and the listener are v4, so
		// the packet would carry an AAAA record and a v4 source and be dropped
		// by every receiver.
		"ipv6": {[]netip.Addr{netip.MustParseAddr("fd00::1")}, "exactly one"},
	} {
		t.Run(name, func(t *testing.T) {
			err := AnnounceOffering(context.Background(), "224.0.0.251:15359",
				"node_refuse000000000", "agenthub-refuse", 7463, testCase.addresses,
				Offer{DisplayName: "refuse", Platform: "test",
					Fingerprint: "1223 03EA 5E96 543A 2DD8 BFEA"})
			if err == nil {
				t.Fatalf("announcing %v reported success", testCase.addresses)
			}
			if !strings.Contains(err.Error(), testCase.wantInErr) {
				t.Errorf("refused %v with %q, want it to mention %q; that means something "+
					"other than the address count did the refusing",
					testCase.addresses, err, testCase.wantInErr)
			}
		})
	}
}

// The membership is re-checked on a ticker, so an interface that appears after
// the process started is joined without anyone restarting anything: an owner
// plugs in a USB Ethernet adapter and runs a cable to the machine they want to
// pair with, which is the case this whole feature is for.
//
// A new interface cannot be conjured in a test, so what is pinned here is the
// property that makes the ticker safe to run every RejoinInterval: a repeat
// refresh reports nothing. The kernel is what guarantees it — a duplicate join
// fails — which is why no record is kept here of what was joined. Without the
// property the log would repeat the same line forever.
func TestRefreshingTheMembershipReportsOnlyWhatIsNew(t *testing.T) {
	target, err := net.ResolveUDPAddr("udp", "224.0.0.251:15360")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.ListenMulticastUDP("udp4", nil, target)
	if err != nil {
		t.Skipf("cannot join the group on this machine: %v", err)
	}
	defer func() { _ = connection.Close() }()

	joins := newMembership(connection, target)
	first := joins.refresh()
	if len(first) == 0 {
		t.Skip("no interface beyond the socket's own join accepted a membership here")
	}
	if second := joins.refresh(); len(second) != 0 {
		t.Errorf("a second refresh reported %v as newly joined", second)
	}
	// A third, to be sure the first answer was not simply the socket warming
	// up: the property has to hold for every tick, not just the second one.
	if third := joins.refresh(); len(third) != 0 {
		t.Errorf("a third refresh reported %v as newly joined", third)
	}
}

// The refresh has to be frequent enough that an interface appearing while a
// peer is announcing is joined while that peer is still announcing. The bound
// is the shortest window a peer can open — an unjoined interface has no
// candidate row, so the row lifetime says nothing about it.
//
// The two numbers live in the pairing package, which imports this one, so they
// are written here rather than referenced. pairing's own tests pin them.
func TestTheRejoinIntervalFitsInsideTheShortestWindow(t *testing.T) {
	const (
		shortestWindow = 30 * time.Second // pairing.MinWindow
		peerAnnounces  = 20 * time.Second // pairing.AnnounceInterval
	)
	// The delay a late-appearing interface costs has to be small next to the
	// interval a peer announces at, or joining it gains nothing. This is a
	// bound on the delay and not a guarantee of catching a window: an interface
	// appearing after a peer's last announcement is joined too late whatever
	// the number, which is why the assertion is about proportion.
	if RejoinInterval > peerAnnounces/2 {
		t.Errorf("re-checking every %v against a peer announcing every %v: an interface can "+
			"miss more than half the announcements in a window", RejoinInterval, peerAnnounces)
	}
	// And it must leave room inside the shortest window for the check plus one
	// announcement, or the shortest windows can never work at all.
	if RejoinInterval+peerAnnounces > shortestWindow {
		t.Errorf("re-checking every %v, against a peer announcing every %v inside a window as "+
			"short as %v, leaves no room at all",
			RejoinInterval, peerAnnounces, shortestWindow)
	}
}

// The membership is re-checked for the listener's whole life, not once at
// startup. Without the loop, an interface plugged in a minute later is never
// joined and a peer announcing onto it is never heard — which looks, from both
// machines, exactly like the peer not announcing.
func TestTheMembershipIsRecheckedUntilTheListenerStops(t *testing.T) {
	target, err := net.ResolveUDPAddr("udp", "224.0.0.251:15361")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.ListenMulticastUDP("udp4", nil, target)
	if err != nil {
		t.Skipf("cannot join the group on this machine: %v", err)
	}
	defer func() { _ = connection.Close() }()

	joins := newMembership(connection, target)
	var checks atomic.Int64
	joins.rejoin = func() []string {
		checks.Add(1)
		return nil
	}
	joins.every = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		joins.keepFresh(ctx, done)
		close(finished)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for checks.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if got := checks.Load(); got < 3 {
		cancel()
		t.Fatalf("the membership was re-checked %d times in two seconds; the loop is not running", got)
	}

	// And it stops with the listener rather than outliving it: a goroutine per
	// Listen call that never returns is a leak.
	cancel()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Error("the refresher did not stop when the context was cancelled")
	}

	// The done channel stops it too, which is what closes it when Listen
	// returns on a read error rather than on cancellation.
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	stopped := make(chan struct{})
	go func() {
		joins.keepFresh(ctx2, done)
		close(stopped)
	}()
	close(done)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Error("the refresher ignored the done channel, so it outlives Listen")
	}
}

// Listen has to subscribe, and the subscription has to keep running — not be
// taken once and forgotten.
//
// This replaces a test that read mdns.go for the call. That one was defeated by
// wrapping the call in `if false`, and would have failed a correct refactor;
// asserting a required call at a particular place is what a seam is for, and
// subscribe is now that seam.
func TestListenSubscribesForTheListenersWholeLife(t *testing.T) {
	original := subscribe
	t.Cleanup(func() { subscribe = original })

	type call struct {
		ctx  context.Context
		done <-chan struct{}
	}
	calls := make(chan call, 4)
	subscribe = func(ctx context.Context, done <-chan struct{}, _ *net.UDPConn, _ *net.UDPAddr) {
		calls <- call{ctx, done}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listening := make(chan error, 1)
	go func() { listening <- Listen(ctx, "224.0.0.251:15370") }()

	var got call
	select {
	case got = <-calls:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("Listen never subscribed, so no interface beyond the default route is ever joined")
	}
	// The context and the done channel are what stop the refresher. Handing it
	// either an already-cancelled context or a closed channel would make it
	// return at once, which is the same as never starting it.
	if got.ctx.Err() != nil {
		t.Errorf("Listen subscribed with an already-cancelled context: %v", got.ctx.Err())
	}
	select {
	case <-got.done:
		t.Error("Listen subscribed with an already-closed done channel")
	default:
	}

	// And the channel closes when Listen returns, so the refresher stops with it.
	cancel()
	select {
	case <-listening:
	case <-time.After(3 * time.Second):
		t.Fatal("Listen did not return after cancellation")
	}
	select {
	case <-got.done:
	case <-time.After(time.Second):
		t.Error("the done channel handed to subscribe was not closed when Listen returned")
	}
}

// A membership on loopback is one only a forgery can use.
//
// Nothing legitimate announces from loopback — the announcing side refuses to —
// so every packet such a membership can receive was written by something local
// that chose what to say. And the check that makes discovery safe, that an
// offer must come from the address it names, is trivially satisfied there.
//
// Measured before this rule existed: a datagram from 127.0.0.1 was received and
// listed with a chosen display name and fingerprint, under the default
// loopback-only policy — the configuration where a forgery from the local
// network is refused. A second user on a shared machine, who cannot read the
// first user's files, could put a row on their candidate list.
//
// A tunnel is excluded for a weaker but real reason: it has an address and no
// segment a peer could answer on, and joining it widens who can inject from one
// network to every VPN the host is attached to.
func TestNothingIsJoinedThatOnlyAForgeryCouldUse(t *testing.T) {
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Skipf("cannot list interfaces: %v", err)
	}

	var sawLoopback, sawTunnel bool
	for i := range interfaces {
		iface := &interfaces[i]
		multicastCapable := iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagMulticast != 0
		switch {
		case iface.Flags&net.FlagLoopback != 0 && multicastCapable:
			sawLoopback = true
			if canCarryAnnouncements(iface) {
				t.Errorf("%s is loopback and would be joined; every packet it can receive "+
					"is a local forgery", iface.Name)
			}
		case iface.Flags&net.FlagPointToPoint != 0 && multicastCapable:
			sawTunnel = true
			if canCarryAnnouncements(iface) {
				t.Errorf("%s is point-to-point and would be joined; it has no segment a peer "+
					"could answer on", iface.Name)
			}
		}
	}
	// Said out loud, because on a machine with neither this test proves nothing
	// and should not read as though it did.
	if !sawLoopback {
		t.Log("no multicast-capable loopback interface here; that half is not exercised")
	}
	if !sawTunnel {
		t.Log("no multicast-capable point-to-point interface here; that half is not exercised")
	}

	// And something is still joined, so the rule is not simply refusing
	// everything — which would pass every assertion above.
	var ordinary int
	for i := range interfaces {
		if canCarryAnnouncements(&interfaces[i]) {
			ordinary++
		}
	}
	if ordinary == 0 {
		t.Error("no interface at all would be joined, so no peer could ever be heard")
	}
}

// An interface holding the address is not enough: it has to be one an
// announcement can leave by, and the reason has to say which of the several
// ways it is not.
//
// The case that made this necessary: utun0 on this machine is
// UP,POINTOPOINT,RUNNING,MULTICAST, so a WireGuard or OpenVPN listener passed a
// check on the flags alone — the exact configuration three places of prose
// claimed was refused.
func TestTheRefusalNamesWhyTheInterfaceCannotAnnounce(t *testing.T) {
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Skipf("cannot list interfaces: %v", err)
	}

	// An address nothing holds is a different answer from an address held by an
	// interface that cannot carry the packet, and an owner acts differently on
	// each.
	_, err = interfaceHolding(netip.MustParseAddr("203.0.113.99"))
	if err == nil {
		t.Fatal("an address this machine does not hold was accepted")
	}
	if !strings.Contains(err.Error(), "no interface") {
		t.Errorf("reason for an unheld address = %q", err)
	}

	for i := range interfaces {
		iface := &interfaces[i]
		if iface.Flags&net.FlagPointToPoint == 0 && iface.Flags&net.FlagLoopback == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			prefix, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			held, ok := netip.AddrFromSlice(prefix.IP)
			if !ok || !held.Unmap().Is4() {
				continue
			}
			_, err := interfaceHolding(held.Unmap())
			if err == nil {
				t.Errorf("%v on %s was accepted for announcing", held, iface.Name)
				continue
			}
			// The interface is named, so the owner knows which one, and the
			// kind of problem is named, so they know what to change.
			if !strings.Contains(err.Error(), iface.Name) {
				t.Errorf("the reason for %v does not name %s: %v", held, iface.Name, err)
			}
			want := "loopback"
			if iface.Flags&net.FlagPointToPoint != 0 {
				want = "point-to-point"
			}
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the reason for %v on %s does not say %q: %v",
					held, iface.Name, want, err)
			}
		}
	}
}

// A join failure that is not one of the expected ones is worth saying, once. A
// Linux host at its twenty-membership limit fails here, and the interface just
// plugged in — the one this whole loop exists for — may be the one refused.
func TestAnUnexpectedJoinFailureIsReportedOnceNotEveryTick(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags)
	})

	target, err := net.ResolveUDPAddr("udp", "224.0.0.251:15371")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.ListenMulticastUDP("udp4", nil, target)
	if err != nil {
		t.Skipf("cannot join the group here: %v", err)
	}
	defer func() { _ = connection.Close() }()

	joins := newMembership(connection, target)
	joins.join = func(*net.Interface, *net.UDPAddr) error {
		return syscall.ENOBUFS
	}
	count := func() int { return strings.Count(logged.String(), "could not listen for announcements") }

	if added := joins.refresh(); len(added) != 0 {
		t.Errorf("a failing join reported %v as joined", added)
	}
	// One line per interface, not one per tick: which interface failed is the
	// useful part, and there is one such line for each.
	first := count()
	if first == 0 {
		t.Fatalf("an unexpected join failure was never reported: %q", logged.String())
	}
	for tick := 0; tick < 4; tick++ {
		if added := joins.refresh(); len(added) != 0 {
			t.Errorf("a failing join reported %v as joined", added)
		}
	}
	if got := count(); got != first {
		t.Errorf("five refreshes logged %d lines where one logged %d; the condition repeats "+
			"every %v forever", got, first, RejoinInterval)
	}

	// The expected ones stay silent: they are what a duplicate join and an
	// interface with no IPv4 stack return, on every tick, forever.
	logged.Reset()
	joins.join = func(*net.Interface, *net.UDPAddr) error { return syscall.EADDRINUSE }
	joins.refresh()
	if logged.Len() != 0 {
		t.Errorf("an expected join failure was logged: %s", logged.String())
	}
}

// Every way an interface can fail to carry an announcement, on flags this test
// chooses rather than flags this machine happens to have.
//
// The tunnel case is why: none of this machine's point-to-point interfaces
// carries an IPv4 address, so a test that walks real interfaces never reached
// that branch — and removing it passed the suite while three places of prose
// said it was the case being caught.
func TestEveryReasonAnInterfaceCannotAnnounce(t *testing.T) {
	address := netip.MustParseAddr("10.8.0.2")
	usable := net.FlagUp | net.FlagMulticast | net.FlagBroadcast

	for name, testCase := range map[string]struct {
		holder    *net.Interface
		wantInErr string
	}{
		"nothing holds it": {nil, "no interface"},
		"down": {
			&net.Interface{Name: "en9", Flags: net.FlagMulticast}, "is down",
		},
		// A tunnel carries MULTICAST, so the flags alone do not exclude it.
		"a tunnel": {
			&net.Interface{Name: "utun3", Flags: usable | net.FlagPointToPoint}, "point-to-point",
		},
		"loopback": {
			&net.Interface{Name: "lo0", Flags: usable | net.FlagLoopback}, "loopback",
		},
		"no multicast": {
			&net.Interface{Name: "wg0", Flags: net.FlagUp}, "multicast",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := announceableFrom(testCase.holder, address)
			if err == nil {
				t.Fatalf("%+v was accepted for announcing %v", testCase.holder, address)
			}
			if !strings.Contains(err.Error(), testCase.wantInErr) {
				t.Errorf("reason = %q, want it to mention %q", err, testCase.wantInErr)
			}
			// An owner needs to know which interface, not just that one of
			// them is wrong.
			if testCase.holder != nil && !strings.Contains(err.Error(), testCase.holder.Name) {
				t.Errorf("reason = %q, want it to name %s", err, testCase.holder.Name)
			}
		})
	}

	// And an ordinary interface is accepted, so the rule is not simply
	// refusing everything.
	if err := announceableFrom(&net.Interface{Name: "en0", Flags: usable}, address); err != nil {
		t.Errorf("an ordinary interface was refused: %v", err)
	}
}

// The two shapes of the flag rule have to agree, always.
//
// They were two functions kept in step by hand — the membership asked a
// boolean, the announcing side asked for a reason — and nothing made them
// match. Two mutations survived on that: dropping the up check and the
// multicast check from the membership's copy, because only the reason-shaped
// one was tested. They are one function now, and this is what would notice if
// they were separated again.
func TestBothAnswersAboutAnInterfaceAgree(t *testing.T) {
	usable := net.FlagUp | net.FlagMulticast | net.FlagBroadcast
	address := netip.MustParseAddr("10.0.0.5")

	// Every combination of the four flags that decide this.
	for _, up := range []net.Flags{0, net.FlagUp} {
		for _, multicast := range []net.Flags{0, net.FlagMulticast} {
			for _, loopback := range []net.Flags{0, net.FlagLoopback} {
				for _, p2p := range []net.Flags{0, net.FlagPointToPoint} {
					iface := &net.Interface{Name: "probe0", Flags: up | multicast | loopback | p2p}
					joinable := canCarryAnnouncements(iface)
					announceable := announceableFrom(iface, address) == nil
					if joinable != announceable {
						t.Errorf("flags %v: joined=%v but announceable=%v; the two answers "+
							"have come apart", iface.Flags, joinable, announceable)
					}
				}
			}
		}
	}
	// And an ordinary interface is accepted by both, so this is not agreement
	// on refusing everything.
	ordinary := &net.Interface{Name: "en0", Flags: usable}
	if !canCarryAnnouncements(ordinary) {
		t.Error("an ordinary interface would not be joined")
	}
	if err := announceableFrom(ordinary, address); err != nil {
		t.Errorf("an ordinary interface cannot announce: %v", err)
	}
}

// subscribeGroup has to join now and keep joining. Reducing its body to nothing
// passed the whole suite before this existed, which is how the multi-interface
// join — the point of the change — came to have no coverage at all.
func TestSubscribingJoinsNowAndKeepsJoining(t *testing.T) {
	target, err := net.ResolveUDPAddr("udp", "224.0.0.251:15392")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.ListenMulticastUDP("udp4", nil, target)
	if err != nil {
		t.Skipf("cannot join the group here: %v", err)
	}
	defer func() { _ = connection.Close() }()

	joins := newMembership(connection, target)
	var refreshes atomic.Int64
	joins.rejoin = func() []string {
		refreshes.Add(1)
		return nil
	}
	joins.every = 10 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	subscribeGroup(ctx, done, joins)

	// The first join is synchronous, so a peer already announcing is heard
	// without waiting for a tick.
	if got := refreshes.Load(); got != 1 {
		t.Errorf("subscribing joined %d times before returning, want 1", got)
	}
	// And it keeps going.
	deadline := time.Now().Add(2 * time.Second)
	for refreshes.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if got := refreshes.Load(); got < 3 {
		t.Errorf("joined %d times in two seconds; the refresher is not running", got)
	}
	cancel()
	close(done)
}

// An address can sit on more than one interface, and one usable holder is
// enough. Taking whichever came last in the enumeration went unnoticed because
// no address on this machine is on two interfaces — so the choice is tested on
// interfaces the test supplies.
func TestAUsableHolderIsChosenWhateverElseHoldsTheAddress(t *testing.T) {
	address := netip.MustParseAddr("10.0.0.5")
	usable := net.FlagUp | net.FlagMulticast | net.FlagBroadcast
	good := &net.Interface{Name: "eth0", Flags: usable}
	alias := &net.Interface{Name: "lo0", Flags: usable | net.FlagLoopback}
	tunnel := &net.Interface{Name: "utun3", Flags: usable | net.FlagPointToPoint}

	// Whatever the order, and whatever else holds it, the usable one wins.
	for name, holders := range map[string][]*net.Interface{
		"good first":           {good, alias},
		"good last":            {alias, good},
		"between two unusable": {tunnel, good, alias},
		"only one, and usable": {good},
	} {
		t.Run(name, func(t *testing.T) {
			chosen, err := chooseHolder(holders, address)
			if err != nil {
				t.Fatalf("chooseHolder(%v) = %v", names(holders), err)
			}
			if chosen != good {
				t.Errorf("chose %s, want eth0", chosen.Name)
			}
		})
	}

	// With no usable holder the reason comes from one of them, and names it —
	// not "nothing holds this", which would be false.
	_, err := chooseHolder([]*net.Interface{alias, tunnel}, address)
	if err == nil {
		t.Fatal("an address held only by a loopback alias and a tunnel was accepted")
	}
	if !strings.Contains(err.Error(), "lo0") && !strings.Contains(err.Error(), "utun3") {
		t.Errorf("the reason names no interface: %v", err)
	}
	if strings.Contains(err.Error(), "no interface on this machine holds") {
		t.Errorf("an address the machine holds was reported as unheld: %v", err)
	}

	// And nothing holding it is still its own answer.
	if _, err := chooseHolder(nil, address); err == nil {
		t.Error("an address nothing holds was accepted")
	} else if !strings.Contains(err.Error(), "no interface") {
		t.Errorf("reason for an unheld address = %v", err)
	}
}

func names(interfaces []*net.Interface) []string {
	out := make([]string, 0, len(interfaces))
	for _, iface := range interfaces {
		out = append(out, iface.Name)
	}
	return out
}

// The log names interfaces an announcement could arrive on, which is not the
// same set as the ones that accepted a join.
//
// Measured on this machine: four interfaces with no address of any kind joined
// successfully, while en0 — the only one with an IPv4 address, and the only one
// a peer could be on — was refused as a duplicate of the socket's own
// membership. A line listing the four read as evidence the join was working.
func TestOnlyInterfacesThatCouldDeliverAreNamed(t *testing.T) {
	target, err := net.ResolveUDPAddr("udp", "224.0.0.251:15393")
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.ListenMulticastUDP("udp4", nil, target)
	if err != nil {
		t.Skipf("cannot join the group here: %v", err)
	}
	defer func() { _ = connection.Close() }()

	joins := newMembership(connection, target)
	// Every join succeeds, so what is left deciding the names is the address.
	joins.join = func(*net.Interface, *net.UDPAddr) error { return nil }
	named := joins.refresh()

	interfaces, err := net.Interfaces()
	if err != nil {
		t.Skipf("cannot list interfaces: %v", err)
	}
	byName := map[string]*net.Interface{}
	var eligible, withAddress int
	for i := range interfaces {
		iface := &interfaces[i]
		byName[iface.Name] = iface
		if !canCarryAnnouncements(iface) {
			continue
		}
		eligible++
		if hasIPv4(iface) {
			withAddress++
		}
	}
	if eligible == 0 {
		t.Skip("no interface here could carry an announcement")
	}
	if eligible == withAddress {
		t.Log("every eligible interface here holds an address, so the filter is not exercised")
	}
	if len(named) != withAddress {
		t.Errorf("named %v (%d) but %d eligible interfaces hold an IPv4 address",
			named, len(named), withAddress)
	}
	for _, name := range named {
		if iface := byName[name]; iface != nil && !hasIPv4(iface) {
			t.Errorf("%s was named though it holds no IPv4 address, so nothing can arrive on it",
				name)
		}
	}
}

// hasIPv4 decides which interfaces the startup line names, and the test that
// checks that line used hasIPv4 to compute its own expectation — so making it
// answer always-true or always-false left the suite green. An oracle has to be
// built from something else.
//
// Here that is the standard library's own IPv4 test, net.IP.To4, over the
// addresses read directly rather than through the function under test.
func TestHasIPv4AgreesWithAnIndependentReading(t *testing.T) {
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Skipf("cannot list interfaces: %v", err)
	}
	var withAddress, without int
	for i := range interfaces {
		iface := &interfaces[i]

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		expected := false
		for _, addr := range addrs {
			prefix, ok := addr.(*net.IPNet)
			if ok && prefix.IP.To4() != nil {
				expected = true
				break
			}
		}
		if got := hasIPv4(iface); got != expected {
			t.Errorf("hasIPv4(%s) = %v, want %v", iface.Name, got, expected)
		}
		if expected {
			withAddress++
		} else {
			without++
		}
	}
	// Both answers have to occur, or an always-true or always-false
	// implementation would satisfy every assertion above.
	if withAddress == 0 {
		t.Skip("no interface here has an IPv4 address, so a true answer is never required")
	}
	if without == 0 {
		t.Skip("every interface here has an IPv4 address, so a false answer is never required")
	}
	t.Logf("%d interfaces with an IPv4 address, %d without", withAddress, without)
}

// fakeQuota is the part of the kernel these tests are about: a socket may hold
// a fixed number of multicast memberships, each keyed on an interface index,
// and a membership on a destroyed interface goes on occupying its slot until
// something leaves the group for it.
//
// Twenty because that is net.ipv4.igmp_max_memberships out of the box, which is
// the number measured against on Linux 6.8 (#143): the twentieth
// destroy-and-rebuild cycle is where JoinGroup starts answering ENOBUFS for
// every interface, and stops answering anything else for the process's life.
// macOS is the same shape with a far higher ceiling — IP_MAX_MEMBERSHIPS is
// 4095 in the SDK's netinet/in.h — so twenty is the hostile case to model.
type fakeQuota struct {
	limit  int
	slots  map[int]bool
	joins  int
	leaves []int
	// retainsOnLeave makes a leave answer EADDRNOTAVAIL while *keeping* the
	// slot, which is the one thing about a vanished interface that is not
	// known: whether xnu frees the inp_moptions entry once ifindex2ifnet[idx]
	// is NULL. Off by default, because the measured behaviour on both platforms
	// is that the call reaches the kernel keyed on the index.
	//
	// It exists because without it this fake defines EADDRNOTAVAIL to mean "the
	// slot is free" — it frees the slot whenever one is held and returns that
	// errno only when one is not — so a kernel that answered EADDRNOTAVAIL and
	// leaked would be indistinguishable here from one that released. That is
	// the shape of omission #142 and #145 were about: the fake left out the
	// thing production might do.
	retainsOnLeave bool
}

func newFakeQuota() *fakeQuota {
	return &fakeQuota{limit: 20, slots: map[int]bool{}}
}

func (q *fakeQuota) join(iface *net.Interface, _ *net.UDPAddr) error {
	q.joins++
	if q.slots[iface.Index] {
		return syscall.EADDRINUSE
	}
	if len(q.slots) >= q.limit {
		return syscall.ENOBUFS
	}
	q.slots[iface.Index] = true
	return nil
}

func (q *fakeQuota) leaveGroup(iface *net.Interface, _ *net.UDPAddr) error {
	q.leaves = append(q.leaves, iface.Index)
	if !q.slots[iface.Index] {
		return syscall.EADDRNOTAVAIL
	}
	if q.retainsOnLeave {
		return syscall.EADDRNOTAVAIL
	}
	delete(q.slots, iface.Index)
	return nil
}

// offline builds a membership with no socket behind it, so the quota, the
// interface table and the clock are all the test's to choose.
func offline(t *testing.T) (*membership, *fakeQuota) {
	t.Helper()
	quota := newFakeQuota()
	joins := &membership{
		group:      &net.UDPAddr{IP: net.IPv4(224, 0, 0, 251), Port: 5353},
		every:      RejoinInterval,
		join:       quota.join,
		leave:      quota.leaveGroup,
		interfaces: func() ([]net.Interface, error) { return nil, nil },
		held:       map[int]*heldMembership{},
	}
	joins.rejoin = joins.refresh
	return joins, quota
}

func usableInterface(name string, index int) net.Interface {
	return net.Interface{
		Index: index,
		Name:  name,
		Flags: net.FlagUp | net.FlagMulticast | net.FlagBroadcast | net.FlagRunning,
	}
}

// The bug this fixes, at the scale that makes it fatal: an interface destroyed
// and re-created leaves its membership behind, and after twenty cycles the
// socket can join nothing at all — not the new interface, not a USB adapter
// plugged in afterwards, nothing — until the process restarts. A node that has
// been up for a week behind a container runtime or a reconnecting VPN is
// exactly the node this happens to, and from the owner's side it looks like
// pairing having quietly stopped working.
//
// Forty cycles, twice the quota, so a fix that merely delays the wall fails
// here too.
func TestAVanishedInterfaceGivesItsMembershipSlotBack(t *testing.T) {
	joins, quota := offline(t)

	index := 4
	for cycle := 1; cycle <= 40; cycle++ {
		// Each rebuild is a new device at a new index, which is what was
		// measured: indexes climbed 4, 6, 8 … and never came back round.
		index += 2
		current := []net.Interface{usableInterface("veth0", index)}
		joins.interfaces = func() ([]net.Interface, error) { return current, nil }

		joins.refresh()

		if !quota.slots[index] {
			t.Fatalf("cycle %d: the interface at index %d was not joined; the socket ran out "+
				"of membership slots after %d cycles and cannot recover", cycle, index, cycle-1)
		}
		// Two, not one: an index is released on the second successive
		// enumeration that omits it, so the interface destroyed on the previous
		// cycle is still held for exactly one more tick. That grace is the
		// price of not acting on a single short netlink dump, and what matters
		// is that it is a constant — the held set does not grow with cycles.
		if len(quota.slots) > 2 {
			t.Fatalf("cycle %d: the socket holds %d memberships where at most the live one and "+
				"one tick's grace are expected: %v", cycle, len(quota.slots), quota.slots)
		}
	}
	if len(quota.leaves) == 0 {
		t.Error("no membership was ever released, yet the quota never filled; the fake is not " +
			"charging for slots and this test proves nothing")
	}
}

// The counterpart, and the reason the test for "gone" is absence from the
// interface table and nothing softer. Measured on #93: an interface that goes
// down or loses every address keeps its device-level group — `ip maddr` and
// /proc/net/igmp go on listing 224.0.0.251 at every step of a link flap and of
// a full DHCP-shaped cycle — and delivery was confirmed on the far side of each
// without a re-join. The rename case below is *not* from those runs, which
// never renamed anything; it is inferred from the index being the key, and is
// here because this code must not release on it either. Releasing a membership
// in any of these cases would trade a working subscription for a slot that is
// not scarce, and cost delivery until the next tick re-joined it.
func TestAMembershipSurvivesAnInterfaceGoingDownOrLosingItsName(t *testing.T) {
	for name, changed := range map[string]net.Interface{
		"goes down":                             {Index: 7, Name: "en5", Flags: net.FlagMulticast},
		"loses its addresses":                   usableInterface("en5", 7),
		"is renamed (inferred, never measured)": usableInterface("enx0023", 7),
		"stops doing multicast": {
			Index: 7, Name: "en5", Flags: net.FlagUp | net.FlagBroadcast,
		},
	} {
		t.Run(name, func(t *testing.T) {
			joins, quota := offline(t)
			current := []net.Interface{usableInterface("en5", 7)}
			joins.interfaces = func() ([]net.Interface, error) { return current, nil }
			joins.refresh()
			if !quota.slots[7] {
				t.Fatal("the interface was not joined to begin with")
			}

			current = []net.Interface{changed}
			joins.refresh()

			if len(quota.leaves) != 0 {
				t.Errorf("the membership on index 7 was released when the interface %s; it is "+
					"still there and still delivering, and the socket is now deaf on it "+
					"until the next tick", name)
			}
			if !quota.slots[7] {
				t.Errorf("the socket no longer holds the membership on index 7 after it %s", name)
			}
		})
	}
}

// A membership this code did not take is not this code's to release. The join
// on the default interface belongs to ListenMulticastUDP — it is the one
// discovery ran on before any of this existed, and refresh sees it as an
// EADDRINUSE every tick. Recording it would mean that the day that interface is
// unplugged, the socket leaves a group it did not join here, which is a way to
// lose delivery rather than to gain a slot.
func TestAMembershipTakenElsewhereIsNeverReleasedHere(t *testing.T) {
	joins, quota := offline(t)
	// Already held, exactly as the socket's own join leaves it.
	quota.slots[3] = true
	current := []net.Interface{usableInterface("en0", 3)}
	joins.interfaces = func() ([]net.Interface, error) { return current, nil }
	joins.refresh()

	current = nil
	joins.refresh()

	if len(quota.leaves) != 0 {
		t.Errorf("left the group on %v, but that membership was taken by the socket itself "+
			"and refused this code's join as a duplicate", quota.leaves)
	}
}

// Absence has to be positive evidence from a list that was actually read. When
// the enumeration fails there is no list, and every membership would look gone
// — which would turn one transient failure into the socket dropping every
// interface it has.
func TestAFailedEnumerationReleasesNothing(t *testing.T) {
	log.SetOutput(&bytes.Buffer{})
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	joins, quota := offline(t)
	current := []net.Interface{usableInterface("en5", 7), usableInterface("en6", 9)}
	joins.interfaces = func() ([]net.Interface, error) { return current, nil }
	joins.refresh()
	if len(quota.slots) != 2 {
		t.Fatalf("expected both interfaces joined, got %v", quota.slots)
	}

	joins.interfaces = func() ([]net.Interface, error) { return nil, syscall.EMFILE }
	joins.refresh()

	if len(quota.leaves) != 0 {
		t.Errorf("a failed interface enumeration released %v; nothing was observed to be gone",
			quota.leaves)
	}
	if len(quota.slots) != 2 {
		t.Errorf("the socket holds %d memberships after a failed enumeration, not 2", len(quota.slots))
	}
}

// The release is attempted once, and the reason is not that a second attempt is
// known to fail. Both platforms leave by index — x/net registers ssoLeaveGroup
// as MCAST_LEAVE_GROUP on darwin exactly as on linux, and the measurement here
// is that LeaveGroup on a vanished index reaches the kernel and answers
// EADDRNOTAVAIL, the same answer as leaving a group twice. The reason is that
// the call is a pure function of an index that is gone and will still be gone
// next tick, so a second attempt can answer nothing the first did not. Retrying
// each tick would be a syscall every ten seconds per interface the host has
// ever destroyed, and a map that grows with them.
func TestAReleaseIsAttemptedOncePerVanishedInterface(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags)
	})

	joins, quota := offline(t)
	current := []net.Interface{usableInterface("veth0", 12)}
	joins.interfaces = func() ([]net.Interface, error) { return current, nil }
	joins.refresh()

	// EADDRNOTAVAIL, which is what a vanished index answers on darwin and what
	// leaving an already-left group answers on both. The slot is released
	// underneath it, which is the case this test is about; the case where it is
	// not is TestALeaveThatKeepsTheSlotIsStillOnlyAttemptedOnce.
	joins.leave = func(iface *net.Interface, group *net.UDPAddr) error {
		quota.leaveGroup(iface, group)
		return syscall.EADDRNOTAVAIL
	}
	current = nil
	for tick := 0; tick < 5; tick++ {
		joins.refresh()
	}

	if got := len(quota.leaves); got != 1 {
		t.Errorf("five refreshes attempted %d releases for one vanished interface", got)
	}
	joins.mu.Lock()
	remembered := len(joins.held)
	joins.mu.Unlock()
	if remembered != 0 {
		t.Errorf("the vanished interface is still remembered %d times over; the record grows "+
			"with every interface this host destroys", remembered)
	}
	if strings.Contains(logged.String(), "could not release") {
		t.Errorf("EADDRNOTAVAIL was reported as a problem, but it means the slot was already "+
			"not held, which is the outcome wanted: %s", logged.String())
	}
}

// A release that fails for a reason nothing here understands is worth one line,
// for the same reason an unexpected join failure is: the quota is that much
// smaller for the rest of the process's life, and the next interface plugged in
// is the one that pays.
func TestAnUnexplainedReleaseFailureIsReportedOnce(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags)
	})

	joins, _ := offline(t)
	current := []net.Interface{usableInterface("veth0", 12), usableInterface("veth1", 14)}
	joins.interfaces = func() ([]net.Interface, error) { return current, nil }
	joins.refresh()

	joins.leave = func(*net.Interface, *net.UDPAddr) error { return syscall.EPERM }
	current = nil
	for tick := 0; tick < 3; tick++ {
		joins.refresh()
	}

	// One line for the family, not one per interface. A release is attempted
	// once per vanished interface, so a per-name key would suppress nothing and
	// would leave a permanent entry behind for every name a host churning
	// veths has ever destroyed. The line still names an interface.
	if got := strings.Count(logged.String(), "could not release"); got != 1 {
		t.Errorf("two interfaces failing to release over three ticks logged %d lines, want 1: %q",
			got, logged.String())
	}
	if !strings.Contains(logged.String(), "veth") {
		t.Errorf("the report does not name the interface whose slot was lost: %q", logged.String())
	}
	joins.mu.Lock()
	keys := len(joins.reported)
	joins.mu.Unlock()
	if keys != 1 {
		t.Errorf("the release path left %d entries in the reported set for two interfaces; it "+
			"grows with every name this host destroys", keys)
	}
}

// Releasing happens before joining, not after, and the difference is only
// visible when a whole set of interfaces goes at once — a container runtime
// tearing its network down and building a new one, which is the shape of host
// this bug was found on. With the quota full, a release that ran after the
// joins would let the replacement interface fail with ENOBUFS and pick it up
// only on the next tick: ten seconds of a peer announcing into a socket that
// cannot hear it, for no reason except the order of two loops.
func TestASlotFreedOnThisTickIsUsableOnThisTick(t *testing.T) {
	joins, quota := offline(t)

	full := make([]net.Interface, 0, quota.limit)
	for i := 0; i < quota.limit; i++ {
		full = append(full, usableInterface(fmt.Sprintf("veth%d", i), 100+i))
	}
	current := full
	joins.interfaces = func() ([]net.Interface, error) { return current, nil }
	joins.refresh()
	if len(quota.slots) != quota.limit {
		t.Fatalf("the quota is not full to begin with: %d of %d slots", len(quota.slots), quota.limit)
	}

	// The whole set is destroyed and one interface takes their place. The first
	// tick only records the absence — one short enumeration is not evidence —
	// so the tick under test is the one that releases.
	current = []net.Interface{usableInterface("br0", 200)}
	joins.refresh()
	quota.leaves = nil

	joins.refresh()

	if len(quota.leaves) == 0 {
		t.Fatal("the second tick released nothing, so this proves nothing about the order")
	}
	if !quota.slots[200] {
		t.Error("the replacement interface was not joined on the tick its predecessors' slots " +
			"were released; the slots were released only after the join had already been refused")
	}
}

// A successful enumeration is not a complete one, and this is the case that
// distinguishes them. net.Interfaces answers from syscall.NetlinkRIB, which
// dumps RTM_GETLINK and never looks at NLM_F_DUMP_INTR — the flag the kernel
// sets when the link table changes underneath the dump. A dump interrupted that
// way can come back short, with no error, and the host that does the
// interrupting is the container-runtime host this whole change is for.
//
// So one absence must buy nothing. The interface here never went anywhere; it
// is simply missing from one dump and back in the next, and releasing on the
// strength of the first would leave the socket deaf on a live interface until
// the following tick re-joined it.
func TestOneShortEnumerationDoesNotReleaseALiveInterface(t *testing.T) {
	joins, quota := offline(t)
	live := usableInterface("eth0", 11)
	current := []net.Interface{live}
	joins.interfaces = func() ([]net.Interface, error) { return current, nil }
	joins.refresh()
	if !quota.slots[11] {
		t.Fatal("the interface was not joined to begin with")
	}

	// One dump comes back short. The interface is still there.
	current = nil
	joins.refresh()

	if len(quota.leaves) != 0 {
		t.Fatalf("a single short enumeration released %v; one interrupted netlink dump is not "+
			"evidence that an interface is gone, and the socket is now deaf on it", quota.leaves)
	}
	if !quota.slots[11] {
		t.Fatal("the membership on the live interface was dropped after one short enumeration")
	}

	// It is listed again, which has to clear the count outright — otherwise two
	// unrelated short dumps, however far apart, would add up to a release.
	current = []net.Interface{live}
	joins.refresh()
	current = nil
	joins.refresh()
	if len(quota.leaves) != 0 {
		t.Fatalf("released %v after two *non-consecutive* absences; being listed again did not "+
			"reset the count", quota.leaves)
	}

	// Two in a row is the evidence, and now it must act — a leak that is never
	// collected is #143 again.
	current = nil
	joins.refresh()
	if len(quota.leaves) != 1 {
		t.Fatalf("two consecutive successful enumerations omitted index 11 and %d releases were "+
			"attempted; the slot is leaked for the life of the process", len(quota.leaves))
	}
	if quota.slots[11] {
		t.Error("the slot was never given back")
	}
}

// The one thing about a vanished interface that is not known: whether xnu frees
// the inp_moptions entry once ifindex2ifnet[idx] is NULL. If it does not, macOS
// answers EADDRNOTAVAIL and keeps the slot.
//
// This does not resolve that — it needs a synthetic interface and root, and has
// not been done. What it pins is that the fake can now *express* it, so the
// question is visible rather than defined out of existence: with the leak on,
// the slots stay held, which is exactly what the default fake cannot show,
// because there EADDRNOTAVAIL is only ever returned when the slot is already
// free. It also pins that the code's own behaviour does not change — still one
// attempt, still no entry kept — because a retry cannot free a slot the kernel
// has decided not to free.
//
// The residual leak is accepted rather than chased: macOS caps a socket at
// IP_MAX_MEMBERSHIPS = 4095 against Linux's default of twenty, so the churn
// that makes this fatal on Linux is two orders of magnitude from mattering.
func TestALeaveThatKeepsTheSlotIsStillOnlyAttemptedOnce(t *testing.T) {
	log.SetOutput(&bytes.Buffer{})
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	joins, quota := offline(t)
	quota.retainsOnLeave = true
	current := []net.Interface{usableInterface("veth0", 12)}
	joins.interfaces = func() ([]net.Interface, error) { return current, nil }
	joins.refresh()
	if !quota.slots[12] {
		t.Fatal("the interface was not joined to begin with")
	}

	current = nil
	for tick := 0; tick < 5; tick++ {
		joins.refresh()
	}

	if !quota.slots[12] {
		t.Fatal("the fake released the slot even with retainsOnLeave set, so it still cannot " +
			"tell a kernel that leaks from one that does not, and this test proves nothing")
	}
	if got := len(quota.leaves); got != 1 {
		t.Errorf("five refreshes attempted %d releases against a kernel that keeps the slot; a "+
			"retry cannot free what the kernel has decided not to free", got)
	}
	joins.mu.Lock()
	remembered := len(joins.held)
	joins.mu.Unlock()
	if remembered != 0 {
		t.Errorf("the vanished interface is still remembered %d times over", remembered)
	}
}

// The name in the record is a log label, and it goes stale. record runs only
// when a join returns nil, and an interface already joined answers EADDRINUSE,
// so a rename between two ticks is never re-recorded by that path. The
// membership is unharmed either way — the kernel matches on the index — but a
// message about a lost slot that names an interface by a name it shed days ago
// sends the reader looking for the wrong thing.
func TestARenamedInterfaceIsReportedByItsCurrentName(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags)
	})

	joins, _ := offline(t)
	current := []net.Interface{usableInterface("eth0", 13)}
	joins.interfaces = func() ([]net.Interface, error) { return current, nil }
	joins.refresh()

	// udev renames it. Same index, so the join is refused as a duplicate.
	current = []net.Interface{usableInterface("enp3s0", 13)}
	joins.refresh()

	joins.leave = func(*net.Interface, *net.UDPAddr) error { return syscall.EPERM }
	current = nil
	joins.refresh()
	joins.refresh()

	if !strings.Contains(logged.String(), "enp3s0") {
		t.Errorf("the report names the interface by a name it no longer had: %q", logged.String())
	}
}

// A membership this code did not take stays untouched by the relabelling too.
// EADDRINUSE is how the socket's own join on the default interface shows up on
// every tick, and if seeing it were enough to put an index in the record, the
// day that interface went away this code would leave a group it never joined.
func TestADuplicateJoinStillRecordsNothing(t *testing.T) {
	joins, quota := offline(t)
	quota.slots[3] = true
	current := []net.Interface{usableInterface("en0", 3)}
	joins.interfaces = func() ([]net.Interface, error) { return current, nil }
	joins.refresh()

	joins.mu.Lock()
	_, recorded := joins.held[3]
	joins.mu.Unlock()
	if recorded {
		t.Fatal("a join refused as a duplicate put index 3 in the record; the membership " +
			"belongs to the socket's own join and releasing it would cost delivery")
	}
}
