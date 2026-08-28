package protocol

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"agenthub.local/agenthub/internal/id"
)

const (
	TypeNodeHello   = "node.hello"
	TypePairRequest = "pair.request"
	TypePairApprove = "pair.approve"
	TypePairReject  = "pair.reject"
	TypePairRevoke  = "pair.revoke"
)

// ErrUnsigned marks an envelope whose signature is missing, malformed, or does
// not match the sender's key. Every one of those means the same thing to a
// receiver: nothing here is known to come from the node it names.
var ErrUnsigned = errors.New("envelope is not authentically signed")

// NodeDescriptor is what a node advertises about itself before it is trusted.
// It carries no session data on purpose: an unpaired node learns nothing about
// what runs here.
type NodeDescriptor struct {
	NodeID      string `json:"nodeId"`
	DisplayName string `json:"displayName"`
	Platform    string `json:"platform"`
	PublicKey   string `json:"publicKey"`
	Fingerprint string `json:"fingerprint"`
}

type HelloPayload struct {
	Node NodeDescriptor `json:"node"`
}

type PairRequestPayload struct {
	Node NodeDescriptor `json:"node"`
}

type PairApprovePayload struct {
	Node      NodeDescriptor `json:"node"`
	RequestID string         `json:"requestId"`
}

type PairRejectPayload struct {
	RequestID string `json:"requestId"`
	Reason    string `json:"reason,omitempty"`
}

type PairRevokePayload struct {
	NodeID string `json:"nodeId"`
}

// Signer produces the signature that makes an envelope attributable.
type Signer interface {
	Sign(message []byte) []byte
}

// NewEnvelope builds and signs an envelope.
//
// Signing happens here rather than at the transport so an unsigned envelope is
// not something a caller can produce by forgetting a step.
func NewEnvelope(nodeID, envelopeType string, sentAt SentAt, payload any, signer Signer) (Envelope, error) {
	messageID, err := id.New("msg_")
	if err != nil {
		return Envelope{}, fmt.Errorf("generate message id: %w", err)
	}
	// The payload is encoded once, here, and the bytes are what travels and
	// what is signed. Re-encoding a decoded payload would not reproduce them:
	// a Go struct marshals in field order while the map it decodes into
	// marshals in key order, and a uint64 comes back as a float64.
	encoded, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, fmt.Errorf("encode payload: %w", err)
	}
	envelope := Envelope{
		ProtocolVersion: Version,
		MessageID:       messageID,
		Type:            envelopeType,
		SentAt:          sentAt.Time(),
		NodeID:          nodeID,
		Payload:         json.RawMessage(encoded),
	}
	envelope.Signature = base64.StdEncoding.EncodeToString(signer.Sign(envelope.signable()))
	return envelope, nil
}

// Verify checks that this envelope was signed by the holder of publicKey and
// that the key belongs to the node the envelope claims to come from.
//
// The caller supplies the key it already trusts for that node ID. Reading the
// key out of the envelope would let any sender claim any identity.
func (e Envelope) Verify(publicKey ed25519.PublicKey, expectedNodeID string) error {
	// ed25519.Verify panics on a wrong-sized key, and the natural shape of a
	// receiver — look the node up, verify with what came back — hands it a zero
	// value for an unknown node. An unknown sender must be refused, not able to
	// crash the process.
	if len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: no usable key for node %q", ErrUnsigned, expectedNodeID)
	}
	if e.NodeID == "" || e.NodeID != expectedNodeID {
		return fmt.Errorf("%w: envelope claims node %q, expected %q", ErrUnsigned, e.NodeID, expectedNodeID)
	}
	if e.Signature == "" {
		return fmt.Errorf("%w: no signature", ErrUnsigned)
	}
	signature, err := base64.StdEncoding.DecodeString(e.Signature)
	if err != nil {
		return fmt.Errorf("%w: signature is not base64", ErrUnsigned)
	}
	if !ed25519.Verify(publicKey, e.signable(), signature) {
		return fmt.Errorf("%w: signature does not match node %q", ErrUnsigned, e.NodeID)
	}
	return nil
}

// signable is the byte sequence a signature covers.
//
// It is built field by field rather than by re-encoding the struct, because a
// signature has to be reproducible by a receiver that only ever saw JSON — and
// by an implementation in another language. Each field is length-prefixed so no
// value can be shifted into its neighbour: "ab" + "c" and "a" + "bc" must not
// produce the same bytes.
func (e Envelope) signable() []byte {
	var buffer bytes.Buffer
	buffer.WriteString(signatureDomain)
	for _, field := range [][]byte{
		[]byte(e.ProtocolVersion),
		[]byte(e.MessageID),
		[]byte(e.Type),
		[]byte(e.SentAt.UTC().Format(time.RFC3339Nano)),
		[]byte(e.NodeID),
		e.Payload,
	} {
		buffer.WriteString(strconv.Itoa(len(field)))
		buffer.WriteByte(':')
		buffer.Write(field)
	}
	return buffer.Bytes()
}

// signatureDomain keeps these signatures from being valid anywhere else, so a
// signature produced for one purpose cannot be replayed as another.
const signatureDomain = "agenthub.broker/v1alpha1/envelope\n"

// DecodePayload reads a typed payload out of an envelope.
//
// The payload is raw bytes so the signature stays reproducible; every reader
// goes through here rather than asserting on a Go type that only exists on the
// sending side.
func DecodePayload[T any](envelope Envelope) (T, error) {
	var payload T
	if len(envelope.Payload) == 0 {
		return payload, fmt.Errorf("envelope %q has no payload", envelope.Type)
	}
	if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
		return payload, fmt.Errorf("decode %q payload: %w", envelope.Type, err)
	}
	return payload, nil
}
