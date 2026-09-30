//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeProc is a /proc of made-up processes, laid out the way Linux lays one
// out, for listNodeProcessesIn. Every entry is this user's, as a directory
// this test creates is.
type fakeProc struct {
	t    *testing.T
	root string
}

func newFakeProc(t *testing.T) *fakeProc {
	t.Helper()
	return &fakeProc{t: t, root: t.TempDir()}
}

// add makes /proc/<pid> for a process named comm in state, with this command
// line and environment, each entry NUL-terminated as the kernel gives them.
// A nil cmdline or env is an empty file.
func (p *fakeProc) add(pid int, comm string, state byte, cmdline, env []string) {
	p.t.Helper()
	if err := os.MkdirAll(p.dir(pid), 0o700); err != nil {
		p.t.Fatal(err)
	}
	p.write(pid, "comm", comm+"\n")
	p.setState(pid, state)
	p.write(pid, "cmdline", nulTerminated(cmdline))
	p.write(pid, "environ", nulTerminated(env))
}

func (p *fakeProc) dir(pid int) string { return filepath.Join(p.root, fmt.Sprint(pid)) }

func (p *fakeProc) setState(pid int, state byte) {
	p.t.Helper()
	p.write(pid, "stat", fmt.Sprintf("%d (%s) %c 1 %d %d 0 -1", pid, nodeExecutable, state, pid, pid))
}

// write replaces one file whole, by rename, so that a listing running at the
// same time reads the old contents or the new and never half of either.
func (p *fakeProc) write(pid int, name, contents string) {
	p.t.Helper()
	temp := filepath.Join(p.dir(pid), "."+name)
	if err := os.WriteFile(temp, []byte(contents), 0o600); err != nil {
		p.t.Fatal(err)
	}
	if err := os.Rename(temp, filepath.Join(p.dir(pid), name)); err != nil {
		p.t.Fatal(err)
	}
}

func (p *fakeProc) list() ([]nodeProcess, error) {
	return listNodeProcessesIn(p.root, os.ReadFile)
}

// install makes listNodes this /proc's listing, counting each call.
func (p *fakeProc) install(calls *int) {
	listWas := listNodes
	p.t.Cleanup(func() { listNodes = listWas })
	listNodes = func(context.Context) ([]nodeProcess, error) {
		*calls++
		return p.list()
	}
}

func nulTerminated(entries []string) string {
	var joined strings.Builder
	for _, entry := range entries {
		joined.WriteString(entry + "\x00")
	}
	return joined.String()
}

var (
	linuxNodeArgv = []string{"/usr/local/bin/agenthub-node", "--db", "/home/me/a b/agenthub.db"}
	linuxNodeEnv  = []string{"HOME=/home/me", "XDG_CONFIG_HOME=/home/me/.config"}
)

// A running node is listed whole; a zombie, which has an empty command line
// and holds nothing, is not listed and does not stop the listing; a node that
// is exiting and not yet a zombie, which also has an empty command line, is
// neither listed nor taken for gone: the listing fails in the way
// listNodesSettled and waitForProcessGone ask again.
func TestLinuxListingTakesAnEmptyCommandLineForANodeExiting(t *testing.T) {
	proc := newFakeProc(t)
	proc.add(100, nodeExecutable, 'S', linuxNodeArgv, linuxNodeEnv)
	proc.add(101, nodeExecutable, 'Z', nil, nil)
	proc.add(102, "bash", 'S', nil, nil) // not a node: its command line is not read
	processes, err := proc.list()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(processes) != 1 || processes[0].PID != 100 || !slices.Equal(processes[0].Argv, linuxNodeArgv) ||
		!slices.Equal(processes[0].Env, linuxNodeEnv) || processes[0].EnvErr != nil {
		t.Fatalf("listed %+v, want pid 100 with argv %q and env %q", processes, linuxNodeArgv, linuxNodeEnv)
	}

	proc.add(103, nodeExecutable, 'S', nil, linuxNodeEnv)
	processes, err = proc.list()
	if err == nil || !transientListError(err) {
		t.Fatalf("a node with an empty command line: err = %v, want one listNodesSettled asks again", err)
	}
	if processes != nil {
		t.Errorf("a failed listing gave processes: %+v", processes)
	}
	if !strings.Contains(err.Error(), "pid 103") {
		t.Errorf("err = %v, want it to name the exiting node", err)
	}
}

// Asked again, a node that finishes exiting — or that turns out to be running
// after all — is listed as it is; one whose command line stays empty is a
// refusal, not an empty machine and not the first start.
func TestLinuxListingAsksAgainANodeCaughtExiting(t *testing.T) {
	attemptsWas, intervalWas := nodeListAttempts, nodeListRetryInterval
	t.Cleanup(func() { nodeListAttempts, nodeListRetryInterval = attemptsWas, intervalWas })
	nodeListRetryInterval = time.Millisecond

	t.Run("it settles", func(t *testing.T) {
		proc := newFakeProc(t)
		proc.add(200, nodeExecutable, 'S', nil, linuxNodeEnv)
		calls := 0
		listWas := listNodes
		t.Cleanup(func() { listNodes = listWas })
		listNodes = func(context.Context) ([]nodeProcess, error) {
			calls++
			if calls == 3 {
				proc.write(200, "cmdline", nulTerminated(linuxNodeArgv))
			}
			return proc.list()
		}
		processes, err := listNodesSettled(context.Background())
		if err != nil {
			t.Fatalf("a node whose command line came back was not asked again: %v", err)
		}
		if calls != 3 || len(processes) != 1 || !slices.Equal(processes[0].Argv, linuxNodeArgv) {
			t.Errorf("after %d listings: %+v, want pid 200 with argv %q", calls, processes, linuxNodeArgv)
		}
	})

	t.Run("it stays empty", func(t *testing.T) {
		proc := newFakeProc(t)
		proc.add(201, nodeExecutable, 'S', nil, linuxNodeEnv)
		calls := 0
		proc.install(&calls)
		plan, running, err := planFromRunningNodes(context.Background())
		if err == nil || !strings.Contains(err.Error(), "will not restart it") {
			t.Fatalf("plan = %+v, running = %+v, err = %v; want a refusal", plan, running, err)
		}
		if running != nil {
			t.Errorf("a refusal named a running node: %+v", running)
		}
		if calls != nodeListAttempts {
			t.Errorf("listed %d times, want %d", calls, nodeListAttempts)
		}
	})
}

// A node gone while it was being read — its /proc directory removed (ENOENT),
// or still open with no process behind it (ESRCH) — is asked again too, at
// every read after its comm said it is a node, as is an empty environment
// that its command line, read again, shows to be a process exiting. A process
// that is not a node going away is nothing to this listing, and a read that
// fails any other way is not asked again.
func TestLinuxListingAsksAgainANodeGoneWhileBeingRead(t *testing.T) {
	failing := func(proc *fakeProc, name string, err error) func(string) ([]byte, error) {
		return func(path string) ([]byte, error) {
			if filepath.Base(path) == name && filepath.Dir(path) == proc.dir(300) {
				return nil, &os.PathError{Op: "read", Path: path, Err: err}
			}
			return os.ReadFile(path) // #nosec G304 -- a file this test made
		}
	}
	for _, file := range []string{"stat", "cmdline", "environ"} {
		t.Run(file+" removed", func(t *testing.T) {
			proc := newFakeProc(t)
			proc.add(300, nodeExecutable, 'S', linuxNodeArgv, linuxNodeEnv)
			if err := os.Remove(filepath.Join(proc.dir(300), file)); err != nil {
				t.Fatal(err)
			}
			if _, err := proc.list(); err == nil || !transientListError(err) {
				t.Errorf("err = %v, want one listNodesSettled asks again", err)
			}
		})
		t.Run(file+" ESRCH", func(t *testing.T) {
			proc := newFakeProc(t)
			proc.add(300, nodeExecutable, 'S', linuxNodeArgv, linuxNodeEnv)
			if _, err := listNodeProcessesIn(proc.root, failing(proc, file, syscall.ESRCH)); err == nil || !transientListError(err) {
				t.Errorf("err = %v, want one listNodesSettled asks again", err)
			}
		})
	}

	t.Run("environment empty as it exits", func(t *testing.T) {
		proc := newFakeProc(t)
		proc.add(300, nodeExecutable, 'S', linuxNodeArgv, nil)
		reads := 0
		read := func(path string) ([]byte, error) {
			if filepath.Base(path) == "cmdline" {
				reads++
				if reads > 1 {
					return nil, nil // exited between the two reads
				}
			}
			return os.ReadFile(path) // #nosec G304 -- a file this test made
		}
		if _, err := listNodeProcessesIn(proc.root, read); err == nil || !transientListError(err) {
			t.Errorf("err = %v, want one listNodesSettled asks again", err)
		}
	})

	t.Run("environment empty and running", func(t *testing.T) {
		proc := newFakeProc(t)
		proc.add(300, nodeExecutable, 'S', linuxNodeArgv, nil)
		processes, err := proc.list()
		if err != nil || len(processes) != 1 || processes[0].Env == nil || len(processes[0].Env) != 0 ||
			processes[0].EnvErr != nil {
			t.Errorf("listed %+v, %v; want pid 300 with an empty environment that was read", processes, err)
		}
	})

	t.Run("not a node", func(t *testing.T) {
		proc := newFakeProc(t)
		proc.add(300, "bash", 'S', []string{"bash"}, nil)
		if err := os.Remove(filepath.Join(proc.dir(300), "comm")); err != nil {
			t.Fatal(err)
		}
		if processes, err := proc.list(); err != nil || len(processes) != 0 {
			t.Errorf("listed %+v, %v; want nothing and no error", processes, err)
		}
	})

	t.Run("another failure", func(t *testing.T) {
		proc := newFakeProc(t)
		proc.add(300, nodeExecutable, 'S', linuxNodeArgv, linuxNodeEnv)
		_, err := listNodeProcessesIn(proc.root, failing(proc, "cmdline", syscall.EACCES))
		if err == nil || transientListError(err) {
			t.Errorf("err = %v, want a failure that is not asked again", err)
		}
	})
}

// After the stop, the wait for the old pid takes a node with an empty command
// line for one still there: it is asked again until the entry is a zombie or
// gone from /proc, and a node that stays that way is not called stopped.
func TestLinuxWaitForProcessGoneWaitsOutAnEmptyCommandLine(t *testing.T) {
	stopWas, pollWas := nodeStopTimeout, nodePollInterval
	t.Cleanup(func() { nodeStopTimeout, nodePollInterval = stopWas, pollWas })
	nodePollInterval = time.Millisecond

	for name, finish := range map[string]func(*fakeProc){
		"it becomes a zombie": func(proc *fakeProc) { proc.setState(400, 'Z') },
		"it is reaped": func(proc *fakeProc) {
			if err := os.RemoveAll(proc.dir(400)); err != nil {
				t.Error(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			nodeStopTimeout = 10 * time.Second
			proc := newFakeProc(t)
			proc.add(400, nodeExecutable, 'S', nil, nil)
			calls := 0
			proc.install(&calls)
			done := make(chan error, 1)
			go func() { done <- waitForProcessGone(context.Background(), 400) }()
			select {
			case err := <-done:
				t.Fatalf("the wait ended while the node's command line was empty: %v", err)
			case <-time.After(200 * time.Millisecond):
			}
			finish(proc)
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("the wait did not end when the node had gone: %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the wait did not end when the node had gone")
			}
		})
	}

	t.Run("it stays empty", func(t *testing.T) {
		nodeStopTimeout = 50 * time.Millisecond
		proc := newFakeProc(t)
		proc.add(401, nodeExecutable, 'S', nil, nil)
		calls := 0
		proc.install(&calls)
		err := waitForProcessGone(context.Background(), 401)
		if err == nil {
			t.Fatal("a node whose command line stayed empty was called stopped")
		}
		if !errors.Is(err, syscall.ESRCH) || calls < 2 {
			t.Errorf("err = %v after %d listings, want the exiting node's error, asked more than once", err, calls)
		}
	})
}

// A /proc file is read in more than one read, and a read made after the node
// let go of its memory answers 0 bytes, which os.ReadFile takes for the end:
// an environment or a command line cut short, with no error. A cut
// environment that still has HOME and not XDG_CONFIG_HOME names another
// database. Such a listing is asked again, and one that stays that way is a
// refusal; a node whose command line reads the same after its environment is
// listed whole, however long either is.
func TestLinuxListingAsksAgainANodeReadCutShort(t *testing.T) {
	attemptsWas, intervalWas := nodeListAttempts, nodeListRetryInterval
	t.Cleanup(func() { nodeListAttempts, nodeListRetryInterval = attemptsWas, intervalWas })
	nodeListRetryInterval = time.Millisecond

	const pid = 500
	longArgv := []string{"/usr/local/bin/agenthub-node", "--display-name", strings.Repeat("n", 600),
		"--db", "/home/me/a b/agenthub.db"}
	longEnv := []string{"HOME=/home/me", "FILLER=" + strings.Repeat("f", 600), "XDG_CONFIG_HOME=/home/me/.config"}
	newProc := func(t *testing.T) *fakeProc {
		proc := newFakeProc(t)
		proc.add(pid, nodeExecutable, 'S', longArgv, longEnv)
		return proc
	}
	// reader answers this node's environ, and the second cmdline read of
	// each listing, with what change makes of the whole file; every other
	// read is the file as it is.
	reader := func(proc *fakeProc, change func(name string, whole []byte) ([]byte, error)) func(string) ([]byte, error) {
		cmdlineReads := 0
		return func(path string) ([]byte, error) {
			whole, err := os.ReadFile(path) // #nosec G304 -- a file this test made
			if err != nil || filepath.Dir(path) != proc.dir(pid) {
				return whole, err
			}
			switch filepath.Base(path) {
			case "environ":
				return change("environ", whole)
			case "cmdline":
				cmdlineReads++
				if cmdlineReads%2 == 0 { // the second of a listing
					return change("cmdline", whole)
				}
			}
			return whole, nil
		}
	}
	// cut is what os.ReadFile returns when its first read, 512 bytes
	// (os.readFileContents), came back whole and the next answered 0.
	cut := func(whole []byte) []byte { return whole[:512] }
	exited := func(name string, whole []byte) ([]byte, error) {
		if name == "environ" {
			return cut(whole), nil
		}
		return nil, nil // gone by then: an empty command line
	}

	t.Run("whole", func(t *testing.T) {
		processes, err := newProc(t).list()
		if err != nil || len(processes) != 1 || !slices.Equal(processes[0].Argv, longArgv) ||
			!slices.Equal(processes[0].Env, longEnv) || processes[0].EnvErr != nil {
			t.Fatalf("listed %+v, %v; want pid %d with argv %q and env %q", processes, err, pid, longArgv, longEnv)
		}
	})

	t.Run("environment cut short as it exits", func(t *testing.T) {
		proc := newProc(t)
		processes, err := listNodeProcessesIn(proc.root, reader(proc, exited))
		if err == nil || !transientListError(err) {
			t.Fatalf("listed %+v, err = %v; want one listNodesSettled asks again", processes, err)
		}
		if processes != nil || !strings.Contains(err.Error(), fmt.Sprintf("pid %d", pid)) {
			t.Errorf("listed %+v, err = %v; want nothing and the exiting node named", processes, err)
		}
	})

	t.Run("environment cut short and it stays that way", func(t *testing.T) {
		proc := newProc(t)
		read := reader(proc, exited)
		calls := 0
		listWas := listNodes
		t.Cleanup(func() { listNodes = listWas })
		listNodes = func(context.Context) ([]nodeProcess, error) {
			calls++
			return listNodeProcessesIn(proc.root, read)
		}
		plan, running, err := planFromRunningNodes(context.Background())
		if err == nil || !strings.Contains(err.Error(), "will not restart it") {
			t.Fatalf("plan = %+v, running = %+v, err = %v; want a refusal", plan, running, err)
		}
		if running != nil {
			t.Errorf("a refusal named a running node: %+v", running)
		}
		if calls != nodeListAttempts {
			t.Errorf("listed %d times, want %d", calls, nodeListAttempts)
		}
	})

	t.Run("command line read again cut short", func(t *testing.T) {
		proc := newProc(t)
		changed := func(name string, whole []byte) ([]byte, error) {
			if name == "cmdline" {
				return cut(whole), nil
			}
			return whole, nil
		}
		processes, err := listNodeProcessesIn(proc.root, reader(proc, changed))
		if err == nil || !transientListError(err) {
			t.Fatalf("listed %+v, err = %v; want one listNodesSettled asks again", processes, err)
		}
	})

	t.Run("command line read again fails another way", func(t *testing.T) {
		proc := newProc(t)
		denied := func(name string, whole []byte) ([]byte, error) {
			if name == "cmdline" {
				return nil, &os.PathError{Op: "read", Path: name, Err: syscall.EACCES}
			}
			return whole, nil
		}
		processes, err := listNodeProcessesIn(proc.root, reader(proc, denied))
		if err == nil || transientListError(err) || strings.Contains(err.Error(), "on its way out") ||
			!errors.Is(err, syscall.EACCES) {
			t.Errorf("listed %+v, err = %v; want the read's own failure, not asked again", processes, err)
		}
	})
}
