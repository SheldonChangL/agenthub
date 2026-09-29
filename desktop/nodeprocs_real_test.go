//go:build darwin || linux || windows

package main

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// The fakes elsewhere say what the restart does with a command line; this asks
// the real operating system for one. A process named agenthub-node is started
// with arguments that have spaces and quotes in them — the default database on
// macOS lives under "Application Support", and on Windows the whole command
// line is one string that has to be split back the way the node's own os.Args
// is — and with an environment whose home and config directory are not this
// test's. The listing has to give every argument back whole, the environment
// as the process has it, and name the process the way pkill -x or taskkill /IM
// will.
//
// The Windows half runs in CI's windows job; nothing else runs it.
func TestListNodeProcessesReadsARealProcess(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, nodeExecutable)
	copyExecutable(t, self, binary)
	home := filepath.Join(dir, "a home", `with "quotes"`)
	config := filepath.Join(dir, "a config dir")
	database := filepath.Join(dir, "Application Support", "not the default", "agenthub.db")
	helperArgs := []string{"-test.run=^TestNodeProcessHelper$", "--", "--db", database,
		"--display-name", `say "hi" to me`, "", `trailing\`}
	// #nosec G204 -- this test's own binary, copied under the node's name.
	command := exec.Command(binary, helperArgs...)
	command.Env = append(os.Environ(), "AGENTHUB_NODE_PROCESS_HELPER=1",
		"HOME="+home, "XDG_CONFIG_HOME="+config, "APPDATA="+config, "USERPROFILE="+home)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		_ = command.Process.Kill()
		if !waited {
			_ = command.Wait()
		}
	})
	want := append([]string{binary}, helperArgs...)
	process := waitForListed(t, command.Process.Pid)
	if !slices.Equal(process.Argv, want) {
		t.Fatalf("argv = %q, want %q", process.Argv, want)
	}
	if process.EnvErr != nil {
		t.Fatalf("its environment was not read: %v", process.EnvErr)
	}
	if process.UserErr != nil {
		t.Errorf("a process this test started is not this user's: %v", process.UserErr)
	}
	paths, err := nodeDefaultPaths(runtime.GOOS, environmentLookup(runtime.GOOS, process.Env))
	if err != nil {
		t.Fatalf("its environment names no default paths: %v", err)
	}
	wantPaths, _ := nodeDefaultPaths(runtime.GOOS, func(name string) string {
		return map[string]string{"HOME": home, "XDG_CONFIG_HOME": config, "AppData": config, "USERPROFILE": home}[name]
	})
	if paths != wantPaths {
		t.Errorf("from its environment: %+v, want %+v", paths, wantPaths)
	}

	// Killed and not yet waited for, it is a zombie on macOS and Linux —
	// what a node this app started and then stopped stays until the app
	// exits. It is not a running node, and it must not stop the listing.
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		processes, err := listNodeProcesses(context.Background())
		if err != nil {
			t.Fatalf("list with an exited node about: %v", err)
		}
		if !slices.ContainsFunc(processes, func(p nodeProcess) bool { return p.PID == command.Process.Pid }) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid %d is still listed after it was killed", command.Process.Pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
	waited = true
	_ = command.Wait()
}

// waitForListed lists agenthub-node processes until pid is among them.
func waitForListed(t *testing.T, pid int) nodeProcess {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second) // PowerShell can take seconds to start cold
	for {
		processes, err := listNodeProcesses(context.Background())
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, process := range processes {
			if process.PID == pid {
				return process
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("pid %d, named %s, was not listed among %d agenthub-node processes", pid, nodeExecutable, len(processes))
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
