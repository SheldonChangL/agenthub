//go:build linux

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// listNodeProcesses finds every agenthub-node running as this user, the
// arguments each was started with, and the environment it runs with.
//
// The same processes stopNodeProcesses' `pkill -x agenthub-node` ends: pkill
// matches /proc/<pid>/comm exactly. Only this user's, because those are the
// only ones pkill run from this app can end. /proc/<pid>/cmdline and
// /proc/<pid>/environ keep each entry whole, separated by NULs; both are
// readable for a process of the same user. A zombie is not listed: it has
// exited, holds nothing open, and has an empty command line — and a node this
// app started and then stopped stays one until the app exits, because the app
// released it rather than waiting on it.
func listNodeProcesses(context.Context) ([]nodeProcess, error) {
	return listNodeProcessesIn("/proc", os.ReadFile)
}

// listNodeProcessesIn is listNodeProcesses on the /proc at root, each file in
// it read with readFile.
//
// An agenthub-node caught on its way out — exiting and not yet a zombie, which
// reads as an empty command line, or gone while it was being read — is not
// taken for gone: nothing is known about it until a listing succeeds, and a
// node taken for gone while it still holds its database is one a restart
// starts a second of. The listing fails instead, with an error that wraps
// ESRCH, and listNodesSettled and waitForProcessGone ask again
// (transientListError), as they do macOS's EINVAL for the same process. Only
// once the entry is known to be an agenthub-node: before its comm is read it
// is any process of this user's, and on a busy machine one of those is always
// on its way out.
func listNodeProcessesIn(root string, readFile func(string) ([]byte, error)) ([]nodeProcess, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	uid := uint32(os.Getuid()) // #nosec G115 -- a uid is never negative
	var found []nodeProcess
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		info, err := os.Stat(dir)
		if err != nil {
			continue // gone since the listing
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uid {
			continue
		}
		comm, err := readFile(filepath.Join(dir, "comm"))
		if err != nil || strings.TrimSuffix(string(comm), "\n") != nodeExecutable {
			continue
		}
		state, err := processState(dir, readFile)
		if vanished(err) {
			return nil, nodeExiting(pid, "its stat", err)
		}
		if state == 'Z' || state == 'X' {
			continue
		}
		raw, err := readFile(filepath.Join(dir, "cmdline"))
		if vanished(err) {
			return nil, nodeExiting(pid, "its command line", err)
		}
		if err != nil {
			return nil, fmt.Errorf("read the command line of agenthub-node (pid %d): %w", pid, err)
		}
		if len(raw) == 0 {
			// A process that is exiting has an empty cmdline; one that is
			// running has at least its own name.
			return nil, nodeExiting(pid, "its command line", nil)
		}
		process := nodeProcess{PID: pid, Argv: nulSeparated(raw)}
		environ, err := readFile(filepath.Join(dir, "environ"))
		if vanished(err) {
			return nil, nodeExiting(pid, "its environment", err)
		}
		switch {
		case err != nil:
			process.EnvErr = fmt.Errorf("read /proc/%d/environ: %w", pid, err)
		case len(environ) == 0:
			// Empty is also what a process that began exiting after its
			// cmdline was read answers, and that is not an empty environment.
			// Its cmdline says which: a running process still has one.
			again, err := readFile(filepath.Join(dir, "cmdline"))
			if err != nil || len(again) == 0 {
				return nil, nodeExiting(pid, "its environment", err)
			}
			process.Env = []string{}
		default:
			process.Env = nulSeparated(environ)
		}
		found = append(found, process)
	}
	return found, nil
}

// vanished is whether reading a /proc/<pid> file failed because its process
// went away while it was being read: the directory is gone (ENOENT), or it is
// still open and its process is not (ESRCH).
func vanished(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

// nodeExiting is the error for an agenthub-node caught on its way out while
// its `what` was read (listNodeProcessesIn). It wraps ESRCH whatever the read
// answered, so that it is asked again rather than refused at once.
func nodeExiting(pid int, what string, cause error) error {
	if cause != nil {
		return fmt.Errorf("agenthub-node (pid %d) is on its way out: reading %s failed (%v): %w",
			pid, what, cause, syscall.ESRCH)
	}
	return fmt.Errorf("agenthub-node (pid %d) is on its way out: %s reads empty: %w", pid, what, syscall.ESRCH)
}

// nulSeparated splits the contents of a /proc file whose entries each end in
// a NUL.
func nulSeparated(raw []byte) []string {
	return strings.Split(string(bytes.TrimSuffix(raw, []byte{0})), "\x00")
}

// processState is the state letter in /proc/<pid>/stat: the field after the
// command name, which is in parentheses and may itself hold spaces or ')'.
func processState(dir string, readFile func(string) ([]byte, error)) (byte, error) {
	stat, err := readFile(filepath.Join(dir, "stat"))
	if err != nil {
		return 0, err
	}
	at := bytes.LastIndexByte(stat, ')')
	if at < 0 || at+2 >= len(stat) {
		return 0, fmt.Errorf("%s/stat has no state field", dir)
	}
	return stat[at+2], nil
}
