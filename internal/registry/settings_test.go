package registry

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"agenthub.local/agenthub/internal/nodeconfig"
)

func TestNodeSettingsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)

	address := "192.168.1.10:7463"
	on := true
	ranges := []string{"122.122.0.0/16", "10.9.0.0/16"}
	if err := store.SaveNodeSettings(ctx, nodeconfig.Partial{
		PeerListen: &address, AllowLAN: &on, TreatAsPrivate: &ranges,
	}); err != nil {
		t.Fatalf("SaveNodeSettings() error = %v", err)
	}

	stored, err := store.GetNodeSettings(ctx)
	if err != nil {
		t.Fatalf("GetNodeSettings() error = %v", err)
	}
	if stored.PeerListen == nil || *stored.PeerListen != address {
		t.Fatalf("peerListen = %v", stored.PeerListen)
	}
	if stored.AllowLAN == nil || !*stored.AllowLAN {
		t.Fatalf("allowLan = %v", stored.AllowLAN)
	}
	if stored.TreatAsPrivate == nil || len(*stored.TreatAsPrivate) != 2 || (*stored.TreatAsPrivate)[0] != ranges[0] {
		t.Fatalf("treatAsPrivate = %v", stored.TreatAsPrivate)
	}
	// Nothing was said about these, so nothing is remembered about them: the
	// node's own defaults apply, rather than whatever this write happened to
	// default them to.
	if stored.Discover != nil || stored.AutoWake != nil {
		t.Fatalf("a write of three settings pinned others: discover = %v, autoWake = %v",
			stored.Discover, stored.AutoWake)
	}
}

// Writing one setting must not disturb the rest. A desktop toggling discovery
// would otherwise silently withdraw the owner's private-range declaration.
func TestSaveNodeSettingsLeavesUnmentionedSettingsAlone(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	on, off := true, false
	ranges := []string{"122.122.0.0/16"}
	if err := store.SaveNodeSettings(ctx, nodeconfig.Partial{AllowLAN: &on, TreatAsPrivate: &ranges}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNodeSettings(ctx, nodeconfig.Partial{Discover: &off}); err != nil {
		t.Fatal(err)
	}
	stored, err := store.GetNodeSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stored.AllowLAN == nil || !*stored.AllowLAN {
		t.Fatalf("allowLan was lost writing discover: %v", stored.AllowLAN)
	}
	if stored.TreatAsPrivate == nil || len(*stored.TreatAsPrivate) != 1 {
		t.Fatalf("treatAsPrivate was lost writing discover: %v", stored.TreatAsPrivate)
	}
	if stored.Discover == nil || *stored.Discover {
		t.Fatalf("discover = %v; a stored false must stay a stored false", stored.Discover)
	}
	// A declaration cleared on purpose is stored as an empty list, which is not
	// the same as never having been set.
	empty := []string{}
	if err := store.SaveNodeSettings(ctx, nodeconfig.Partial{TreatAsPrivate: &empty}); err != nil {
		t.Fatal(err)
	}
	cleared, err := store.GetNodeSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.TreatAsPrivate == nil || len(*cleared.TreatAsPrivate) != 0 {
		t.Fatalf("cleared ranges = %v", cleared.TreatAsPrivate)
	}
}

// A database written by a build that had no settings table must open, and must
// behave the way that build did: everything absent, so the flag defaults apply.
func TestOpeningADatabaseFromAnEarlierBuildAddsTheSettingsTable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agenthub.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// The shape an earlier build left behind: sessions and an identity, and no
	// node_settings anywhere.
	if _, err := old.ExecContext(ctx, `
CREATE TABLE node_identity (singleton INTEGER PRIMARY KEY, id TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL, platform TEXT NOT NULL, created_at_ms INTEGER NOT NULL);
INSERT INTO node_identity VALUES (1, 'node_1234567890123456', 'desk', 'darwin', 1700000000000);`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() on a database from an earlier build: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	stored, err := store.GetNodeSettings(ctx)
	if err != nil {
		t.Fatalf("GetNodeSettings() error = %v", err)
	}
	if !stored.Empty() {
		t.Fatalf("an upgraded database already remembers %+v; it must start with nothing, "+
			"so the node keeps the behaviour it had", stored)
	}
	settings, _ := nodeconfig.Resolve(nodeconfig.Partial{}, stored, nodeconfig.DefaultSettings())
	if settings.PeerListen != "127.0.0.1:7463" || settings.AllowLAN || settings.Discover || settings.AutoWake {
		t.Fatalf("an upgraded database resolves to %+v, not the previous build's defaults", settings)
	}
	// And the table is now there: a write survives a reopen.
	on := true
	if err := store.SaveNodeSettings(ctx, nodeconfig.Partial{Discover: &on}); err != nil {
		t.Fatalf("SaveNodeSettings() after upgrade: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	again, err := reopened.GetNodeSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if again.Discover == nil || !*again.Discover {
		t.Fatalf("discover did not survive a reopen: %v", again.Discover)
	}
}
