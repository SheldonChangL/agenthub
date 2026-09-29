//go:build darwin

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// listNodeProcesses finds every agenthub-node running as this user, the
// arguments each was started with, and the environment it runs with.
//
// The same processes stopNodeProcesses' `pkill -x agenthub-node` ends: matched
// by the kernel's process name, exactly. Only this user's, because those are
// the only ones pkill run from this app can end. A zombie is not listed: it has
// exited, holds nothing open, and has no arguments left to read — and a node
// this app started and then stopped stays one until the app exits, because the
// app released it rather than waiting on it.
//
// The arguments come from kern.procargs2, which keeps each one whole. `ps`
// would join them with spaces, and the node's own default database lives under
// "Application Support" — a path with a space in it is the ordinary case here.
func listNodeProcesses(context.Context) ([]nodeProcess, error) {
	processes, err := unix.SysctlKinfoProcSlice("kern.proc.uid", os.Getuid())
	if err != nil {
		return nil, fmt.Errorf("list this user's processes: %w", err)
	}
	var found []nodeProcess
	for _, process := range processes {
		name := process.Proc.P_comm[:]
		if end := bytes.IndexByte(name, 0); end >= 0 {
			name = name[:end]
		}
		if string(name) != nodeExecutable || process.Proc.P_stat == zombie {
			continue
		}
		pid := int(process.Proc.P_pid)
		raw, err := unix.SysctlRaw("kern.procargs2", pid)
		if err != nil {
			return nil, fmt.Errorf("read the command line of agenthub-node (pid %d): %w", pid, err)
		}
		argv, env, envErr, err := parseProcargs2(raw)
		if err != nil {
			return nil, fmt.Errorf("read the command line of agenthub-node (pid %d): %w", pid, err)
		}
		found = append(found, nodeProcess{PID: pid, Argv: argv, Env: env, EnvErr: envErr})
	}
	return found, nil
}

// zombie is SZOMB in <sys/proc.h>: a process that has exited and not yet been
// waited for.
const zombie = 5

// parseProcargs2 reads one process's argv and environment out of
// kern.procargs2: a native int argc; the executable path, NUL-terminated and
// padded with NULs to a multiple of the pointer size; argc NUL-terminated
// arguments; the environment, NUL-terminated entries ending at an empty one;
// then the kernel's own strings, which are not read.
//
// The padding is measured rather than skipped over: an argv[0] that is the
// empty string is one NUL, the same byte as the padding, and skipping every
// NUL after the path would swallow it and read the first environment entry as
// the last argument.
//
// The environment's error is separate, because a command line that reads
// cleanly is still worth having without it.
func parseProcargs2(raw []byte) (argv, env []string, envErr, err error) {
	if len(raw) < 4 {
		return nil, nil, nil, fmt.Errorf("kern.procargs2 answered %d bytes", len(raw))
	}
	argc := int(int32(binary.LittleEndian.Uint32(raw[:4]))) // #nosec G115 -- the kernel's int, read back as one
	rest := raw[4:]
	// Every argument is at least its NUL, so argc cannot exceed what is left.
	if argc < 0 || argc > len(rest) {
		return nil, nil, nil, fmt.Errorf("kern.procargs2 says %d arguments in %d bytes", argc, len(rest))
	}
	end := bytes.IndexByte(rest, 0)
	if end < 0 {
		return nil, nil, nil, fmt.Errorf("kern.procargs2 has no end to its executable path")
	}
	const word = 8 // the pointer size of every Mac this app runs on
	padded := (end + 1 + word - 1) / word * word
	if padded > len(rest) {
		return nil, nil, nil, fmt.Errorf("kern.procargs2 ends inside the padding after its executable path")
	}
	for _, b := range rest[end:padded] {
		if b != 0 {
			return nil, nil, nil, fmt.Errorf("kern.procargs2 has no padding after its executable path")
		}
	}
	rest = rest[padded:]
	argv = make([]string, 0, argc)
	for len(argv) < argc {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			return nil, nil, nil, fmt.Errorf("kern.procargs2 ended after %d of %d arguments", len(argv), argc)
		}
		argv = append(argv, string(rest[:end]))
		rest = rest[end+1:]
	}
	env = []string{}
	for {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			return argv, nil, errors.New("kern.procargs2 ended inside the environment"), nil
		}
		if end == 0 {
			return argv, env, nil, nil
		}
		env = append(env, string(rest[:end]))
		rest = rest[end+1:]
	}
}
