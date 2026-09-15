package adapter

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

// A rename appends a new title line rather than editing the old one, and a
// long transcript pushes the current name far past the head scan. Reading the
// first title, or only the first lines, is how a session shows a name it was
// renamed away from hours ago.
func TestClaudeDiscoverReadsTheCurrentTitleFromTheEndOfALongTranscript(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "projects", "synthetic-project")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	id := "11111111-1111-4111-8111-111111111111"
	path := filepath.Join(projectDir, id+".jsonl")

	var builder strings.Builder
	builder.WriteString("{\"type\":\"user\",\"sessionId\":\"" + id + "\",\"cwd\":\"/work/demo\"}\n")
	builder.WriteString("{\"type\":\"custom-title\",\"customTitle\":\"the old name\",\"sessionId\":\"" + id + "\"}\n")
	// More lines than the head scan reads, so the current title is reachable
	// only from the end of the file.
	filler := strings.Repeat("x", 4096)
	for i := 0; i < 400; i++ {
		builder.WriteString("{\"type\":\"assistant\",\"sessionId\":\"" + id + "\",\"pad\":\"" + filler + "\"}\n")
	}
	builder.WriteString("{\"type\":\"custom-title\",\"customTitle\":\"  the current name\\u0007  \",\"sessionId\":\"" + id + "\"}\n")
	builder.WriteString("{\"type\":\"assistant\",\"sessionId\":\"" + id + "\"}\n")
	if err := os.WriteFile(path, []byte(builder.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 9, 15, 1, 0, 0, 0, time.UTC)
	sessions, err := ClaudeDiscoverer{Root: root, Now: func() time.Time { return now }}.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d; want 1", len(sessions))
	}
	// Trimmed, and the BEL is gone: a title lands in a one-line cell.
	if got := sessions[0].Title; got != "the current name" {
		t.Fatalf("title = %q; want %q", got, "the current name")
	}
}

// A session the provider never named has no title at all. The empty string is
// what tells the UI to fall back to the ID, so it must not become something
// else along the way.
func TestClaudeDiscoverLeavesAnUnnamedSessionWithoutATitle(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "projects", "synthetic-project")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	id := "22222222-2222-4222-8222-222222222222"
	data := "{\"type\":\"user\",\"sessionId\":\"" + id + "\",\"cwd\":\"/work/demo\"}\n"
	if err := os.WriteFile(filepath.Join(projectDir, id+".jsonl"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 15, 1, 0, 0, 0, time.UTC)
	sessions, err := ClaudeDiscoverer{Root: root, Now: func() time.Time { return now }}.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(sessions) != 1 || sessions[0].Title != "" {
		t.Fatalf("sessions = %#v; want one session with no title", sessions)
	}
}

// A title longer than the column can ever show is cut, not dropped: the first
// words of a long name still say which conversation the row is.
func TestClaudeDiscoverClipsAnOverlongTitle(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "projects", "synthetic-project")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	id := "33333333-3333-4333-8333-333333333333"
	long := strings.Repeat("標", model.MaxTitleLength+40)
	data := "{\"type\":\"user\",\"sessionId\":\"" + id + "\",\"cwd\":\"/work/demo\"}\n" +
		"{\"type\":\"custom-title\",\"customTitle\":\"" + long + "\",\"sessionId\":\"" + id + "\"}\n"
	if err := os.WriteFile(filepath.Join(projectDir, id+".jsonl"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 15, 1, 0, 0, 0, time.UTC)
	sessions, err := ClaudeDiscoverer{Root: root, Now: func() time.Time { return now }}.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if got := []rune(sessions[0].Title); len(got) != model.MaxTitleLength {
		t.Fatalf("title length = %d; want %d", len(got), model.MaxTitleLength)
	}
}
