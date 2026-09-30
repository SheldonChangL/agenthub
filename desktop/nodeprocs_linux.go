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
// ESRCH. listNodesSettled asks again on that kind of error
// (transientListError), as it does on macOS's EINVAL for the same process;
// waitForProcessGone asks again whatever a listing fails with. Only once the
// entry is known to be an agenthub-node: before its comm is read it is any
// process of this user's, and on a busy machine one of those is always on its
// way out.
//
// Nor is a node caught exiting read as a shorter one. os.ReadFile reads a
// /proc file in more than one read — the first is 512 bytes, and an
// environment is usually several KB — and a read made after the process let
// go of its memory answers 0 bytes, which ReadFile takes for the end of the
// file: what it returns is cut short, with no error. An environment cut
// before XDG_CONFIG_HOME, or a command line cut before --db, names another
// database. So the command line is read again after the environment, and the
// entry is listed only if that second reading is not empty and is the first
// one byte for byte. That shows neither was cut: the kernel answers
// /proc/<pid>/cmdline from task->mm of the thread group leader, which is the
// task /proc/<pid> is, and exit_mm (kernel/exit.c) sets that to NULL before
// its mmput, so a command line read with anything in it means the memory was
// still in use then; environ_read (fs/proc/base.c) answers from the same
// memory while mmget_not_zero succeeds on it, and a count of users that has
// reached zero never rises again. Every read before the second command line
// was therefore a whole one. The order is what the argument rests on: the
// environment is read between the two command lines, never after both.
//
// That holds only for a node that does not exec itself. An exec gives the
// process new memory: the environment, read from memory taken when the file
// was opened, is cut short once the old memory's last user lets go of it,
// while the command line is read from the new memory and can be the same
// word for word. Nothing in this repository execs in place — there is no
// syscall.Exec, unix.Exec or Execve in it — so an agenthub-node's memory is
// the one it started with until it exits.
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
		environ, envErr := readFile(filepath.Join(dir, "environ"))
		if vanished(envErr) {
			return nil, nodeExiting(pid, "its environment", envErr)
		}
		// Whole or cut short by an exit (see above): the command line, read
		// again, says which. Empty — an environment with nothing in it — is
		// no different: it is also what an exit answers.
		again, err := readFile(filepath.Join(dir, "cmdline"))
		if vanished(err) {
			return nil, nodeExiting(pid, "its command line again", err)
		}
		if err != nil {
			return nil, fmt.Errorf("read the command line of agenthub-node (pid %d) again: %w", pid, err)
		}
		if !bytes.Equal(again, raw) { // raw is not empty, so neither is an again equal to it
			return nil, fmt.Errorf("agenthub-node (pid %d) is on its way out: its command line, read again, is empty or not what it was: %w",
				pid, syscall.ESRCH)
		}
		switch {
		case envErr != nil:
			process.EnvErr = fmt.Errorf("read /proc/%d/environ: %w", pid, envErr)
		case len(environ) == 0:
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
