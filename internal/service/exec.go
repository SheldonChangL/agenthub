package service

import (
	"context"

	"agenthub.local/agenthub/internal/quiet"
)

// ExecRunner drives the real service manager.
type ExecRunner struct{}

// Run executes the command and returns its combined output. The error carries
// the exit status; the output carries what the manager said, which is usually
// the part a person needs.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	// #nosec G204 -- name is launchctl or systemctl, chosen by this package;
	// args are the unit path and label it built.
	// quiet.Command: on Windows these are schtasks and tasklist, run from a
	// window that has no console; a plain exec would open one for each call.
	output, err := quiet.Command(ctx, name, args...).CombinedOutput()
	return string(output), err
}
