package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// countsServer answers /v1/inbox/counts with what the node would send, and
// fails the test if the command asks for anything else — a batch read that
// quietly went back to one request per session is the failure issue #146 is
// about, and it would otherwise pass every assertion about the printed table.
func countsServer(t *testing.T, payload map[string]any) (*httptest.Server, *int) {
	t.Helper()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/v1/inbox/counts" {
			t.Errorf("asked for %s; the counts command reads one batch endpoint", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func TestInboxCountsPrintsATableInOneRequest(t *testing.T) {
	server, requests := countsServer(t, map[string]any{
		"counts": map[string]any{
			"claude:quiet-ish": map[string]any{"held": 2, "capacity": 500, "full": false},
			"codex:drowning":   map[string]any{"held": 500, "capacity": 500, "full": true},
		},
		"generatedAt": "2026-09-18T10:00:00Z",
	})

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--url", server.URL, "inbox", "counts"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if *requests != 1 {
		t.Errorf("made %d requests; the whole point is one", *requests)
	}
	out := stdout.String()
	for _, want := range []string{"SESSION", "HELD", "claude:quiet-ish", "2/500", "codex:drowning", "500/500", "FULL"} {
		if !strings.Contains(out, want) {
			t.Errorf("the table does not contain %q:\n%s", want, out)
		}
	}
	// Fullest first: the row that needs attention is the one being refused mail.
	if strings.Index(out, "codex:drowning") > strings.Index(out, "claude:quiet-ish") {
		t.Errorf("the fuller inbox is not listed first:\n%s", out)
	}
	// And the column is named for what it is. "Unread" is the reading this
	// endpoint exists to avoid: nothing marks anything read.
	if !strings.Contains(out, "not what is unread") {
		t.Errorf("the output never says held is not unread:\n%s", out)
	}
	if strings.Contains(strings.ToUpper(out), "UNREAD\t") {
		t.Errorf("a column is headed UNREAD:\n%s", out)
	}
}

// Empty is a fact, not an empty table. The node answers with only the sessions
// holding something, so no rows means every inbox is empty — which must not
// read as "this node has no sessions".
func TestInboxCountsSaysWhenEveryInboxIsEmpty(t *testing.T) {
	server, _ := countsServer(t, map[string]any{"counts": map[string]any{}})

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--url", server.URL, "inbox", "counts"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "empty") {
		t.Errorf("an empty answer printed %q", stdout.String())
	}
}

func TestInboxCountsJSONIsThePayload(t *testing.T) {
	server, _ := countsServer(t, map[string]any{
		"counts":      map[string]any{"claude:one": map[string]any{"held": 1, "capacity": 500, "full": false}},
		"generatedAt": "2026-09-18T10:00:00Z",
	})

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"--url", server.URL, "--json", "inbox", "counts"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	var decoded struct {
		Counts map[string]struct {
			Held int `json:"held"`
		} `json:"counts"`
		GeneratedAt string `json:"generatedAt"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatalf("--json output is not JSON: %v (%s)", err, stdout.String())
	}
	if decoded.Counts["claude:one"].Held != 1 || decoded.GeneratedAt == "" {
		t.Errorf("--json dropped part of the node's answer: %s", stdout.String())
	}
}

// `counts` is a word, not a session id, and the two readings have to stay
// separable — the same rule `ah inbox delete` relies on.
func TestInboxCountsDoesNotShadowASessionRead(t *testing.T) {
	asked := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"messages": []any{}, "held": 0, "capacity": 500})
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--url", server.URL, "inbox", "claude:counts"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(asked, "claude:counts") {
		t.Errorf("reading the session claude:counts asked for %q", asked)
	}
}

func TestInboxCountsTakesNoArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"--url", "http://127.0.0.1:1", "inbox", "counts", "extra"}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "usage") {
		t.Errorf("exit = %d, stderr = %q; want a usage error", code, stderr.String())
	}
}
