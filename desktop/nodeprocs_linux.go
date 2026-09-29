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

// listNodeProcesses finds every agenthub-node running as this user and the
// arguments each was started with.
//
// The same processes stopNodeProcesses' `pkill -x agenthub-node` ends: pkill
// matches /proc/<pid>/comm exactly. Only this user's, because those are the
// only ones pkill run from this app can end. /proc/<pid>/cmdline keeps each
// argument whole, separated by NULs.
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
		argv := strings.Split(string(bytes.TrimSuffix(raw, []byte{0})), "\x00")
		found = append(found, nodeProcess{PID: pid, Argv: argv})
	}
	return found, nil
}
