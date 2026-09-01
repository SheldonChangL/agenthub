package nodeconfig

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

func ValidateLoopback(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", address, err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listen address %q is not loopback; authenticated LAN mode is not implemented", address)
	}
	return nil
}

// ErrNotPrivate marks a listen address outside the private ranges.
var ErrNotPrivate = errors.New("listen address is not on a private network")

// ValidatePeerListen decides where the peer surface may be bound.
//
// Loopback is always allowed and remains the default: a node that is not told
// otherwise serves nothing to the network, exactly as before.
//
// Beyond loopback the address must be private — link-local, or one of the
// RFC 1918 / RFC 4193 ranges — and must be named explicitly. Binding the peer
// surface to a public address would put session metadata on the open internet,
// which is a different decision from sharing it with the machine on the next
// desk, and not one a command-line flag should be able to make by accident.
func ValidatePeerListen(address string, allowLAN bool) error {
	if err := ValidateLoopback(address); err == nil {
		return nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid listen address %q: %w", address, err)
	}
	if !allowLAN {
		return fmt.Errorf(
			"peer listener %q is not loopback; pass -allow-lan to serve paired peers on this network", address)
	}

	// The unspecified address binds every interface, including any public one
	// the host may gain later. Naming the interface is the point of this check:
	// the owner should be choosing a network, not accepting all of them.
	if host == "" || host == "0.0.0.0" || host == "::" {
		return fmt.Errorf("%w: %q binds every interface, including any public one; name the address to serve on",
			ErrNotPrivate, address)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf(
			"peer listener %q must be an IP address, not a name; a name can resolve somewhere else later", address)
	}
	if !isPrivate(ip) {
		return fmt.Errorf(
			"%w: %q is a public address, and AgentHub shares session metadata with paired peers on a local network, not with the internet",
			ErrNotPrivate, address)
	}
	return nil
}

// isPrivate reports whether an address belongs to a range that is not routed on
// the public internet.
//
// net.IP.IsPrivate covers RFC 1918 and RFC 4193. Link-local is added because a
// direct cable between two machines lands there and is exactly the case this
// feature exists for. Everything else is refused, including carrier-grade NAT,
// which looks private and is shared with strangers.
func isPrivate(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLoopback()
}
