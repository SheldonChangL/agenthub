package pairing

import (
	"errors"
	"net/netip"
	"strings"
	"testing"

	"agenthub.local/agenthub/internal/transport"
)

// The address in an announcement is what a peer will dial. It has to be the one
// the peer listener is actually bound to, and nothing else.
//
// This is the bug that made the earlier version wrong: it walked this machine's
// interfaces instead. With -allow-lan and the default loopback -peer-listen —
// which nodeconfig.ValidatePeerListen permits, since loopback is allowed
// whatever -allow-lan says — the walk found 192.168.161.2, the policy accepted
// it, and the node announced 192.168.161.2:7463 while its TLS listener sat on
// 127.0.0.1:7463. Verified against two live nodes: the observer listed the LAN
// address with a matching fingerprint, and connecting to it was refused. That
// is worse than announcing nothing, because everything looks like it worked.
func TestOnlyTheBoundAddressIsAnnounced(t *testing.T) {
	lan := transport.PrivateNetworks(nil)

	for name, testCase := range map[string]struct {
		peerListen string
		policy     func(string) error
		want       []netip.Addr
		wantPort   int
	}{
		"a private address is announced": {
			"192.168.161.2:7483", lan, []netip.Addr{netip.MustParseAddr("192.168.161.2")}, 7483,
		},
		// The whole finding: -allow-lan does not put the listener on the LAN,
		// so the policy alone must not decide this.
		"loopback under a LAN policy announces nothing": {
			"127.0.0.1:7463", lan, nil, 7463,
		},
		"ipv6 loopback announces nothing": {
			"[::1]:7463", lan, nil, 7463,
		},
		// A v4-mapped spelling of loopback is still loopback.
		"a mapped loopback announces nothing": {
			"[::ffff:127.0.0.1]:7463", lan, nil, 7463,
		},
		"the all-interfaces wildcard announces nothing": {
			":7463", lan, nil, 7463,
		},
		// The unspecified address binds every interface, including any public
		// one, so there is no single address it means. ValidatePeerListen
		// refuses it beyond loopback; announcing it would be meaningless
		// either way.
		"the unspecified v4 address announces nothing": {
			"0.0.0.0:7463", lan, nil, 7463,
		},
		"the unspecified v6 address announces nothing": {
			"[::]:7463", lan, nil, 7463,
		},
		// A v4-mapped LAN address is announced in its plain form: the peer
		// compares what it reads against the datagram's source, and the two
		// spellings do not compare equal.
		"a mapped private address is announced unmapped": {
			"[::ffff:192.168.161.2]:7483", lan,
			[]netip.Addr{netip.MustParseAddr("192.168.161.2")}, 7483,
		},
		"a name announces nothing": {
			"localhost:7463", lan, nil, 7463,
		},
		// Link-local v6 counts as private and can be bound, but it is
		// ambiguous without a zone and ValidatePeerListen refuses zones.
		"ipv6 link-local announces nothing": {
			"[fe80::1]:7463", lan, nil, 7463,
		},
		// A zone names an interface on this machine, so it cannot travel in a
		// packet. ValidatePeerListen refuses one, and announcing the address
		// with the zone stripped would advertise an ambiguous address.
		"a zoned address announces nothing": {
			"[fe80::1%en0]:7463", lan, nil, 7463,
		},
		"a zoned private address announces nothing": {
			"[fd00::1%en0]:7463", lan, nil, 7463,
		},
		// The policy still has the last word, so one policy decides where this
		// node delivers, what it records and what it announces.
		"a loopback-only node announces nothing": {
			"192.168.161.2:7483", transport.LoopbackOnly, nil, 7483,
		},
		"a public address the policy refuses announces nothing": {
			"203.0.113.7:7483", lan, nil, 7483,
		},
		// A service name is what net.Listen accepts, so refusing it here would
		// stop a node over an address that works.
		"a service name resolves to its port": {
			"192.168.161.2:https", lan, []netip.Addr{netip.MustParseAddr("192.168.161.2")}, 443,
		},
	} {
		t.Run(name, func(t *testing.T) {
			endpoint, err := PeerEndpoint(testCase.policy, testCase.peerListen)
			if err != nil {
				t.Fatalf("PeerEndpoint(%q) error = %v", testCase.peerListen, err)
			}
			if endpoint.Port != testCase.wantPort {
				t.Errorf("port = %d, want %d", endpoint.Port, testCase.wantPort)
			}
			got := endpoint.Addresses()
			// Anything not announced has to come with a reason an owner can
			// act on: "loopback" and "IPv6" are different problems.
			if len(testCase.want) == 0 && endpoint.Unannounceable == "" {
				t.Error("nothing is announced and no reason was given")
			}
			if len(testCase.want) > 0 && endpoint.Unannounceable != "" {
				t.Errorf("an announceable endpoint carries a reason not to be: %q",
					endpoint.Unannounceable)
			}
			if len(got) != len(testCase.want) {
				t.Fatalf("announced %v, want %v", got, testCase.want)
			}
			for i := range testCase.want {
				if got[i] != testCase.want[i] {
					t.Errorf("announced[%d] = %v, want %v", i, got[i], testCase.want[i])
				}
			}
			// A zone names an interface on this machine and means nothing to
			// the peer reading the packet.
			for _, addr := range got {
				if addr.Zone() != "" {
					t.Errorf("announced %v with a zone", addr)
				}
			}
		})
	}
}

// An address the node cannot announce from must stop the node, not produce a
// silent empty announcement: the owner asked for a peer listener there, and a
// window that quietly announces nothing is the failure this whole change is
// about.
func TestAPeerListenerThatCannotBeAnnouncedIsAnError(t *testing.T) {
	for name, peerListen := range map[string]string{
		// The kernel picks the port, so this number is not the one the listener
		// ends up on.
		"port zero":    "127.0.0.1:0",
		"no port":      "192.168.161.2",
		"not a port":   "192.168.161.2:not-a-service",
		"out of range": "192.168.161.2:70000",
		"empty":        "",
	} {
		t.Run(name, func(t *testing.T) {
			endpoint, err := PeerEndpoint(transport.PrivateNetworks(nil), peerListen)
			if err == nil {
				t.Errorf("PeerEndpoint(%q) = %+v, want an error", peerListen, endpoint)
			}
			if endpoint.Addresses != nil {
				t.Error("a refused peer listener still produced an address function")
			}
		})
	}
}

// A policy that refuses everything must leave nothing to announce, rather than
// panicking or falling back to the unfiltered address.
func TestAPolicyThatRefusesEverythingLeavesNothingToAnnounce(t *testing.T) {
	refuseAll := func(string) error { return errors.New("no") }
	endpoint, err := PeerEndpoint(refuseAll, "192.168.161.2:7483")
	if err != nil {
		t.Fatal(err)
	}
	if got := endpoint.Addresses(); len(got) != 0 {
		t.Errorf("announced %v against a policy that refuses everything", got)
	}
	// And the reason quotes the policy, so an owner is not left guessing which
	// of several rules refused their address.
	if !strings.Contains(endpoint.Unannounceable, "no") {
		t.Errorf("the reason does not carry the policy's own words: %q", endpoint.Unannounceable)
	}
}
