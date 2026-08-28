package hub

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
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

// stubStore lets the test choose what the registry does per session, which is
// the only way to reach the skip-versus-abort decision: the metadata parsers
// already reject every record the real store would refuse.
type stubStore struct {
	err map[string]error
	got []string
}

func (s *stubStore) UpsertSession(_ context.Context, session model.Session) (model.Session, error) {
	if err, ok := s.err[session.ProviderSessionID]; ok {
		return model.Session{}, err
	}
	s.got = append(s.got, session.ProviderSessionID)
	return session, nil
}

func discoveryFixture(t *testing.T) Config {
	t.Helper()
	base := t.TempDir()
	claudeRoot := filepath.Join(base, "claude")
	codexRoot := filepath.Join(base, "codex")
	writeFixture(t, filepath.Join(claudeRoot, "projects", "p", "a.jsonl"),
		"{\"sessionId\":\"good-1\",\"cwd\":\"/claude\"}\n")
	writeFixture(t, filepath.Join(claudeRoot, "projects", "p", "b.jsonl"),
		"{\"sessionId\":\"bad-1\",\"cwd\":\"/claude\"}\n")
	writeFixture(t, filepath.Join(codexRoot, "sessions", "2026", "08", "28", "x.jsonl"),
		"{\"type\":\"session_meta\",\"payload\":{\"id\":\"codex-1\",\"cwd\":\"/codex\"}}\n")
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)
	return Config{
		ClaudeRoot: claudeRoot,
		CodexRoot:  codexRoot,
		Now:        func() time.Time { return now },
		Processes: func(context.Context) map[model.Provider]processmodel.State {
			return map[model.Provider]processmodel.State{
				model.ProviderClaude: {Known: true, Running: true},
				model.ProviderCodex:  {Known: true, Running: true},
			}
		},
	}
}

// A record the store will never accept costs only that record. Anything able to
// write a file under a provider directory could otherwise disable discovery.
func TestDiscoverSkipsUnusableSessionsWithoutLosingTheBatch(t *testing.T) {
	store := &stubStore{err: map[string]error{
		"bad-1": fmt.Errorf("%w: provider session id contains a separator", registry.ErrInvalidSession),
	}}

	result, err := New(store, discoveryFixture(t)).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v; one unusable record must not fail the scan", err)
	}
	if result.Claude != 1 {
		t.Errorf("Claude = %d, want the one usable session", result.Claude)
	}
	if result.Codex != 1 {
		t.Errorf("Codex = %d; an unrelated provider must be unaffected", result.Codex)
	}
	if result.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", result.Skipped)
	}
	if slices.Contains(store.got, "bad-1") {
		t.Error("the rejected session was registered anyway")
	}
}

// An unavailable database is not an unusable record. Reporting a successful but
// empty scan would read as "you have no sessions" rather than "the scan failed".
func TestDiscoverFailsWhenTheStoreIsUnavailable(t *testing.T) {
	for name, storeErr := range map[string]error{
		"context cancelled": context.Canceled,
		"database busy":     errors.New("database is locked"),
	} {
		t.Run(name, func(t *testing.T) {
			store := &stubStore{err: map[string]error{"good-1": storeErr, "bad-1": storeErr, "codex-1": storeErr}}
			result, err := New(store, discoveryFixture(t)).Discover(context.Background())
			if err == nil {
				t.Fatalf("Discover() returned %+v and no error", result)
			}
			if result != (DiscoveryResult{}) {
				t.Errorf("Discover() reported counts alongside a failure: %+v", result)
			}
		})
	}
}
