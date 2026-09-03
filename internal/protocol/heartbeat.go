package protocol

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"time"

	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/registry"
)

const (
	Version           = "agenthub.broker/v1alpha1"
	TypeNodeHeartbeat = "node.heartbeat"

	heartbeatTTL = 30 * time.Second
)

// Envelope is the broker wire contract from docs/broker-protocol.schema.json.
// GET /v1/heartbeat returns exactly this shape so the local preview and the
// future network payload can never drift apart unnoticed.
type Envelope struct {
	ProtocolVersion string    `json:"protocolVersion"`
	MessageID       string    `json:"messageId"`
	Type            string    `json:"type"`
	SentAt          time.Time `json:"sentAt"`
	NodeID          string    `json:"nodeId"`
	// RecipientNodeID names the one node this envelope was built for. It is
	// covered by the signature, so an envelope cannot be redirected to a peer it
	// was not built for. Every node.heartbeat carries one; types whose protocol
	// work has not happened yet may still travel undirected, and omit it.
	RecipientNodeID string `json:"recipientNodeId,omitempty"`
	// Payload travels and is signed as the exact bytes the sender produced.
	// Re-encoding a decoded payload would not reproduce them.
	Payload json.RawMessage `json:"payload"`
	// Signature is empty only while an envelope is being signed; a receiver
	// treats a missing one as unauthenticated.
	Signature string `json:"signature,omitempty"`
}

// SentAt wraps the timestamp so callers cannot accidentally pass a local clock
// where a UTC instant is required.
type SentAt struct{ instant time.Time }

func At(instant time.Time) SentAt { return SentAt{instant: instant.UTC()} }

func (s SentAt) Time() time.Time { return s.instant }

// HeartbeatPayload is a replaceable presence snapshot. Consumers replace a
// node's previous snapshot wholesale; a session missing from Sessions has had
// its publication revoked, and merging would resurrect it.
type HeartbeatPayload struct {
	Sequence     uint64           `json:"sequence"`
	ExpiresAt    time.Time        `json:"expiresAt"`
	Capabilities []string         `json:"capabilities"`
	Sessions     []SessionSummary `json:"sessions"`
}

// HeartbeatBuilder produces this node's heartbeats.
//
// It holds no sequence of its own. The counter lives in the registry, because a
// builder is recreated whenever the process is, and an in-memory counter would
// restart at one: a receiver that rejects a sequence it has already seen would
// then have to reject the restarted sender forever, or stop checking.
type HeartbeatBuilder struct {
	store  *registry.Registry
	node   model.NodeIdentity
	signer Signer
}

// AnswerChallenge signs a challenge with this node's key.
//
// It lives on the builder because the builder is what already holds the signer;
// exposing the signer itself would make "sign these arbitrary bytes" available
// to every caller, which is precisely the capability this indirection keeps
// narrow.
func (b *HeartbeatBuilder) AnswerChallenge(responderNodeID, challengerNodeID string, nonce []byte) (string, error) {
	return AnswerChallenge(responderNodeID, challengerNodeID, nonce, b.signer)
}

// BuildMessage signs one outbound message for a peer.
//
// It lives here for the same reason AnswerChallenge does: the builder already
// holds the signer, and exposing the signer itself would make "sign these
// arbitrary bytes" available to every caller. Each thing this node signs gets a
// named method whose payload shape is fixed, so the set of things its key can
// be made to say stays enumerable.
func (b *HeartbeatBuilder) BuildMessage(recipientNodeID string, payload MessagePayload) (Envelope, error) {
	return NewMessageEnvelope(b.node.ID, recipientNodeID, payload, b.signer)
}

func NewHeartbeatBuilder(store *registry.Registry, node model.NodeIdentity, signer Signer) *HeartbeatBuilder {
	return &HeartbeatBuilder{store: store, node: node, signer: signer}
}

// Build renders the owner's preview: everything that leaves this host at all.
//
// It is not what any peer receives. A "selected" session reaches only the nodes
// its owner named, so the envelope a peer gets comes from BuildFor.
//
// The preview is addressed to this node. That keeps it a preview: it is a real,
// signed heartbeat, and a peer that received one would have to reject it,
// because the recipient it names is not that peer.
func (b *HeartbeatBuilder) Build(ctx context.Context, now time.Time) (Envelope, error) {
	return b.build(ctx, now, b.node.ID, ownerPreview)
}

// recipientFilter says whether the recipient also decides which sessions the
// envelope may carry. The owner preview is addressed to this node but is still
// the union of everything published anywhere, so the two questions — who is this
// for, and what may they see — are answered separately.
type recipientFilter bool

const (
	ownerPreview   recipientFilter = false
	peerExportView recipientFilter = true
)

// ErrPeerNotTrusted marks a recipient this owner has not paired with. A caller
// that gets it must send nothing, not fall back to the owner preview.
var ErrPeerNotTrusted = errors.New("peer node is not trusted")

// BuildFor renders the envelope one peer may receive.
//
// Passing the recipient in is what makes "selected" mean anything: without it
// every peer would get the same envelope and a session published to one node
// would reach all of them.
//
// The recipient must be a node currently in trusted_nodes. An audience of
// all_paired means "every node this owner paired with", and the audience filter
// alone cannot enforce that — it admits any non-empty string. Checking trust
// here is what keeps all_paired from meaning "anyone who supplies a node id".
// Revoking a node therefore stops its heartbeats immediately, without the
// owner having to revisit every session's audience.
func (b *HeartbeatBuilder) BuildFor(ctx context.Context, now time.Time, peerNodeID string) (Envelope, error) {
	if peerNodeID == "" {
		return Envelope{}, fmt.Errorf("a peer node id is required to build a heartbeat for a recipient")
	}
	if _, err := b.store.TrustedNode(ctx, peerNodeID); err != nil {
		if errors.Is(err, registry.ErrNotFound) {
			return Envelope{}, fmt.Errorf("%w: %q is not paired with this node", ErrPeerNotTrusted, peerNodeID)
		}
		return Envelope{}, fmt.Errorf("check trust for peer %q: %w", peerNodeID, err)
	}
	return b.build(ctx, now, peerNodeID, peerExportView)
}

func (b *HeartbeatBuilder) build(ctx context.Context, now time.Time, recipientNodeID string, filter recipientFilter) (Envelope, error) {
	sessions, err := b.store.ListSessions(ctx, registry.ListOptions{PublicOnly: true})
	if err != nil {
		return Envelope{}, fmt.Errorf("list public sessions: %w", err)
	}

	summaries := make([]SessionSummary, 0, len(sessions))
	for _, session := range sessions {
		// The registry filter answers "does this leave the host at all". Only
		// the recipient answers "may this peer see it", so a per-peer build
		// applies the grant list here.
		if filter == peerExportView && !session.Audience.PublishesTo(recipientNodeID) {
			continue
		}
		// An id a peer would refuse costs that session, not the heartbeat. A
		// receiver refuses a whole snapshot over one bad row, so exporting it
		// would take every other session with it — and unlike a working
		// directory an id cannot be dropped and the session kept. Claude and
		// Codex both produce UUIDs, so this is about a store written by
		// something else.
		if err := ExportableSessionID(session.ID); err != nil {
			log.Printf("not exporting session %q: %v", session.ID, err)
			continue
		}
		// A refusal here means the registry returned something the export view
		// must not carry. Fail the whole heartbeat rather than send a partial
		// one: the query already filters on visibility, so reaching this is a
		// bug, and a bug in this path is exactly what must not ship silently.
		summary, err := Summarize(b.node.ID, session)
		if err != nil {
			return Envelope{}, fmt.Errorf("project session for export: %w", err)
		}
		summaries = append(summaries, summary)
	}

	// The sequence is reserved last, and a failure here produces no envelope at
	// all. Reserving it may leave a gap, which costs a receiver nothing; sending
	// a snapshot without a fresh number would leave it unable to order the
	// snapshots it already holds.
	sequence, err := b.store.NextHeartbeatSequence(ctx)
	if err != nil {
		return Envelope{}, fmt.Errorf("reserve heartbeat sequence: %w", err)
	}

	payload := HeartbeatPayload{
		Sequence:     sequence,
		ExpiresAt:    now.UTC().Add(heartbeatTTL),
		Capabilities: []string{"session.list", "session.status", "message.send", "message.inbox"},
		Sessions:     summaries,
	}
	return NewDirectedEnvelope(b.node.ID, recipientNodeID, TypeNodeHeartbeat, At(now), payload, b.signer)
}
