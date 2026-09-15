//go:build windows

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// StartDetached starts the node so that it outlives the process starting it,
// with no console window of its own.
//
// This is what the scheduled task's action ends up doing: Task Scheduler runs
// ah, ah runs this, and the node is left running with its output on a file.
// The alternative — pointing the task straight at agenthub-node.exe — gives a
// console window that sits in the taskbar for as long as the node runs, for
// every owner, forever. DETACHED_PROCESS is how that window never exists,
// without touching the node's own subsystem: run by hand from a terminal,
// agenthub-node.exe still prints, which is the only diagnosis available to
// someone whose node will not start.
func StartDetached(binary string, args []string, logPath string) (int, error) {
	log, err := openLog(logPath)
	if err != nil {
		return 0, err
	}
	defer log.Close()
	// #nosec G204 -- binary is the path this package was given to register,
	// resolved to an absolute path by the caller; args are the node's own
	// flags, carried verbatim from the command line that installed it.
	command := exec.Command(binary, args...)
	command.Dir = filepath.Dir(binary)
	command.Stdout = log
	command.Stderr = log
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP | 0x00000008, // DETACHED_PROCESS
	}
	if err := command.Start(); err != nil {
		return 0, err
	}
	pid := command.Process.Pid
	// Released, not waited on: this process is a starter and is about to exit.
	// Without it the node would be a child of a process that has gone.
	if err := command.Process.Release(); err != nil {
		return pid, err
	}
	return pid, nil
}

func openLog(path string) (*os.File, error) {
	if path == "" {
		return nil, fmt.Errorf("the node's output needs a file to go to; pass --log")
	}
	// 0700: the log carries the node id, fingerprint and session counts.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create log directory: %w", err)
	}
	// #nosec G304 -- the path is the one this package chose (DefaultLogPath)
	// or the one the owner passed on their own command line.
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}
