package protocol_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
)

const (
	senderNode = "node_local0000000000"
	peerA      = "node_a00000000000000"
	peerB      = "node_b00000000000000"
)

func trustPeers(t *testing.T, store *registry.Registry, nodeIDs ...string) {
	t.Helper()
	for _, nodeID := range nodeIDs {
		if err := store.TrustNode(context.Background(), registry.TrustedNode{
			NodeID: nodeID, DisplayName: "peer " + nodeID, Platform: "linux/amd64",
			PublicKey: "key-" + nodeID, Fingerprint: "2DCF 9604 DBA9 778A 6DDD 035B",
		}); err != nil {
			t.Fatalf("trust %q: %v", nodeID, err)
		}
	}
}

func heartbeatBuilder(t *testing.T, store *registry.Registry, signer protocol.Signer) *protocol.HeartbeatBuilder {
	t.Helper()
	return protocol.NewHeartbeatBuilder(store,
		model.NodeIdentity{ID: senderNode, DisplayName: "test", Platform: "darwin/arm64"}, signer)
}

// A heartbeat says who it is for. Without that, a snapshot built for one peer is
// a valid snapshot for every peer that trusts the sender.
func TestHeartbeatNamesItsRecipient(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	trustPeers(t, store, peerA)
	builder := heartbeatBuilder(t, store, newTestKeypair(t))

	directed, err := builder.BuildFor(ctx, time.Now(), peerA)
	if err != nil {
		t.Fatal(err)
	}
	if directed.RecipientNodeID != peerA {
		t.Errorf("BuildFor recipient = %q, want %q", directed.RecipientNodeID, peerA)
	}

	// The owner preview stays the union of everything that leaves this host, but
	// it is addressed to this node, so no peer can accept it as its heartbeat.
	preview, err := builder.Build(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if preview.RecipientNodeID != senderNode {
		t.Errorf("owner preview recipient = %q, want the local node %q", preview.RecipientNodeID, senderNode)
	}
}

// The threat this closes: node B trusts the sender, so a signature check alone
// accepts a snapshot that was built for node A.
func TestHeartbeatForOneNodeIsRejectedByAnother(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	trustPeers(t, store, peerA, peerB)
	key := newTestKeypair(t)
	builder := heartbeatBuilder(t, store, key)

	forA, err := builder.BuildFor(ctx, time.Now(), peerA)
	if err != nil {
		t.Fatal(err)
	}

	if err := forA.VerifyDirected(key.public, senderNode, peerA); err != nil {
		t.Fatalf("node A rejected its own heartbeat: %v", err)
	}
	err = forA.VerifyDirected(key.public, senderNode, peerB)
	if err == nil {
		t.Fatal("node B accepted a heartbeat addressed to node A")
	}
	if !errors.Is(err, protocol.ErrNotAddressed) {
		t.Errorf("error = %v; callers branch on ErrNotAddressed", err)
	}
}

// Redirecting a signed envelope must fail, whether the recipient is replaced,
// removed, or swapped for a node that also trusts the sender.
func TestDirectedVerificationRefusesEveryRedirection(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	trustPeers(t, store, peerA, peerB)
	key := newTestKeypair(t)
	builder := heartbeatBuilder(t, store, key)

	cases := map[string]func(protocol.Envelope) (protocol.Envelope, string){
		"recipient substituted": func(e protocol.Envelope) (protocol.Envelope, string) {
			e.RecipientNodeID = peerB
			return e, peerB
		},
		"recipient removed": func(e protocol.Envelope) (protocol.Envelope, string) {
			e.RecipientNodeID = ""
			return e, peerB
		},
		"recipient removed and nothing expected": func(e protocol.Envelope) (protocol.Envelope, string) {
			e.RecipientNodeID = ""
			return e, ""
		},
		"expected recipient left empty": func(e protocol.Envelope) (protocol.Envelope, string) {
			return e, ""
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			signed, err := builder.BuildFor(ctx, time.Now(), peerA)
			if err != nil {
				t.Fatal(err)
			}
			envelope, expected := mutate(signed)
			if err := envelope.VerifyDirected(key.public, senderNode, expected); err == nil {
				t.Fatalf("verification accepted %s: recipient=%q expected=%q",
					name, envelope.RecipientNodeID, expected)
			}
		})
	}
}

// A sender signature is not recipient authorization. The API that only checks
// the signature must refuse to answer for a directed envelope at all, so no
// receiver can reach an accept decision without checking who the envelope names.
func TestVerifySenderRefusesToAnswerForADirectedEnvelope(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	trustPeers(t, store, peerA)
	key := newTestKeypair(t)

	directed, err := heartbeatBuilder(t, store, key).BuildFor(ctx, time.Now(), peerA)
	if err != nil {
		t.Fatal(err)
	}
	if err := directed.VerifySender(key.public, senderNode); err == nil {
		t.Fatal("VerifySender accepted a directed envelope; a signature check is not authorization")
	}

	// An undirected envelope is still verifiable that way: this is a guard on
	// directed envelopes, not a second signature rule.
	undirected, err := protocol.NewEnvelope(senderNode, protocol.TypeNodeHello, protocol.At(time.Now()),
		protocol.HelloPayload{Node: descriptor(senderNode, key.public)}, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := undirected.VerifySender(key.public, senderNode); err != nil {
		t.Errorf("VerifySender rejected an undirected envelope: %v", err)
	}
}

// A heartbeat with no recipient must be unrepresentable, not merely discouraged:
// the undirected constructor refuses the type outright.
func TestUndirectedHeartbeatsCannotBeBuilt(t *testing.T) {
	key := newTestKeypair(t)
	if _, err := protocol.NewEnvelope(senderNode, protocol.TypeNodeHeartbeat, protocol.At(time.Now()),
		protocol.HeartbeatPayload{}, key); err == nil {
		t.Error("NewEnvelope produced an undirected heartbeat")
	}

	// A recipient that is not a usable node identifier is refused too: an
	// envelope addressed to " " names nobody a receiver could match.
	for name, recipient := range map[string]string{
		"empty":        "",
		"too short":    "node_x",
		"control byte": "node_0123456789abcd\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := protocol.NewDirectedEnvelope(senderNode, recipient, protocol.TypeNodeHeartbeat,
				protocol.At(time.Now()), protocol.HeartbeatPayload{}, key); err == nil {
				t.Errorf("NewDirectedEnvelope accepted recipient %q", recipient)
			}
		})
	}
}

// BuildFor still refuses a peer this owner has not paired with, and refuses it
// before any session data is read.
func TestBuildForStillRefusesUntrustedRecipients(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	builder := heartbeatBuilder(t, store, newTestKeypair(t))

	_, err := builder.BuildFor(ctx, time.Now(), "node_stranger0000000")
	if !errors.Is(err, protocol.ErrPeerNotTrusted) {
		t.Errorf("error = %v; want ErrPeerNotTrusted", err)
	}
}

// The published schema and the runtime output have to agree: a heartbeat
// without a recipient must fail validation, and the one this build produces
// must pass it.
func TestSchemaRequiresAHeartbeatRecipient(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	trustPeers(t, store, peerA)
	schema := compileSchema(t)

	directed, err := heartbeatBuilder(t, store, newTestKeypair(t)).BuildFor(ctx, time.Now(), peerA)
	if err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(roundTrip(t, directed)); err != nil {
		t.Fatalf("a directed heartbeat does not satisfy the schema: %v", err)
	}

	document, ok := roundTrip(t, directed).(map[string]any)
	if !ok {
		t.Fatalf("envelope did not round trip to an object")
	}
	delete(document, "recipientNodeId")
	if err := schema.Validate(document); err == nil {
		t.Error("the schema accepted a heartbeat that names no recipient")
	}

	document["recipientNodeId"] = "short"
	if err := schema.Validate(document); err == nil {
		t.Error("the schema accepted a recipient that is not a node identifier")
	}
}

// The signed bytes must cover the recipient, so redirecting an envelope breaks
// the signature rather than only the equality check above.
func TestSignableBytesCoverTheRecipient(t *testing.T) {
	base := protocol.Envelope{
		ProtocolVersion: protocol.Version,
		MessageID:       "msg_1",
		Type:            protocol.TypeNodeHeartbeat,
		SentAt:          time.Date(2026, 8, 28, 2, 0, 0, 0, time.UTC),
		NodeID:          senderNode,
		RecipientNodeID: peerA,
		Payload:         []byte(`{}`),
	}
	redirected := base
	redirected.RecipientNodeID = peerB

	if string(protocol.SignableBytes(base)) == string(protocol.SignableBytes(redirected)) {
		t.Fatal("two recipients produced the same signable bytes")
	}
	if !strings.Contains(string(protocol.SignableBytes(base)), peerA) {
		t.Error("the signable bytes do not carry the recipient")
	}
}
