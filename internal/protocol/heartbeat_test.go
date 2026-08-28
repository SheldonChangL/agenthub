package protocol

import (
	"context"
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

	builder := NewHeartbeatBuilder(store, model.NodeIdentity{ID: "node-1234567890123456", DisplayName: "test", Platform: "test"})
	got, err := builder.Build(ctx, now)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(got.Sessions) != 1 || got.Sessions[0].ID != "codex:public" {
		t.Fatalf("heartbeat sessions = %#v; want public session only", got.Sessions)
	}
	if got.Sequence != 1 || !got.ExpiresAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("heartbeat sequence/expiresAt = %d/%v", got.Sequence, got.ExpiresAt)
	}
}
