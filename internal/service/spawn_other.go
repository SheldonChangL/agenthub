//go:build !windows

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// StartDetached starts the node so that it outlives the process starting it.
//
// The launcher exists for Windows, where the scheduled task must not hold a
// console window open for the life of the node. It is built here too so that
// `ah service run-node` is one command with one behaviour on every platform,
// and so this file's half of it is compiled and vetted by CI, which runs on
// Linux and would otherwise never look at either.
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
	// Its own session: not killed with the starter's process group, and no
	// controlling terminal to be stopped by.
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return 0, err
	}
	pid := command.Process.Pid
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
