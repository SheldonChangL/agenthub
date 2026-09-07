package pairing

import (
	"fmt"
	"net"
	"net/netip"
)

// PeerEndpoint reports where a peer on the local network could reach this node:
// the address its peer listener is bound to, and the port it answers on.
//
// Both come from the peer listener's own address rather than from a walk over
// this machine's interfaces, because those are not the same set and the
// difference is not academic. With -allow-lan and the default loopback
// -peer-listen — a legal combination, since nodeconfig.ValidatePeerListen
// allows loopback whatever -allow-lan says — a walk finds this machine's LAN
// address, the delivery policy accepts it, and the node announces an address
// its TLS listener is not bound to. The peer then sees a candidate that looks
// right, with a fingerprint that matches, and gets a refused connection: the
// same dead end as announcing nothing, but harder to diagnose because
// everything appears to be working.
//
// ValidatePeerListen is what makes this exact. Beyond loopback the address must
// be a single literal private IP — no wildcard, no name, no zone — so there is
// exactly one address a peer could use, or none at all.
//
// The policy is applied even though ValidatePeerListen has already accepted the
// address, so that one policy still decides all three of where this node
// delivers, which addresses it records, and which it announces. If they
// disagreed, an owner could be handed an address this build would never use.
func PeerEndpoint(policy func(address string) error, peerListen string) (Endpoint, error) {
	host, portText, err := net.SplitHostPort(peerListen)
	if err != nil {
		return Endpoint{}, fmt.Errorf("read the peer listener %q: %w", peerListen, err)
	}
	// LookupPort rather than Atoi, because a listen address may name a service
	// ("localhost:https") and net.Listen accepts one. Refusing what the
	// listener accepts would stop the node over an address that works.
	port, err := net.LookupPort("tcp", portText)
	if err != nil {
		return Endpoint{}, fmt.Errorf("port %q in %q is not a port this node can announce: %w",
			portText, peerListen, err)
	}
	// Port zero asks the kernel to choose, so this number is not the one the
	// listener ends up on; announcing it would invite a connection to nothing.
	if port == 0 {
		return Endpoint{}, fmt.Errorf(
			"the peer listener %q must name a fixed port, not 0, so an announcement can carry it", peerListen)
	}
	address, unannounceable := reachableAt(policy, host, port)
	endpoint := Endpoint{Port: port, Unannounceable: unannounceable}
	if unannounceable != "" {
		endpoint.Addresses = func() []netip.Addr { return nil }
		return endpoint, nil
	}
	endpoint.Addresses = func() []netip.Addr { return []netip.Addr{address} }
	return endpoint, nil
}

// Endpoint is where a peer could reach this node, and — when nowhere — why.
//
// The reason travels with the answer because every message about it has to name
// the actual cause. A peer listener on loopback is unreachable; one on an IPv6
// address is perfectly reachable and merely cannot be discovered, since
// announcements go out on the IPv4 group. Telling an owner the second is the
// first sends them to change the wrong thing.
type Endpoint struct {
	// Addresses is what an announcement carries: the one bound address, or
	// nothing. Never nil, so a caller need not check before calling it.
	Addresses Addresses
	// Port is the port the peer listener answers on, which is what a peer
	// dials — not the port a multicast packet arrived from.
	Port int
	// Unannounceable is why this node cannot be announced, in words an owner
	// can act on. Empty when it can be.
	Unannounceable string
}

// reachableAt decides whether the bound address is one a peer could find this
// node at, and says why not when it is not.
//
// Answered once rather than on every announcement. The peer listener's address
// is fixed for the process's life as a matter of configuration: it is whatever
// -peer-listen named. If the machine later loses that address the listener does
// not fail — a bound TCP socket keeps accepting nothing rather than erroring —
// so this answer can go stale.
//
// What a stale answer produces is a refusal, not a wrong announcement. Every
// send looks up the interface holding the address, so a lost address means no
// packet leaves and the reason appears in the announce status; and the API asks
// the same question before opening a window. Nothing is sent naming an address
// this machine no longer has.
func reachableAt(policy func(address string) error, host string, port int) (netip.Addr, string) {
	parsed, err := netip.ParseAddr(host)
	if err != nil {
		// Either a name or the wildcard. ValidatePeerListen refuses both beyond
		// loopback — a name because it can resolve somewhere else later — and
		// neither is a single address to put in an announcement.
		return netip.Addr{}, "the peer listener names a host rather than one address " +
			"(a name, or every interface), and an announcement carries one address. Restart the " +
			"node with -peer-listen on one of this machine's network addresses"
	}
	// A zone names an interface on this machine, so it cannot travel in a
	// packet. Asked before Unmap, which discards it: ::ffff:192.168.1.5%en0
	// would otherwise arrive at the policy as a plain v4 address with the zone
	// already gone, and be announced.
	if parsed.Zone() != "" {
		return netip.Addr{}, "the peer listener's address carries a %zone, which names an " +
			"interface on this machine and means nothing to another one. Restart the node with " +
			"-peer-listen giving the address without %zone"
	}
	// Unmapped, because ::ffff:192.168.1.5 and 192.168.1.5 are the same address
	// written two ways and do not compare equal. The receiving side checks an
	// announced address against the datagram's source, so announcing the mapped
	// spelling would fail that check.
	parsed = parsed.Unmap()
	// Loopback is the case this whole function exists for. The delivery policy
	// accepts it — it has to, since a node delivers to itself over loopback —
	// so nothing below would catch it, and announcing it would tell a peer to
	// connect to its own machine.
	if parsed.IsLoopback() {
		return netip.Addr{}, "the peer listener is on loopback, which no other machine can reach. " +
			"Restart the node with -allow-lan and -peer-listen on one of this machine's network " +
			"addresses"
	}
	// Announcements go out on the IPv4 group and are read from it, so an IPv6
	// address cannot be discovered however reachable it is: the packet would
	// carry an AAAA record and a v4 source, and every receiver drops an
	// announcement whose address is not the address it came from. Verified by
	// building the packet and observing the drop.
	//
	// This covers link-local v6 as a special case of the same thing — which it
	// would need anyway, being ambiguous without a zone, and a zone names an
	// interface on this machine so it cannot travel in a packet.
	//
	// A v6 peer listener still works for everything else. It is pairing by
	// announcement that cannot reach it, and `ah pair` does not need to.
	if parsed.Is6() {
		return netip.Addr{}, "the peer listener is on an IPv6 address, and announcements go out " +
			"on the IPv4 group, so no other machine could discover this one. It is reachable: " +
			"pairing by hand with `ah pair` works"
	}
	// The policy has the last word, and is the only word on everything else.
	// PrivateNetworks refuses the unspecified address explicitly and refuses
	// anything outside the ranges this build will deliver to; LoopbackOnly
	// refuses all of that as a consequence of refusing anything but loopback.
	// Repeating any of it here would be a second answer to the same question,
	// and the copy that stopped being load-bearing would be the one nobody
	// noticed had rotted.
	//
	// In the shipped binary this is unreachable, and deliberately kept anyway.
	// nodeconfig.ValidatePeerListen applies the same ranges before the process
	// starts, so an owner never sees this refusal; what it guards against is
	// the two drifting apart, which is exactly the bug that would announce an
	// address this build refuses to deliver to.
	if err := policy(netip.AddrPortFrom(parsed, uint16(port)).String()); err != nil { // #nosec G115 -- LookupPort bounds this to 0-65535
		return netip.Addr{}, "this build will not use the peer listener's address: " + err.Error()
	}
	return parsed, ""
}
