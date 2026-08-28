package registry

import (
	"context"
	"errors"
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

func TestCreateMessageQueuesForLocalInbox(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := testSession("claude:inbox")
	if _, err := store.UpsertSession(ctx, session); err != nil {
		t.Fatal(err)
	}

	created, err := store.CreateMessage(ctx, model.Message{To: session.ID, From: "codex:sender", Body: "review schema"})
	if err != nil {
		t.Fatalf("CreateMessage() error = %v", err)
	}
	if created.ID == "" || created.Body != "review schema" {
		t.Fatalf("created message = %#v", created)
	}

	messages, err := store.Inbox(ctx, session.ID, 50)
	if err != nil {
		t.Fatalf("Inbox() error = %v", err)
	}
	if len(messages) != 1 || messages[0].ID != created.ID {
		t.Fatalf("Inbox() = %#v; want created message", messages)
	}
}

func TestCreateMessageRejectsUnknownRecipientWithoutDatabaseDetails(t *testing.T) {
	store := openTestRegistry(t)

	_, err := store.CreateMessage(context.Background(), model.Message{To: "codex:missing", Body: "hello"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("CreateMessage() error = %v; want ErrNotFound", err)
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

// A provider session ID comes from untrusted metadata, not a filename. A path
// separator in it would split the qualified export address
// <node-id>/<provider>:<id> somewhere this node did not intend.
func TestUpsertRejectsPathSeparatorInProviderSessionID(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)

	now := time.Now().UTC()
	hostile := model.Session{
		ID:                "claude:evil/node_b",
		Provider:          model.ProviderClaude,
		ProviderSessionID: "evil/node_b",
		Management:        model.Unmanaged,
		Status:            model.StatusIdle,
		StatusSource:      "test",
		LastSeenAt:        now,
		UpdatedAt:         now,
	}
	if _, err := store.UpsertSession(ctx, hostile); err == nil {
		t.Fatal("UpsertSession accepted a provider session id containing a path separator")
	}
}

// The Go-layer check is the only defence for a database created by an older
// build, which carries no CHECK constraint. Test it directly so the SQL
// constraint cannot mask its removal.
func TestValidateSessionRejectsSeparatorIndependentlyOfSQL(t *testing.T) {
	now := time.Now().UTC()
	err := validateSession(model.Session{
		ID:                "claude:evil/node_b",
		Provider:          model.ProviderClaude,
		ProviderSessionID: "evil/node_b",
		Management:        model.Unmanaged,
		Status:            model.StatusIdle,
		StatusSource:      "test",
		LastSeenAt:        now,
		UpdatedAt:         now,
	})
	if err == nil {
		t.Fatal("validateSession accepted a provider session id containing a separator")
	}
	if !errors.Is(err, ErrInvalidSession) {
		t.Errorf("error = %v; callers rely on ErrInvalidSession to tell a bad record from a broken store", err)
	}
}
