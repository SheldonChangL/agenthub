package nodeconfig

import (
	"fmt"
	"net/netip"
	"strings"
)

// PrivateRanges are networks the owner has declared private for this
// installation, in addition to the ranges that are private by definition.
//
// It exists because "private" is not always visible from an address. A pair of
// machines on a direct cable can be using a block that IANA assigned to
// somebody else on the public internet; the addresses look routable and are
// not. No amount of inspection can tell that apart, so the owner has to say.
//
// The declaration is specific on purpose. A flag that merely disabled the check
// would be easier to use and would also be the thing somebody reaches for at
// 2am without thinking about what else it lets through. Naming a block means
// stating what you believe about your own network, and the belief is recorded
// in the startup log where it can be questioned later.
type PrivateRanges []netip.Prefix

// ParsePrivateRanges reads CIDR blocks the owner declared private.
//
// A declaration must name real unicast addresses. Blocks that reach past that
// are refused rather than trusted, because each of them turns "the network on
// my cable" into something much larger:
//
//   - a block containing the unspecified address, because binding it means
//     every interface, including any public one the host has or later gains
//   - a block containing multicast or the broadcast address, which are not
//     destinations a peer lives at
//   - an IPv4-mapped IPv6 prefix, which would be logged as declared and then
//     match nothing, so the log would describe an allowance that is not real
//
// Refusing "everything" outright and refusing these by what they contain are
// the same rule: a declaration says which network you believe is private, and
// a block that swallows special-purpose space is not an answer to that.
func ParsePrivateRanges(values []string) (PrivateRanges, error) {
	ranges := make(PrivateRanges, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil {
			return nil, fmt.Errorf("declared private range %q is not a CIDR block: %w", value, err)
		}
		if prefix.Addr().Is4In6() {
			return nil, fmt.Errorf(
				"declared private range %q is an IPv4-mapped prefix; write it as an IPv4 block such as 122.122.0.0/16",
				value)
		}
		masked := prefix.Masked()
		if reason := overreaching(masked); reason != "" {
			return nil, fmt.Errorf("declared private range %q %s; name the network you actually mean", value, reason)
		}
		ranges = append(ranges, masked)
	}
	return ranges, nil
}

// overreaching says why a prefix is too broad to be a declaration, or "" if it
// is fine.
func overreaching(prefix netip.Prefix) string {
	unspecified := netip.IPv4Unspecified()
	multicast := netip.MustParseAddr("224.0.0.1")
	broadcast := netip.MustParseAddr("255.255.255.255")
	if prefix.Addr().Is6() {
		unspecified = netip.IPv6Unspecified()
		multicast = netip.MustParseAddr("ff02::1")
		broadcast = unspecified // IPv6 has no broadcast; the check below is a no-op
	}
	switch {
	case prefix.Bits() == 0:
		return "covers every address"
	case prefix.Contains(unspecified):
		return "contains the unspecified address, which binds every interface"
	case prefix.Contains(multicast):
		return "contains multicast, which is not where a peer lives"
	case prefix.Addr().Is4() && prefix.Contains(broadcast):
		return "contains the broadcast address"
	}
	return ""
}

// Contains reports whether an address falls in a declared range.
func (p PrivateRanges) Contains(ip netip.Addr) bool {
	unmapped := ip.Unmap()
	for _, prefix := range p {
		if prefix.Contains(unmapped) {
			return true
		}
	}
	return false
}

// String renders the declaration for a log line.
func (p PrivateRanges) String() string {
	if len(p) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(p))
	for _, prefix := range p {
		parts = append(parts, prefix.String())
	}
	return strings.Join(parts, ", ")
}

// IsPrivateAddress reports whether an address may carry peer traffic.
//
// This is the single definition both sides use. The listener asks it before
// binding and the publisher asks it before sending, so a node can never be
// configured to serve on an address it would refuse to deliver to.
func IsPrivateAddress(ip netip.Addr, declared PrivateRanges) bool {
	unmapped := ip.Unmap()
	if unmapped.IsLoopback() || unmapped.IsPrivate() || unmapped.IsLinkLocalUnicast() {
		return true
	}
	return declared.Contains(unmapped)
}
