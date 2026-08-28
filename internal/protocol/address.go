package protocol

import (
	"errors"
	"fmt"
	"strings"

	"agenthub.local/agenthub/internal/model"
)

// ErrUnknownNode marks an address that names a node this installation does not
// know. It is a routing answer, not a malformed input: the caller wrote a
// well-formed address for a machine that has not been paired.
var ErrUnknownNode = errors.New("unknown node")

// Address is a resolved AgentHub destination.
type Address struct {
	// NodeID is empty for a bare local address.
	NodeID string
	// SessionID is always the local <provider>:<provider-session-id> form.
	SessionID string
}

// Local reports whether the address names a session on this node.
func (a Address) Local() bool { return a.NodeID == "" }

// ParseAddress accepts both forms a caller may write:
//
//	<provider>:<provider-session-id>              this node
//	<node-id>/<provider>:<provider-session-id>    a named node
//
// Both are accepted everywhere so a script can use one form throughout. A
// qualified address naming this node resolves to the local form rather than
// being treated as remote.
func ParseAddress(raw string, localNodeID string) (Address, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Address{}, errors.New("address is required")
	}

	nodeID, sessionID, qualified := SplitQualifiedID(raw)
	if !qualified {
		if err := validateLocalSessionID(raw); err != nil {
			return Address{}, err
		}
		return Address{SessionID: raw}, nil
	}
	if err := model.ValidateNodeID(nodeID); err != nil {
		return Address{}, err
	}
	if err := validateLocalSessionID(sessionID); err != nil {
		return Address{}, err
	}
	if nodeID == localNodeID {
		return Address{SessionID: sessionID}, nil
	}
	return Address{NodeID: nodeID, SessionID: sessionID}, nil
}

// ResolveLocal parses an address and insists it names a session on this node.
//
// It returns ErrUnknownNode for a well-formed address this installation cannot
// reach, so a caller can tell a routing answer from a malformed request without
// re-deriving the distinction.
func ResolveLocal(raw string, localNodeID string) (string, error) {
	address, err := ParseAddress(raw, localNodeID)
	if err != nil {
		return "", err
	}
	if !address.Local() {
		return "", fmt.Errorf("%w: %q", ErrUnknownNode, address.NodeID)
	}
	return address.SessionID, nil
}

func validateLocalSessionID(sessionID string) error {
	provider, providerSessionID, found := strings.Cut(sessionID, ":")
	if !found {
		return fmt.Errorf("address %q is not <provider>:<session-id>", sessionID)
	}
	switch model.Provider(provider) {
	case model.ProviderClaude, model.ProviderCodex:
	default:
		return fmt.Errorf("address %q names an unknown provider %q", sessionID, provider)
	}
	return model.ValidateProviderSessionID(providerSessionID)
}
