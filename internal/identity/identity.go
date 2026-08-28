package identity

import (
	"context"
	"errors"
	"os"
	"runtime"
	"time"

	"agenthub.local/agenthub/internal/id"
	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/registry"
)

func LoadOrCreate(ctx context.Context, store *registry.Registry) (model.NodeIdentity, error) {
	identity, err := store.GetNodeIdentity(ctx)
	if err == nil {
		return identity, nil
	}
	if !errors.Is(err, registry.ErrNotFound) {
		return model.NodeIdentity{}, err
	}

	nodeID, err := id.New("node_")
	if err != nil {
		return model.NodeIdentity{}, err
	}
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "agenthub-node"
	}
	identity = model.NodeIdentity{
		ID:          nodeID,
		DisplayName: hostname,
		Platform:    runtime.GOOS + "/" + runtime.GOARCH,
		CreatedAt:   time.Now().UTC(),
	}
	if err := store.SaveNodeIdentity(ctx, identity); err != nil {
		return model.NodeIdentity{}, err
	}
	return store.GetNodeIdentity(ctx)
}
