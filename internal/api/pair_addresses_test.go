package api

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/identity"
	"agenthub.local/agenthub/internal/nodeconfig"
	"agenthub.local/agenthub/internal/pairing"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/transport"
)

// offering is a pair node on a LAN-shaped delivery policy that offers its own
// loopback listener first and then these addresses, the way a node serving an
// Ethernet and a Wi-Fi address offers both.
func offering(extra ...string) func(string) []Option {
	return func(own string) []Option {
		return []Option{
			WithDeliveryPolicy(transport.PrivateNetworks(nil)),
			WithPairExchange(pairing.NewRequests(), transport.NewPairDialer(transport.LoopbackOnly),
				fixedPeerAddresses(append([]string{own}, extra...))),
		}
	}
}

func fixedPeerAddresses(addresses []string) func() []string {
	return func() []string { return addresses }
}

// portOf is the port of a host:port, so the invented alternates share the
// listener's port as a real node's addresses do.
func portOf(t *testing.T, address string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func pairBoth(t *testing.T, requester, receiver *pairNode) {
	t.Helper()
	receiver.openWindow()
	started := requester.startRequest(receiver.address())
	if started.Code != http.StatusCreated {
		t.Fatalf("start pairing request = %d %s", started.Code, started.Body.String())
	}
	var outgoing pairing.Request
	if err := json.Unmarshal(started.Body.Bytes(), &outgoing); err != nil {
		t.Fatal(err)
	}
	receiver.decide(t, outgoing.ID, "approve", http.StatusOK)
	requester.decide(t, outgoing.ID, "confirm", http.StatusOK)
}

func storedAddresses(t *testing.T, holder *pairNode) (string, []string) {
	t.Helper()
	trusted := holder.trusted()
	if len(trusted) != 1 {
		t.Fatalf("%s trusts %d nodes, want 1", holder.node.DisplayName, len(trusted))
	}
	return trusted[0].Address, trusted[0].Alternates
}

// ADR-005 §4, both directions of one exchange: the requester keeps the address
// it typed as preferred and the approval's addresses as alternates; the
// receiver keeps the request's address as preferred and its addresses as
// alternates.
func TestPairingRecordsThePreferredAndTheAlternatesOnBothSides(t *testing.T) {
	requesterPort := "7463"
	receiverPort := "7463"
	requester := newPairNodeWith(t, "asks", func(own string) []Option {
		requesterPort = portOf(t, own)
		return offering("10.0.0.1:"+requesterPort, "192.168.50.1:"+requesterPort)(own)
	})
	receiver := newPairNodeWith(t, "decides", func(own string) []Option {
		receiverPort = portOf(t, own)
		return offering("10.0.0.2:" + receiverPort)(own)
	})
	pairBoth(t, requester, receiver)

	preferred, alternates := storedAddresses(t, requester)
	if preferred != receiver.address() || !reflect.DeepEqual(alternates, []string{"10.0.0.2:" + receiverPort}) {
		t.Errorf("requester stored %q %q; want %q preferred and the approval's address behind it",
			preferred, alternates, receiver.address())
	}
	preferred, alternates = storedAddresses(t, receiver)
	want := []string{"10.0.0.1:" + requesterPort, "192.168.50.1:" + requesterPort}
	if preferred != requester.address() || !reflect.DeepEqual(alternates, want) {
		t.Errorf("receiver stored %q %q; want %q preferred and %q behind it",
			preferred, alternates, requester.address(), want)
	}
}

// signedPairRequest is a pair.request from a throwaway node carrying exactly
// this payload's addresses, including what no honest sender would put there.
func signedPairRequest(t *testing.T, address string, addresses []string) protocol.Envelope {
	t.Helper()
	keypair, nodeID := throwawayIdentity(t)
	descriptor := protocol.NodeDescriptor{
		NodeID: nodeID, DisplayName: "claims a lot", Platform: "test",
		PublicKey: identity.EncodePublicKey(keypair.Public), Fingerprint: keypair.Fingerprint(),
	}
	envelope, err := protocol.NewEnvelope(nodeID, protocol.TypePairRequest, protocol.At(time.Now()),
		protocol.PairRequestPayload{Node: descriptor, Address: address, Addresses: addresses}, keypair)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

// An address the receiver would not deliver to is dropped — the preferred one
// again, public, loopback (another machine's loopback is this one), a name for
// loopback, zoned, not host:port, a repeat — and the pairing still goes ahead:
// an address is never a reason to refuse one.
func TestAReceiverKeepsOnlyAddressesItWouldDeliverTo(t *testing.T) {
	receiver := newPairNodeWith(t, "decides", func(string) []Option {
		return []Option{WithDeliveryPolicy(transport.PrivateNetworks(nil))}
	})
	for _, claimed := range [][]string{
		{"10.0.0.1:7463", "203.0.113.5:7463", "127.0.0.1:7463", "10.0.0.2:7463"},
		{"localhost:7463", "[fe80::1%en0]:7463", "10.0.0.2", "10.0.0.2:7463"},
		{"10.0.0.2:7463", "10.0.0.2:7463"},
	} {
		got := receiver.server.acceptableAlternates("10.0.0.1:7463", claimed)
		if !reflect.DeepEqual(got, []string{"10.0.0.2:7463"}) {
			t.Errorf("acceptableAlternates(%q) = %q; want only 10.0.0.2:7463", claimed, got)
		}
	}
}

// The schema allows four addresses in a pairing payload. A list longer than
// that is read no further than its fourth entry, so padding it with what the
// receiver refuses does not get the entries behind the padding kept.
func TestAReceiverReadsNoFurtherThanTheFourthAddress(t *testing.T) {
	receiver := newPairNodeWith(t, "decides", func(string) []Option {
		return []Option{WithDeliveryPolicy(transport.PrivateNetworks(nil))}
	})
	receiver.openWindow()
	envelope := signedPairRequest(t, "10.0.0.1:7463", []string{
		"10.0.0.1:7463",    // the preferred again
		"203.0.113.5:7463", // public
		"10.0.0.2:7463",
		"10.0.0.3:7463",
		"10.0.0.4:7463", // a fifth, past what the schema allows
		"10.0.0.5:7463",
	})
	posted := perform(t, receiver.server.PeerHandler(), http.MethodPost, "/v1/pair/requests", envelope)
	if posted.Code != http.StatusAccepted {
		t.Fatalf("pair request = %d %s", posted.Code, posted.Body.String())
	}
	rows := receiver.requests()
	if len(rows) != 1 {
		t.Fatalf("rows = %v", rows)
	}
	want := []string{"10.0.0.2:7463", "10.0.0.3:7463"}
	if rows[0].Address != "10.0.0.1:7463" || !reflect.DeepEqual(rows[0].Alternates, want) {
		t.Fatalf("request recorded %q %q; want 10.0.0.1:7463 and %q", rows[0].Address, rows[0].Alternates, want)
	}
	receiver.decide(t, rows[0].ID, "approve", http.StatusOK)
	preferred, alternates := storedAddresses(t, receiver)
	if preferred != "10.0.0.1:7463" || !reflect.DeepEqual(alternates, want) {
		t.Fatalf("trusted with %q %q; want 10.0.0.1:7463 and %q", preferred, alternates, want)
	}
}

// A preferred address the receiver refuses leaves room for four alternates,
// and the first becomes preferred when the pairing is trusted.
func TestARefusedPreferredLeavesTheAlternatesToStandIn(t *testing.T) {
	receiver := newPairNodeWith(t, "decides", func(string) []Option {
		return []Option{WithDeliveryPolicy(transport.PrivateNetworks(nil))}
	})
	receiver.openWindow()
	// Four addresses, as many as the schema allows, none of them the refused
	// preferred one.
	envelope := signedPairRequest(t, "203.0.113.5:7463",
		[]string{"10.0.0.2:7463", "10.0.0.3:7463", "10.0.0.4:7463", "10.0.0.5:7463"})
	if posted := perform(t, receiver.server.PeerHandler(), http.MethodPost, "/v1/pair/requests",
		envelope); posted.Code != http.StatusAccepted {
		t.Fatalf("pair request = %d %s", posted.Code, posted.Body.String())
	}
	receiver.decide(t, receiver.requests()[0].ID, "approve", http.StatusOK)
	preferred, alternates := storedAddresses(t, receiver)
	if preferred != "10.0.0.2:7463" ||
		!reflect.DeepEqual(alternates, []string{"10.0.0.3:7463", "10.0.0.4:7463", "10.0.0.5:7463"}) {
		t.Fatalf("trusted with %q %q", preferred, alternates)
	}
}

// An older requester sends address and no list, which is the shape this
// receiver has always read; it records that one address and nothing beside it.
func TestAnOlderRequesterIsRecordedByItsOneAddress(t *testing.T) {
	receiver := newPairNodeWith(t, "decides", func(string) []Option {
		return []Option{WithDeliveryPolicy(transport.PrivateNetworks(nil))}
	})
	receiver.openWindow()
	envelope := signedPairRequest(t, "10.0.0.1:7463", nil)
	if strings.Contains(string(envelope.Payload), "addresses") {
		t.Fatalf("the payload carries a list an older build would not send: %s", envelope.Payload)
	}
	if posted := perform(t, receiver.server.PeerHandler(), http.MethodPost, "/v1/pair/requests",
		envelope); posted.Code != http.StatusAccepted {
		t.Fatalf("pair request = %d %s", posted.Code, posted.Body.String())
	}
	receiver.decide(t, receiver.requests()[0].ID, "approve", http.StatusOK)
	preferred, alternates := storedAddresses(t, receiver)
	if preferred != "10.0.0.1:7463" || len(alternates) != 0 {
		t.Fatalf("trusted with %q %q; want the one address", preferred, alternates)
	}
}

// What a node offers is what its listener set has bound, not what it was
// configured with: an address that failed to bind is not offered, because a
// peer that recorded it would spend an attempt on it every round.
func TestOnlyBoundAddressesAreOffered(t *testing.T) {
	policy := transport.PrivateNetworks(nil)
	var set *nodeconfig.ListenerSet
	requester := newPairNodeWith(t, "asks", func(own string) []Option {
		port := portOf(t, own)
		set = nodeconfig.NewListenerSet(
			[]string{"10.0.0.5:" + port, "192.168.1.9:" + port, "10.0.0.6:" + port},
			nodeconfig.ListenerSetOptions{
				Listen: func(network, address string) (net.Listener, error) {
					if strings.HasPrefix(address, "192.168.1.9:") {
						return nil, errors.New("listen tcp " + address + ": bind: can't assign requested address")
					}
					return net.Listen(network, "127.0.0.1:0")
				},
				Interfaces: func() ([]string, error) { return nil, nil },
				Probe:      func(string, string) error { return errors.New("not held") },
			})
		if err := set.Bind(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = set.Close() })
		return []Option{
			WithDeliveryPolicy(policy),
			// The same wiring cmd/agenthub-node uses.
			WithPairExchange(pairing.NewRequests(), transport.NewPairDialer(transport.LoopbackOnly),
				func() []string { return pairing.ReachableAddresses(policy, set.Bound()) }),
		}
	})
	receiver := newPairNodeWith(t, "decides", func(string) []Option {
		return []Option{WithDeliveryPolicy(policy)}
	})
	receiver.openWindow()
	started := requester.startRequest(receiver.address())
	if started.Code != http.StatusCreated {
		t.Fatalf("start pairing request = %d %s", started.Code, started.Body.String())
	}
	rows := receiver.requests()
	if len(rows) != 1 {
		t.Fatalf("rows = %v", rows)
	}
	port := portOf(t, requester.address())
	if rows[0].Address != "10.0.0.5:"+port || !reflect.DeepEqual(rows[0].Alternates, []string{"10.0.0.6:" + port}) {
		t.Fatalf("the request offered %q %q; want the two bound addresses and not the one that failed",
			rows[0].Address, rows[0].Alternates)
	}
}
