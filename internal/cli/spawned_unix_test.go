//go:build !windows

package cli

import "syscall"

// spawnedProcessExited reports whether the process `ah service run-node`
// started has already finished.
//
// Wait4 rather than a signal-0 probe, because the node is started with
// Process.Release: nothing waits on it, so on Unix it stays a zombie child of
// this test binary until the binary exits, and a probe answers "alive" for a
// process that ended half a second ago. WNOHANG asks without blocking, reaps
// the child when it has ended, and answers 0 for one still running.
//
// False for anything it cannot answer — a pid that is not this process's child,
// or an interrupted call. A wrong "it exited" would fail the test on the spot,
// so the doubtful answer is the one that keeps waiting.
func spawnedProcessExited(pid int) bool {
	var status syscall.WaitStatus
	waited, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil)
	return err == nil && waited == pid
}
