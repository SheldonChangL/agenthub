package protocol

import (
	"context"
	"crypto/ed25519"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/address"
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

// Whatever this node sends, a node running this code must accept.
//
// The receiving side refuses snapshots whose contents are not what the fields
// are for (ValidateIncomingPayload). If the builder could produce something that
// check refuses, this build would reject its own peers — so the two are pinned
// to each other rather than tested apart.
func TestWhatTheBuilderProducesPassesTheIncomingCheck(t *testing.T) {
	ctx := context.Background()
	store, err := registry.Open(ctx, filepath.Join(t.TempDir(), "heartbeat.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)

	// One of each provider, one published every way a session can be.
	for _, seed := range []struct {
		id       string
		provider model.Provider
		cwd      string
	}{
		{"claude:one", model.ProviderClaude, "/home/someone/a project with spaces"},
		{"codex:two", model.ProviderCodex, ""},
	} {
		providerID := seed.id[strings.Index(seed.id, ":")+1:]
		if _, err := store.UpsertSession(ctx, model.Session{
			ID: seed.id, Provider: seed.provider, ProviderSessionID: providerID,
			Management: model.Unmanaged, Status: model.StatusActive, StatusSource: "test",
			CWD: seed.cwd, LastSeenAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SetAudience(ctx, seed.id, model.Audience{
			Mode: model.AudienceAllPaired, ExportCWD: true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	node := model.NodeIdentity{ID: "node_1234567890123456", DisplayName: "test", Platform: "test"}
	envelope, err := NewHeartbeatBuilder(store, node, internalTestSigner{}).Build(ctx, now)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	payload, err := DecodePayload[HeartbeatPayload](envelope)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Sessions) != 2 {
		t.Fatalf("want 2 sessions, got %d; the test would not exercise much", len(payload.Sessions))
	}
	if err := ValidateIncomingPayload(node.ID, payload); err != nil {
		t.Errorf("this node's own heartbeat would be refused by a node running this code: %v", err)
	}
}
