package registry

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
)

func sessionFixture(providerSessionID string) model.Session {
	now := time.Now().UTC()
	return model.Session{
		ID:                model.SessionID(model.ProviderClaude, providerSessionID),
		Provider:          model.ProviderClaude,
		ProviderSessionID: providerSessionID,
		Management:        model.Unmanaged,
		Status:            model.StatusIdle,
		StatusSource:      "test",
		CWD:               "/tmp/example",
		LastSeenAt:        now,
		UpdatedAt:         now,
	}
}

func TestDiscoveredSessionsPublishToNobody(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := sessionFixture("fresh")
	if _, err := store.UpsertSession(ctx, session); err != nil {
		t.Fatal(err)
	}

	audience, err := store.GetAudience(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if audience.Mode != model.AudienceNone {
		t.Errorf("mode = %q, want none", audience.Mode)
	}
	if audience.ExportCWD || audience.AcceptMessages {
		t.Errorf("export flags default open: %+v", audience)
	}
}

// "selected" with no grants reaches nobody, so it must not read as published.
func TestSelectedWithoutGrantsIsNotPublished(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := sessionFixture("empty-selection")
	if _, err := store.UpsertSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAudience(ctx, session.ID, model.Audience{Mode: model.AudienceSelected}); err != nil {
		t.Fatal(err)
	}

	published, err := store.ListSessions(ctx, ListOptions{PublicOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(published) != 0 {
		t.Errorf("an empty selection was published: %+v", published)
	}

	if err := store.SetAudience(ctx, session.ID, model.Audience{
		Mode: model.AudienceSelected, Nodes: []string{"node_peer"},
	}); err != nil {
		t.Fatal(err)
	}
	published, err = store.ListSessions(ctx, ListOptions{PublicOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(published) != 1 {
		t.Fatalf("granting a node did not publish the session: %+v", published)
	}
	if !published[0].Audience.PublishesTo("node_peer") {
		t.Error("PublishesTo(granted node) = false")
	}
}

// Removing a node must remove its access, not leave a stale grant behind.
func TestSetAudienceReplacesGrants(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := sessionFixture("regrant")
	if _, err := store.UpsertSession(ctx, session); err != nil {
		t.Fatal(err)
	}

	if err := store.SetAudience(ctx, session.ID, model.Audience{
		Mode: model.AudienceSelected, Nodes: []string{"node_a", "node_b", "node_a"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAudience(ctx, session.ID, model.Audience{
		Mode: model.AudienceSelected, Nodes: []string{"node_b"},
	}); err != nil {
		t.Fatal(err)
	}

	audience, err := store.GetAudience(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(audience.Nodes) != 1 || audience.Nodes[0] != "node_b" {
		t.Fatalf("grants = %v, want only node_b", audience.Nodes)
	}
	if audience.PublishesTo("node_a") {
		t.Error("a revoked node still has access")
	}
}

func TestSetAudienceRejectsIncoherentPolicies(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := sessionFixture("incoherent")
	if _, err := store.UpsertSession(ctx, session); err != nil {
		t.Fatal(err)
	}

	cases := map[string]model.Audience{
		"unknown mode":            {Mode: "everyone"},
		"node list without mode":  {Mode: model.AudienceNone, Nodes: []string{"node_a"}},
		"node list on all paired": {Mode: model.AudienceAllPaired, Nodes: []string{"node_a"}},
		"empty node id":           {Mode: model.AudienceSelected, Nodes: []string{""}},
	}
	for name, audience := range cases {
		t.Run(name, func(t *testing.T) {
			if err := store.SetAudience(ctx, session.ID, audience); err == nil {
				t.Errorf("SetAudience accepted %+v", audience)
			}
		})
	}
}

// Rediscovery must not touch the owner's choice, exactly as it must not have
// touched visibility.
func TestRediscoveryPreservesAudience(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := sessionFixture("preserved")
	if _, err := store.UpsertSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAudience(ctx, session.ID, model.Audience{
		Mode: model.AudienceSelected, Nodes: []string{"node_a"}, ExportCWD: true, AcceptMessages: true,
	}); err != nil {
		t.Fatal(err)
	}

	updated := session
	updated.Status = model.StatusActive
	updated.UpdatedAt = time.Now().UTC().Add(time.Minute)
	if _, err := store.UpsertSession(ctx, updated); err != nil {
		t.Fatal(err)
	}

	audience, err := store.GetAudience(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if audience.Mode != model.AudienceSelected || len(audience.Nodes) != 1 || audience.Nodes[0] != "node_a" {
		t.Errorf("rediscovery changed the audience: %+v", audience)
	}
	if !audience.ExportCWD || !audience.AcceptMessages {
		t.Errorf("rediscovery reset the export flags: %+v", audience)
	}
}

// ADR-001: a database written before peers existed publishes nothing until the
// owner chooses again. "public" then meant inclusion in a local preview, and
// there was no remote recipient it could have been consent for.
func TestUpgradeFromVisibilityPublishesNothing(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")

	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(ctx, `
CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    provider_session_id TEXT NOT NULL,
    management TEXT NOT NULL,
    visibility TEXT NOT NULL DEFAULT 'private',
    status TEXT NOT NULL,
    status_source TEXT NOT NULL,
    cwd TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    metadata_path TEXT NOT NULL DEFAULT '',
    last_seen_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL,
    UNIQUE(provider, provider_session_id)
);
INSERT INTO sessions VALUES
    ('claude:was-public', 'claude', 'was-public', 'unmanaged', 'public',
     'idle', 'test', '/tmp/x', '', '', 1, 1);
`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("opening a pre-audience database failed: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	audience, err := store.GetAudience(ctx, "claude:was-public")
	if err != nil {
		t.Fatal(err)
	}
	if audience.Mode != model.AudienceNone {
		t.Errorf("a row marked public upgraded to audience %q; ADR-001 requires none", audience.Mode)
	}
	published, err := store.ListSessions(ctx, ListOptions{PublicOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(published) != 0 {
		t.Errorf("an upgraded database published %d sessions", len(published))
	}
}

// A grant read from a listing must mean the same as one read on its own.
// Loading grants lazily once left PublishesTo answering "no" for a selected
// session that came from ListSessions.
func TestListedSessionsCarryTheirGrants(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := sessionFixture("listed")
	if _, err := store.UpsertSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAudience(ctx, session.ID, model.Audience{
		Mode: model.AudienceSelected, Nodes: []string{"node_a", "node_b"},
	}); err != nil {
		t.Fatal(err)
	}

	listed, err := store.ListSessions(ctx, ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed %d sessions", len(listed))
	}
	fetched, err := store.GetSession(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}

	for name, got := range map[string]model.Session{"listed": listed[0], "fetched": fetched} {
		if !got.Audience.PublishesTo("node_a") {
			t.Errorf("%s: PublishesTo(node_a) = false", name)
		}
		if got.Audience.PublishesTo("node_c") {
			t.Errorf("%s: PublishesTo(node_c) = true", name)
		}
		if len(got.Audience.Nodes) != 2 {
			t.Errorf("%s: grants = %v", name, got.Audience.Nodes)
		}
	}
}

func TestSetAudienceRejectsControlCharactersInNodeIDs(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := sessionFixture("control-chars")
	if _, err := store.UpsertSession(ctx, session); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAudience(ctx, session.ID, model.Audience{
		Mode: model.AudienceSelected, Nodes: []string{"node_a\x1fnode_b"},
	}); err == nil {
		t.Error("SetAudience accepted a node id containing the grant separator")
	}
}

// A session that has not opted in must not accumulate a queue nobody intends
// to deliver.
func TestCreateMessageRequiresOptIn(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := sessionFixture("closed-inbox")
	if _, err := store.UpsertSession(ctx, session); err != nil {
		t.Fatal(err)
	}

	if _, err := store.CreateMessage(ctx, model.Message{To: session.ID, Body: "hello"}); err == nil {
		t.Fatal("CreateMessage accepted a message for a session that does not accept them")
	}

	if err := store.SetAudience(ctx, session.ID, model.Audience{
		Mode: model.AudienceNone, AcceptMessages: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateMessage(ctx, model.Message{To: session.ID, Body: "hello"}); err != nil {
		t.Fatalf("CreateMessage() after opting in = %v", err)
	}
}

// Publishing says who may see a session, not how much of it.
//
// The compatibility path once turned on the working directory export as a side
// effect, so a user who ran `ah publish` shared their account and project names
// without ever being asked.
func TestPublishLeavesExportFlagsClosed(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := sessionFixture("publish-flags")
	if _, err := store.UpsertSession(ctx, session); err != nil {
		t.Fatal(err)
	}

	if err := store.SetVisibility(ctx, session.ID, model.VisibilityPublic); err != nil {
		t.Fatal(err)
	}
	audience, err := store.GetAudience(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if audience.Mode != model.AudienceAllPaired {
		t.Errorf("mode = %q, want all_paired", audience.Mode)
	}
	if audience.ExportCWD {
		t.Error("publishing enabled the working directory export without being asked")
	}
	if audience.AcceptMessages {
		t.Error("publishing enabled the inbox without being asked")
	}
}
