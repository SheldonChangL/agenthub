package hub

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
	processmodel "agenthub.local/agenthub/internal/process"
	"agenthub.local/agenthub/internal/registry"
)

func TestDiscoverUpsertsBothProvidersAsPrivate(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	claudeRoot := filepath.Join(base, "claude")
	codexRoot := filepath.Join(base, "codex")
	writeFixture(t, filepath.Join(claudeRoot, "projects", "p", "c.jsonl"), "{\"sessionId\":\"c1\",\"cwd\":\"/claude\"}\n")
	writeFixture(t, filepath.Join(codexRoot, "sessions", "2026", "08", "28", "x.jsonl"), "{\"type\":\"session_meta\",\"payload\":{\"id\":\"x1\",\"cwd\":\"/codex\"}}\n")

	store, err := registry.Open(ctx, filepath.Join(base, "agenthub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	service := New(store, Config{
		ClaudeRoot: claudeRoot,
		CodexRoot:  codexRoot,
		Now:        func() time.Time { return now },
		Processes: func(context.Context) map[model.Provider]processmodel.State {
			return map[model.Provider]processmodel.State{
				model.ProviderClaude: {Known: true, Running: true},
				model.ProviderCodex:  {Known: true, Running: true},
			}
		},
	})

	result, err := service.Discover(ctx)
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if result.Claude != 1 || result.Codex != 1 || result.Total != 2 {
		t.Fatalf("result = %#v", result)
	}
	sessions, err := store.ListSessions(ctx, registry.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range sessions {
		if session.Visibility != model.VisibilityPrivate {
			t.Fatalf("session %q visibility = %q; want private", session.ID, session.Visibility)
		}
	}
}

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
