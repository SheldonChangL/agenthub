package adapter

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
)

func TestCodexDiscoverReadsSessionMeta(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "2026", "08", "28")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionDir, "rollout-2026-08-28T01-00-00-01a045ef-7f39-76a1-a638-e72b3153571d.jsonl")
	data := "{\"type\":\"session_meta\",\"payload\":{\"id\":\"01a045ef-7f39-76a1-a638-e72b3153571d\",\"cwd\":\"C:\\\\work\\\\demo\",\"source\":\"cli\",\"base_instructions\":\"must-not-be-stored\"}}\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 28, 1, 1, 0, 0, time.UTC)
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}

	discoverer := CodexDiscoverer{
		Root:    root,
		Now:     func() time.Time { return now },
		Process: ProcessState{Known: true, Running: true},
	}
	sessions, err := discoverer.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d; want 1", len(sessions))
	}
	got := sessions[0]
	if got.ID != "codex:01a045ef-7f39-76a1-a638-e72b3153571d" || got.Source != "cli" {
		t.Fatalf("session = %#v", got)
	}
	if got.Status != model.StatusActive || got.Visibility != model.VisibilityPrivate {
		t.Fatalf("status = %q, visibility = %q", got.Status, got.Visibility)
	}
}
