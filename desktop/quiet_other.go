//go:build !windows

package main

import "os/exec"

// quietly is a no-op where there are no console windows to suppress.
func quietly(command *exec.Cmd) *exec.Cmd { return command }
