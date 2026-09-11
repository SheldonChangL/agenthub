package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// The `.mcp.json` an agent needs is three facts the owner cannot see from one
// place: where agenthub-mcp is, which session this row is, and whether this
// machine's node is somewhere other than the default. Assembled by hand it goes
// wrong quietly — on 2026-09-10 an Ubuntu session id ended up in a config on a
// mac, and the agent it produced spoke for a session on the other machine.
//
// So the window, which already knows all three, writes the snippet itself.

// MCPConfigResult is the snippet and the binary it names. It carries nothing
// about the clipboard: copying is CopyText's, so that a reply arriving after
// the owner has moved on to another row can be dropped whole, instead of
// having already overwritten the clipboard on its way here.
type MCPConfigResult struct {
	// Text is the whole `.mcp.json`, ready to save as a file.
	Text string `json:"text"`
	// Command is the absolute path the snippet names, so the dialog can say
	// which agenthub-mcp this config would start.
	Command string `json:"command"`
}

// mcpServer is one entry of the `mcpServers` object. Built and marshalled
// rather than formatted into a string: a session id is provider metadata, and
// a quote or a backslash in one would otherwise produce a file Claude Code
// cannot parse — or, worse, one that parses into something else.
type mcpServer struct {
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

type mcpConfigFile struct {
	Servers map[string]mcpServer `json:"mcpServers"`
}

// setClipboard writes text to the system clipboard. A variable so tests can
// watch the call instead of reaching the machine's own clipboard.
var setClipboard = func(ctx context.Context, text string) error {
	return runtime.ClipboardSetText(ctx, text)
}

// CopyText puts text on the system clipboard. Separate from MCPConfig so the
// window decides what lands there and when: only the dialog still on screen
// copies, and a reply for a row the owner has already left copies nothing.
func (a *App) CopyText(text string) error {
	return setClipboard(a.ctx, text)
}

// MCPConfig builds the `.mcp.json` that binds one agenthub-mcp to one session.
func (a *App) MCPConfig(sessionID string) (MCPConfigResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return MCPConfigResult{}, fmt.Errorf("select a session first")
	}
	command, err := locateBinary("agenthub-mcp", "AGENTHUB_MCP")
	if err != nil {
		return MCPConfigResult{}, err
	}

	// The URL is always written, the default included. `agenthub-mcp` reads
	// AGENTHUB_URL before falling back to its own default, so leaving the flag
	// off does not mean "the default" — it means "whatever node the environment
	// Claude Code inherited happens to name". The snippet has to pin the node
	// this window is actually talking to, because that is the node holding the
	// session the id belongs to.
	_, nodeURL := a.current()
	args := []string{"-as", sessionID, "-url", nodeURL}

	encoded, err := json.MarshalIndent(mcpConfigFile{
		Servers: map[string]mcpServer{"agenthub": {Command: command, Args: args}},
	}, "", "  ")
	if err != nil {
		return MCPConfigResult{}, fmt.Errorf("build the MCP config: %w", err)
	}
	return MCPConfigResult{Text: string(encoded) + "\n", Command: command}, nil
}
