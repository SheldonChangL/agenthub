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
		out := make([]netip.Addr, 0, 4)
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
				parsed = parsed.Unmap()
				if !parsed.IsValid() || parsed.IsLoopback() || parsed.IsUnspecified() {
					continue
				}
				// The zone is dropped because it names an interface on this
				// machine, which means nothing to the peer reading the packet.
				parsed = parsed.WithZone("")
				if policy(netip.AddrPortFrom(parsed, uint16(port)).String()) != nil { // #nosec G115 -- the caller's own listen port
					continue
				}
				out = append(out, parsed)
			}
		}
		return out
	}
}
