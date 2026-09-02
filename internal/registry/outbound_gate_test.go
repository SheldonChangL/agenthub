package registry

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"agenthub.local/agenthub/internal/model"
)

// Willing to receive is not willing to send. The three flags are independent,
// and the one that lets data leave starts closed.
func TestOutboundIsClosedUntilTheOwnerOpensIt(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := sessionFixture("one")
	if _, err := store.UpsertSession(ctx, session); err != nil {
		t.Fatal(err)
	}

	audience, err := store.GetAudience(ctx, session.ID)
	if err != nil {
		t.Fatalf("audience: %v", err)
	}
	if audience.AllowOutbound {
		t.Fatal("a freshly discovered session may send; it must start closed")
	}

	// Opening the inbound flag must not open the outbound one.
	if err := store.SetAudience(ctx, session.ID, model.Audience{
		Mode: model.AudienceAllPaired, AcceptMessages: true, ExportCWD: true,
	}); err != nil {
		t.Fatalf("set audience: %v", err)
	}
	audience, err = store.GetAudience(ctx, session.ID)
	if err != nil {
		t.Fatalf("audience: %v", err)
	}
	if audience.AllowOutbound {
		t.Error("accepting messages opened the outbound gate; the two must be independent")
	}
	if !audience.AcceptMessages {
		t.Error("acceptMessages was lost")
	}

	// And it round-trips when set on its own.
	if err := store.SetAudience(ctx, session.ID, model.Audience{
		Mode: model.AudienceNone, AllowOutbound: true,
	}); err != nil {
		t.Fatalf("set audience: %v", err)
	}
	audience, err = store.GetAudience(ctx, session.ID)
	if err != nil {
		t.Fatalf("audience: %v", err)
	}
	if !audience.AllowOutbound {
		t.Error("allowOutbound did not persist")
	}
	if audience.AcceptMessages {
		t.Error("opening outbound opened inbound too")
	}
}

// A database written before this column existed must come back closed. An
// upgrade that silently granted every existing session the ability to send
// would be the worst possible default.
func TestUpgradingADatabaseDoesNotGrantOutbound(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "old.db")

	// Build a sessions table the way it looked before allow_outbound, with one
	// row whose other flags are all on.
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := raw.ExecContext(ctx, `
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    provider TEXT NOT NULL CHECK (provider IN ('claude', 'codex')),
    provider_session_id TEXT NOT NULL,
    management TEXT NOT NULL CHECK (management IN ('managed', 'unmanaged')),
    audience_mode TEXT NOT NULL DEFAULT 'none',
    export_cwd INTEGER NOT NULL DEFAULT 0,
    accept_messages INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL CHECK (status IN ('active', 'idle', 'inactive', 'unknown')),
    status_source TEXT NOT NULL,
    cwd TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    metadata_path TEXT NOT NULL DEFAULT '',
    last_seen_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL,
    UNIQUE(provider, provider_session_id)
);
INSERT INTO sessions VALUES
  ('codex:old', 'codex', 'old', 'unmanaged', 'all_paired', 1, 1, 'active', 'test', '', '', '', 0, 0);
`); err != nil {
		t.Fatalf("build old schema: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("open upgraded: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	audience, err := store.GetAudience(ctx, "codex:old")
	if err != nil {
		t.Fatalf("audience: %v", err)
	}
	if audience.AllowOutbound {
		t.Error("upgrading granted an existing session the ability to send")
	}
	// The flags it did have must survive.
	if !audience.AcceptMessages || !audience.ExportCWD {
		t.Errorf("the upgrade lost existing flags: %+v", audience)
	}
	if audience.Mode != model.AudienceAllPaired {
		t.Errorf("the upgrade lost the audience mode: %v", audience.Mode)
	}
}

// Publishing says who may see a session. It must not also decide what that
// session may do — and in particular must never open the outbound gate, which
// the caller of this endpoint has no way to express a choice about.
func TestPublishingDoesNotOpenOutbound(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := sessionFixture("publish-me")
	if _, err := store.UpsertSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := store.SetVisibility(ctx, session.ID, model.VisibilityPublic); err != nil {
		t.Fatalf("publish: %v", err)
	}
	audience, err := store.GetAudience(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if audience.AllowOutbound {
		t.Error("publishing opened the outbound gate")
	}
	if audience.Mode != model.AudienceAllPaired {
		t.Errorf("mode = %q, want all_paired", audience.Mode)
	}

	// And the reverse: an owner who opened outbound, then published, finds it
	// closed. That is the safe direction, and it is asserted so the behaviour is
	// a decision rather than an accident.
	if err := store.SetAudience(ctx, session.ID, model.Audience{
		Mode: model.AudienceNone, AllowOutbound: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetVisibility(ctx, session.ID, model.VisibilityPublic); err != nil {
		t.Fatal(err)
	}
	after, err := store.GetAudience(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.AllowOutbound {
		t.Error("publishing left an already-open outbound gate open; it must reset to closed")
	}
}
