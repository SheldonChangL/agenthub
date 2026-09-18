//go:build windows

package quiet

import (
	"os/exec"
	"syscall"
)

// createNoWindow is CREATE_NO_WINDOW from processthreadsapi.h, which the
// syscall package does not name. HideWindow alone covers programs that draw a
// window; this flag is the one that stops a console being allocated at all.
const createNoWindow = 0x08000000

func hideWindow(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.HideWindow = true
	command.SysProcAttr.CreationFlags |= createNoWindow
}
