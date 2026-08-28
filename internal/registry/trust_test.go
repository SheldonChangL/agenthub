package registry

import (
	"context"
	"errors"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
)

func peer(nodeID, key string) TrustedNode {
	return TrustedNode{
		NodeID:      nodeID,
		DisplayName: "peer",
		Platform:    "linux/amd64",
		PublicKey:   key,
		Fingerprint: "2DCF 9604 DBA9 778A 6DDD 035B",
	}
}

func TestTrustStoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)

	if err := store.TrustNode(ctx, peer("node_peer0000000000000", "key-a")); err != nil {
		t.Fatal(err)
	}
	got, err := store.TrustedNode(ctx, "node_peer0000000000000")
	if err != nil {
		t.Fatal(err)
	}
	if got.PublicKey != "key-a" || got.PairedAt.IsZero() {
		t.Errorf("trusted node = %+v", got)
	}
	nodes, err := store.TrustedNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Errorf("listed %d nodes", len(nodes))
	}
}

// Silently accepting a new key for a known node is how a machine gets
// impersonated. The owner must revoke first, so the decision is deliberate.
func TestTrustNodeRefusesAKeyChange(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	const nodeID = "node_peer0000000000000"

	if err := store.TrustNode(ctx, peer(nodeID, "key-a")); err != nil {
		t.Fatal(err)
	}
	err := store.TrustNode(ctx, peer(nodeID, "key-impostor"))
	if err == nil {
		t.Fatal("a node was re-trusted with a different key")
	}
	if !errors.Is(err, ErrInvalidSession) {
		t.Errorf("error = %v", err)
	}

	got, err := store.TrustedNode(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PublicKey != "key-a" {
		t.Errorf("the stored key changed to %q", got.PublicKey)
	}
}

// Revoking must remove access, not merely the trust row: a grant left behind
// would take effect again if the node were ever paired a second time.
func TestRevokeRemovesEverySessionGrant(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	const nodeID = "node_peer0000000000000"

	session := sessionFixture("granted")
	if _, err := store.UpsertSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := store.TrustNode(ctx, peer(nodeID, "key-a")); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAudience(ctx, session.ID, model.Audience{
		Mode: model.AudienceSelected, Nodes: []string{nodeID, "node_other000000000000"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := store.RevokeNode(ctx, nodeID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.TrustedNode(ctx, nodeID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the node is still trusted: %v", err)
	}

	audience, err := store.GetAudience(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if audience.PublishesTo(nodeID) {
		t.Error("a revoked node still has a session grant")
	}
	if !audience.PublishesTo("node_other000000000000") {
		t.Error("revoking one node removed another node's grant")
	}
}

// Pairing is about identity. It must not publish anything by itself.
func TestTrustingANodePublishesNothing(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := sessionFixture("untouched")
	if _, err := store.UpsertSession(ctx, session); err != nil {
		t.Fatal(err)
	}

	if err := store.TrustNode(ctx, peer("node_peer0000000000000", "key-a")); err != nil {
		t.Fatal(err)
	}
	published, err := store.ListSessions(ctx, ListOptions{PublicOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(published) != 0 {
		t.Errorf("pairing published %d sessions", len(published))
	}
}

// Contact must not create trust.
func TestMarkNodeSeenNeverCreatesTrust(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)

	err := store.MarkNodeSeen(ctx, "node_stranger00000000", time.Now())
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("MarkNodeSeen on an unknown node = %v; want ErrNotFound", err)
	}
	nodes, err := store.TrustedNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Errorf("contact created %d trusted nodes", len(nodes))
	}
}

func TestTrustNodeValidatesInput(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)

	cases := map[string]TrustedNode{
		"short id":        peer("short", "key"),
		"separator in id": peer("node_a/node_b00000000", "key"),
		"no key":          {NodeID: "node_peer0000000000000", DisplayName: "peer", Fingerprint: "x"},
		"no name":         {NodeID: "node_peer0000000000000", PublicKey: "k", Fingerprint: "x"},
	}
	for name, node := range cases {
		t.Run(name, func(t *testing.T) {
			if err := store.TrustNode(ctx, node); err == nil {
				t.Errorf("TrustNode accepted %+v", node)
			}
		})
	}
}
