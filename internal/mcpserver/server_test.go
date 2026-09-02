package mcpserver_test

import (
	"context"
	"sort"
	"strings"
	"testing"

	"agenthub.local/agenthub/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connect runs the server in-process and returns an initialised client session.
func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	client := nodeStub(t, map[string]bool{"codex:demo": true})
	binding, err := mcpserver.Bind(context.Background(), client, "codex:demo")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	built, err := mcpserver.New(client, binding, "node_0123456789abcdef0123")
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	server := built.MCPServer()

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := server.Connect(context.Background(), serverTransport, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

// The surface is the whole security argument for this process existing: an
// agent reaching a message written on another machine must not also gain a way
// to read files or run commands through this server.
func TestTheSurfaceIsFourToolsAndNothingElse(t *testing.T) {
	session := connect(t)
	result, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	got := make([]string, 0, len(result.Tools))
	for _, tool := range result.Tools {
		got = append(got, tool.Name)
	}
	sort.Strings(got)
	want := []string{"agent_inbox", "agent_list", "agent_send", "agent_status"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("tools = %v, want exactly %v", got, want)
	}

	// Named separately from the count: a future tool could keep the count at
	// four while introducing exactly the reach this server must not have. The
	// exact-set assertion above is the real guard; this one catches a rename
	// that slips a capability in under a familiar count.
	forbidden := []string{"read", "write", "file", "shell", "bash", "exec", "command", "process", "sql", "query", "open", "fetch"}
	for _, tool := range result.Tools {
		name := strings.ToLower(tool.Name)
		for _, word := range forbidden {
			if strings.Contains(name, word) {
				t.Errorf("tool %q suggests %q access; this server exposes none", tool.Name, word)
			}
		}
	}
}

// Every tool's schema refuses unknown fields. Without this an agent could smuggle
// a field past the declared surface and have it silently ignored, which is how a
// tool ends up doing something its description does not mention.
func TestToolSchemasRefuseUnknownFields(t *testing.T) {
	session := connect(t)
	for _, tool := range []string{"agent_list", "agent_status", "agent_inbox", "agent_send"} {
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      tool,
			Arguments: map[string]any{"agentId": "codex:demo", "message": "x", "smuggled": "value"},
		})
		if err != nil {
			t.Errorf("%s: transport error %v", tool, err)
			continue
		}
		if !result.IsError {
			t.Errorf("%s accepted an unknown field", tool)
			continue
		}
		text := ""
		for _, content := range result.Content {
			if c, ok := content.(*mcp.TextContent); ok {
				text += c.Text
			}
		}
		if !strings.Contains(text, "smuggled") {
			t.Errorf("%s rejected the call but not for the unknown field: %q", tool, text)
		}
	}
}
