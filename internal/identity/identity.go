package identity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"agenthub.local/agenthub/internal/id"
	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/registry"
)

// MaxDisplayName is what the trust store accepts for a peer's name, applied to
// this node's own so it cannot store a name a peer would refuse.
const MaxDisplayName = 128

// LoadOrCreate returns this node's identity, creating it on first run.
//
// A chosen name replaces whatever is stored, not only what is created: a node
// whose name was wrong from the day it started needs a way to correct it, and
// that is the case this argument exists for. Trust is keyed on the node id, so
// the name can change without breaking a pairing — a peer keeps the name it
// recorded until it hears otherwise.
func LoadOrCreate(ctx context.Context, store *registry.Registry, chosen string) (model.NodeIdentity, error) {
	chosen = strings.TrimSpace(chosen)
	if len(chosen) > MaxDisplayName {
		return model.NodeIdentity{}, fmt.Errorf(
			"display name is %d characters; the most a peer will accept is %d",
			len(chosen), MaxDisplayName)
	}

	identity, err := store.GetNodeIdentity(ctx)
	if err == nil {
		if chosen == "" || chosen == identity.DisplayName {
			return identity, nil
		}
		// SetNodeDisplayName rather than SaveNodeIdentity: the latter refuses
		// to overwrite, deliberately, because the id must not change.
		if err := store.SetNodeDisplayName(ctx, chosen); err != nil {
			return model.NodeIdentity{}, err
		}
		return store.GetNodeIdentity(ctx)
	}
	if !errors.Is(err, registry.ErrNotFound) {
		return model.NodeIdentity{}, err
	}

	nodeID, err := id.New("node_")
	if err != nil {
		return model.NodeIdentity{}, err
	}
	name := chosen
	if name == "" {
		name = MachineName()
	}
	identity = model.NodeIdentity{
		ID:          nodeID,
		DisplayName: name,
		Platform:    runtime.GOOS + "/" + runtime.GOARCH,
		CreatedAt:   time.Now().UTC(),
	}
	if err := store.SaveNodeIdentity(ctx, identity); err != nil {
		return model.NodeIdentity{}, err
	}
	return store.GetNodeIdentity(ctx)
}

// MachineName is what this machine calls itself, for a node that has not been
// given a name.
//
// Not os.Hostname() alone. On macOS with no HostName set — the default —
// gethostname() answers with whatever DHCP and DNS say this address is called,
// so a machine can end up announcing a name that belongs to whoever held the
// address before it. Measured on one: ComputerName was "sheldon.chang mac"
// while os.Hostname() returned "J-FrankieChang.jet-opto.com.tw", a previous
// occupant of that DNS record. The node then broadcast that name to everyone on
// the segment, which is both wrong and somebody else's.
//
// So the machine's own name is asked for first, and the network-derived one is
// the fallback rather than the source.
func MachineName() string {
	if name := strings.TrimSpace(localMachineName()); name != "" {
		return truncateName(name)
	}
	if hostname, err := os.Hostname(); err == nil && strings.TrimSpace(hostname) != "" {
		return truncateName(strings.TrimSpace(hostname))
	}
	return "agenthub-node"
}

// truncateName keeps a name inside what a peer will accept, on a rune boundary
// so a multi-byte name is not cut into invalid UTF-8.
func truncateName(name string) string {
	if len(name) <= MaxDisplayName {
		return name
	}
	trimmed := name[:MaxDisplayName]
	for len(trimmed) > 0 && !utf8.ValidString(trimmed) {
		trimmed = trimmed[:len(trimmed)-1]
	}
	if trimmed == "" {
		return "agenthub-node"
	}
	return trimmed
}
