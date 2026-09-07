package pairing

import (
	"errors"
	"net/netip"
	"testing"

	"agenthub.local/agenthub/internal/transport"
)

// What goes into an announcement is what a peer will dial. An address that
// cannot be dialled is worse than no address: the row appears in the other
// machine's candidate list and the connection goes nowhere, which reads as this
// node being broken rather than as this node having nothing to offer.
func TestOnlyAddressesAPeerCouldDialAreAnnounced(t *testing.T) {
	found := []netip.Addr{
		netip.MustParseAddr("192.168.161.2"),
		// A laptop has one of these per interface — utun0-5, awdl0, llw0, en8.
		// Stripped of its zone, fe80::/10 names no particular interface, so a
		// peer has nothing to dial.
		netip.MustParseAddr("fe80::1cd4:8f1a:2b3c:4d5e%25en0"),
		netip.MustParseAddr("fe80::aaaa%25awdl0"),
		// macOS assigns the same address to awdl0 and llw0. Announcing it twice
		// says nothing the first one did not.
		netip.MustParseAddr("192.168.161.2"),
		netip.MustParseAddr("127.0.0.1"),
		netip.MustParseAddr("::1"),
		netip.MustParseAddr("0.0.0.0"),
		// A v4-mapped form of an address already listed, which is the same
		// address written differently.
		netip.MustParseAddr("::ffff:192.168.161.2"),
		// Private, so the policy allows it, and a second distinct address is
		// legitimate: a machine on wifi and ethernet at once.
		netip.MustParseAddr("10.1.2.3"),
	}

	got := announceable(transport.PrivateNetworks(nil), 7483, found)
	want := []netip.Addr{netip.MustParseAddr("192.168.161.2"), netip.MustParseAddr("10.1.2.3")}
	if len(got) != len(want) {
		t.Fatalf("announced %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("announced[%d] = %v, want %v", i, got[i], want[i])
		}
	}
	// And zones do not travel: a zone names an interface on this machine.
	for _, addr := range got {
		if addr.Zone() != "" {
			t.Errorf("announced %v with a zone", addr)
		}
	}
}

// The policy is what stops this node advertising an address that would pair
// over the internet with whatever answered. It is the same policy delivery
// uses, so the two cannot disagree.
func TestThePolicyDecidesWhatIsAnnounced(t *testing.T) {
	found := []netip.Addr{
		netip.MustParseAddr("192.168.161.2"),
		netip.MustParseAddr("203.0.113.7"),
	}
	if got := announceable(transport.PrivateNetworks(nil), 7483, found); len(got) != 1 ||
		got[0] != netip.MustParseAddr("192.168.161.2") {
		t.Errorf("announced %v, want only the private address", got)
	}
	// On a node that will only deliver to loopback there is nothing to
	// announce at all, which is the condition the API reports rather than
	// opening a window over.
	if got := announceable(transport.LoopbackOnly, 7483, found); len(got) != 0 {
		t.Errorf("a loopback-only node announced %v", got)
	}
	// A policy that refuses everything leaves an empty list, not a nil-pointer
	// panic or the unfiltered set.
	refuseAll := func(string) error { return errors.New("no") }
	if got := announceable(refuseAll, 7483, found); len(got) != 0 {
		t.Errorf("announced %v against a policy that refuses everything", got)
	}
}
