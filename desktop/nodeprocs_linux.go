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
	entries, err := os.ReadDir("/proc")
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
		dir := filepath.Join("/proc", entry.Name())
		info, err := os.Stat(dir)
		if err != nil {
			continue // gone since the listing
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Uid != uid {
			continue
		}
		comm, err := os.ReadFile(filepath.Join(dir, "comm")) // #nosec G304 -- /proc/<number>/comm
		if err != nil || strings.TrimSuffix(string(comm), "\n") != nodeExecutable {
			continue
		}
		if state, err := processState(dir); errors.Is(err, fs.ErrNotExist) || state == 'Z' || state == 'X' {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, "cmdline")) // #nosec G304 -- /proc/<number>/cmdline
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read the command line of agenthub-node (pid %d): %w", pid, err)
		}
		if len(raw) == 0 {
			// A process that is exiting has an empty cmdline; one that is
			// running has at least its own name.
			return nil, fmt.Errorf("agenthub-node (pid %d) has an empty command line", pid)
		}
		process := nodeProcess{PID: pid, Argv: nulSeparated(raw)}
		environ, err := os.ReadFile(filepath.Join(dir, "environ")) // #nosec G304 -- /proc/<number>/environ
		switch {
		case err != nil:
			process.EnvErr = fmt.Errorf("read /proc/%d/environ: %w", pid, err)
		case len(environ) == 0:
			process.Env = []string{}
		default:
			process.Env = nulSeparated(environ)
		}
		found = append(found, process)
	}
	return found, nil
}

// nulSeparated splits the contents of a /proc file whose entries each end in
// a NUL.
func nulSeparated(raw []byte) []string {
	return strings.Split(string(bytes.TrimSuffix(raw, []byte{0})), "\x00")
}

// processState is the state letter in /proc/<pid>/stat: the field after the
// command name, which is in parentheses and may itself hold spaces or ')'.
func processState(dir string) (byte, error) {
	stat, err := os.ReadFile(filepath.Join(dir, "stat")) // #nosec G304 -- /proc/<number>/stat
	if err != nil {
		return 0, err
	}
	at := bytes.LastIndexByte(stat, ')')
	if at < 0 || at+2 >= len(stat) {
		return 0, fmt.Errorf("%s/stat has no state field", dir)
	}
	return stat[at+2], nil
}
