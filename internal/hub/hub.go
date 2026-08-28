package hub

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"agenthub.local/agenthub/internal/adapter"
	"agenthub.local/agenthub/internal/model"
	processmodel "agenthub.local/agenthub/internal/process"
	"agenthub.local/agenthub/internal/registry"
)

type Config struct {
	ClaudeRoot string
	CodexRoot  string
	Now        func() time.Time
	Processes  func(context.Context) map[model.Provider]processmodel.State
}

type DiscoveryResult struct {
	Claude int `json:"claude"`
	Codex  int `json:"codex"`
	Total  int `json:"total"`
	// Skipped counts sessions whose metadata failed validation. They are
	// reported rather than hidden: a rising count means a provider directory
	// holds records this build cannot represent.
	Skipped int `json:"skipped"`
}

// SessionStore is the only registry behaviour discovery needs. It is an
// interface so the error handling around it can be tested: the difference
// between skipping an unusable record and abandoning the scan is not otherwise
// reachable from a fixture, because the metadata parsers reject the same
// records the store does.
type SessionStore interface {
	UpsertSession(ctx context.Context, session model.Session) (model.Session, error)
}

type Hub struct {
	store  SessionStore
	config Config
}

func New(store SessionStore, config Config) *Hub {
	if config.Now == nil {
		config.Now = func() time.Time { return time.Now().UTC() }
	}
	if config.Processes == nil {
		config.Processes = processmodel.Snapshot
	}
	return &Hub{store: store, config: config}
}

func (h *Hub) Discover(ctx context.Context) (DiscoveryResult, error) {
	states := h.config.Processes(ctx)
	discoverers := []struct {
		provider   model.Provider
		discoverer adapter.Discoverer
	}{
		{
			provider: model.ProviderClaude,
			discoverer: adapter.ClaudeDiscoverer{
				Root: h.config.ClaudeRoot, Process: adapterState(states[model.ProviderClaude]), Now: h.config.Now,
			},
		},
		{
			provider: model.ProviderCodex,
			discoverer: adapter.CodexDiscoverer{
				Root: h.config.CodexRoot, Process: adapterState(states[model.ProviderCodex]), Now: h.config.Now,
			},
		},
	}

	var result DiscoveryResult
	for _, item := range discoverers {
		sessions, err := item.discoverer.Discover(ctx)
		if err != nil {
			return DiscoveryResult{}, fmt.Errorf("discover %s sessions: %w", item.provider, err)
		}

		registered := 0
		for _, session := range sessions {
			_, err := h.store.UpsertSession(ctx, session)
			switch {
			case err == nil:
				registered++
			case errors.Is(err, registry.ErrInvalidSession):
				// One unusable record must not cost the user every other one.
				// Provider metadata is untrusted, so anything able to write a
				// file under a provider directory could otherwise disable
				// discovery entirely. Skipping is safe here because refusing on
				// ingest only ever means fewer rows; the boundary that must
				// fail closed is the export projection, not this one.
				log.Printf("discovery skipped an unusable %s session: %v", item.provider, err)
				result.Skipped++
			default:
				// A cancelled context, a busy or full database, a corrupt file:
				// nothing about the next record will go better, and reporting
				// an empty but successful scan would read as "you have no
				// sessions" rather than "the scan failed".
				return DiscoveryResult{}, fmt.Errorf("register discovered %s session: %w", item.provider, err)
			}
		}

		switch item.provider {
		case model.ProviderClaude:
			result.Claude = registered
		case model.ProviderCodex:
			result.Codex = registered
		}
	}
	result.Total = result.Claude + result.Codex
	return result, nil
}

func adapterState(state processmodel.State) adapter.ProcessState {
	return adapter.ProcessState{Known: state.Known, Running: state.Running}
}
