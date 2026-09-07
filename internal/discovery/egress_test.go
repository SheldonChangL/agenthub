package discovery

import (
	"bytes"
	"context"
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
// property that makes the ticker safe to run every thirty seconds: a repeat
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
	// A check, then the interface appears, then the next check: an owner should
	// still catch an announcement before the window closes.
	if RejoinInterval+peerAnnounces > shortestWindow {
		t.Errorf("re-checking every %v, against a peer announcing every %v inside a window as "+
			"short as %v, can miss the window entirely",
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
