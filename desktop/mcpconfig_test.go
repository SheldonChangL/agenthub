package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeMCPBinary puts a file where AGENTHUB_MCP points and returns its path.
func fakeMCPBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agenthub-mcp")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// watchClipboard replaces the clipboard write for the length of one test. No
// test may reach the machine's own clipboard: `go test` would then overwrite
// whatever the person running it had copied.
func watchClipboard(t *testing.T, err error) *[]string {
	t.Helper()
	written := []string{}
	previous := setClipboard
	setClipboard = func(_ context.Context, text string) error {
		written = append(written, text)
		return err
	}
	t.Cleanup(func() { setClipboard = previous })
	return &written
}

func TestMCPConfigNamesTheBinaryAndTheSession(t *testing.T) {
	binary := fakeMCPBinary(t)
	t.Setenv("AGENTHUB_MCP", binary)
	written := watchClipboard(t, nil)

	app := &App{ctx: context.Background(), client: newClient(defaultNodeURL), url: defaultNodeURL}
	result, err := app.MCPConfig(" claude:74d8025b ")
	if err != nil {
		t.Fatal(err)
	}

	var decoded struct {
		Servers map[string]struct {
			Command string   `json:"command"`
			Args    []string `json:"args"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(result.Text), &decoded); err != nil {
		t.Fatalf("the snippet is not JSON: %v\n%s", err, result.Text)
	}
	entry, ok := decoded.Servers["agenthub"]
	if !ok {
		t.Fatalf("no agenthub server in the snippet:\n%s", result.Text)
	}
	if !filepath.IsAbs(entry.Command) {
		t.Errorf("command %q is not absolute; a config file is read from wherever the agent runs", entry.Command)
	}
	if entry.Command != binary || result.Command != binary {
		t.Errorf("command = %q / %q, want %q", entry.Command, result.Command, binary)
	}
	// Order matters: -as and -url each take the next argument.
	if strings.Join(entry.Args, " ") != "-as claude:74d8025b -url "+defaultNodeURL {
		t.Errorf("args = %v, want [-as claude:74d8025b -url %s] with the id trimmed", entry.Args, defaultNodeURL)
	}
	if !strings.Contains(result.Text, "\n  \"mcpServers\"") {
		t.Errorf("the snippet is not indented two spaces:\n%s", result.Text)
	}
	// Building the snippet must not touch the clipboard: the window copies,
	// after it has decided the reply still belongs to the dialog on screen.
	if len(*written) != 0 {
		t.Errorf("MCPConfig wrote the clipboard %d times; copying is CopyText's", len(*written))
	}
}

// The default is pinned too. `agenthub-mcp` reads AGENTHUB_URL before falling
// back to its own default, so a snippet with no -url does not say "the default
// node" — it says "whichever node the environment Claude Code inherited names",
// which is how a config for a session on this node ends up dialling another.
func TestMCPConfigAlwaysPinsTheNodeURL(t *testing.T) {
	t.Setenv("AGENTHUB_MCP", fakeMCPBinary(t))
	watchClipboard(t, nil)

	for _, nodeURL := range []string{"http://127.0.0.1:7999", defaultNodeURL} {
		app := &App{ctx: context.Background(), client: newClient(nodeURL), url: nodeURL}
		result, err := app.MCPConfig("codex:thread")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(result.Text, `"-url"`) || !strings.Contains(result.Text, nodeURL) {
			t.Errorf("the node the window is talking to (%s) was not pinned:\n%s", nodeURL, result.Text)
		}
	}
}

func TestMCPConfigSaysWhereItLookedForTheServer(t *testing.T) {
	t.Setenv("AGENTHUB_MCP", filepath.Join(t.TempDir(), "not-here"))
	t.Setenv("PATH", t.TempDir())
	watchClipboard(t, nil)

	app := &App{ctx: context.Background(), client: newClient(defaultNodeURL), url: defaultNodeURL}
	_, err := app.MCPConfig("claude:x")
	if err == nil {
		t.Fatal("a missing agenthub-mcp produced a config anyway")
	}
	for _, want := range []string{"agenthub-mcp", "AGENTHUB_MCP", "looked"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestCopyTextWritesWhatItWasGiven(t *testing.T) {
	written := watchClipboard(t, nil)

	app := &App{ctx: context.Background()}
	if err := app.CopyText("{\"mcpServers\":{}}\n"); err != nil {
		t.Fatal(err)
	}
	if len(*written) != 1 || (*written)[0] != "{\"mcpServers\":{}}\n" {
		t.Errorf("clipboard got %v, want the one string it was handed", *written)
	}
}

// A clipboard that refuses must be reported, not swallowed: the dialog says
// "已複製" or "please copy it by hand" from this answer, and the wrong one
// sends someone to paste whatever they had copied before into a config file.
func TestCopyTextReportsAClipboardThatRefuses(t *testing.T) {
	watchClipboard(t, errors.New("no clipboard on this display"))

	if err := (&App{ctx: context.Background()}).CopyText("anything"); err == nil {
		t.Error("a refused clipboard was reported as a successful copy")
	}
}

func TestMCPConfigRefusesAnEmptySession(t *testing.T) {
	if _, err := (&App{ctx: context.Background()}).MCPConfig("  "); err == nil {
		t.Error("an empty session id produced a config binding an agent to nothing")
	}
}

// locateBinary's answer is written into a file another program reads from its
// own directory, so a relative name must not survive into it.
func TestLocateBinaryAnswersAbsolutely(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "agenthub-mcp")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	working, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(working, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTHUB_MCP", relative)
	found, err := locateBinary("agenthub-mcp", "AGENTHUB_MCP")
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(found) {
		t.Errorf("locateBinary returned %q for the relative %q", found, relative)
	}
}
