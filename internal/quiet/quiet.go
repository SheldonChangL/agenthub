// Package quiet builds exec commands that never open a console window.
//
// On Windows a console program started from a process that has no console —
// the desktop app, or the node when Task Scheduler runs it in the interactive
// session — gets a brand-new console window for the few milliseconds it runs.
// The node calls tasklist on every discovery tick, so without this an owner
// sees a black window flash on their desktop every few seconds. On the other
// platforms this package is exec.CommandContext and nothing else.
package quiet

import (
	"context"
	"os/exec"
)

// Command is exec.CommandContext with the window suppressed where windows exist.
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	// #nosec G204 -- this is a constructor; every caller documents its own name
	// and args at its own call site, which is where the rule is answered.
	command := exec.CommandContext(ctx, name, args...)
	hideWindow(command)
	return command
}
