package discovery

import (
	"context"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

// localV4Addresses lists this machine's usable v4 addresses, one per interface,
// so the test can announce from each of them.
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
