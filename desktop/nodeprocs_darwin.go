//go:build darwin

package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// listNodeProcesses finds every agenthub-node running as this user and the
// arguments each was started with.
//
// The same processes stopNodeProcesses' `pkill -x agenthub-node` ends: matched
// by the kernel's process name, exactly. Only this user's, because those are
// the only ones pkill run from this app can end.
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
		if string(name) != nodeExecutable {
			continue
		}
		pid := int(process.Proc.P_pid)
		argv, err := processArguments(pid)
		if err != nil {
			return nil, fmt.Errorf("read the command line of agenthub-node (pid %d): %w", pid, err)
		}
		found = append(found, nodeProcess{PID: pid, Argv: argv})
	}
	return found, nil
}

// processArguments is one process's argv, read from kern.procargs2: a native
// int argc, the executable path, NUL padding, then argc NUL-terminated
// arguments (and the environment after them, which is not read).
func processArguments(pid int) ([]string, error) {
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil, err
	}
	return parseProcargs2(raw)
}

func parseProcargs2(raw []byte) ([]string, error) {
	if len(raw) < 4 {
		return nil, fmt.Errorf("kern.procargs2 answered %d bytes", len(raw))
	}
	argc := int(binary.LittleEndian.Uint32(raw[:4]))
	rest := raw[4:]
	// The executable path, then the padding after it.
	end := bytes.IndexByte(rest, 0)
	if end < 0 {
		return nil, fmt.Errorf("kern.procargs2 has no end to its executable path")
	}
	rest = rest[end:]
	for len(rest) > 0 && rest[0] == 0 {
		rest = rest[1:]
	}
	argv := make([]string, 0, argc)
	for len(argv) < argc {
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			return nil, fmt.Errorf("kern.procargs2 ended after %d of %d arguments", len(argv), argc)
		}
		argv = append(argv, string(rest[:end]))
		rest = rest[end+1:]
	}
	return argv, nil
}
