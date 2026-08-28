//go:build windows

package process

import (
	"bytes"
	"context"
	"encoding/csv"
	"io"
	"os/exec"

	"agenthub.local/agenthub/internal/model"
)

func Snapshot(ctx context.Context) map[model.Provider]State {
	output, err := exec.CommandContext(ctx, "tasklist", "/FO", "CSV", "/NH").Output()
	if err != nil {
		return unknownStates()
	}
	reader := csv.NewReader(bytes.NewReader(output))
	var names []string
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil || len(record) == 0 {
			continue
		}
		names = append(names, record[0])
	}
	return classifyNames(names)
}

func unknownStates() map[model.Provider]State {
	return map[model.Provider]State{
		model.ProviderClaude: {},
		model.ProviderCodex:  {},
	}
}
