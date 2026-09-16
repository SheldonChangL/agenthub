package nodeconfig

import (
	"fmt"
	"net"
)

// Why a peer listener this node was configured to serve could not be bound.
//
// These are reasons an owner can act on, not error classes: each one has a
// different next step, and the desktop offers a different button for each. A
// reason nobody can act on is not worth a name, which is what Unusable is for.
const (
	// ListenAddressGone: no interface on this machine holds that address any
	// more. A cable unplugged, a network changed, a VPN dropped. The address is
	// still the right answer for the network it names — it is this machine that
	// moved — so nothing is rewritten and the owner is offered the addresses
	// this machine does have.
	ListenAddressGone = "address_gone"
	// ListenPortInUse: the address is here, the port is taken. Another node,
	// another program, or a previous instance that has not let go yet.
	ListenPortInUse = "port_in_use"
	// ListenUnusable: it is neither of those. Permission, an address family
	// this machine cannot serve, or something this code has not met.
	ListenUnusable = "unusable"
)

// ClassifyListenFailure says which of the three a bind failure was.
//
// By looking at the machine rather than at the error number. errno would be
// the obvious way and it does not survive the crossing: Windows returns
// WSAEADDRNOTAVAIL, which `errors.Is(err, syscall.EADDRNOTAVAIL)` does not
// match, because Go's Errno.Is on Windows maps only the four portable
// os.Err* sentinels. A wrong answer here is worse than no answer: it would put
// a button on screen that fixes a problem the owner does not have.
//
// So the question is asked of the machine directly, in the order the answers
// differ: does anything here hold that address, and if it does, is the port
// the part that is taken. probe is how the second half is asked — net.Listen
// in production, a stub in a test that must not open a socket.
func ClassifyListenFailure(address string, interfaceAddresses []string,
	probe func(network, address string) error) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return ListenUnusable
	}
	if !holdsAddress(host, interfaceAddresses) {
		return ListenAddressGone
	}
	// The address is here, so the port is the candidate. Asked rather than
	// assumed: binding port 0 on the same host says whether this machine will
	// serve that address at all, which separates "that port is taken" from
	// "this machine will not let me have this address", and those two have
	// different fixes — one is a port number, the other is not the owner's to
	// solve from this panel.
	if probe == nil {
		return ListenPortInUse
	}
	if err := probe("tcp", net.JoinHostPort(host, "0")); err != nil {
		return ListenUnusable
	}
	return ListenPortInUse
}

// holdsAddress says whether this machine answers to host.
//
// Loopback is true without asking: it is always here, and an interface list
// that happens not to mention it must not turn a busy port into "your address
// is gone", which is the one answer that would send the owner looking at their
// cables over a port number.
func holdsAddress(host string, interfaceAddresses []string) bool {
	parsed := net.ParseIP(host)
	if parsed == nil {
		// A name, not an address. Whether it resolves to something here is a
		// question with a timeout attached, and this runs on the start-up path.
		return true
	}
	if parsed.IsLoopback() || parsed.IsUnspecified() {
		return true
	}
	for _, candidate := range interfaceAddresses {
		// Interface addresses arrive in CIDR form from net.Interfaces, and as
		// bare addresses from a test. Both are accepted so neither caller has
		// to reshape them.
		if ip, _, err := net.ParseCIDR(candidate); err == nil {
			if ip.Equal(parsed) {
				return true
			}
			continue
		}
		if ip := net.ParseIP(candidate); ip != nil && ip.Equal(parsed) {
			return true
		}
	}
	return false
}

// InterfaceAddresses is what this machine currently holds, in CIDR form.
//
// A failure is not one: an empty list makes every non-loopback address read as
// gone, and telling an owner their address disappeared because this process
// could not enumerate its own interfaces is a lie with a button attached. The
// caller gets the error and can degrade to the reason it can still stand
// behind.
func InterfaceAddresses() ([]string, error) {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(addresses))
	for _, address := range addresses {
		out = append(out, address.String())
	}
	return out, nil
}

// ListenFailureReason is the sentence for a reason code, spelled once so the
// node's start-up log and the owner's API cannot drift apart — the same
// arrangement WithdrawalReason has, and for the same reason.
//
// It says what happened and what is true now. What to do about it is the
// desktop's to offer, because only it knows which addresses this machine has
// to offer instead.
func ListenFailureReason(reason, address, runningOn string) string {
	switch reason {
	case ListenAddressGone:
		return fmt.Sprintf("no interface on this machine holds %s any more, so the peer listener "+
			"could not be bound; this node is serving %s until that address comes back or another one is chosen",
			address, runningOn)
	case ListenPortInUse:
		return fmt.Sprintf("something else is already listening on %s, so the peer listener could not "+
			"be bound; this node is serving %s until that port is free or another one is chosen",
			address, runningOn)
	default:
		return fmt.Sprintf("the peer listener %s could not be bound; this node is serving %s instead",
			address, runningOn)
	}
}
