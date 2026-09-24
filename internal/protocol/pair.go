package protocol

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"agenthub.local/agenthub/internal/identity"
	"agenthub.local/agenthub/internal/model"
)

// ErrPairRequestUnusable marks a pair.request whose contents do not hold
// together: no key, a key that is not a key, or a signature that belongs to a
// different node than the descriptor names.
//
// Distinct from ErrUnsigned because the receiver reacts the same way — refuse —
// but the owner reading the log needs to know whether something forged a
// signature or something sent nonsense.
var ErrPairRequestUnusable = errors.New("pair request does not describe a node this one can verify")

// Descriptor is what this node says about itself to a machine that has not
// paired with it.
//
// It carries the public key and the fingerprint of that key. Both are safe to
// publish and neither is evidence on its own: a receiver derives the
// fingerprint again from the key it actually received, and a person compares
// that against the other machine's screen.
func (b *HeartbeatBuilder) Descriptor() NodeDescriptor {
	return NodeDescriptor{
		NodeID:      b.node.ID,
		DisplayName: b.node.DisplayName,
		Platform:    b.node.Platform,
		PublicKey:   b.node.PublicKey,
		Fingerprint: b.node.Fingerprint,
	}
}

// BuildPairRequest signs this node's request to be trusted by another.
//
// Undirected, because the requester does not yet know the other node's
// identifier — finding that out is part of what the exchange does. Every other
// signed thing this node produces names a recipient; this one cannot, and that
// is exactly why an approval must be directed: see BuildPairApprove.
//
// addresses is where this node answers, preferred first, so the far side can
// record them when it approves. They are claims, checked by the receiver
// against its own address policy, and none simply means there is nothing to
// record. The first also goes in address, which is all an older receiver reads.
func (b *HeartbeatBuilder) BuildPairRequest(sentAt time.Time, addresses []string) (Envelope, error) {
	addresses = firstPairAddresses(addresses)
	payload := PairRequestPayload{Node: b.Descriptor(), Addresses: addresses}
	if len(addresses) > 0 {
		payload.Address = addresses[0]
	}
	return NewEnvelope(b.node.ID, TypePairRequest, At(sentAt), payload, b.signer)
}

// firstPairAddresses drops empties and repeats and keeps MaxPairAddresses.
// Nil when nothing is left, so the field is omitted rather than sent empty.
func firstPairAddresses(addresses []string) []string {
	var kept []string
	seen := map[string]bool{"": true}
	for _, address := range addresses {
		if seen[address] {
			continue
		}
		seen[address] = true
		kept = append(kept, address)
		if len(kept) == MaxPairAddresses {
			break
		}
	}
	return kept
}

// BuildPairApprove signs an approval for one request.
//
// Directed at the requester. The recipient is covered by the signature, so an
// approval handed to a different node is not a valid approval there: without
// that, an approval polled off one exchange could be replayed into another.
//
// addresses is where this node answers, on the terms BuildPairRequest states.
func (b *HeartbeatBuilder) BuildPairApprove(sentAt time.Time, recipientNodeID, requestID string,
	addresses []string) (Envelope, error) {
	return NewDirectedEnvelope(b.node.ID, recipientNodeID, TypePairApprove, At(sentAt),
		PairApprovePayload{Node: b.Descriptor(), RequestID: requestID, Addresses: firstPairAddresses(addresses)},
		b.signer)
}

// BuildPairReject signs a refusal, with the reason the requester is shown.
//
// Directed for the same reason an approval is, and because "no" is an answer to
// one request from one node.
func (b *HeartbeatBuilder) BuildPairReject(sentAt time.Time, recipientNodeID, requestID, reason string) (Envelope, error) {
	return NewDirectedEnvelope(b.node.ID, recipientNodeID, TypePairReject, At(sentAt),
		PairRejectPayload{RequestID: requestID, Reason: reason}, b.signer)
}

// ReadPairRequest verifies a pair.request against the key it carries and
// returns what it claims.
//
// Verifying against the embedded key looks circular and is not. It proves the
// sender holds the private half of the key in the envelope, which is what binds
// the key to this exchange; it proves nothing about which machine that is. Node
// identifiers here are random labels, so the only thing that ever ties a key to
// a machine is a person reading the fingerprint off both screens. This function
// is the first half of that and must never be mistaken for the second.
func ReadPairRequest(envelope Envelope) (NodeDescriptor, ed25519.PublicKey, error) {
	payload, err := DecodePayload[PairRequestPayload](envelope)
	if err != nil {
		return NodeDescriptor{}, nil, fmt.Errorf("%w: %w", ErrPairRequestUnusable, err)
	}
	descriptor := payload.Node
	if err := model.ValidateNodeID(descriptor.NodeID); err != nil {
		return NodeDescriptor{}, nil, fmt.Errorf("%w: %w", ErrPairRequestUnusable, err)
	}
	// The envelope's sender and the descriptor's node must be the same node.
	// Otherwise the signature would attest to one identity while the receiver
	// stored another, and the fingerprint the owner compared would belong to
	// neither.
	if envelope.NodeID != descriptor.NodeID {
		return NodeDescriptor{}, nil, fmt.Errorf(
			"%w: envelope is from %q but describes %q", ErrPairRequestUnusable,
			envelope.NodeID, descriptor.NodeID)
	}
	public, err := identity.DecodePublicKey(descriptor.PublicKey)
	if err != nil {
		return NodeDescriptor{}, nil, fmt.Errorf("%w: %w", ErrPairRequestUnusable, err)
	}
	if err := envelope.VerifySender(public, descriptor.NodeID); err != nil {
		return NodeDescriptor{}, nil, err
	}
	return descriptor, public, nil
}

// PairAddress is the address a pair.request claimed, or empty.
func PairAddress(envelope Envelope) string {
	payload, err := DecodePayload[PairRequestPayload](envelope)
	if err != nil {
		return ""
	}
	return payload.Address
}

// PairAddresses is every address a pair.request claimed, unchecked: the
// preferred one, and the rest as sent. An older requester sends no list, and
// then the rest is empty.
func PairAddresses(envelope Envelope) (string, []string) {
	payload, err := DecodePayload[PairRequestPayload](envelope)
	if err != nil {
		return "", nil
	}
	return payload.Address, payload.Addresses
}
