//go:build darwin || linux

package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// TestNodeProcessHelper is not a test: it is the body of the process
// TestListNodeProcessesReadsARealProcess starts under the node's name.
func TestNodeProcessHelper(t *testing.T) {
	if os.Getenv("AGENTHUB_NODE_PROCESS_HELPER") != "1" {
		t.Skip("helper process")
	}
	time.Sleep(30 * time.Second)
}

// The fakes above say what the restart does with a command line; this asks
// the real kernel for one. A process named agenthub-node is started with an
// argument that has a space in it — the default database on macOS lives under
// "Application Support" — and the listing has to give that argument back
// whole, and name the process the way pkill -x will.
func TestListNodeProcessesReadsARealProcess(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), nodeExecutable)
	copyExecutable(t, self, binary)
	database := "/Users/me/Library/Application Support/agenthub/agenthub.db"
	// #nosec G204 -- this test's own binary, copied under the node's name.
	command := exec.Command(binary, "-test.run=^TestNodeProcessHelper$", "--", "--db", database)
	command.Env = append(os.Environ(), "AGENTHUB_NODE_PROCESS_HELPER=1")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	want := []string{binary, "-test.run=^TestNodeProcessHelper$", "--", "--db", database}
	deadline := time.Now().Add(5 * time.Second)
	for {
		processes, err := listNodeProcesses(context.Background())
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, process := range processes {
			if process.PID != command.Process.Pid {
				continue
			}
			if !slices.Equal(process.Argv, want) {
				t.Fatalf("argv = %q, want %q", process.Argv, want)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid %d, named %s, was not listed among %d agenthub-node processes",
				command.Process.Pid, nodeExecutable, len(processes))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func copyExecutable(t *testing.T, from, to string) {
	t.Helper()
	source, err := os.Open(from) // #nosec G304 -- this test's own executable
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	target, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700) // #nosec G302 G304 -- a temp copy that has to be executable
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(target, source); err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
}
