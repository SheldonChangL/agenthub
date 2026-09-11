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
	// Order matters: -as takes the next argument.
	if strings.Join(entry.Args, " ") != "-as claude:74d8025b" {
		t.Errorf("args = %v, want [-as claude:74d8025b] with the id trimmed", entry.Args)
	}
	if !strings.Contains(result.Text, "\n  \"mcpServers\"") {
		t.Errorf("the snippet is not indented two spaces:\n%s", result.Text)
	}
	if !result.Copied || len(*written) != 1 || (*written)[0] != result.Text {
		t.Errorf("copied = %v, clipboard got %d writes; want the snippet written once", result.Copied, len(*written))
	}
}

func TestMCPConfigCarriesANonDefaultNodeURL(t *testing.T) {
	t.Setenv("AGENTHUB_MCP", fakeMCPBinary(t))
	watchClipboard(t, nil)

	const other = "http://127.0.0.1:7999"
	app := &App{ctx: context.Background(), client: newClient(other), url: other}
	result, err := app.MCPConfig("codex:thread")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, `"-url"`) || !strings.Contains(result.Text, other) {
		t.Errorf("a node somewhere other than the default was not passed to the server:\n%s", result.Text)
	}

	app = &App{ctx: context.Background(), client: newClient(defaultNodeURL), url: defaultNodeURL}
	result, err = app.MCPConfig("codex:thread")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result.Text, "-url") {
		t.Errorf("the default node was written as a setting the owner now has to maintain:\n%s", result.Text)
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

// A clipboard that refuses is not a failure: the snippet is on screen and can
// be selected by hand. Returning an error here would take that away as well.
func TestMCPConfigStillReturnsTheSnippetWhenTheClipboardRefuses(t *testing.T) {
	t.Setenv("AGENTHUB_MCP", fakeMCPBinary(t))
	watchClipboard(t, errors.New("no clipboard on this display"))

	app := &App{ctx: context.Background(), client: newClient(defaultNodeURL), url: defaultNodeURL}
	result, err := app.MCPConfig("claude:x")
	if err != nil {
		t.Fatalf("a clipboard failure was reported as a failed call: %v", err)
	}
	if result.Copied {
		t.Error("copied = true after the clipboard refused; the dialog would tell the owner to paste nothing")
	}
	if !strings.Contains(result.Text, "claude:x") {
		t.Errorf("the snippet was not returned:\n%s", result.Text)
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
