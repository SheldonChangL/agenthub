package identity

import (
	"context"
	"path/filepath"
	"testing"

	"agenthub.local/agenthub/internal/registry"
)

func TestLoadOrCreatePersistsStableIdentity(t *testing.T) {
	ctx := context.Background()
	store, err := registry.Open(ctx, filepath.Join(t.TempDir(), "agenthub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	first, err := LoadOrCreate(ctx, store)
	if err != nil {
		t.Fatalf("first LoadOrCreate() error = %v", err)
	}
	second, err := LoadOrCreate(ctx, store)
	if err != nil {
		t.Fatalf("second LoadOrCreate() error = %v", err)
	}
	if first.ID == "" || first.ID != second.ID {
		t.Fatalf("identity IDs = %q, %q; want same non-empty ID", first.ID, second.ID)
	}
}
