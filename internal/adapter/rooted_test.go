package adapter

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"agenthub.local/agenthub/internal/model"
)

const (
	externalClaudeSessionID = "33333333-3333-4333-8333-333333333333"
	externalCodexSessionID  = "01a045ef-7f39-76a1-a638-e72b31535aaa"
	localClaudeSessionID    = "44444444-4444-4444-8444-444444444444"
	localCodexSessionID     = "01a045ef-7f39-76a1-a638-e72b31535bbb"
)

// writeExternal drops a JSONL file that a provider root has no business
// reading, and returns its path plus the directory holding it.
func writeExternal(t *testing.T, name, data string) (string, string) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, directory
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func TestClaudeDiscoverRefusesSymlinkEscapingRoot(t *testing.T) {
	external, externalDir := writeExternal(t, "external.jsonl",
		"{\"sessionId\":\""+externalClaudeSessionID+"\",\"cwd\":\"/outside\"}\n")

	root := t.TempDir()
	projectDir := filepath.Join(root, "projects", "synthetic-project")
	if err := os.MkdirAll(projectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legit := filepath.Join(projectDir, localClaudeSessionID+".jsonl")
	if err := os.WriteFile(legit, []byte("{\"sessionId\":\""+localClaudeSessionID+"\",\"cwd\":\"/work/demo\"}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, external, filepath.Join(projectDir, "escaped.jsonl"))
	symlinkOrSkip(t, externalDir, filepath.Join(root, "projects", "escaped-dir"))

	sessions, err := (ClaudeDiscoverer{Root: root}).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d; want 1", len(sessions))
	}
	got := sessions[0]
	if got.ProviderSessionID != localClaudeSessionID {
		t.Fatalf("providerSessionID = %q; want the in-root session", got.ProviderSessionID)
	}
	if got.MetadataPath != legit {
		t.Fatalf("metadataPath = %q; want %q", got.MetadataPath, legit)
	}
	for _, session := range sessions {
		if session.ProviderSessionID == externalClaudeSessionID {
			t.Fatalf("registered a session read from outside the root: %#v", session)
		}
	}
}

func TestCodexDiscoverRefusesSymlinkEscapingRoot(t *testing.T) {
	external, externalDir := writeExternal(t, "external.jsonl",
		"{\"type\":\"session_meta\",\"payload\":{\"id\":\""+externalCodexSessionID+"\",\"cwd\":\"/outside\",\"source\":\"cli\"}}\n")

	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", "2026", "08", "28")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legit := filepath.Join(sessionDir, "rollout-"+localCodexSessionID+".jsonl")
	data := "{\"type\":\"session_meta\",\"payload\":{\"id\":\"" + localCodexSessionID + "\",\"cwd\":\"/work/demo\",\"source\":\"cli\"}}\n"
	if err := os.WriteFile(legit, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, external, filepath.Join(sessionDir, "escaped.jsonl"))
	symlinkOrSkip(t, externalDir, filepath.Join(root, "sessions", "escaped-dir"))

	sessions, err := (CodexDiscoverer{Root: root}).Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover() error = %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("len(sessions) = %d; want 1", len(sessions))
	}
	got := sessions[0]
	if got.ProviderSessionID != localCodexSessionID {
		t.Fatalf("providerSessionID = %q; want the in-root session", got.ProviderSessionID)
	}
	if got.MetadataPath != legit {
		t.Fatalf("metadataPath = %q; want %q", got.MetadataPath, legit)
	}
	for _, session := range sessions {
		if session.ProviderSessionID == externalCodexSessionID {
			t.Fatalf("registered a session read from outside the root: %#v", session)
		}
	}
}

// TestWalkJSONLPropagatesVanishedEntry proves the discovery run reports a
// refused open instead of quietly returning a shorter registry. The walk reads
// a directory in one batch, so deleting the second entry while the first is
// being visited makes the second open fail deterministically, exactly like the
// escapes, permission denials and I/O errors that must not be swallowed.
func TestWalkJSONLPropagatesVanishedEntry(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "a.jsonl")
	second := filepath.Join(root, "b.jsonl")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	visited := 0
	sessions, err := walkJSONL(root, func(path string, _ *os.File, _ fs.FileInfo) (model.Session, bool, error) {
		visited++
		if path == first {
			if err := os.Remove(second); err != nil {
				t.Fatal(err)
			}
		}
		return model.Session{}, false, nil
	})
	if err == nil {
		t.Fatalf("walkJSONL() error = nil, sessions = %#v; want the failed open reported", sessions)
	}
	if sessions != nil {
		t.Fatalf("sessions = %#v; want nil alongside the error", sessions)
	}
	if !strings.Contains(err.Error(), second) {
		t.Fatalf("error = %v; want it to name %q", err, second)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error = %v; want it to wrap fs.ErrNotExist", err)
	}
	if visited != 1 {
		t.Fatalf("visited = %d; want the run to stop after the first entry", visited)
	}
}

// TestWalkJSONLPropagatesUnreadableEntry covers the permission denial the
// rooted open can hit on a file the walk did list.
func TestWalkJSONLPropagatesUnreadableEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits do not gate opens on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses the permission bits this test relies on")
	}

	root := t.TempDir()
	path := filepath.Join(root, "unreadable.jsonl")
	if err := os.WriteFile(path, []byte("{}\n"), 0o000); err != nil {
		t.Fatal(err)
	}

	sessions, err := walkJSONL(root, func(string, *os.File, fs.FileInfo) (model.Session, bool, error) {
		t.Fatal("visit called for an entry that could not be opened")
		return model.Session{}, false, nil
	})
	if err == nil {
		t.Fatalf("walkJSONL() error = nil, sessions = %#v; want the denied open reported", sessions)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("error = %v; want it to wrap fs.ErrPermission", err)
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error = %v; want it to name %q", err, path)
	}
}
