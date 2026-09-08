package registry

import (
	"context"
	"path/filepath"
	"testing"

	"agenthub.local/agenthub/internal/model"
)

// storedSession puts a bare session in the store, with every flag closed.
func storedSession(t *testing.T, store *Registry, id string) model.Session {
	t.Helper()
	stored, err := store.UpsertSession(context.Background(), testSession(id))
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

// The third switch is closed unless somebody opens it, and it is closed
// independently of the other two.
//
// Willing to receive is not willing to be woken. Under acceptMessages alone a
// hostile message waits in an inbox until a person asks an agent to read it,
// and that person is at the keyboard when it lands in the context. Under
// autoWake the same bytes start a turn with nobody there. A default that
// followed acceptMessages would make that second decision on the owner's
// behalf, silently, at upgrade time.
func TestAutoWakeIsClosedUntilAskedFor(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := storedSession(t, store, "claude:wake-target")

	if session.Audience.AutoWake {
		t.Error("a newly discovered session may be woken by anyone who can reach it")
	}

	// Opening the inbound switch does not open this one.
	if err := store.SetAudience(ctx, session.ID, model.Audience{
		Mode: model.AudienceAllPaired, AcceptMessages: true, AllowOutbound: true,
	}); err != nil {
		t.Fatal(err)
	}
	after, err := store.GetAudience(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.AutoWake {
		t.Error("accepting messages turned on waking; they are separate decisions")
	}
	if !after.AcceptMessages || !after.AllowOutbound {
		t.Fatalf("the other two flags did not survive: %+v", after)
	}

	// And it can be opened on its own, without implying the others.
	if err := store.SetAudience(ctx, session.ID, model.Audience{
		Mode: model.AudienceAllPaired, AcceptMessages: true, AutoWake: true,
	}); err != nil {
		t.Fatal(err)
	}
	woken, err := store.GetAudience(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !woken.AutoWake {
		t.Error("autoWake was set and did not come back")
	}
	if woken.AllowOutbound {
		t.Error("waking implied the right to send; it must not")
	}
}

// The flag survives being read back through a listing, not only through
// GetAudience. Two read paths that disagree would mean the desktop and the CLI
// could show different answers for the same session.
func TestAutoWakeSurvivesEveryReadPath(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := storedSession(t, store, "claude:wake-listed")
	if err := store.SetAudience(ctx, session.ID, model.Audience{
		Mode: model.AudienceAllPaired, AcceptMessages: true, AutoWake: true,
	}); err != nil {
		t.Fatal(err)
	}

	direct, err := store.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !direct.Audience.AutoWake {
		t.Error("GetSession lost autoWake")
	}

	listed, err := store.ListSessions(ctx, ListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range listed {
		if item.ID != session.ID {
			continue
		}
		found = true
		if !item.Audience.AutoWake {
			t.Error("ListSessions lost autoWake")
		}
	}
	if !found {
		t.Fatalf("the session was not in the listing at all")
	}
}

// A database written before the column existed gains it closed.
//
// The default matters more here than for the other flags: an upgrade that
// turned this on would let messages already sitting in an inbox start turns
// nobody asked for, on a machine whose owner never agreed to that.
func TestAnOlderDatabaseGainsAutoWakeClosed(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agenthub.db")
	store := openRegistryAt(t, path)
	session := storedSession(t, store, "claude:wake-upgraded")
	if err := store.SetAudience(ctx, session.ID, model.Audience{
		Mode: model.AudienceAllPaired, AcceptMessages: true, AutoWake: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `ALTER TABLE sessions DROP COLUMN auto_wake`); err != nil {
		t.Fatalf("could not reproduce the older schema: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openRegistryAt(t, path)
	after, err := reopened.GetAudience(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.AutoWake {
		t.Error("an upgrade granted waking to a session whose owner never agreed to it")
	}
	if !after.AcceptMessages {
		t.Error("the migration lost the flags that were already set")
	}
}
