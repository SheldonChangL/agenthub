package registry

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
	if err := store.TrustNode(ctx, peer("node_other000000000000", "key-b")); err != nil {
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

// A grant must never outlive the ability to remove it. Revoking a node that is
// not trusted still drops any grant naming it, so an authorization cannot sit
// dormant waiting for that node to be paired.
func TestRevokeDropsGrantsEvenWithoutATrustRow(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	const ghost = "node_ghost0000000000"

	session := sessionFixture("haunted")
	if _, err := store.UpsertSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := store.TrustNode(ctx, peer(ghost, "key-a")); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAudience(ctx, session.ID, model.Audience{
		Mode: model.AudienceSelected, Nodes: []string{ghost},
	}); err != nil {
		t.Fatal(err)
	}

	// Remove the trust row behind the store's back, the way an older build or a
	// partial revoke could have left things.
	if _, err := store.db.ExecContext(ctx, `DELETE FROM trusted_nodes WHERE node_id = ?`, ghost); err != nil {
		t.Fatal(err)
	}

	err := store.RevokeNode(ctx, ghost)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("RevokeNode() = %v; want ErrNotFound for a node that is not trusted", err)
	}

	audience, audienceErr := store.GetAudience(ctx, session.ID)
	if audienceErr != nil {
		t.Fatal(audienceErr)
	}
	if audience.PublishesTo(ghost) {
		t.Error("a grant survived a revoke and would take effect if the node were paired again")
	}
}

// Pairing must not activate an authorization nobody made.
func TestPairingCannotActivateAnUnmadeGrant(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	const stranger = "node_stranger0000000"

	session := sessionFixture("stranger")
	if _, err := store.UpsertSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAudience(ctx, session.ID, model.Audience{
		Mode: model.AudienceSelected, Nodes: []string{stranger},
	}); err == nil {
		t.Fatal("SetAudience granted access to a node that is not paired")
	}

	if err := store.TrustNode(ctx, peer(stranger, "key-a")); err != nil {
		t.Fatal(err)
	}
	audience, err := store.GetAudience(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if audience.PublishesTo(stranger) {
		t.Error("pairing a node published a session to it")
	}
}

// A node that has never been in contact must not read as having been seen at
// the zero time. encoding/json does not omit a zero time.Time under omitempty,
// and the desktop view renders any present value as "last seen N days ago".
func TestNeverContactedNodeHasNoLastSeenInJSON(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	if err := store.TrustNode(ctx, peer("node_peer0000000000000", "key-a")); err != nil {
		t.Fatal(err)
	}
	node, err := store.TrustedNode(ctx, "node_peer0000000000000")
	if err != nil {
		t.Fatal(err)
	}

	encoded, err := json.Marshal(node)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "lastSeenAt") {
		t.Errorf("a never-contacted node carries lastSeenAt: %s", encoded)
	}

	if err := store.MarkNodeSeen(ctx, node.NodeID, time.Now()); err != nil {
		t.Fatal(err)
	}
	seen, err := store.TrustedNode(ctx, node.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(seen)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), "lastSeenAt") {
		t.Errorf("a contacted node lost lastSeenAt: %s", encoded)
	}
}

// TestSetNodeAddressNeverCreatesARow is the second half of discovery's safety
// argument, and it was untested.
//
// Discovery filters announcements to already-paired nodes, but the claim that
// nothing on the network can decide who this node believes in rests on two
// independent gates. This is the other one: even a caller that skipped the
// filter cannot introduce a node by announcing an address for it.
func TestSetNodeAddressNeverCreatesARow(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)

	err := store.SetNodeAddress(ctx, "node_stranger00000000", "192.0.2.10:7463")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetNodeAddress() error = %v; want ErrNotFound", err)
	}
	nodes, err := store.TrustedNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Fatalf("trusted nodes = %#v; announcing an address must not create trust", nodes)
	}
	ids, err := store.TrustedNodeIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 0 {
		t.Fatalf("trusted node ids = %v", ids)
	}
}

// TestSetNodeAddressUpdatesAPairedNode is the positive half, so the test above
// cannot pass because the call never works at all.
func TestSetNodeAddressUpdatesAPairedNode(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	const nodeID = "node_peer0000000000000"

	if err := store.TrustNode(ctx, peer(nodeID, "key-a")); err != nil {
		t.Fatal(err)
	}
	if err := store.SetNodeAddress(ctx, nodeID, "127.0.0.1:7463"); err != nil {
		t.Fatalf("SetNodeAddress() error = %v", err)
	}
	node, err := store.TrustedNode(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if node.Address != "127.0.0.1:7463" {
		t.Fatalf("address = %q", node.Address)
	}

	// Clearing is allowed and means "no longer located", without touching trust.
	if err := store.SetNodeAddress(ctx, nodeID, ""); err != nil {
		t.Fatal(err)
	}
	node, err = store.TrustedNode(ctx, nodeID)
	if err != nil {
		t.Fatal(err)
	}
	if node.Address != "" {
		t.Fatalf("address = %q; want it cleared", node.Address)
	}
	if node.PublicKey != "key-a" {
		t.Fatalf("clearing an address disturbed the trust record: %+v", node)
	}
}
