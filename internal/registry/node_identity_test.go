package registry

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
)

func identityStore(t *testing.T) *Registry {
	t.Helper()
	return openRegistryAt(t, filepath.Join(t.TempDir(), "agenthub.db"))
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
	created := model.NodeIdentity{
		ID:          "node_0123456789abcdef",
		DisplayName: "the wrong name",
		Platform:    "darwin/arm64",
		CreatedAt:   time.Now().UTC().Truncate(time.Millisecond),
	}
	if err := store.SaveNodeIdentity(ctx, created); err != nil {
		t.Fatal(err)
	}

	if err := store.SetNodeDisplayName(ctx, "the machine on my desk"); err != nil {
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

// Renaming a node that does not exist reports it. Without the row count this
// returns nil, having changed nothing: the caller reads back the old name and
// has no way to tell a failed rename from one the store quietly declined.
func TestRenamingBeforeThereIsAnIdentityIsRefused(t *testing.T) {
	err := identityStore(t).SetNodeDisplayName(context.Background(), "a name")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetNodeDisplayName() error = %v; want ErrNotFound", err)
	}
}

// The store holds one bound for every display name it keeps. A node that stored
// a longer name for itself would announce one its peers refuse under the same
// constant, and the failure would surface on the other machine.
func TestANameTooLongForAPeerIsNotStoredForThisNode(t *testing.T) {
	ctx := context.Background()
	store := identityStore(t)
	if err := store.SaveNodeIdentity(ctx, model.NodeIdentity{
		ID: "node_0123456789abcdef", DisplayName: "start", Platform: "linux/amd64",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}

	if err := store.SetNodeDisplayName(ctx, strings.Repeat("a", MaxDisplayName+1)); err == nil {
		t.Error("a name longer than a peer accepts was stored for this node")
	}
	if got := storedIdentity(t, store).DisplayName; got != "start" {
		t.Errorf("display name = %q; the refused name was written anyway", got)
	}
	if err := store.SetNodeDisplayName(ctx, ""); err == nil {
		t.Error("an empty name was accepted, leaving the node with nothing to announce")
	}
	if err := store.SetNodeDisplayName(ctx, strings.Repeat("a", MaxDisplayName)); err != nil {
		t.Errorf("a name of exactly the maximum was refused: %v", err)
	}
}
