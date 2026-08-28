//go:build !windows

package process

import (
	"context"
	"os/exec"
	"strings"

	"agenthub.local/agenthub/internal/model"
)

func Snapshot(ctx context.Context) map[model.Provider]State {
	output, err := exec.CommandContext(ctx, "ps", "-axo", "comm=").Output()
	if err != nil {
		return unknownStates()
	}
	return classifyNames(strings.Split(string(output), "\n"))
}

func unknownStates() map[model.Provider]State {
	return map[model.Provider]State{
		model.ProviderClaude: {},
		model.ProviderCodex:  {},
	}
}
