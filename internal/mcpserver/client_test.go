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
)

// This process writes message bodies authored on other machines into an agent's
// reasoning context. A node URL someone else controls would let that party
// choose what the agent reads.
func TestOnlyALoopbackNodeIsAccepted(t *testing.T) {
	for _, raw := range []string{
		"http://192.168.1.10:7462",
		"http://example.com:7462",
		"https://node.internal:7462",
		"http://0.0.0.0:7462",
	} {
		if _, err := mcpserver.NewClient(raw); err == nil {
			t.Errorf("NewClient(%q) succeeded; only loopback may be used", raw)
		}
	}

	// A zoned ::1 IS loopback. It is refused only because net.ParseIP cannot
	// read a zone, so this is a conservative rejection, not a security one.
	// Recorded separately so that hardening the check with netip.ParseAddr —
	// which does read zones and would call this loopback — reads as the
	// deliberate change it would be, rather than as a test regression.
	if _, err := mcpserver.NewClient("http://[::1%25eth0]:7462"); err == nil {
		t.Error("NewClient with a zoned ::1 succeeded; today it is conservatively refused")
	}
	for _, raw := range []string{
		"http://127.0.0.1:7462",
		"http://localhost:7462",
		"http://[::1]:7462",
	} {
		if _, err := mcpserver.NewClient(raw); err != nil {
			t.Errorf("NewClient(%q) = %v, want success", raw, err)
		}
	}
}

// A query or fragment survives url.String() and would land in the middle of
// every path this client builds.
func TestANodeURLCarriesNothingButSchemeHostPort(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1:7462?x=1",
		"http://127.0.0.1:7462#frag",
		"http://user:pass@127.0.0.1:7462",
	} {
		_, err := mcpserver.NewClient(raw)
		if err == nil {
			t.Errorf("NewClient(%q) succeeded", raw)
			continue
		}
		if !strings.Contains(err.Error(), "bare scheme") {
			t.Errorf("NewClient(%q) = %v; the error should say what shape is wanted", raw, err)
		}
	}
}

// ReadInbox is exported on an exported type, so it has to hold on its own —
// not because the tool in front of it happens to clamp first.
func TestReadInboxClampsItsLimitAndClipsAnOverDeliveringNode(t *testing.T) {
	asked := []int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := 0
		_, _ = fmt.Sscanf(r.URL.Query().Get("limit"), "%d", &want)
		asked = append(asked, want)
		// One more than asked for, every time, and always more to come.
		messages := make([]map[string]any, want+1)
		for i := range messages {
			messages[i] = map[string]any{
				"id": fmt.Sprintf("msg_%d_%d", len(asked), i), "from": "node_peer000000000000/claude:x",
				"to": "codex:mine", "body": "x", "createdAt": "2026-09-02T10:00:00Z",
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"messages": messages, "held": 999, "capacity": 500, "full": false, "next": "more",
		})
	}))
	defer server.Close()
	client, err := mcpserver.NewClient(server.URL)
	if err != nil {
		t.Fatal(err)
	}

	for name, c := range map[string]struct{ limit, want int }{
		"a negative limit does not panic": {-1, 50},
		"zero means the default":          {0, 50},
		"over the ceiling is clamped":     {1000, 200},
		"an ordinary limit is honoured":   {7, 7},
	} {
		asked = nil
		inbox, err := client.ReadInbox(context.Background(), "codex:mine", c.limit)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(inbox.Messages) != c.want {
			t.Errorf("%s: got %d messages, want %d", name, len(inbox.Messages), c.want)
		}
		total := 0
		for _, n := range asked {
			total += n
		}
		if total != c.want {
			t.Errorf("%s: asked the node for %d messages, want %d", name, total, c.want)
		}
	}
}
