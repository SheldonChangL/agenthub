// Package address parses and renders AgentHub session addresses.
//
// It is separate from internal/protocol because protocol reaches the registry,
// and a process that only needs to read an address must not link a database
// driver to do it. agenthub-mcp is that process: see internal/mcpserver.
package address

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

// QualifiedID renders the address a peer uses to reach a session:
// <node-id>/<provider>:<provider-session-id>.
func QualifiedID(nodeID, sessionID string) string {
	return nodeID + model.SessionIDSeparator + sessionID
}

// SplitQualifiedID reverses QualifiedID. ok is false when the value is not a
// qualified address, which callers must treat as a local session ID rather
// than guessing a node.
func SplitQualifiedID(qualified string) (nodeID, sessionID string, ok bool) {
	nodeID, sessionID, found := strings.Cut(qualified, model.SessionIDSeparator)
	if !found || nodeID == "" || sessionID == "" {
		return "", "", false
	}
	return nodeID, sessionID, true
}

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
		if err := ValidateLocalSessionID(raw); err != nil {
			return Address{}, err
		}
		return Address{SessionID: raw}, nil
	}
	if err := model.ValidateNodeID(nodeID); err != nil {
		return Address{}, err
	}
	if err := ValidateLocalSessionID(sessionID); err != nil {
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

// ValidateLocalSessionID accepts the bare <provider>:<id> form only.
func ValidateLocalSessionID(sessionID string) error {
	provider, providerSessionID, found := strings.Cut(sessionID, ":")
	if !found {
		return fmt.Errorf("address %q is not <provider>:<session-id>", sessionID)
	}
	// The same set ValidateNodeID uses to keep the two namespaces disjoint.
	//
	// Two guards, deliberately: this one and the sessions table's
	// CHECK (provider IN ('claude', 'codex')). Adding a provider to
	// KnownProvider without touching the CHECK breaks upserts loudly, which is
	// the failure worth having — the alternative is a provider this build
	// accepts and the store silently rejects.
	if !model.KnownProvider(provider) {
		return fmt.Errorf("address %q names an unknown provider %q", sessionID, provider)
	}
	return model.ValidateProviderSessionID(providerSessionID)
}
