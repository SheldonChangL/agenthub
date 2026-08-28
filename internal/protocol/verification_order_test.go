package protocol_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/protocol"
)

// Authenticity is decided before the address, and the two answers are not
// interchangeable.
//
// ErrNotAddressed says "this is genuinely from that node, and it was built for
// someone else" — a receiver may reasonably count, log or ignore that as traffic
// from a peer it knows. If a forged envelope could produce it, that reading
// would be wrong, and the error would have turned an unauthenticated stranger
// into a known peer. So every failure of authenticity — an unusable key, the
// wrong sender, a missing or malformed signature, or a mutation of any signed
// field — must answer ErrUnsigned first, whatever the address says.
func TestAuthenticityIsDecidedBeforeTheAddress(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	trustPeers(t, store, peerA)
	key := newTestKeypair(t)
	other := newTestKeypair(t)

	forA := func(t *testing.T) protocol.Envelope {
		t.Helper()
		envelope, err := heartbeatBuilder(t, store, key).BuildFor(ctx, time.Now(), peerA)
		if err != nil {
			t.Fatal(err)
		}
		return envelope
	}

	// Every case below is checked at node B, which is not the recipient. Only
	// the first is authentic, so only the first may be reported as a mis-addressed
	// envelope; the rest are forgeries that happen to name the wrong node.
	cases := map[string]struct {
		mutate func(protocol.Envelope) (protocol.Envelope, ed25519.PublicKey)
		want   error
	}{
		"intact heartbeat for another node": {
			mutate: func(e protocol.Envelope) (protocol.Envelope, ed25519.PublicKey) { return e, key.public },
			want:   protocol.ErrNotAddressed,
		},
		"recipient rewritten to this node": {
			mutate: func(e protocol.Envelope) (protocol.Envelope, ed25519.PublicKey) {
				e.RecipientNodeID = peerB
				return e, key.public
			},
			want: protocol.ErrUnsigned,
		},
		"signature removed": {
			mutate: func(e protocol.Envelope) (protocol.Envelope, ed25519.PublicKey) {
				e.Signature = ""
				return e, key.public
			},
			want: protocol.ErrUnsigned,
		},
		"signature malformed": {
			mutate: func(e protocol.Envelope) (protocol.Envelope, ed25519.PublicKey) {
				e.Signature = "!!!"
				return e, key.public
			},
			want: protocol.ErrUnsigned,
		},
		"another node's key": {
			mutate: func(e protocol.Envelope) (protocol.Envelope, ed25519.PublicKey) { return e, other.public },
			want:   protocol.ErrUnsigned,
		},
		"unusable key": {
			mutate: func(e protocol.Envelope) (protocol.Envelope, ed25519.PublicKey) { return e, nil },
			want:   protocol.ErrUnsigned,
		},
		"payload rewritten": {
			mutate: func(e protocol.Envelope) (protocol.Envelope, ed25519.PublicKey) {
				e.Payload = []byte(`{"sequence":1}`)
				return e, key.public
			},
			want: protocol.ErrUnsigned,
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			envelope, public := testCase.mutate(forA(t))
			err := envelope.VerifyDirected(public, senderNode, peerB)
			if err == nil {
				t.Fatal("verification accepted an envelope built for another node")
			}
			if !errors.Is(err, testCase.want) {
				t.Errorf("error = %v; want %v", err, testCase.want)
			}
			if testCase.want == protocol.ErrUnsigned && errors.Is(err, protocol.ErrNotAddressed) {
				t.Errorf("a forgery was reported as an authentic envelope for another node: %v", err)
			}
		})
	}
}

// VerifySender must keep refusing a directed envelope — a signature check is not
// authorization — without classifying a forgery as an authentic directed
// envelope on the way there.
func TestVerifySenderDecidesAuthenticityFirst(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	trustPeers(t, store, peerA)
	key := newTestKeypair(t)

	authentic, err := heartbeatBuilder(t, store, key).BuildFor(ctx, time.Now(), peerA)
	if err != nil {
		t.Fatal(err)
	}
	err = authentic.VerifySender(key.public, senderNode)
	if !errors.Is(err, protocol.ErrNotAddressed) {
		t.Errorf("error = %v; a valid directed envelope must still be refused as mis-addressed", err)
	}

	forged := authentic
	forged.Signature = ""
	err = forged.VerifySender(key.public, senderNode)
	if !errors.Is(err, protocol.ErrUnsigned) {
		t.Errorf("error = %v; an unsigned directed envelope must be reported unsigned", err)
	}
	if errors.Is(err, protocol.ErrNotAddressed) {
		t.Error("an unsigned envelope was classified as an authentic directed envelope")
	}
}

// The local node ID is an identity, not a string to compare. A receiver that
// does not yet know its own ID, or holds an unusable one, must not be able to
// match a deserialized envelope carrying the same unusable value.
func TestDirectedVerificationRefusesAnUnusableLocalNodeID(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	trustPeers(t, store, peerA)
	key := newTestKeypair(t)

	authentic, err := heartbeatBuilder(t, store, key).BuildFor(ctx, time.Now(), peerA)
	if err != nil {
		t.Fatal(err)
	}

	for name, local := range map[string]string{
		"empty":        "",
		"too short":    "node_x",
		"control byte": "node_0123456789abcd\n",
	} {
		t.Run(name, func(t *testing.T) {
			// The envelope names the same unusable value, so a plain equality
			// check would accept it.
			forged := authentic
			forged.RecipientNodeID = local
			if err := forged.VerifyDirected(key.public, senderNode, local); err == nil {
				t.Fatalf("verification accepted %q as a destination", local)
			}

			// And an authentic envelope is not accepted by such a receiver either.
			if err := authentic.VerifyDirected(key.public, senderNode, local); err == nil {
				t.Fatalf("a receiver identifying itself as %q accepted an envelope", local)
			}
		})
	}
}
