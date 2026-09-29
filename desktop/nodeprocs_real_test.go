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
	"strings"
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
// Eight of them, the last entry of each one's environment a byte longer than
// the last: on macOS the kernel pads the environment's end to eight bytes, and
// one that ends on the boundary has nothing between it and the kernel's own
// strings (nodeprocs_darwin.go parseProcargs2). Whichever length this
// environment happens to be, one of the eight ends there.
//
// The Windows half runs in CI's windows job, the macOS half in its macos job
// as well as on a developer's Mac.
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
	want := append([]string{binary}, helperArgs...)
	wantPaths, _ := nodeDefaultPaths(runtime.GOOS, func(name string) string {
		return map[string]string{"HOME": home, "XDG_CONFIG_HOME": config, "AppData": config, "USERPROFILE": home}[name]
	})

	const lengths = 8
	commands := make([]*exec.Cmd, 0, lengths)
	fills := map[int]string{}
	for fill := range lengths {
		// #nosec G204 -- this test's own binary, copied under the node's name.
		command := exec.Command(binary, helperArgs...)
		// Last, so that its length is where the environment ends.
		command.Env = append(os.Environ(), "AGENTHUB_NODE_PROCESS_HELPER=1",
			"HOME="+home, "XDG_CONFIG_HOME="+config, "APPDATA="+config, "USERPROFILE="+home,
			"AGENTHUB_TEST_FILL="+strings.Repeat("f", fill))
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = command.Process.Kill()
			_ = command.Wait()
		})
		commands = append(commands, command)
		fills[command.Process.Pid] = strings.Repeat("f", fill)
	}

	for _, process := range waitForListed(t, fills) {
		fill := fills[process.PID]
		if !slices.Equal(process.Argv, want) {
			t.Fatalf("fill %d: argv = %q, want %q", len(fill), process.Argv, want)
		}
		if process.EnvErr != nil {
			t.Fatalf("fill %d: its environment was not read: %v", len(fill), process.EnvErr)
		}
		if process.UserErr != nil {
			t.Errorf("fill %d: a process this test started is not this user's: %v", len(fill), process.UserErr)
		}
		lookup := environmentLookup(runtime.GOOS, process.Env)
		if got := lookup("AGENTHUB_TEST_FILL"); got != fill {
			t.Errorf("fill %d: the environment's last entry read back as %q", len(fill), got)
		}
		paths, err := nodeDefaultPaths(runtime.GOOS, lookup)
		if err != nil {
			t.Fatalf("fill %d: its environment names no default paths: %v", len(fill), err)
		}
		if paths != wantPaths {
			t.Errorf("fill %d: from its environment: %+v, want %+v", len(fill), paths, wantPaths)
		}
	}

	// Killed and not yet waited for, each is a zombie on macOS and Linux —
	// what a node this app started and then stopped stays until the app
	// exits. It is not a running node, and it must not stop the listing. On
	// its way there a process can be caught exiting and not yet a zombie,
	// which macOS answers with EINVAL: that error, and only that kind
	// (transientListError), is asked again rather than failed.
	for _, command := range commands {
		if err := command.Process.Kill(); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(15 * time.Second) // PowerShell again
	for {
		processes, err := listNodeProcesses(context.Background())
		if err != nil && !transientListError(err) {
			t.Fatalf("list with exited nodes about: %v", err)
		}
		if err == nil && !slices.ContainsFunc(processes, func(p nodeProcess) bool { _, ours := fills[p.PID]; return ours }) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the killed processes are still listed, or the listing still fails: %d listed, %v", len(processes), err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitForListed lists agenthub-node processes until every pid in want is
// among them. A listing that fails the way one fails on a process caught
// exiting — another agenthub-node on this machine may be on its way out — is
// asked again until the deadline; any other failure is the test's. Asking
// again on every error would hide a misread: the kernel's strings after a new
// process's environment are wiped by the process itself moments after it
// starts, and a parse that fails only before that would pass on the retry.
func waitForListed(t *testing.T, want map[int]string) []nodeProcess {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second) // PowerShell can take seconds to start cold
	for {
		processes, err := listNodeProcesses(context.Background())
		if err != nil && !transientListError(err) {
			t.Fatalf("list: %v", err)
		}
		var found []nodeProcess
		for _, process := range processes {
			if _, ok := want[process.PID]; ok {
				found = append(found, process)
			}
		}
		if err == nil && len(found) == len(want) {
			return found
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d processes named %s were listed among %d agenthub-node processes (last error: %v)",
				len(found), len(want), nodeExecutable, len(processes), err)
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
