//go:build !windows

package quiet

import "os/exec"

func hideWindow(*exec.Cmd) {}
