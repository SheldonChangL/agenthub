package protocol

import (
	"agenthub.local/agenthub/internal/address"
	"context"
	"crypto/ed25519"
	"path/filepath"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/registry"
)

func TestHeartbeatContainsPublicSessionsOnly(t *testing.T) {
	ctx := context.Background()
	store, err := registry.Open(ctx, filepath.Join(t.TempDir(), "agenthub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)

	for _, id := range []string{"claude:private", "codex:public"} {
		provider := model.ProviderClaude
		providerID := id[len("claude:"):]
		if id == "codex:public" {
			provider = model.ProviderCodex
			providerID = "public"
		}
		_, err := store.UpsertSession(ctx, model.Session{
			ID: id, Provider: provider, ProviderSessionID: providerID,
			Management: model.Unmanaged, Status: model.StatusIdle, StatusSource: "test",
			LastSeenAt: now, UpdatedAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetVisibility(ctx, "codex:public", model.VisibilityPublic); err != nil {
		t.Fatal(err)
	}

	node := model.NodeIdentity{ID: "node-1234567890123456", DisplayName: "test", Platform: "test"}
	builder := NewHeartbeatBuilder(store, node, internalTestSigner{})
	envelope, err := builder.Build(ctx, now)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if envelope.Type != TypeNodeHeartbeat || envelope.NodeID != node.ID {
		t.Fatalf("envelope type/nodeId = %q/%q", envelope.Type, envelope.NodeID)
	}
	got, err := DecodePayload[HeartbeatPayload](envelope)
	if err != nil {
		t.Fatal(err)
	}
	want := address.QualifiedID(node.ID, "codex:public")
	if len(got.Sessions) != 1 || got.Sessions[0].ID != want {
		t.Fatalf("heartbeat sessions = %#v; want only %q", got.Sessions, want)
	}
	if got.Sequence != 1 || !got.ExpiresAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("heartbeat sequence/expiresAt = %d/%v", got.Sequence, got.ExpiresAt)
	}
}

// internalTestSigner keeps the in-package test independent of the external
// test helper while still producing a real signature.
type internalTestSigner struct{}

func (internalTestSigner) Sign(message []byte) []byte {
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		panic(err)
	}
	return ed25519.Sign(private, message)
}
