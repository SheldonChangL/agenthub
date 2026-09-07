package discovery

import (
	"context"
	"net"
	"net/netip"
	"runtime"
	"strings"
	"sync"
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
	for name, addresses := range map[string][]netip.Addr{
		"none": nil,
		"two": {
			netip.MustParseAddr("192.168.1.5"),
			netip.MustParseAddr("192.168.1.6"),
		},
		// v6 cannot be announced at all: the group and the listener are v4, so
		// the packet would carry an AAAA record and a v4 source and be dropped
		// by every receiver.
		"ipv6": {netip.MustParseAddr("fd00::1")},
	} {
		t.Run(name, func(t *testing.T) {
			err := AnnounceOffering(context.Background(), "224.0.0.251:15359",
				"node_refuse000000000", "agenthub-refuse", 7463, addresses,
				Offer{DisplayName: "refuse", Platform: "test",
					Fingerprint: "1223 03EA 5E96 543A 2DD8 BFEA"})
			if err == nil {
				t.Fatalf("announcing %v reported success", addresses)
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
// peer is announcing is joined before that peer's row would have expired had it
// been heard. Otherwise an owner who plugs in a cable waits without knowing
// what for.
func TestTheRejoinIntervalOutpacesACandidateExpiring(t *testing.T) {
	if RejoinInterval*2 > CandidateTTL {
		t.Errorf("re-checking every %v against a %v candidate lifetime leaves no room",
			RejoinInterval, CandidateTTL)
	}
}
