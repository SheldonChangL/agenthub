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

// MCPConfigResult is the snippet, plus whether the clipboard took it.
type MCPConfigResult struct {
	// Text is the whole `.mcp.json`, ready to save as a file.
	Text string `json:"text"`
	// Command is the absolute path the snippet names, so the dialog can say
	// which agenthub-mcp this config would start.
	Command string `json:"command"`
	// Copied is false when the clipboard refused. Not an error: the snippet is
	// on screen either way and can be selected by hand, and failing the whole
	// call because a clipboard was unavailable would take that away too.
	Copied bool `json:"copied"`
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

// setClipboard writes the snippet to the system clipboard. A variable so tests
// can watch the call instead of reaching the machine's own clipboard.
var setClipboard = func(ctx context.Context, text string) error {
	return runtime.ClipboardSetText(ctx, text)
}

// MCPConfig builds the `.mcp.json` that binds one agenthub-mcp to one session
// and puts it on the clipboard.
func (a *App) MCPConfig(sessionID string) (MCPConfigResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return MCPConfigResult{}, fmt.Errorf("select a session first")
	}
	command, err := locateBinary("agenthub-mcp", "AGENTHUB_MCP")
	if err != nil {
		return MCPConfigResult{}, err
	}

	args := []string{"-as", sessionID}
	// Only when it is not the default. The flag is what `agenthub-mcp` assumes
	// without it, so writing it always would turn a default into a setting the
	// owner has to maintain — and a stale one is a server dialling a node that
	// has moved.
	if _, nodeURL := a.current(); nodeURL != defaultNodeURL {
		args = append(args, "-url", nodeURL)
	}

	encoded, err := json.MarshalIndent(mcpConfigFile{
		Servers: map[string]mcpServer{"agenthub": {Command: command, Args: args}},
	}, "", "  ")
	if err != nil {
		return MCPConfigResult{}, fmt.Errorf("build the MCP config: %w", err)
	}
	text := string(encoded) + "\n"

	result := MCPConfigResult{Text: text, Command: command, Copied: true}
	if err := setClipboard(a.ctx, text); err != nil {
		result.Copied = false
	}
	return result, nil
}
