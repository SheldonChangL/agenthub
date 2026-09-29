//go:build !darwin && !linux && !windows

package main

import (
	"context"
	"errors"
)

// listNodeProcesses has no reader on this platform, and a restart that cannot
// read how the node was started does not restart it (planNodeRestart).
func listNodeProcesses(context.Context) ([]nodeProcess, error) {
	return nil, errors.New("reading another process's command line is not implemented on this platform")
}
