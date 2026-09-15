//go:build windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// nodeExecutable is what the running node is called in the task list.
const nodeExecutable = "agenthub-node.exe"

// stopNodeProcesses ends every agenthub-node on this machine.
//
// /F because the node has no window to close politely and no unflushed state
// of its own: every write it makes is a committed SQLite transaction. This is
// the same thing the installer does before overwriting the binary
// (desktop/build/windows/installer/project.nsi).
func stopNodeProcesses(ctx context.Context) (string, error) {
	return runCommand(ctx, "taskkill", "/F", "/IM", nodeExecutable)
}

// startNodeDetached starts the node so that it outlives this window.
//
// DETACHED_PROCESS gives it no console of its own, which is also why the
// output has to go to a file: there is no window left for it to print to.
// CREATE_NEW_PROCESS_GROUP keeps a Ctrl+C aimed at this app from reaching it.
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
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x00000008, // DETACHED_PROCESS
	}
	if err := command.Start(); err != nil {
		return err
	}
	// Released rather than waited on: this app is a front end for a node that
	// keeps running, not its parent. Without this the process stays a zombie
	// child of the window until the window closes.
	return command.Process.Release()
}

// nodeLogDir is where a node started by this app writes its output.
func nodeLogDir() (string, error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("find a place for the node log: %w", err)
		}
		base = filepath.Join(home, "AppData", "Local")
	}
	return filepath.Join(base, "agenthub"), nil
}
