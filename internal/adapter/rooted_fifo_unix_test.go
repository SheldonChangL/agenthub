//go:build unix

package adapter

import (
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
)

// TestWalkJSONLSkipsFIFOWithoutBlocking pins the one non-regular case the walk
// may drop on its own: a FIFO the directory entry already identifies. Opening
// it would park the run until somebody writes, so it must never be opened.
func TestWalkJSONLSkipsFIFOWithoutBlocking(t *testing.T) {
	root := t.TempDir()
	fifo := filepath.Join(root, "pipe.jsonl")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	regular := filepath.Join(root, "session.jsonl")
	if err := os.WriteFile(regular, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	type result struct {
		visited []string
		err     error
	}
	done := make(chan result, 1)
	go func() {
		var visited []string
		_, err := walkJSONL(root, func(path string, _ *os.File, _ fs.FileInfo) (model.Session, bool, error) {
			visited = append(visited, path)
			return model.Session{}, false, nil
		})
		done <- result{visited: visited, err: err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("walkJSONL() error = %v; want the FIFO skipped without failing the run", got.err)
		}
		if len(got.visited) != 1 || got.visited[0] != regular {
			t.Fatalf("visited = %v; want only %q", got.visited, regular)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("walkJSONL blocked; the FIFO was opened instead of skipped")
	}
}
