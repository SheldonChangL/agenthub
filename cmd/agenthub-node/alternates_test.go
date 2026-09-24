package main

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/api"
	"agenthub.local/agenthub/internal/id"
	"agenthub.local/agenthub/internal/identity"
	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
	"agenthub.local/agenthub/internal/transport"
)

// e2eNode is one node's store, identity and API, as run() assembles them.
type e2eNode struct {
	store   *registry.Registry
	keypair identity.Keypair
	node    model.NodeIdentity
	api     *api.Server
	builder *protocol.HeartbeatBuilder
}

func newE2ENode(t *testing.T, name string) *e2eNode {
	t.Helper()
	directory := t.TempDir()
	keypair, err := identity.LoadOrCreateKeypair(directory)
	if err != nil {
		t.Fatal(err)
	}
	nodeID, err := id.New("node_")
	if err != nil {
		t.Fatal(err)
	}
	store, err := registry.Open(context.Background(), filepath.Join(directory, "agenthub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	node := model.NodeIdentity{
		ID: nodeID, DisplayName: name, Platform: "test",
		PublicKey: identity.EncodePublicKey(keypair.Public), Fingerprint: keypair.Fingerprint(),
	}
	builder := protocol.NewHeartbeatBuilder(store, node, keypair)
	return &e2eNode{store: store, keypair: keypair, node: node, builder: builder,
		api: api.NewServer(store, nil, builder, node)}
}

func (n *e2eNode) trust(t *testing.T, other *e2eNode) {
	t.Helper()
	if err := n.store.TrustNode(context.Background(), registry.TrustedNode{
		NodeID: other.node.ID, DisplayName: other.node.DisplayName, Platform: other.node.Platform,
		PublicKey: other.node.PublicKey, Fingerprint: other.node.Fingerprint,
	}); err != nil {
		t.Fatal(err)
	}
}

// servePeers runs this node's peer surface on these addresses the way run()
// does — one ListenerSet, one TLS server — and returns what stops it, which is
// what a restart without an address does to that address.
func (n *e2eNode) servePeers(t *testing.T, addresses []string) func() {
	t.Helper()
	set, err := bindPeerListeners(addresses, freeLoopback(t), func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if got := set.Bound(); !reflect.DeepEqual(got, addresses) {
		_ = set.Close()
		t.Fatalf("bound %q, want %q", got, addresses)
	}
	rotating := identity.NewRotatingCertificate(n.keypair, n.node.ID)
	server := &http.Server{
		Handler:           n.api.PeerHandler(),
		ReadHeaderTimeout: 5 * time.Second,
		TLSConfig:         &tls.Config{GetCertificate: rotating.GetCertificate, MinVersion: tls.VersionTLS13},
	}
	servePeerListeners(set, server, func(error) {})
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		_ = server.Close()
		_ = set.Close()
	}
	t.Cleanup(stop)
	return stop
}

// twoLoopbacksOnOnePort is 127.0.0.1:P and 127.0.0.2:P, both free: two
// addresses of one machine sharing the port, as ADR-005 §1 requires. Linux
// routes all of 127/8 to lo; macOS has 127.0.0.2 only after an alias is added
// (`sudo ifconfig lo0 alias 127.0.0.2`), and the test is skipped without it.
func twoLoopbacksOnOnePort(t *testing.T) (string, string) {
	t.Helper()
	probe, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("127.0.0.2 is not usable here (%v); on macOS add it with `sudo ifconfig lo0 alias 127.0.0.2`", err)
	}
	_ = probe.Close()
	for range 20 {
		first := freeLoopback(t)
		_, port, err := net.SplitHostPort(first)
		if err != nil {
			t.Fatal(err)
		}
		second := net.JoinHostPort("127.0.0.2", port)
		if listener, err := net.Listen("tcp", second); err == nil {
			_ = listener.Close()
			return first, second
		}
	}
	t.Fatal("found no port free on both 127.0.0.1 and 127.0.0.2")
	return "", ""
}

// The composition ADR-005 is for, with nothing faked in between: node A serves
// its peer surface on two addresses; node B knows both, preferred first. When
// A stops serving its preferred address — restarted without it — B's next
// heartbeat still arrives, over the other address, and B prefers that address
// from then on. Asserted on what A recorded, not on what B thinks it sent.
func TestAHeartbeatSurvivesThePreferredAddressGoingAway(t *testing.T) {
	first, second := twoLoopbacksOnOnePort(t)
	a := newE2ENode(t, "a")
	b := newE2ENode(t, "b")
	a.trust(t, b)
	b.trust(t, a)
	ctx := context.Background()
	if err := b.store.SetNodeAddresses(ctx, a.node.ID, []string{first, second}, transport.LoopbackOnly); err != nil {
		t.Fatal(err)
	}
	publisher := transport.NewPublisher(b.store, b.builder, b.node.ID, transport.LoopbackOnly, time.Hour)

	received := func() uint64 {
		t.Helper()
		snapshot, found, err := a.store.PeerSnapshotFor(ctx, b.node.ID)
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			return 0
		}
		return snapshot.Sequence
	}
	preferred := func() (string, []string) {
		t.Helper()
		node, err := b.store.TrustedNode(ctx, a.node.ID)
		if err != nil {
			t.Fatal(err)
		}
		return node.Address, node.Alternates
	}

	stopBoth := a.servePeers(t, []string{first, second})
	if result, err := publisher.PublishOnce(ctx); err != nil || result.Delivered != 1 {
		t.Fatalf("with both addresses served: %+v, %v", result, err)
	}
	before := received()
	if before == 0 {
		t.Fatal("A recorded no heartbeat from B")
	}
	if address, _ := preferred(); address != first {
		t.Fatalf("preferred = %q after a round the preferred address answered; want %q", address, first)
	}

	// A restarts with only the second address.
	stopBoth()
	a.servePeers(t, []string{second})
	started := time.Now()
	if result, err := publisher.PublishOnce(ctx); err != nil || result.Delivered != 1 {
		t.Fatalf("with the preferred address gone: %+v, %v", result, err)
	}
	elapsed := time.Since(started)
	if after := received(); after <= before {
		t.Fatalf("A's snapshot of B is still sequence %d; the heartbeat over the alternate did not arrive", after)
	}
	address, alternates := preferred()
	if address != second || !reflect.DeepEqual(alternates, []string{first}) {
		t.Fatalf("B now knows A as %q %q; want %q preferred and %q behind it", address, alternates, second, first)
	}
	t.Logf("delivered over the alternate in %s", elapsed)

	// And the next round goes straight to it: one address tried, not two.
	if result, err := publisher.PublishOnce(ctx); err != nil || result.Delivered != 1 {
		t.Fatalf("the round after: %+v, %v", result, err)
	}
	if address, _ := preferred(); address != second {
		t.Fatalf("preferred moved again to %q", address)
	}
}
