package nodeconfig

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
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
// Beyond loopback the address must be named explicitly and must be private —
// either private by definition (RFC 1918 / RFC 4193 / link-local) or in a block
// the owner declared private for this installation. Binding the peer surface to
// an address that is neither would put session metadata somewhere the owner has
// not thought about, which is a different decision from sharing it with the
// machine on the next desk.
func ValidatePeerListen(address string, allowLAN bool, declared PrivateRanges) error {
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

	if host == "" {
		return fmt.Errorf("%w: %q names no address; say which one to serve on", ErrNotPrivate, address)
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf(
			"peer listener %q must be an IP address, not a name; a name can resolve somewhere else later", address)
	}
	// The unspecified address binds every interface, including any public one
	// the host has or later gains. This is asked of the parsed address rather
	// than the string, because "0.0.0.0" is only one of its spellings: "0::0",
	// "::0", "0:0:0:0:0:0:0:0" and "::ffff:0.0.0.0" all bind everything too,
	// and a string comparison lets them through.
	if ip.Unmap().IsUnspecified() {
		return fmt.Errorf("%w: %q binds every interface, including any public one; name the address to serve on",
			ErrNotPrivate, address)
	}
	// A zoned literal parses here but breaks when a URL is built from it, so a
	// peer configured this way would be reachable and silently never contacted.
	if ip.Zone() != "" {
		return fmt.Errorf("peer listener %q carries a zone; give the address without %%zone", address)
	}
	if !IsPrivateAddress(ip, declared) {
		return fmt.Errorf(
			"%w: %q is not in a private range. If this network is private despite the address, declare it with -treat-as-private",
			ErrNotPrivate, address)
	}
	return nil
}
