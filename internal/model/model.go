package model

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

type Provider string

const (
	ProviderClaude Provider = "claude"
	ProviderCodex  Provider = "codex"
)

type LifecycleStatus string

const (
	StatusActive   LifecycleStatus = "active"
	StatusIdle     LifecycleStatus = "idle"
	StatusInactive LifecycleStatus = "inactive"
	StatusUnknown  LifecycleStatus = "unknown"
)

// ValidLifecycleStatus reports whether a status is one this build assigns.
//
// Exported because a peer's reported status arrives as a bare string and has to
// be checked against the same set the local one comes from.
func ValidLifecycleStatus(status LifecycleStatus) bool {
	switch status {
	case StatusActive, StatusIdle, StatusInactive, StatusUnknown:
		return true
	}
	return false
}

type Management string

const (
	Managed   Management = "managed"
	Unmanaged Management = "unmanaged"
)

type Visibility string

const (
	VisibilityPrivate Visibility = "private"
	VisibilityPublic  Visibility = "public"
)

// AudienceMode answers "published to whom".
//
// A boolean cannot: "public" has to distinguish between every node this owner
// has paired with, including ones paired later, and a chosen few. The two are
// different decisions and diverge the moment a new node appears, so they are
// separate modes rather than a list that happens to hold everything.
type AudienceMode string

const (
	// AudienceNone is the default for every discovered session and the value
	// every existing row takes on upgrade. A choice made when publishing only
	// affected a local preview was never consent to reach a remote machine.
	AudienceNone AudienceMode = "none"
	// AudienceAllPaired includes nodes paired after the choice was made.
	AudienceAllPaired AudienceMode = "all_paired"
	// AudienceSelected includes only the nodes named in the grant table.
	AudienceSelected AudienceMode = "selected"
)

func ValidAudienceMode(mode AudienceMode) bool {
	switch mode {
	case AudienceNone, AudienceAllPaired, AudienceSelected:
		return true
	}
	return false
}

// Audience is a session's export policy: the mode plus, for AudienceSelected,
// the nodes it names.
type Audience struct {
	Mode  AudienceMode `json:"mode"`
	Nodes []string     `json:"nodes,omitempty"`
	// ExportCWD, AcceptMessages and AllowOutbound default to false. An export
	// view says as little as it can until the owner says otherwise.
	ExportCWD      bool `json:"exportCwd"`
	AcceptMessages bool `json:"acceptMessages"`
	// AllowOutbound lets an agent bound to this session send messages to other
	// nodes.
	//
	// Separate from AcceptMessages because willing to receive is not willing to
	// send. It bounds what an agent can do after reading a message written on
	// another machine, which reaches its context through agent_inbox.
	//
	// Enforced today by agenthub-mcp, which is a client of this node rather than
	// the node itself (#75). That closes the path an agent takes by following an
	// instruction it read; it does not stop a process that posts to the owner's
	// API directly.
	AllowOutbound bool `json:"allowOutbound"`
}

// PublishesTo reports whether a peer may see the session.
func (a Audience) PublishesTo(nodeID string) bool {
	switch a.Mode {
	case AudienceAllPaired:
		return nodeID != ""
	case AudienceSelected:
		return slices.Contains(a.Nodes, nodeID)
	default:
		return false
	}
}

// PublishesToAnyone reports whether the session leaves this host at all. It is
// the owner-local view's summary, not an authorization decision.
func (a Audience) PublishesToAnyone() bool {
	switch a.Mode {
	case AudienceAllPaired:
		return true
	case AudienceSelected:
		return len(a.Nodes) > 0
	default:
		return false
	}
}

type Session struct {
	ID                string     `json:"id"`
	Provider          Provider   `json:"provider"`
	ProviderSessionID string     `json:"providerSessionId"`
	Management        Management `json:"management"`
	Visibility        Visibility `json:"visibility"`
	// Audience is the export policy. Visibility above is derived from it for
	// owner-local views and stays only until every caller reads Audience.
	Audience     Audience        `json:"audience"`
	Status       LifecycleStatus `json:"status"`
	StatusSource string          `json:"statusSource"`
	CWD          string          `json:"cwd,omitempty"`
	Source       string          `json:"source,omitempty"`
	MetadataPath string          `json:"-"`
	LastSeenAt   time.Time       `json:"lastSeenAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
}

func SessionID(provider Provider, providerSessionID string) string {
	return string(provider) + ":" + providerSessionID
}

type NodeIdentity struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"displayName"`
	Platform    string    `json:"platform"`
	CreatedAt   time.Time `json:"createdAt"`
	// PublicKey and Fingerprint are what a peer checks before trusting this
	// node. Both are safe to publish; the private half never leaves the host
	// and is not part of this struct at all.
	PublicKey   string `json:"publicKey,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

type Message struct {
	ID   string `json:"id"`
	From string `json:"from,omitempty"`
	To   string `json:"to"`
	// DestinationNodeID names the node whose inbox this message belongs in.
	//
	// It is recorded even when that node is this one. A message is addressed to
	// a session on a machine, and leaving the machine implicit means the only
	// record of where a message was meant to go is the absence of a prefix —
	// which stops being readable the moment any message is for somewhere else.
	DestinationNodeID string    `json:"destinationNodeId"`
	Body              string    `json:"body"`
	CreatedAt         time.Time `json:"createdAt"`
}

// SessionIDSeparator is the character that joins a node ID to a session ID in
// the qualified export address <node-id>/<provider>:<provider-session-id>.
const SessionIDSeparator = "/"

// The two halves of an address have named bounds because the sender label a
// peer attaches to a message is built from them, and its bound has to be
// derived from theirs rather than guessed.
const (
	MaxNodeIDLength            = 128
	MaxProviderSessionIDLength = 128
	// MaxMessageIDLength bounds an id a peer chooses for a message it sends.
	// The receiving edge and the inbox cursor both have to agree on it: a
	// message the store admits but the cursor cannot name is a page boundary
	// the node cannot get past.
	MaxMessageIDLength = 128
	// MaxProviderNameLength bounds nothing new — KnownProvider is an exact
	// match on a short list — but the label's derivation needs a number here.
	MaxProviderNameLength = 16
	// MaxSenderLabelLength is <node-id>/<provider>:<id> with every part at its
	// limit. A legitimate sender at every limit fits; nothing longer does.
	MaxSenderLabelLength = MaxNodeIDLength + 1 + MaxProviderNameLength + 1 + MaxProviderSessionIDLength
)

// ValidateNodeID constrains a node identifier to what this project generates
// and a person can compare.
//
// Without it "node_a" and "node_a " and "NODE_A" become three separate trust
// entries that look identical in a list, and a lookalike built from non-ASCII
// characters looks identical too.
func ValidateNodeID(nodeID string) error {
	if len(nodeID) < 16 || len(nodeID) > MaxNodeIDLength {
		return fmt.Errorf("node id %q must be 16 to %d characters", nodeID, MaxNodeIDLength)
	}
	if strings.Contains(nodeID, SessionIDSeparator) {
		return fmt.Errorf("node id %q contains %q", nodeID, SessionIDSeparator)
	}
	for _, r := range nodeID {
		if r < '!' || r > '~' {
			return fmt.Errorf("node id %q contains a character outside printable ASCII", nodeID)
		}
	}
	// A node id must not also be readable as a session id.
	//
	// Every local session id of sixteen characters or more otherwise satisfies
	// every rule above, so the two namespaces overlap — and a reader holding one
	// bare string cannot tell which it has. That is not hypothetical: a message
	// whose sender named no session is stored as the bare sender node id, and a
	// peer that chose `claude:0123456789abcdef` at pairing time would have its
	// messages read as though they had been queued on this machine.
	//
	// Enforced here, at the edge where an id is admitted, so every reader gets
	// the guarantee rather than each having to re-derive it.
	if provider, _, found := strings.Cut(nodeID, ":"); found && KnownProvider(strings.ToLower(provider)) {
		return fmt.Errorf(
			"node id %q begins with a provider name, which would make it readable as a session id", nodeID)
	}
	return nil
}

// KnownProvider reports whether a name is one this build discovers.
//
// Exact, because it decides what a session id may be, and a session id is
// compared byte for byte everywhere else — the registry stores the lowercase
// constant and looks it up exactly. Folding case here would admit "CLAUDE:x" as
// a second spelling of a real session, which nothing downstream would match.
//
// Callers that want the looser reading fold before calling; ValidateNodeID does,
// because there the question is the reverse — whether a string could be
// mistaken for a session id by a person, not whether it is one.
func KnownProvider(name string) bool {
	switch Provider(name) {
	case ProviderClaude, ProviderCodex:
		return true
	}
	return false
}

// ValidateProviderSessionID rejects provider session IDs that would corrupt a
// qualified address.
//
// The value comes from a metadata JSON field, not a filename, so a provider —
// or anything that can write a file under a provider's directory — chooses it.
// Every write path must agree on this rule, which is why it lives here rather
// than in one store.
func ValidateProviderSessionID(providerSessionID string) error {
	if providerSessionID == "" {
		return errors.New("provider session id is required")
	}
	// Real ones are UUIDs. Unbounded, this was the widest free-text field in
	// the system: it is stored, it travels in every heartbeat and on every
	// message as the sender's label, and it is the first thing a reader sees.
	// The value is not echoed: the point is that it may be enormous.
	if len(providerSessionID) > MaxProviderSessionIDLength {
		return fmt.Errorf("provider session id is %d bytes, over the %d limit",
			len(providerSessionID), MaxProviderSessionIDLength)
	}
	if strings.Contains(providerSessionID, SessionIDSeparator) {
		return fmt.Errorf("provider session id %q contains %q", providerSessionID, SessionIDSeparator)
	}
	return nil
}
