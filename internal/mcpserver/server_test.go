package mcpserver_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

// docs/mcp-tools.json is the contract an agent author reads. What the server
// serves is generated from the Go argument structs, so the two drift silently:
// the file offered pageSize, cursor and idempotencyKey long after the server had
// begun rejecting them as additional properties. A field documented but not
// accepted is worse than one never offered — it is an invitation to write a call
// that fails.
//
// Compared here: the tool set, each tool's title and description, its argument
// names, which are required, each argument's type and description, and the
// annotations — in both directions, because an omission on either side is a
// claim too. What is deliberately not compared is the enums, the bounds and the
// defaults: those are the server's behaviour rather than its schema, and the
// file says so.
func TestTheDocumentedToolArgumentsAreTheOnesServed(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "mcp-tools.json"))
	if err != nil {
		t.Fatal(err)
	}
	type property struct {
		Type        string `json:"type"`
		Description string `json:"description"`
	}
	var contract struct {
		Tools []struct {
			Name        string `json:"name"`
			Title       string `json:"title"`
			Description string `json:"description"`
			InputSchema struct {
				Properties map[string]property `json:"properties"`
				Required   []string            `json:"required"`
			} `json:"inputSchema"`
			Annotations map[string]any `json:"annotations"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatalf("mcp-tools.json does not parse: %v", err)
	}

	result, err := connect(t).ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	// The served schema is whatever the SDK generated; read it back as JSON so
	// this test compares what a client sees, not an internal representation.
	type schema struct {
		Properties map[string]property `json:"properties"`
		Required   []string            `json:"required"`
	}
	served := make(map[string]schema, len(result.Tools))
	annotations := make(map[string]map[string]any, len(result.Tools))
	descriptions := make(map[string]string, len(result.Tools))
	titles := make(map[string]string, len(result.Tools))
	for _, tool := range result.Tools {
		encoded, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatalf("%s: re-encode served schema: %v", tool.Name, err)
		}
		var decoded schema
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("%s: decode served schema: %v", tool.Name, err)
		}
		served[tool.Name] = decoded
		descriptions[tool.Name] = tool.Description
		titles[tool.Name] = tool.Title
		if tool.Annotations != nil {
			encodedAnnotations, err := json.Marshal(tool.Annotations)
			if err != nil {
				t.Fatalf("%s: re-encode annotations: %v", tool.Name, err)
			}
			var hints map[string]any
			if err := json.Unmarshal(encodedAnnotations, &hints); err != nil {
				t.Fatalf("%s: decode annotations: %v", tool.Name, err)
			}
			annotations[tool.Name] = hints
		}
	}

	if len(contract.Tools) != len(served) {
		t.Errorf("the contract describes %d tools, the server serves %d", len(contract.Tools), len(served))
	}
	// Counted names, not just the count: a file that documents one tool twice
	// and omits another has the right length and the right membership.
	seen := map[string]bool{}
	for _, documented := range contract.Tools {
		if seen[documented.Name] {
			t.Errorf("the contract documents %q twice", documented.Name)
		}
		seen[documented.Name] = true
	}
	for name := range served {
		if !seen[name] {
			t.Errorf("the server serves %q, which the contract does not describe", name)
		}
	}
	for _, documented := range contract.Tools {
		actual, ok := served[documented.Name]
		if !ok {
			t.Errorf("the contract describes %q, which the server does not serve", documented.Name)
			continue
		}
		want := make([]string, 0, len(documented.InputSchema.Properties))
		for name := range documented.InputSchema.Properties {
			want = append(want, name)
		}
		got := make([]string, 0, len(actual.Properties))
		for name := range actual.Properties {
			got = append(got, name)
		}
		sort.Strings(want)
		sort.Strings(got)
		if strings.Join(want, ",") != strings.Join(got, ",") {
			t.Errorf("%s: the contract documents %v, the server accepts %v", documented.Name, want, got)
		}
		// An argument's type and its one-line description are what an author
		// reads before writing the call; a wrong type here is a call that fails
		// and a wrong description is one that does the wrong thing.
		for name, documentedProperty := range documented.InputSchema.Properties {
			servedProperty, present := actual.Properties[name]
			if !present {
				continue // already reported by the name comparison above
			}
			if documentedProperty.Type != servedProperty.Type {
				t.Errorf("%s.%s: the contract says type %q, the server says %q",
					documented.Name, name, documentedProperty.Type, servedProperty.Type)
			}
			if documentedProperty.Description != servedProperty.Description {
				t.Errorf("%s.%s: the contract's description is not the one served:\n  file:   %q\n  server: %q",
					documented.Name, name, documentedProperty.Description, servedProperty.Description)
			}
		}
		wantRequired := append([]string(nil), documented.InputSchema.Required...)
		gotRequired := append([]string(nil), actual.Required...)
		sort.Strings(wantRequired)
		sort.Strings(gotRequired)
		if strings.Join(wantRequired, ",") != strings.Join(gotRequired, ",") {
			t.Errorf("%s: the contract requires %v, the server requires %v",
				documented.Name, wantRequired, gotRequired)
		}
		// The hints are advice a client acts on: destructiveHint and
		// openWorldHint default to true when absent, so a hint documented here
		// and not served tells the reader the opposite of what a client sees.
		// Both directions: a hint the file omits is one the reader assumes the
		// default for, and two of these default to true.
		for hint, want := range documented.Annotations {
			got, present := annotations[documented.Name][hint]
			if !present {
				t.Errorf("%s: the contract documents %s=%v, which the server does not send",
					documented.Name, hint, want)
				continue
			}
			if got != want {
				t.Errorf("%s: the contract says %s=%v, the server sends %v",
					documented.Name, hint, want, got)
			}
		}
		for hint, got := range annotations[documented.Name] {
			if _, present := documented.Annotations[hint]; !present {
				t.Errorf("%s: the server sends %s=%v, which the contract does not document",
					documented.Name, hint, got)
			}
		}
		if documented.Description != descriptions[documented.Name] {
			t.Errorf("%s: the contract's description is not the one served:\n  file:   %q\n  server: %q",
				documented.Name, documented.Description, descriptions[documented.Name])
		}
		if documented.Title != titles[documented.Name] {
			t.Errorf("%s: the contract's title is %q, the server sends %q",
				documented.Name, documented.Title, titles[documented.Name])
		}
	}
}
