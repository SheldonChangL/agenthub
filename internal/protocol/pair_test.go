package protocol_test

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/identity"
	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/protocol"
)

const (
	requesterID = "node_0123456789abcdef0123"
	receiverID  = "node_abcdef01234567890abc"
)

func pairBuilder(t *testing.T, nodeID string, key testKeypair) *protocol.HeartbeatBuilder {
	t.Helper()
	return protocol.NewHeartbeatBuilder(nil, model.NodeIdentity{
		ID: nodeID, DisplayName: "peer", Platform: "linux/amd64",
		PublicKey:   identity.EncodePublicKey(key.public),
		Fingerprint: identity.Fingerprint(key.public),
	}, key)
}

// The three envelopes this exchange puts on the wire must match the published
// schema, address and all: an implementation in another language reads that
// file and nothing else.
func TestPairingEnvelopesMatchTheSchema(t *testing.T) {
	schema := compileSchema(t)
	key := newTestKeypair(t)
	builder := pairBuilder(t, requesterID, key)
	now := time.Now()

	build := map[string]func() (protocol.Envelope, error){
		"request with an address": func() (protocol.Envelope, error) {
			return builder.BuildPairRequest(now, "192.168.1.42:7463")
		},
		"request without one": func() (protocol.Envelope, error) {
			return builder.BuildPairRequest(now, "")
		},
		"approve": func() (protocol.Envelope, error) {
			return builder.BuildPairApprove(now, receiverID, "pair_0123456789abcdef")
		},
		"reject": func() (protocol.Envelope, error) {
			return builder.BuildPairReject(now, receiverID, "pair_0123456789abcdef", "declined")
		},
	}
	for name, make := range build {
		t.Run(name, func(t *testing.T) {
			envelope, err := make()
			if err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(roundTrip(t, envelope)); err != nil {
				t.Fatalf("schema rejected the %s envelope: %v", name, err)
			}
		})
	}
}

// ReadPairRequest verifies against the key the envelope carries, which proves
// possession of that key and nothing about which machine holds it. What it must
// not accept is an envelope whose signature, sender and descriptor do not all
// name one node: that is where a key could be bound to somebody else's id.
func TestReadPairRequestRefusesWhatDoesNotHoldTogether(t *testing.T) {
	key := newTestKeypair(t)
	other := newTestKeypair(t)

	valid, err := pairBuilder(t, requesterID, key).BuildPairRequest(time.Now(), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, public, err := protocol.ReadPairRequest(valid); err != nil {
		t.Fatalf("a well-formed request did not read: %v", err)
	} else if !public.Equal(key.public) {
		t.Fatal("the key returned is not the key in the request")
	}

	cases := map[string]struct {
		envelope func() protocol.Envelope
		want     error
	}{
		"descriptor names another node than the signature": {
			envelope: func() protocol.Envelope {
				envelope, err := protocol.NewEnvelope(requesterID, protocol.TypePairRequest,
					protocol.At(time.Now()),
					protocol.PairRequestPayload{Node: descriptor(receiverID, key.public)}, key)
				if err != nil {
					t.Fatal(err)
				}
				return envelope
			},
			want: protocol.ErrPairRequestUnusable,
		},
		"signed by a key that is not the one described": {
			envelope: func() protocol.Envelope {
				envelope, err := protocol.NewEnvelope(requesterID, protocol.TypePairRequest,
					protocol.At(time.Now()),
					protocol.PairRequestPayload{Node: descriptor(requesterID, key.public)}, other)
				if err != nil {
					t.Fatal(err)
				}
				return envelope
			},
			want: protocol.ErrUnsigned,
		},
		"key that is not a key": {
			envelope: func() protocol.Envelope {
				node := descriptor(requesterID, key.public)
				node.PublicKey = base64.StdEncoding.EncodeToString([]byte("short"))
				envelope, err := protocol.NewEnvelope(requesterID, protocol.TypePairRequest,
					protocol.At(time.Now()), protocol.PairRequestPayload{Node: node}, key)
				if err != nil {
					t.Fatal(err)
				}
				return envelope
			},
			want: protocol.ErrPairRequestUnusable,
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := protocol.ReadPairRequest(test.envelope()); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

// An approval is directed, so one collected from one exchange is not a valid
// approval in another. Undirected, it would be: the requester verifies the
// signature of a node it is already talking to.
func TestPairApproveIsAddressedToTheRequester(t *testing.T) {
	key := newTestKeypair(t)
	envelope, err := pairBuilder(t, receiverID, key).BuildPairApprove(time.Now(), requesterID, "pair_1")
	if err != nil {
		t.Fatal(err)
	}
	if err := envelope.VerifyDirected(key.public, receiverID, requesterID); err != nil {
		t.Fatalf("the requester could not verify its own approval: %v", err)
	}
	if err := envelope.VerifyDirected(key.public, receiverID, "node_someoneelse00000000"); !errors.Is(err, protocol.ErrNotAddressed) {
		t.Fatalf("another node accepted this approval: %v", err)
	}
}
