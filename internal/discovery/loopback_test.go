package discovery

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/transport"
)

// A local process must not be able to put a row on the owner's candidate list.
//
// This is the vulnerability three rounds of review circled before it was
// understood. IP_MULTICAST_LOOP hands a copy of every outgoing multicast
// datagram back to local sockets that have joined the group, and the copy is
// matched against the membership of the interface it was sent on — the source
// address is never consulted. So a process sending to the group from 127.0.0.1
// reaches this node through its ordinary membership on the default interface.
//
// Two consequences that made this hard to see. Which interfaces are joined
// cannot prevent it: an earlier fix stopped joining loopback and the row still
// landed, because loopback was never the membership delivering it. And the
// source check the candidate layer relies on is void here, because a forger on
// loopback trivially sends from the address it claims.
//
// Under the default loopback-only policy this is worse than a LAN forgery,
// which that policy refuses: a second user on a shared machine, who cannot read
// the first user's files, could put a chosen display name and fingerprint in
// front of them at the moment they are deciding which machine to trust.
func TestALocalProcessCannotPutARowOnTheList(t *testing.T) {
	const group = "224.0.0.251:15391"
	target, err := net.ResolveUDPAddr("udp", group)
	if err != nil {
		t.Fatal(err)
	}
	// Exactly the socket Listen opens, memberships included.
	listener, err := net.ListenMulticastUDP("udp", nil, target)
	if err != nil {
		t.Skipf("cannot join the group on this machine: %v", err)
	}
	defer func() { _ = listener.Close() }()
	newMembership(listener, target).refresh()

	forged, err := buildAnnouncement("node_attacker0000000", "attacker", 9443,
		[]netip.Addr{netip.MustParseAddr("127.0.0.1")},
		Offer{DisplayName: "a name the owner trusts", Platform: "darwin",
			Fingerprint: "DEAD BEEF CAFE 1234 5678 9ABC"})
	if err != nil {
		t.Fatal(err)
	}
	sender, err := net.DialUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")}, target)
	if err != nil {
		t.Skipf("cannot send from loopback here: %v", err)
	}
	// The write reports EADDRNOTAVAIL on macOS and the loop copy is delivered
	// anyway, so its error is deliberately not checked.
	_, _ = sender.Write(forged)
	_ = sender.Close()

	_ = listener.SetReadDeadline(time.Now().Add(2 * time.Second))
	buffer := make([]byte, maxPacket)
	read, from, err := listener.ReadFromUDPAddrPort(buffer)
	if err != nil {
		// The packet has to arrive for this test to mean anything: if it does
		// not, the machine is not exercising the mechanism and a pass here
		// would be empty.
		t.Skipf("the loopback copy did not arrive on this machine, so this proves nothing: %v", err)
	}
	if !from.Addr().Unmap().IsLoopback() {
		t.Skipf("the copy arrived from %v rather than loopback", from.Addr())
	}
	t.Logf("the forged datagram was delivered, from %v — as expected", from.Addr())

	// 1. dispatch drops it, which is what protects every handler including the
	//    paired-peer address path that does not look at the source at all.
	var reached int
	dispatch(context.Background(), from, buffer[:read],
		[]PacketHandler{func(context.Context, netip.Addr, []Announcement) { reached++ }})
	if reached != 0 {
		t.Error("a loopback-sourced datagram reached the handlers")
	}

	// 2. And the candidate layer refuses it on its own, so the property does
	//    not rest on one call site.
	candidates := NewCandidates("node_local0000000000",
		func(context.Context, string) (bool, error) { return false, nil },
		transport.LoopbackOnly)
	changed, err := candidates.ObserveAll(context.Background(), from.Addr(),
		ParseAnnouncements(buffer[:read]))
	if err != nil {
		t.Fatalf("ObserveAll: %v", err)
	}
	if changed != 0 || len(candidates.List()) != 0 {
		t.Errorf("a local forgery was listed: changed=%d rows=%+v", changed, candidates.List())
	}
}

// An offer naming a loopback address is an offer to connect to the reader's own
// machine, and it is not listed.
//
// What refuses it is the source comparison, not a check on the address itself:
// the offer says 127.0.0.1 and the datagram came from somewhere else, so they
// do not match. Worth pinning because it is the reason a separate loopback
// address check would be dead code — an offer naming loopback either came from
// loopback, and is refused for that, or did not, and is refused for this.
func TestAnOfferNamingLoopbackIsNotListed(t *testing.T) {
	candidates := NewCandidates("node_local0000000000",
		func(context.Context, string) (bool, error) { return false, nil },
		transport.LoopbackOnly)
	// A source that is not loopback, so it is the announced address doing the
	// refusing and not the check above it.
	source := netip.MustParseAddr("192.168.9.9")
	changed, err := candidates.ObserveAll(context.Background(), source, []Announcement{{
		NodeID:      "node_claimsloopback0",
		Address:     "127.0.0.1:9443",
		DisplayName: "loopback claimer",
		Fingerprint: "DEAD BEEF CAFE 1234 5678 9ABC",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if changed != 0 || len(candidates.List()) != 0 {
		t.Errorf("an offer naming loopback was listed: %+v", candidates.List())
	}
}
