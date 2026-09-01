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
		// A default route is not a declaration, it is the absence of one. If
		// somebody wants to serve every address they should say which
		// addresses, and if they cannot enumerate them they have not thought
		// about it enough to be doing this.
		if prefix.Bits() == 0 {
			return nil, fmt.Errorf(
				"declared private range %q covers every address; name the network you actually mean", value)
		}
		ranges = append(ranges, prefix.Masked())
	}
	return ranges, nil
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
