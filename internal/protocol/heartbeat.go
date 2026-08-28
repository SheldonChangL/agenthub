package protocol

import (
	"context"
	"sync/atomic"
	"time"

	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/registry"
)

const Version = "agenthub.broker/v1alpha1"

type Heartbeat struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Node            model.NodeIdentity `json:"node"`
	Sequence        uint64             `json:"sequence"`
	SentAt          time.Time          `json:"sentAt"`
	ExpiresAt       time.Time          `json:"expiresAt"`
	Capabilities    []string           `json:"capabilities"`
	Sessions        []model.Session    `json:"sessions"`
}

type HeartbeatBuilder struct {
	store    *registry.Registry
	node     model.NodeIdentity
	sequence atomic.Uint64
}

func NewHeartbeatBuilder(store *registry.Registry, node model.NodeIdentity) *HeartbeatBuilder {
	return &HeartbeatBuilder{store: store, node: node}
}

func (b *HeartbeatBuilder) Build(ctx context.Context, now time.Time) (Heartbeat, error) {
	sessions, err := b.store.ListSessions(ctx, registry.ListOptions{PublicOnly: true})
	if err != nil {
		return Heartbeat{}, err
	}
	return Heartbeat{
		ProtocolVersion: Version,
		Node:            b.node,
		Sequence:        b.sequence.Add(1),
		SentAt:          now.UTC(),
		ExpiresAt:       now.UTC().Add(30 * time.Second),
		Capabilities:    []string{"session.list", "session.status", "message.send", "message.inbox"},
		Sessions:        sessions,
	}, nil
}
