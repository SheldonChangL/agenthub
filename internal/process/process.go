package process

import (
	"path"
	"strings"

	"agenthub.local/agenthub/internal/model"
)

type State struct {
	Known   bool
	Running bool
}

func classifyNames(names []string) map[model.Provider]State {
	states := map[model.Provider]State{
		model.ProviderClaude: {Known: true},
		model.ProviderCodex:  {Known: true},
	}
	for _, name := range names {
		normalized := strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
		base := strings.TrimSuffix(strings.ToLower(path.Base(normalized)), ".exe")
		switch base {
		case "claude":
			states[model.ProviderClaude] = State{Known: true, Running: true}
		case "codex", "codex-code-mode-host":
			states[model.ProviderCodex] = State{Known: true, Running: true}
		}
	}
	return states
}
