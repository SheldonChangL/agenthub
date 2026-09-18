//go:build windows

package process

import (
	"bytes"
	"context"
	"encoding/csv"
	"io"

	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/quiet"
)

func Snapshot(ctx context.Context) map[model.Provider]State {
	// quiet: this runs on every discovery tick, and the node is started with no
	// console of its own; a plain exec here is a black window flashing on the
	// owner's desktop every few seconds.
	output, err := quiet.Command(ctx, "tasklist", "/FO", "CSV", "/NH").Output()
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
