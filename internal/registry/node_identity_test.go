package registry

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/label"
	"agenthub.local/agenthub/internal/model"
)

func identityStore(t *testing.T) *Registry {
	t.Helper()
	return openRegistryAt(t, filepath.Join(t.TempDir(), "agenthub.db"))
}

func seedIdentity(t *testing.T, store *Registry, name string) model.NodeIdentity {
	t.Helper()
	created := model.NodeIdentity{
		ID:          "node_0123456789abcdef",
		DisplayName: name,
		Platform:    "darwin/arm64",
		CreatedAt:   time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := store.SaveNodeIdentity(context.Background(), created); err != nil {
		t.Fatalf("SaveNodeIdentity() error = %v", err)
	}
	return created
}

func storedIdentity(t *testing.T, store *Registry) model.NodeIdentity {
	t.Helper()
	identity, err := store.GetNodeIdentity(context.Background())
	if err != nil {
		t.Fatalf("GetNodeIdentity() error = %v", err)
	}
	return identity
}

// Renaming changes the label and nothing else. Every pairing is keyed on the
// node id, so an id that moved with the name would silently break all of them —
// and the owner would see it as peers going quiet, not as a rename.
func TestRenamingANodeLeavesItsIdentityIntact(t *testing.T) {
	ctx := context.Background()
	store := identityStore(t)
	created := seedIdentity(t, store, "the wrong name")

	if err := store.SetNodeDisplayName(ctx, "the machine on my desk", true); err != nil {
		t.Fatalf("SetNodeDisplayName() error = %v", err)
	}
	after := storedIdentity(t, store)
	if after.DisplayName != "the machine on my desk" {
		t.Errorf("display name = %q, want the new one", after.DisplayName)
	}
	if after.ID != created.ID {
		t.Errorf("node id = %q, want %q unchanged; a rename must not break pairings",
			after.ID, created.ID)
	}
	if after.Platform != created.Platform {
		t.Errorf("platform = %q, want %q unchanged", after.Platform, created.Platform)
	}
	if !after.CreatedAt.Equal(created.CreatedAt) {
		t.Errorf("created at = %v, want %v unchanged", after.CreatedAt, created.CreatedAt)
	}
}

// Whether a person picked the name is stored with it, and survives a reopen.
//
// It is the whole basis for leaving one name alone and re-reading another, so a
// column that silently forgot would turn a pinned name back into one that
// follows the machine — undoing the owner's choice on the next restart.
func TestWhetherTheNameWasPickedIsRemembered(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agenthub.db")
	store := openRegistryAt(t, path)
	seedIdentity(t, store, "read from the machine")

	if got := storedIdentity(t, store).NameIsChosen; got {
		t.Error("a name saved with no choice recorded came back as chosen")
	}
	if err := store.SetNodeDisplayName(ctx, "picked by hand", true); err != nil {
		t.Fatal(err)
	}
	if got := storedIdentity(t, store).NameIsChosen; !got {
		t.Fatal("a chosen name came back as one nobody picked")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openRegistryAt(t, path)
	after := storedIdentity(t, reopened)
	if !after.NameIsChosen {
		t.Error("the choice was forgotten across a reopen; the next start would overwrite it")
	}
	if after.DisplayName != "picked by hand" {
		t.Errorf("display name = %q after a reopen", after.DisplayName)
	}

	// And it can be handed back: a name re-read from the machine is not chosen.
	if err := reopened.SetNodeDisplayName(ctx, "read again", false); err != nil {
		t.Fatal(err)
	}
	if storedIdentity(t, reopened).NameIsChosen {
		t.Error("a name recorded as read from the machine came back as chosen")
	}
}

// A database created before the provenance column exists gains it, and the
// names already in it read as nobody's choice.
//
// That is not a convenient default, it is the fact: before the column there was
// no way to pick a name, so every name in such a database came off the machine.
// Reading them as chosen would pin the wrong names this change exists to fix.
func TestAnOlderDatabaseGainsProvenanceAndReadsAsUnchosen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agenthub.db")
	store := openRegistryAt(t, path)
	seedIdentity(t, store, "J-SomeoneElse.example.com.tw")
	// Drop the column to stand in for a database written by the earlier build.
	if _, err := store.db.ExecContext(ctx,
		`ALTER TABLE node_identity DROP COLUMN name_is_chosen`); err != nil {
		t.Fatalf("could not reproduce the older schema: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openRegistryAt(t, path)
	after := storedIdentity(t, reopened)
	if after.NameIsChosen {
		t.Error("a name from before the column existed reads as one somebody picked; " +
			"the wrong name would then be kept forever")
	}
	if after.DisplayName != "J-SomeoneElse.example.com.tw" {
		t.Errorf("display name = %q; the migration lost it", after.DisplayName)
	}
}

// Renaming a node that does not exist reports it. Without the row count this
// returns nil, having changed nothing: the caller reads back the old name and
// has no way to tell a failed rename from one the store quietly declined.
func TestRenamingBeforeThereIsAnIdentityIsRefused(t *testing.T) {
	err := identityStore(t).SetNodeDisplayName(context.Background(), "a name", true)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetNodeDisplayName() error = %v; want ErrNotFound", err)
	}
}

// This node's own name is held to what an announcement will actually carry.
//
// Not the trust store's larger bound. A longer name is stored, printed at
// startup as the announced name, shown by the desktop as the string the network
// sees — and then dropped from the mDNS TXT record, which has nowhere to report
// that. The owner looks for their machine on another screen and it has no name.
func TestANameThatCouldNotBeAnnouncedIsNotStoredForThisNode(t *testing.T) {
	ctx := context.Background()
	store := identityStore(t)
	seedIdentity(t, store, "start")

	// Between the two bounds: accepted by the trust store for a peer, dropped
	// by the announcement for this node. 22 CJK characters is 66 bytes, and an
	// unremarkable name in a product whose own UI is written in Chinese.
	tooLong := strings.Repeat("三", 22)
	if len(tooLong) <= label.MaxLength || len(tooLong) > MaxDisplayName {
		t.Fatalf("the fixture is %d bytes; it has to sit between the announcement's %d and "+
			"the trust store's %d for this test to mean anything",
			len(tooLong), label.MaxLength, MaxDisplayName)
	}
	if err := store.SetNodeDisplayName(ctx, tooLong, true); err == nil {
		t.Error("a name the announcement would silently drop was stored as this node's own")
	}
	if got := storedIdentity(t, store).DisplayName; got != "start" {
		t.Errorf("display name = %q; the refused name was written anyway", got)
	}

	// A name that renders as nothing is refused for the same reason: it would
	// be dropped from the announcement, not shown as an empty row.
	for _, unshowable := range []string{" ", "\u200b\u200b", "\x1b[31mred", "a\r\nb", "\xff\xfe"} {
		if err := store.SetNodeDisplayName(ctx, unshowable, true); err == nil {
			t.Errorf("SetNodeDisplayName(%q) was accepted", unshowable)
		}
	}

	// A name the announcement would rewrite is stored in the rewritten form,
	// not refused. What the owner reads back is then the string on the wire,
	// which is the property this exists for; making them retype it was not.
	for typed, announced := range map[string]string{
		"my  mac":          "my mac",
		"cafe\u0301 mac":   "caf\u00e9 mac",
		"\u2615\ufe0f mac": "\u2615 mac",
	} {
		if err := store.SetNodeDisplayName(ctx, typed, true); err != nil {
			t.Errorf("SetNodeDisplayName(%q) error = %v", typed, err)
			continue
		}
		if got := storedIdentity(t, store).DisplayName; got != announced {
			t.Errorf("SetNodeDisplayName(%q) stored %q, want the announced form %q",
				typed, got, announced)
		}
	}

	// And a name right at the bound is usable, so nothing is refused by an
	// off-by-one.
	atLimit := strings.Repeat("a", label.MaxLength)
	if err := store.SetNodeDisplayName(ctx, atLimit, false); err != nil {
		t.Errorf("a name of exactly %d bytes was refused: %v", label.MaxLength, err)
	}
}

// The other write path enforces the same rule. Two write paths that disagree
// leave the invariant true only for whichever one the caller happened to take —
// and SaveNodeIdentity is the one that creates the row in the first place.
func TestCreatingANodeWithAnUnannounceableNameIsRefused(t *testing.T) {
	ctx := context.Background()
	store := identityStore(t)
	err := store.SaveNodeIdentity(ctx, model.NodeIdentity{
		ID:          "node_0123456789abcdef",
		DisplayName: strings.Repeat("a", label.MaxLength+1),
		Platform:    "linux/amd64",
		CreatedAt:   time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("a node was created with a name no announcement would carry")
	}
	if _, err := store.GetNodeIdentity(ctx); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetNodeIdentity() error = %v; the refused identity was written anyway", err)
	}
}
