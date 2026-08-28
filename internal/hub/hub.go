package hub

import (
	"context"
	"fmt"
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
}

type Hub struct {
	store  *registry.Registry
	config Config
}

func New(store *registry.Registry, config Config) *Hub {
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
		for _, session := range sessions {
			if _, err := h.store.UpsertSession(ctx, session); err != nil {
				return DiscoveryResult{}, fmt.Errorf("register discovered session: %w", err)
			}
		}
		switch item.provider {
		case model.ProviderClaude:
			result.Claude = len(sessions)
		case model.ProviderCodex:
			result.Codex = len(sessions)
		}
	}
	result.Total = result.Claude + result.Codex
	return result, nil
}

func adapterState(state processmodel.State) adapter.ProcessState {
	return adapter.ProcessState{Known: state.Known, Running: state.Running}
}
