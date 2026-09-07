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
func PeerEndpoint(policy func(address string) error, peerListen string) (Addresses, int, error) {
	host, portText, err := net.SplitHostPort(peerListen)
	if err != nil {
		return nil, 0, fmt.Errorf("read the peer listener %q: %w", peerListen, err)
	}
	// LookupPort rather than Atoi, because a listen address may name a service
	// ("localhost:https") and net.Listen accepts one. Refusing what the
	// listener accepts would stop the node over an address that works.
	port, err := net.LookupPort("tcp", portText)
	if err != nil {
		return nil, 0, fmt.Errorf("port %q in %q is not a port this node can announce: %w",
			portText, peerListen, err)
	}
	// Port zero asks the kernel to choose, so this number is not the one the
	// listener ends up on; announcing it would invite a connection to nothing.
	if port == 0 {
		return nil, 0, fmt.Errorf(
			"the peer listener %q must name a fixed port, not 0, so an announcement can carry it", peerListen)
	}
	address, announceable := reachableAt(policy, host, port)
	return func() []netip.Addr {
		if !announceable {
			return nil
		}
		return []netip.Addr{address}
	}, port, nil
}

// reachableAt decides whether the bound address is one a peer could dial.
//
// Answered once rather than on every announcement: the peer listener is bound
// at startup and the process does not survive losing it, so unlike a walk over
// interfaces there is nothing here that changes while the node runs.
func reachableAt(policy func(address string) error, host string, port int) (netip.Addr, bool) {
	// A wildcard listener has no single address to announce. ValidatePeerListen
	// refuses one beyond loopback, so this is the loopback wildcard, which a
	// peer could not reach anyway.
	if host == "" {
		return netip.Addr{}, false
	}
	parsed, err := netip.ParseAddr(host)
	if err != nil {
		// A name, which ValidatePeerListen refuses beyond loopback because it
		// can resolve somewhere else later. Whatever it resolves to now is not
		// something to put in an announcement.
		return netip.Addr{}, false
	}
	parsed = parsed.Unmap()
	if !parsed.IsValid() || parsed.IsLoopback() || parsed.IsUnspecified() {
		return netip.Addr{}, false
	}
	// A zone names an interface on this machine and means nothing to the peer
	// reading the packet. ValidatePeerListen refuses one beyond loopback, so
	// this is belt and braces — but announcing a zoned address is never right,
	// and stripping the zone silently would announce an ambiguous one.
	if parsed.Zone() != "" {
		return netip.Addr{}, false
	}
	// An IPv6 link-local address counts as private, so it can be bound, but it
	// is ambiguous without a zone — and a zone is exactly what cannot travel.
	// There is nothing a peer could do with it.
	if parsed.Is6() && parsed.IsLinkLocalUnicast() {
		return netip.Addr{}, false
	}
	if policy(netip.AddrPortFrom(parsed, uint16(port)).String()) != nil { // #nosec G115 -- checked non-zero above
		return netip.Addr{}, false
	}
	return parsed, true
}
