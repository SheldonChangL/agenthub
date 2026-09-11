package service

import (
	"context"
	"os/exec"
)

// ExecRunner drives the real service manager.
type ExecRunner struct{}

// Run executes the command and returns its combined output. The error carries
// the exit status; the output carries what the manager said, which is usually
// the part a person needs.
func (ExecRunner) Run(ctx context.Context, name string, args ...string) (string, error) {
	// #nosec G204 -- name is launchctl or systemctl, chosen by this package;
	// args are the unit path and label it built.
	output, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return string(output), err
}
