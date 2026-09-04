package registry

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
)

// testNodeID stands in for this node in tests that record a message destination.
const testNodeID = "node_local000000000000"

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
	// A session accepts messages only when its owner said so.
	if err := store.SetAudience(ctx, session.ID, model.Audience{
		Mode: model.AudienceNone, AcceptMessages: true,
	}); err != nil {
		t.Fatal(err)
	}

	created, err := store.CreateMessage(ctx, model.Message{
		To: session.ID, From: "codex:sender", DestinationNodeID: testNodeID, Body: "review schema",
	})
	if err != nil {
		t.Fatalf("CreateMessage() error = %v", err)
	}
	if created.ID == "" || created.Body != "review schema" {
		t.Fatalf("created message = %#v", created)
	}

	messages, err := store.Inbox(ctx, session.ID, 50, InboxStart)
	if err != nil {
		t.Fatalf("Inbox() error = %v", err)
	}
	if len(messages) != 1 || messages[0].ID != created.ID {
		t.Fatalf("Inbox() = %#v; want created message", messages)
	}
}

func TestCreateMessageRejectsUnknownRecipientWithoutDatabaseDetails(t *testing.T) {
	store := openTestRegistry(t)

	// Every other field is valid, so the unknown recipient is the only thing
	// left for the error to be about.
	_, err := store.CreateMessage(context.Background(), model.Message{
		To: "codex:missing", DestinationNodeID: testNodeID, Body: "hello",
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("CreateMessage() error = %v; want ErrNotFound", err)
	}
}

// openRegistryAt opens a registry at a caller-chosen path, so a test can close
// and reopen the same database to exercise a migration.
func openRegistryAt(t *testing.T, path string) *Registry {
	t.Helper()
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open(%q) error = %v", path, err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// acceptingSession stores a session whose owner has opted in to messages.
func acceptingSession(t *testing.T, store *Registry) model.Session {
	t.Helper()
	ctx := context.Background()
	session := testSession("claude:inbox-target")
	stored, err := store.UpsertSession(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAudience(ctx, stored.ID, model.Audience{
		Mode: model.AudienceAllPaired, AcceptMessages: true,
	}); err != nil {
		t.Fatal(err)
	}
	return stored
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

// TestAMessageRecordsWhereItWasAddressed is issue #7's remaining change-scope
// item: the row must say which node the message was for.
func TestAMessageRecordsWhereItWasAddressed(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := acceptingSession(t, store)

	created, err := store.CreateMessage(ctx, model.Message{
		To: session.ID, DestinationNodeID: testNodeID, Body: "hello",
	})
	if err != nil {
		t.Fatalf("CreateMessage() error = %v", err)
	}
	if created.DestinationNodeID != testNodeID {
		t.Fatalf("destination = %q", created.DestinationNodeID)
	}

	inbox, err := store.Inbox(ctx, session.ID, 10, InboxStart)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 {
		t.Fatalf("inbox = %#v", inbox)
	}
	if inbox[0].DestinationNodeID != testNodeID {
		t.Fatalf("stored destination = %q; want it read back", inbox[0].DestinationNodeID)
	}
}

// TestAMessageWithoutADestinationIsRefused keeps the column from quietly
// filling with empty strings, now that a row can name another node.
func TestAMessageWithoutADestinationIsRefused(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := acceptingSession(t, store)

	if _, err := store.CreateMessage(ctx, model.Message{To: session.ID, Body: "hello"}); !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("error = %v; want ErrInvalidSession", err)
	}
}

// TestAnOlderDatabaseGainsTheDestinationColumn covers the upgrade path: a
// database written before the column existed must open, keep its messages, and
// read them back with an empty destination rather than an invented one.
func TestAnOlderDatabaseGainsTheDestinationColumn(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agenthub.db")
	store := openRegistryAt(t, path)
	session := acceptingSession(t, store)
	if _, err := store.CreateMessage(ctx, model.Message{
		To: session.ID, DestinationNodeID: testNodeID, Body: "before the upgrade",
	}); err != nil {
		t.Fatal(err)
	}

	// Drop the column to reproduce a database from the earlier build.
	if _, err := store.db.ExecContext(ctx, `ALTER TABLE messages DROP COLUMN destination_node_id`); err != nil {
		t.Skipf("this SQLite build cannot drop a column: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopening runs the migration.
	reopened := openRegistryAt(t, path)
	inbox, err := reopened.Inbox(ctx, session.ID, 10, InboxStart)
	if err != nil {
		t.Fatalf("Inbox() after upgrade = %v", err)
	}
	if len(inbox) != 1 {
		t.Fatalf("the upgrade lost messages: %#v", inbox)
	}
	if inbox[0].Body != "before the upgrade" {
		t.Fatalf("body = %q", inbox[0].Body)
	}
	if inbox[0].DestinationNodeID != "" {
		t.Fatalf("destination = %q; a pre-upgrade row must read as unrecorded, not as an invented node",
			inbox[0].DestinationNodeID)
	}

	// And new messages still record a destination.
	if _, err := reopened.CreateMessage(ctx, model.Message{
		To: session.ID, DestinationNodeID: testNodeID, Body: "after the upgrade",
	}); err != nil {
		t.Fatalf("CreateMessage() after upgrade = %v", err)
	}
}

// The provider set exists in two places: model.KnownProvider and the sessions
// table's CHECK. They must agree, or a provider this build accepts becomes one
// the store rejects — or worse, the reverse.
func TestTheProviderCheckAgreesWithKnownProvider(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)

	for _, provider := range []model.Provider{model.ProviderClaude, model.ProviderCodex} {
		if !model.KnownProvider(string(provider)) {
			t.Errorf("KnownProvider(%q) is false but the constant exists", provider)
		}
		session := sessionFixture("agree-" + string(provider))
		session.Provider = provider
		session.ID = model.SessionID(provider, "agree-"+string(provider))
		session.ProviderSessionID = "agree-" + string(provider)
		if _, err := store.UpsertSession(ctx, session); err != nil {
			t.Errorf("the CHECK rejected %q, which KnownProvider accepts: %v", provider, err)
		}
	}

	// And a provider neither knows is refused by both.
	if model.KnownProvider("gemini") {
		t.Error("KnownProvider accepts an unknown provider")
	}
	rogue := sessionFixture("rogue")
	rogue.Provider = model.Provider("gemini")
	rogue.ID = "gemini:rogue"
	if _, err := store.UpsertSession(ctx, rogue); err == nil {
		t.Error("the store accepted a provider KnownProvider refuses")
	}
}
