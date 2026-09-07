package pairing

import (
	"net"
	"net/netip"
)

// LocalAddresses reports the addresses on this machine that a peer on the local
// network could reach, filtered by the same policy delivery uses.
//
// Read on every announcement rather than once at startup: a laptop moving from
// wifi to ethernet keeps its identity and loses its address, and an
// announcement carrying the old one invites a connection to nothing.
//
// The policy is what keeps this honest. Without it a machine with a public
// address would announce it, and a candidate list would offer to pair over the
// internet with whatever answered.
func LocalAddresses(policy func(address string) error, port int) Addresses {
	return func() []netip.Addr {
		interfaces, err := net.Interfaces()
		if err != nil {
			return nil
		}
		found := make([]netip.Addr, 0, 8)
		for _, iface := range interfaces {
			// Down interfaces have addresses that answer nothing, and loopback
			// is not somewhere another machine can reach.
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
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
				found = append(found, parsed)
			}
		}
		return announceable(policy, port, found)
	}
}

// announceable is the decision about each address, separated from the walk over
// this machine's interfaces so it can be tested on addresses a test chooses
// rather than on whatever the machine running the test happens to have.
func announceable(policy func(address string) error, port int, found []netip.Addr) []netip.Addr {
	out := make([]netip.Addr, 0, len(found))
	seen := make(map[netip.Addr]bool, len(found))
	for _, parsed := range found {
		parsed = parsed.Unmap()
		if !parsed.IsValid() || parsed.IsLoopback() || parsed.IsUnspecified() {
			continue
		}
		// The zone is dropped because it names an interface on this machine,
		// which means nothing to the peer reading the packet.
		parsed = parsed.WithZone("")
		// And an IPv6 link-local address is the one that cannot survive losing
		// its zone: fe80::/10 is ambiguous without one, and a laptop has one
		// per interface — tunnels, AirDrop, wired, wireless. Announcing ten of
		// them would fill a candidate row with addresses nobody can dial and
		// bury the one that works.
		if parsed.Is6() && parsed.IsLinkLocalUnicast() {
			continue
		}
		if policy(netip.AddrPortFrom(parsed, uint16(port)).String()) != nil { // #nosec G115 -- the caller's own listen port
			continue
		}
		// Two interfaces can hold the same address — macOS assigns one to both
		// awdl0 and llw0 — and announcing it twice says nothing the first one
		// did not.
		if seen[parsed] {
			continue
		}
		seen[parsed] = true
		out = append(out, parsed)
	}
	return out
}
