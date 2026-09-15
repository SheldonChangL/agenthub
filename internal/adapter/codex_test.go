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

// Codex keeps a thread's name outside the rollout file, in one index for the
// whole installation. A session listed there shows that name; one that is not
// listed keeps its ID, and a missing index costs names, never sessions.
func TestCodexDiscoverReadsThreadNamesFromTheIndex(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "2026", "09", "15")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	named := "019efc25-2358-7491-9348-2b02e4dd38b3"
	anonymous := "019efc25-2358-7491-9348-2b02e4dd38b4"
	for _, id := range []string{named, anonymous} {
		line := "{\"type\":\"session_meta\",\"payload\":{\"id\":\"" + id + "\",\"cwd\":\"/work/demo\",\"source\":\"vscode\"}}\n"
		if err := os.WriteFile(filepath.Join(sessionDir, "rollout-"+id+".jsonl"), []byte(line), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	index := "{\"id\":\"" + named + "\",\"thread_name\":\"an early name\"}\n" +
		"{\"id\":\"" + named + "\",\"thread_name\":\"Build frontend testing workflows\"}\n" +
		"{\"id\":\"99999999-9999-4999-8999-999999999999\",\"thread_name\":\"a thread with no rollout\"}\n"
	if err := os.WriteFile(filepath.Join(root, "session_index.jsonl"), []byte(index), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 9, 15, 1, 0, 0, 0, time.UTC)
	sessions, err := CodexDiscoverer{Root: root, Now: func() time.Time { return now }}.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	titles := map[string]string{}
	for _, session := range sessions {
		titles[session.ProviderSessionID] = session.Title
	}
	// Later lines win: the index is appended to when a thread is renamed.
	if got := titles[named]; got != "Build frontend testing workflows" {
		t.Fatalf("title for the named thread = %q", got)
	}
	if got, ok := titles[anonymous]; !ok || got != "" {
		t.Fatalf("title for the unlisted thread = %q (present: %v); want empty", got, ok)
	}
}

func TestCodexDiscoverSurvivesAMissingThreadIndex(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "2026", "09", "15")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	id := "019efc25-2358-7491-9348-2b02e4dd38b3"
	line := "{\"type\":\"session_meta\",\"payload\":{\"id\":\"" + id + "\",\"cwd\":\"/work/demo\"}}\n"
	if err := os.WriteFile(filepath.Join(sessionDir, "rollout-"+id+".jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 15, 1, 0, 0, 0, time.UTC)
	sessions, err := CodexDiscoverer{Root: root, Now: func() time.Time { return now }}.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(sessions) != 1 || sessions[0].Title != "" {
		t.Fatalf("sessions = %#v; want one untitled session", sessions)
	}
}
