package protocol_test

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/protocol"
)

// reencodePayloadForMutationTest simulates a transport that rebuilds the payload
// from a decoded value instead of carrying the bytes through. Flipping it to
// true must break TestSignatureSurvivesTheWire; that is what proves the test
// measures the property the fix depends on.
const reencodePayloadForMutationTest = false

func descriptor(nodeID string, public ed25519.PublicKey) protocol.NodeDescriptor {
	return protocol.NodeDescriptor{
		NodeID:      nodeID,
		DisplayName: "peer",
		Platform:    "linux/amd64",
		PublicKey:   "AAAA",
		Fingerprint: "2DCF 9604 DBA9 778A 6DDD 035B",
	}
}

func TestSignedEnvelopeVerifiesAgainstItsNode(t *testing.T) {
	key := newTestKeypair(t)
	envelope, err := protocol.NewEnvelope("node_0123456789abcdef0123", protocol.TypePairRequest,
		protocol.At(time.Now()), protocol.PairRequestPayload{Node: descriptor("node_0123456789abcdef0123", key.public)}, key)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Signature == "" {
		t.Fatal("NewEnvelope produced an unsigned envelope")
	}
	if err := envelope.Verify(key.public, "node_0123456789abcdef0123"); err != nil {
		t.Errorf("Verify() = %v", err)
	}
}

// Every way an envelope can fail to be attributable must land on one answer:
// nothing here is known to come from the node it names.
func TestVerifyRefusesEveryUnattributableEnvelope(t *testing.T) {
	key := newTestKeypair(t)
	other := newTestKeypair(t)
	const nodeID = "node_0123456789abcdef0123"

	sign := func() protocol.Envelope {
		envelope, err := protocol.NewEnvelope(nodeID, protocol.TypeNodeHello, protocol.At(time.Now()),
			protocol.HelloPayload{Node: descriptor(nodeID, key.public)}, key)
		if err != nil {
			t.Fatal(err)
		}
		return envelope
	}

	cases := map[string]func(protocol.Envelope) (protocol.Envelope, ed25519.PublicKey, string){
		"another node's key": func(e protocol.Envelope) (protocol.Envelope, ed25519.PublicKey, string) {
			return e, other.public, nodeID
		},
		"claims a different node": func(e protocol.Envelope) (protocol.Envelope, ed25519.PublicKey, string) {
			return e, key.public, "node_somewhere_else000000"
		},
		"signature removed": func(e protocol.Envelope) (protocol.Envelope, ed25519.PublicKey, string) {
			e.Signature = ""
			return e, key.public, nodeID
		},
		"signature not base64": func(e protocol.Envelope) (protocol.Envelope, ed25519.PublicKey, string) {
			e.Signature = "!!!"
			return e, key.public, nodeID
		},
		"payload edited after signing": func(e protocol.Envelope) (protocol.Envelope, ed25519.PublicKey, string) {
			edited, err := json.Marshal(protocol.HelloPayload{Node: descriptor("node_impostor00000000000", key.public)})
			if err != nil {
				t.Fatal(err)
			}
			e.Payload = edited
			return e, key.public, nodeID
		},
		"type edited after signing": func(e protocol.Envelope) (protocol.Envelope, ed25519.PublicKey, string) {
			e.Type = protocol.TypePairApprove
			return e, key.public, nodeID
		},
		"timestamp edited after signing": func(e protocol.Envelope) (protocol.Envelope, ed25519.PublicKey, string) {
			e.SentAt = e.SentAt.Add(time.Hour)
			return e, key.public, nodeID
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			envelope, public, expected := mutate(sign())
			err := envelope.Verify(public, expected)
			if err == nil {
				t.Fatalf("Verify accepted an unattributable envelope: type=%q nodeId=%q expected=%q signature=%q",
					envelope.Type, envelope.NodeID, expected, envelope.Signature)
			}
			if !errors.Is(err, protocol.ErrUnsigned) {
				t.Errorf("error = %v; callers branch on ErrUnsigned", err)
			}
		})
	}
}

// A pairing request carries the requester's key, but holding the key must not
// by itself confer identity: verification always uses the key the receiver
// already trusts for that node.
func TestVerifyIgnoresKeysCarriedInsideThePayload(t *testing.T) {
	impostor := newTestKeypair(t)
	const victim = "node_victim00000000000000"

	envelope, err := protocol.NewEnvelope(victim, protocol.TypePairRequest, protocol.At(time.Now()),
		protocol.PairRequestPayload{Node: descriptor(victim, impostor.public)}, impostor)
	if err != nil {
		t.Fatal(err)
	}

	trusted := newTestKeypair(t)
	if err := envelope.Verify(trusted.public, victim); err == nil {
		t.Error("an envelope signed by an impostor verified against the node's real key")
	}
}

func TestEnvelopeRoundTripsThroughJSON(t *testing.T) {
	key := newTestKeypair(t)
	const nodeID = "node_0123456789abcdef0123"
	envelope, err := protocol.NewEnvelope(nodeID, protocol.TypeNodeHello, protocol.At(time.Now()),
		protocol.HelloPayload{Node: descriptor(nodeID, key.public)}, key)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"signature"`) {
		t.Error("the encoded envelope has no signature field")
	}
	if strings.Contains(string(encoded), "sessions") {
		t.Error("a hello envelope mentioned sessions")
	}
}

// The receiver only ever sees JSON. Signing a Go value would make every
// cross-process verification fail: a struct marshals in field order while the
// map it decodes into marshals in key order, and a uint64 returns as a float64.
func TestSignatureSurvivesTheWire(t *testing.T) {
	key := newTestKeypair(t)
	const nodeID = "node_0123456789abcdef0123"

	for name, payload := range map[string]any{
		"hello": protocol.HelloPayload{Node: descriptor(nodeID, key.public)},
		"pair request": protocol.PairRequestPayload{
			Node: descriptor("node_peer0000000000000", key.public)},
		"heartbeat with a large sequence": protocol.HeartbeatPayload{
			Sequence:     1 << 60,
			ExpiresAt:    time.Now().UTC().Add(time.Minute),
			Capabilities: []string{"session.list"},
			Sessions:     []protocol.SessionSummary{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			sent, err := protocol.NewEnvelope(nodeID, protocol.TypeNodeHello, protocol.At(time.Now()), payload, key)
			if err != nil {
				t.Fatal(err)
			}

			// Exactly what a transport does: encode, ship, decode.
			wire, err := json.Marshal(sent)
			if err != nil {
				t.Fatal(err)
			}
			var received protocol.Envelope
			if err := json.Unmarshal(wire, &received); err != nil {
				t.Fatal(err)
			}
			if reencodePayloadForMutationTest {
				var decoded any
				_ = json.Unmarshal(received.Payload, &decoded)
				reencoded, _ := json.Marshal(decoded)
				received.Payload = reencoded
			}

			if err := received.Verify(key.public, nodeID); err != nil {
				t.Fatalf("a decoded envelope did not verify: %v", err)
			}

			// The property that makes the above possible: the payload bytes
			// are carried through untouched. If the payload were re-encoded
			// from a Go value on either side, these would differ — a struct
			// marshals in field order, the map it decodes into marshals in key
			// order — and every signature would fail.
			if !bytes.Equal(sent.Payload, received.Payload) {
				t.Errorf("payload bytes changed in transit:\n sent: %s\n got:  %s", sent.Payload, received.Payload)
			}
		})
	}
}

// A heartbeat's sequence must survive the wire exactly; a float64 round trip
// would corrupt large values and break ordering.
func TestHeartbeatSequenceSurvivesTheWire(t *testing.T) {
	key := newTestKeypair(t)
	const nodeID = "node_0123456789abcdef0123"
	const sequence = uint64(1) << 60

	sent, err := protocol.NewEnvelope(nodeID, protocol.TypeNodeHeartbeat, protocol.At(time.Now()),
		protocol.HeartbeatPayload{
			Sequence: sequence, ExpiresAt: time.Now().UTC(),
			Capabilities: []string{"session.list"}, Sessions: []protocol.SessionSummary{},
		}, key)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(sent)
	if err != nil {
		t.Fatal(err)
	}
	var received protocol.Envelope
	if err := json.Unmarshal(wire, &received); err != nil {
		t.Fatal(err)
	}
	payload, err := protocol.DecodePayload[protocol.HeartbeatPayload](received)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Sequence != sequence {
		t.Errorf("sequence = %d, want %d", payload.Sequence, sequence)
	}
}

// An unknown node yields a zero-value key, and ed25519.Verify panics on one.
// A stranger must be refused, not able to crash the process.
func TestVerifyRefusesUnusableKeysWithoutPanicking(t *testing.T) {
	key := newTestKeypair(t)
	const nodeID = "node_0123456789abcdef0123"
	envelope, err := protocol.NewEnvelope(nodeID, protocol.TypeNodeHello, protocol.At(time.Now()),
		protocol.HelloPayload{Node: descriptor(nodeID, key.public)}, key)
	if err != nil {
		t.Fatal(err)
	}

	for name, public := range map[string]ed25519.PublicKey{
		"nil key":       nil,
		"empty key":     {},
		"truncated key": key.public[:16],
		"oversized key": append(append(ed25519.PublicKey{}, key.public...), 0),
	} {
		t.Run(name, func(t *testing.T) {
			err := envelope.Verify(public, nodeID)
			if err == nil {
				t.Fatal("Verify accepted an unusable key")
			}
			if !errors.Is(err, protocol.ErrUnsigned) {
				t.Errorf("error = %v", err)
			}
		})
	}
}
