package nodeconfig

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// The peer listener as a list (ADR-005 §1).
//
// A machine on a cable and on Wi-Fi at once has two addresses a paired machine
// could reach it at, and one setting that named a single address made it
// reachable on one of them. The obvious fix — the unspecified address — stays
// refused (ValidatePeerListen): it binds every interface the machine has or
// later gains, public ones included. So the node serves exactly the addresses
// the owner named, several of them.
//
// Old readers are why the scalar survives. A window or an `ah` written before
// the list reads and writes peerListen alone, and the two rules here are what
// keep such a reader telling the truth:
//
//   - writing the scalar replaces the whole list, so "this machine only" from
//     an older window closes every LAN address, not just the first; and
//   - a stored list is believed only while its first entry is the stored
//     scalar, so a downgraded node that rewrote the scalar is obeyed.

// FieldPeerListens is the JSON name, and the database key, of the list. Not in
// SettingNames: peerListen is the one setting, and this is its second spelling.
const FieldPeerListens = "peerListens"

// MaxPeerListens bounds the list. A machine with a cable and Wi-Fi needs two;
// four leaves room for a dock and a direct link without making the list a way
// to approximate the unspecified address one interface at a time. Loosening
// this later is compatible; tightening it would refuse stored lists.
const MaxPeerListens = 4

// ErrPeerListenMismatch is a request that gave both spellings and made them
// disagree. Refused rather than resolved: either answer would be a guess about
// which of two contradictory instructions the caller meant.
var ErrPeerListenMismatch = errors.New("peerListen and peerListens disagree")

// PeerListenList is the peer listener this Partial names, as a list, and
// whether it names one at all.
//
// The same rule as the database read (internal/registry): a list is believed
// when there is no scalar beside it or when its first entry is that scalar;
// otherwise the scalar wins and is a list of one. An empty list is the
// loopback default. The result is a fresh slice the caller may keep.
func (p Partial) PeerListenList() ([]string, bool) {
	if p.PeerListens != nil && len(*p.PeerListens) > 0 &&
		(p.PeerListen == nil || (*p.PeerListens)[0] == *p.PeerListen) {
		return append([]string(nil), (*p.PeerListens)...), true
	}
	if p.PeerListen != nil {
		return []string{*p.PeerListen}, true
	}
	if p.PeerListens != nil {
		return []string{DefaultPeerListen}, true
	}
	return nil, false
}

// NormalizePeerListen makes the two spellings of the peer listener agree, or
// refuses a request whose two spellings contradict each other.
//
//   - scalar only: the list becomes [scalar]. Writing the scalar replaces the
//     whole list — the safety core of ADR-005.
//   - list only: the scalar becomes list[0]; an empty list is the default.
//   - both: list[0] must equal the scalar.
//
// It runs on what a caller gives before anything is resolved or stored, so
// that every later step sees one setting rather than two fields.
func NormalizePeerListen(p Partial) (Partial, error) {
	if p.PeerListen != nil && p.PeerListens != nil {
		list := *p.PeerListens
		if len(list) == 0 {
			list = []string{DefaultPeerListen}
		}
		if list[0] != *p.PeerListen {
			return Partial{}, fmt.Errorf("%w: peerListen is %q and peerListens starts with %q; "+
				"send one of them, or make peerListen the first entry", ErrPeerListenMismatch,
				*p.PeerListen, list[0])
		}
	}
	list, ok := p.PeerListenList()
	if !ok {
		return p, nil
	}
	first := list[0]
	p.PeerListen, p.PeerListens = &first, &list
	return p, nil
}

// peerListenList is the list this configuration serves: PeerListens, or the
// scalar alone for a configuration built before the list existed.
func (s Settings) peerListenList() []string {
	if len(s.PeerListens) == 0 {
		return []string{s.PeerListen}
	}
	return s.PeerListens
}

// PeerListenList is the exported form of peerListenList, for the API and the
// node, which read a Settings built by callers that may know only the scalar.
func (s Settings) PeerListenList() []string {
	return append([]string(nil), s.peerListenList()...)
}

// ValidatePeerListens decides whether a list of peer addresses may be served
// together.
//
// Every entry passes ValidatePeerListen on its own — every spelling of the
// unspecified address, zones, names and addresses outside the private ranges
// are refused there. Then the list as a whole: at most MaxPeerListens entries,
// no name (the "localhost" a single entry may be), no address twice
// (::ffff:192.168.1.10 is 192.168.1.10), one port for all of them, and
// loopback never beside a network address.
//
// One port, because a peer records one address and a port per address would
// make "the other address of this machine" a second thing to learn. Loopback
// apart from the network, because a list mixing them is either a node that
// serves the network or one that does not, and the window has to be able to
// say which in one word.
func ValidatePeerListens(list []string, allowLAN bool, declared PrivateRanges) error {
	if len(list) == 0 {
		return errors.New("no peer listener address given")
	}
	if len(list) > MaxPeerListens {
		return fmt.Errorf("%d peer listener addresses given; at most %d can be served", len(list), MaxPeerListens)
	}
	seen := make(map[string]string, len(list))
	var port, portFrom string
	loopback, network := "", ""
	for _, address := range list {
		if err := ValidatePeerListen(address, allowLAN, declared); err != nil {
			return err
		}
		host, portText, err := net.SplitHostPort(address)
		if err != nil {
			return fmt.Errorf("invalid listen address %q: %w", address, err)
		}
		ip, err := netip.ParseAddr(host)
		if err != nil {
			// Only "localhost" gets here: ValidatePeerListen refuses every other
			// name. Alone it stays accepted, as it always was. In a list it is
			// refused, because whether it is 127.0.0.1 or ::1 is the resolver's
			// answer at bind time, so "localhost" beside "127.0.0.1" may be the
			// same socket twice, and a check that compares what was written
			// cannot see it.
			if len(list) > 1 {
				return fmt.Errorf("peer listener %q names a host; in a list of addresses give each as "+
					"an IP address (127.0.0.1 or [::1]), so that no two of them are the same socket", address)
			}
		}
		key := strings.ToLower(host)
		if err == nil {
			key = ip.Unmap().String()
		}
		if earlier, ok := seen[key]; ok {
			return fmt.Errorf("peer listener %q is the same address as %q; give each address once", address, earlier)
		}
		seen[key] = address
		number, err := net.LookupPort("tcp", portText)
		if err != nil {
			return fmt.Errorf("port %q in %q is not a port: %w", portText, address, err)
		}
		canonical := fmt.Sprint(number)
		if len(list) > 1 && number == 0 {
			return fmt.Errorf("peer listener %q asks the kernel to choose a port, and every address "+
				"in the list has to answer on the same one; give a fixed port", address)
		}
		if port == "" {
			port, portFrom = canonical, address
		} else if canonical != port {
			return fmt.Errorf("peer listener %q is on a different port from %q; every address has to "+
				"answer on the same port", address, portFrom)
		}
		if ValidateLoopback(address) == nil {
			loopback = address
		} else {
			network = address
		}
		if loopback != "" && network != "" {
			return fmt.Errorf("peer listener %q is loopback and %q is on a network; a node either "+
				"serves only this machine or serves the network, so the list cannot mix them",
				loopback, network)
		}
	}
	return nil
}
