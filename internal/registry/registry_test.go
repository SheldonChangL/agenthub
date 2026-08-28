package registry

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
)

func TestUpsertSessionDefaultsNewSessionToPrivate(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := testSession("claude:session-1")
	session.Visibility = model.VisibilityPublic

	got, err := store.UpsertSession(ctx, session)
	if err != nil {
		t.Fatalf("UpsertSession() error = %v", err)
	}
	if got.Visibility != model.VisibilityPrivate {
		t.Fatalf("Visibility = %q; want private", got.Visibility)
	}
}

func TestUpsertSessionPreservesExplicitVisibility(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := testSession("codex:session-2")

	if _, err := store.UpsertSession(ctx, session); err != nil {
		t.Fatalf("first UpsertSession() error = %v", err)
	}
	if err := store.SetVisibility(ctx, session.ID, model.VisibilityPublic); err != nil {
		t.Fatalf("SetVisibility() error = %v", err)
	}

	session.CWD = "/updated/project"
	session.Visibility = model.VisibilityPrivate
	got, err := store.UpsertSession(ctx, session)
	if err != nil {
		t.Fatalf("second UpsertSession() error = %v", err)
	}
	if got.Visibility != model.VisibilityPublic {
		t.Fatalf("Visibility = %q; want public", got.Visibility)
	}
	if got.CWD != "/updated/project" {
		t.Fatalf("CWD = %q; want updated path", got.CWD)
	}
}

func TestListSessionsPublicOnlyDoesNotLeakPrivateSessions(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	privateSession := testSession("claude:private")
	publicSession := testSession("codex:public")

	if _, err := store.UpsertSession(ctx, privateSession); err != nil {
		t.Fatalf("UpsertSession(private) error = %v", err)
	}
	if _, err := store.UpsertSession(ctx, publicSession); err != nil {
		t.Fatalf("UpsertSession(public) error = %v", err)
	}
	if err := store.SetVisibility(ctx, publicSession.ID, model.VisibilityPublic); err != nil {
		t.Fatalf("SetVisibility() error = %v", err)
	}

	got, err := store.ListSessions(ctx, ListOptions{PublicOnly: true})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(got) != 1 || got[0].ID != publicSession.ID {
		t.Fatalf("ListSessions() = %#v; want only %q", got, publicSession.ID)
	}
}

func openTestRegistry(t *testing.T) *Registry {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "agenthub.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testSession(id string) model.Session {
	provider := model.ProviderClaude
	providerID := "session-1"
	if id == "codex:session-2" || id == "codex:public" {
		provider = model.ProviderCodex
		providerID = id[len("codex:"):]
	} else if len(id) > len("claude:") {
		providerID = id[len("claude:"):]
	}
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	return model.Session{
		ID:                id,
		Provider:          provider,
		ProviderSessionID: providerID,
		Management:        model.Unmanaged,
		Status:            model.StatusInactive,
		StatusSource:      "test",
		CWD:               "/project",
		LastSeenAt:        now,
		UpdatedAt:         now,
	}
}
