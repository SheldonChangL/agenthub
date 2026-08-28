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
	"agenthub.local/agenthub/internal/model"
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

// ErrNotAddressed marks an envelope that is authentic but was built for another
// node. It is deliberately distinct from ErrUnsigned: the sender is who it
// claims to be, and the envelope is still not this node's to accept.
var ErrNotAddressed = errors.New("envelope is not addressed to this node")

// ErrUndirected marks an envelope that must name a recipient and does not.
var ErrUndirected = errors.New("envelope names no recipient")

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

// NewEnvelope builds and signs an envelope that names no recipient.
//
// Signing happens here rather than at the transport so an unsigned envelope is
// not something a caller can produce by forgetting a step.
//
// Types whose protocol work has not happened yet may travel undirected, but
// node.heartbeat may not: a heartbeat carries the export view of one peer, and
// an undirected one is replayable to every peer that trusts the sender. That is
// refused here rather than left to a reviewer to notice at the call site.
func NewEnvelope(nodeID, envelopeType string, sentAt SentAt, payload any, signer Signer) (Envelope, error) {
	if envelopeType == TypeNodeHeartbeat {
		return Envelope{}, fmt.Errorf("%w: %s must be built with NewDirectedEnvelope", ErrUndirected, TypeNodeHeartbeat)
	}
	return newEnvelope(nodeID, "", envelopeType, sentAt, payload, signer)
}

// NewDirectedEnvelope builds and signs an envelope addressed to one node.
//
// The recipient is covered by the signature, so an envelope built for one peer
// cannot be handed to another: redirecting it invalidates it. The recipient must
// be a usable node identifier, because a receiver matches it against its own ID
// and a value nothing can match is an envelope with no reachable destination.
func NewDirectedEnvelope(nodeID, recipientNodeID, envelopeType string, sentAt SentAt, payload any, signer Signer) (Envelope, error) {
	if err := model.ValidateNodeID(recipientNodeID); err != nil {
		return Envelope{}, fmt.Errorf("%w: recipient: %w", ErrUndirected, err)
	}
	return newEnvelope(nodeID, recipientNodeID, envelopeType, sentAt, payload, signer)
}

func newEnvelope(nodeID, recipientNodeID, envelopeType string, sentAt SentAt, payload any, signer Signer) (Envelope, error) {
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
		RecipientNodeID: recipientNodeID,
		Payload:         json.RawMessage(encoded),
	}
	envelope.Signature = base64.StdEncoding.EncodeToString(signer.Sign(envelope.signable()))
	return envelope, nil
}

// VerifySender checks that this envelope was signed by the holder of publicKey
// and that the key belongs to the node the envelope claims to come from.
//
// It answers one question — is this authentically from that node — and nothing
// about whether the receiver may act on it. A directed envelope is refused
// outright rather than reported as authentic, so no receiver can reach an accept
// decision on a heartbeat without also checking who it was addressed to; that is
// VerifyDirected's job.
//
// The caller supplies the key it already trusts for that node ID. Reading the
// key out of the envelope would let any sender claim any identity.
func (e Envelope) VerifySender(publicKey ed25519.PublicKey, expectedNodeID string) error {
	if e.RecipientNodeID != "" {
		return fmt.Errorf(
			"%w: envelope is addressed to %q; verify it with VerifyDirected, which checks the recipient",
			ErrNotAddressed, e.RecipientNodeID)
	}
	return e.verifySignature(publicKey, expectedNodeID)
}

// VerifyDirected checks that this envelope is authentically from expectedNodeID
// and was built for localNodeID, the node doing the verification.
//
// Both halves are required. A signature proves who wrote the envelope, not who
// it was for: without the recipient check, a snapshot a sender built for one
// peer is a valid snapshot for every peer that trusts that sender.
func (e Envelope) VerifyDirected(publicKey ed25519.PublicKey, expectedNodeID, localNodeID string) error {
	// An empty expectation would match an envelope that names nobody, which is
	// how "we do not know who we are yet" turns into "anything is for us".
	if localNodeID == "" {
		return fmt.Errorf("%w: no local node id to match against", ErrNotAddressed)
	}
	if e.RecipientNodeID != localNodeID {
		return fmt.Errorf("%w: envelope is addressed to %q, not %q",
			ErrNotAddressed, e.RecipientNodeID, localNodeID)
	}
	return e.verifySignature(publicKey, expectedNodeID)
}

func (e Envelope) verifySignature(publicKey ed25519.PublicKey, expectedNodeID string) error {
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

// SignableBytes is the byte sequence a signature covers.
//
// It is built field by field rather than by re-encoding the struct, because a
// signature has to be reproducible by a receiver that only ever saw JSON — and
// by an implementation in another language. Each field is length-prefixed so no
// value can be shifted into its neighbour: "ab" + "c" and "a" + "bc" must not
// produce the same bytes.
//
// The recipient is one of those fields, so a signature covers who the envelope
// was for. An undirected envelope contributes an empty value there rather than
// omitting the field: a receiver reproducing these bytes must not have to guess
// whether a field is absent or empty.
//
// It is exported because those bytes are a contract with other implementations,
// documented in docs/broker-protocol.schema.json, not an implementation detail.
func SignableBytes(envelope Envelope) []byte { return envelope.signable() }

func (e Envelope) signable() []byte {
	var buffer bytes.Buffer
	buffer.WriteString(signatureDomain)
	for _, field := range [][]byte{
		[]byte(e.ProtocolVersion),
		[]byte(e.MessageID),
		[]byte(e.Type),
		[]byte(e.SentAt.UTC().Format(time.RFC3339Nano)),
		[]byte(e.NodeID),
		[]byte(e.RecipientNodeID),
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
