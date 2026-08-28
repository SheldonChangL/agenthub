package protocol

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"agenthub.local/agenthub/internal/id"
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
	Payload         any       `json:"payload"`
}

// HeartbeatPayload is a replaceable presence snapshot. Consumers replace a
// node's previous snapshot wholesale; a session missing from Sessions has had
// its publication revoked, and merging would resurrect it.
type HeartbeatPayload struct {
	Sequence     uint64           `json:"sequence"`
	ExpiresAt    time.Time        `json:"expiresAt"`
	Capabilities []string         `json:"capabilities"`
	Sessions     []SessionSummary `json:"sessions"`
}

type HeartbeatBuilder struct {
	store    *registry.Registry
	node     model.NodeIdentity
	sequence atomic.Uint64
}

func NewHeartbeatBuilder(store *registry.Registry, node model.NodeIdentity) *HeartbeatBuilder {
	return &HeartbeatBuilder{store: store, node: node}
}

// Build reads the export view and renders one heartbeat envelope.
//
// The registry query is the privacy boundary: only sessions the owner marked
// public are read at all, so a projection mistake cannot leak a private one.
func (b *HeartbeatBuilder) Build(ctx context.Context, now time.Time) (Envelope, error) {
	sessions, err := b.store.ListSessions(ctx, registry.ListOptions{PublicOnly: true})
	if err != nil {
		return Envelope{}, fmt.Errorf("list public sessions: %w", err)
	}

	summaries := make([]SessionSummary, 0, len(sessions))
	for _, session := range sessions {
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

	messageID, err := id.New("msg_")
	if err != nil {
		return Envelope{}, fmt.Errorf("generate message id: %w", err)
	}

	return Envelope{
		ProtocolVersion: Version,
		MessageID:       messageID,
		Type:            TypeNodeHeartbeat,
		SentAt:          now.UTC(),
		NodeID:          b.node.ID,
		Payload: HeartbeatPayload{
			Sequence:     b.sequence.Add(1),
			ExpiresAt:    now.UTC().Add(heartbeatTTL),
			Capabilities: []string{"session.list", "session.status", "message.send", "message.inbox"},
			Sessions:     summaries,
		},
	}, nil
}
