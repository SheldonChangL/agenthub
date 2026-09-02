package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"agenthub.local/agenthub/internal/address"
)

// ErrNoBinding marks a server started without being told which session it acts
// for.
var ErrNoBinding = errors.New("this server must be told which session it acts for")

// Binding is the one session an agenthub-mcp process may act as.
//
// A stdio MCP server is launched by the agent as a child process, and the MCP
// protocol carries nothing that says which session made the call. If the server
// trusted a session id supplied per call, any agent on this machine could read
// any other session's inbox and send messages as any session — which would make
// the per-session acceptMessages and audience settings meaningless on the local
// side, where they are supposed to be decided.
//
// So the identity is fixed once, at startup, from a flag the owner sets when
// wiring the server into the agent. One process serves one session.
type Binding struct {
	// Unexported so that Bind is the only way to obtain a non-zero Binding.
	// With an exported field, mcpserver.New(client, Binding{SessionID: "anyone"})
	// would compile and run, and every later tool would trust it — which is the
	// same bypass -as exists to prevent, reached from inside the process
	// instead of from the command line.
	sessionID string
}

// SessionID is the session this server may act for.
func (b Binding) SessionID() string { return b.sessionID }

// valid reports whether this Binding came from Bind.
func (b Binding) valid() bool { return b.sessionID != "" }

// Bind validates what the owner passed to -as and confirms the node knows it.
//
// The check is made against the node rather than by parsing alone: a well-formed
// id for a session that does not exist would otherwise produce a server that
// looks healthy and answers every call with an empty result, which reads as "no
// messages" rather than "you pointed this at nothing".
func Bind(ctx context.Context, client *Client, raw string) (Binding, error) {
	if client == nil {
		return Binding{}, errors.New("binding needs a node client to confirm the session against")
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Binding{}, ErrNoBinding
	}
	// The local node's id is not needed to reject a qualified address: this
	// server only ever acts for a session on the node it talks to, so any
	// node-id prefix is wrong even when it names that same node. Passing an
	// empty local id makes every qualified form parse as remote, which is the
	// answer we want.
	parsed, err := address.ParseAddress(raw, "")
	if err != nil {
		return Binding{}, fmt.Errorf("-as %q: %w", raw, err)
	}
	if !parsed.Local() {
		return Binding{}, fmt.Errorf(
			"-as %q names another node; this server can only act for a session on the node it connects to", raw)
	}
	if err := client.SessionExists(ctx, parsed.SessionID); err != nil {
		return Binding{}, fmt.Errorf("-as %q: %w", raw, err)
	}
	return Binding{sessionID: parsed.SessionID}, nil
}
