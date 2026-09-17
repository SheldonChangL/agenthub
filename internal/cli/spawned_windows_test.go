//go:build windows

package cli

// spawnedProcessExited is the Unix answer's stand-in on Windows, where this
// test has no cheap way to ask.
//
// The detached process is started with DETACHED_PROCESS and released, so this
// test holds no handle to wait on, and re-opening one by pid can name a process
// that has been reused. Answering "still running" costs the deadline in the one
// case where the node died silently, and answering wrongly the other way would
// fail a run that was merely slow — so this waits.
func spawnedProcessExited(int) bool { return false }
