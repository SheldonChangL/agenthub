package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agenthub.local/agenthub/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// A peer must not be able to make agent_inbox stop working.
//
// Bodies are capped at 32 KiB decoded, but JSON escaping expands "<", "&" and
// every control byte sixfold, so fifty such messages are ~9.4 MiB of response —
// past the client's read cap. Truncated JSON does not decode, so the tool
// returned an error and NO messages at all, until the owner noticed and cleared
// the inbox by hand. Fifty signed messages is well inside both the rate limit
// and the 500-message inbox bound.
func TestAPeerCannotJamTheInbox(t *testing.T) {
	const heavy = 50
	messages := make([]map[string]any, heavy)
	for i := range messages {
		messages[i] = map[string]any{
			"id":   "msg_" + strings.Repeat("0", 20) + string(rune('a'+i%26)),
			"from": "node_peer000000000000/claude:x", "to": "codex:mine",
			"body":              strings.Repeat("<", 32768),
			"destinationNodeId": localNode,
			"createdAt":         "2026-09-03T10:00:00Z",
		}
	}
	served := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/node":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": localNode})
		case r.URL.Path == "/v1/nodes":
			_ = json.NewEncoder(w).Encode(map[string]any{"nodes": []map[string]any{
				{"nodeId": "node_peer000000000000", "fingerprint": "F"}}})
		case strings.HasPrefix(r.URL.Path, "/v1/inbox/"):
			// Honour the limit the client asks for, the way the node does.
			limit := heavy
			if v := r.URL.Query().Get("limit"); v != "" {
				var n int
				_, _ = fmt.Sscanf(v, "%d", &n)
				if n < limit {
					limit = n
				}
			}
			served += limit
			_ = json.NewEncoder(w).Encode(map[string]any{
				"messages": messages[:limit], "held": heavy, "capacity": 500, "full": false,
			})
		case strings.HasPrefix(r.URL.Path, "/v1/sessions/"):
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "codex:mine"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	client, err := mcpserver.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := mcpserver.Bind(context.Background(), client, "codex:mine")
	if err != nil {
		t.Fatal(err)
	}
	built, err := mcpserver.New(client, binding, localNode)
	if err != nil {
		t.Fatal(err)
	}
	st, ct := mcp.NewInMemoryTransports()
	if _, err := built.MCPServer().Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).
		Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })

	text, isErr := call(t, session, "agent_inbox", map[string]any{})
	if isErr {
		t.Fatalf("a peer disabled agent_inbox with %d messages: %s", heavy, text)
	}
	// Something must come back — the owner has to be able to see what is there.
	if !strings.Contains(text, "msg_") {
		t.Errorf("no messages were returned: %s", text[:min(400, len(text))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
