package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
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
//
// Every one of the fifty must come back, once: a reader that pages but never
// advances would pass a weaker test with five copies of the first ten.
func TestAPeerCannotJamTheInbox(t *testing.T) {
	const heavy = 50
	messages := make([]map[string]any, heavy)
	for i := range messages {
		messages[i] = map[string]any{
			"id":   fmt.Sprintf("msg_%026d", i),
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
			// Honour limit and the cursor the way the node does; the cursor is
			// opaque to the client, so here it is simply the next index.
			limit := heavy
			if v := r.URL.Query().Get("limit"); v != "" {
				var n int
				_, _ = fmt.Sscanf(v, "%d", &n)
				if n < limit {
					limit = n
				}
			}
			start := 0
			if v := r.URL.Query().Get("after"); v != "" {
				_, _ = fmt.Sscanf(v, "%d", &start)
			}
			end := start + limit
			if end > heavy {
				end = heavy
			}
			served += end - start
			page := map[string]any{
				"messages": messages[start:end], "held": heavy, "capacity": 500, "full": false,
			}
			if end < heavy {
				page["next"] = fmt.Sprint(end)
			}
			_ = json.NewEncoder(w).Encode(page)
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
	distinct := map[string]bool{}
	for _, id := range regexp.MustCompile(`msg_\d{26}`).FindAllString(text, -1) {
		distinct[id] = true
	}
	if len(distinct) != heavy {
		t.Errorf("%d distinct messages came back, want all %d", len(distinct), heavy)
	}
	if served != heavy {
		t.Errorf("the node served %d messages for %d held; a page was repeated or skipped", served, heavy)
	}
}
