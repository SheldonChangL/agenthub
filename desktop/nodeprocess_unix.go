//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
)

// nodeExecutable is what the running node is called in a process list.
const nodeExecutable = "agenthub-node"

// stopNodeProcesses ends every agenthub-node on this machine.
//
// -x so the name has to match exactly: a pattern match would also catch
// agenthub-node-something, and on a developer's machine the `go run` of one.
// This path is only reached when no service manager holds the node — a
// registered launchd job or systemd unit is restarted through ah instead, so
// nothing here races a KeepAlive that would start the node behind this.
func stopNodeProcesses(ctx context.Context) (string, error) {
	return runCommand(ctx, "pkill", "-x", nodeExecutable)
}

// startNodeDetached starts the node so that it outlives this window.
//
// Setsid puts it in its own session, so it is not killed with the app's
// process group and has no controlling terminal to be stopped by.
func startNodeDetached(binary, logPath string) error {
	log, err := openNodeLog(logPath)
	if err != nil {
		return err
	}
	defer log.Close()
	// #nosec G204 -- binary is what findNode resolved: named by the owner in
	// the environment, or sitting beside this executable where the packaging
	// step put it. PATH is deliberately not among the places it looks.
	command := exec.Command(binary)
	command.Dir = filepath.Dir(binary)
	command.Stdout = log
	command.Stderr = log
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return err
	}
	// Released rather than waited on: this app is a front end for a node that
	// keeps running, not its parent.
	return command.Process.Release()
}

// nodeLogDir is where a node started by this app writes its output. macOS has
// one place for a user's logs; on Linux the state directory is where a file
// the owner may want to read after a reboot belongs.
func nodeLogDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find a place for the node log: %w", err)
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Logs", "agenthub"), nil
	}
	if state := os.Getenv("XDG_STATE_HOME"); state != "" {
		return filepath.Join(state, "agenthub"), nil
	}
	return filepath.Join(home, ".local", "state", "agenthub"), nil
}
