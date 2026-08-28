package adapter

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
)

func TestClaudeDiscoverReadsMetadataWithoutMessageContent(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "projects", "synthetic-project")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projectDir, "11111111-1111-4111-8111-111111111111.jsonl")
	data := "{\"type\":\"user\",\"sessionId\":\"11111111-1111-4111-8111-111111111111\",\"cwd\":\"/work/demo\",\"timestamp\":\"2026-08-28T01:00:00Z\",\"message\":{\"content\":\"must-not-be-stored\"}}\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 28, 1, 1, 0, 0, time.UTC)
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}

	discoverer := ClaudeDiscoverer{
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
	if got.ID != "claude:11111111-1111-4111-8111-111111111111" || got.CWD != "/work/demo" {
		t.Fatalf("session = %#v", got)
	}
	if got.Status != model.StatusActive || got.StatusSource != "metadata_process_heuristic" {
		t.Fatalf("status = %q, source = %q", got.Status, got.StatusSource)
	}
	if got.Visibility != model.VisibilityPrivate {
		t.Fatalf("visibility = %q; want private", got.Visibility)
	}
}

func TestClaudeDiscoverSkipsSidechainSessions(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "projects", "synthetic-project")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projectDir, "22222222-2222-4222-8222-222222222222.jsonl")
	data := "{\"sessionId\":\"22222222-2222-4222-8222-222222222222\",\"cwd\":\"/work/demo\",\"isSidechain\":true}\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	sessions, err := (ClaudeDiscoverer{Root: root}).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(sessions) != 0 {
		t.Fatalf("len(sessions) = %d; want 0", len(sessions))
	}
}
