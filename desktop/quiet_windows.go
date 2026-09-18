//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// createNoWindow is CREATE_NO_WINDOW, which the syscall package does not name.
const createNoWindow = 0x08000000

// quietly stops a console window opening for a short-lived console program
// started from this window, which has no console of its own. ah.exe is one;
// every panel refresh runs it. Same idea as internal/quiet in the root module,
// duplicated because this module does not import that one.
func quietly(command *exec.Cmd) *exec.Cmd {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.HideWindow = true
	command.SysProcAttr.CreationFlags |= createNoWindow
	return command
}
