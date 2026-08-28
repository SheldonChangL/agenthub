package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestSummarizeCountsEveryDimension(t *testing.T) {
	counts := summarize([]Session{
		{Provider: "claude", Status: "active", Visibility: "public"},
		{Provider: "claude", Status: "idle", Visibility: "private"},
		{Provider: "codex", Status: "inactive", Visibility: "private"},
	})
	for field, want := range map[string]int{
		"total": 3, "public": 1, "private": 2,
		"claude": 2, "codex": 1,
		"active": 1, "idle": 1, "inactive": 1,
	} {
		if counts[field] != want {
			t.Errorf("counts[%q] = %d, want %d", field, counts[field], want)
		}
	}
}

func TestSetNodeURLRejectsNonLoopback(t *testing.T) {
	app := &App{client: newClient(defaultNodeURL), url: defaultNodeURL}
	for _, raw := range []string{"http://192.168.1.20:7462", "http://example.com", "ftp://127.0.0.1", ""} {
		if err := app.SetNodeURL(raw); err == nil {
			t.Errorf("SetNodeURL(%q) accepted a non-loopback or invalid URL", raw)
		}
	}
	if app.NodeURL() != defaultNodeURL {
		t.Errorf("rejected URL mutated state: %q", app.NodeURL())
	}
	if err := app.SetNodeURL("http://localhost:9000/"); err != nil {
		t.Fatalf("SetNodeURL(loopback) = %v", err)
	}
	if app.NodeURL() != "http://localhost:9000" {
		t.Errorf("NodeURL() = %q, want trailing slash trimmed", app.NodeURL())
	}
}

func TestSetVisibilityRejectsBadInput(t *testing.T) {
	app := &App{client: newClient(defaultNodeURL), url: defaultNodeURL, ctx: context.Background()}
	if _, err := app.SetVisibility([]string{"claude:a"}, "exposed"); err == nil {
		t.Error("accepted an unknown visibility value")
	}
	if _, err := app.SetVisibility(nil, "public"); err == nil {
		t.Error("accepted an empty selection")
	}
}

// TestSetVisibilityBatch is the behavior the CLI could not offer: one choice
// applied to many sessions, with partial failures reported rather than hidden.
func TestSetVisibilityBatch(t *testing.T) {
	var mu sync.Mutex
	published := make([]string, 0)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/visibility") || r.Method != http.MethodPut {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var payload struct {
			Visibility string `json:"visibility"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/sessions/"), "/visibility")
		if strings.Contains(id, "broken") {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{"code": "REGISTRY_ERROR", "message": "boom"},
			})
			return
		}
		mu.Lock()
		published = append(published, id+"="+payload.Visibility)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "visibility": payload.Visibility})
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	result, err := app.SetVisibility([]string{"claude:one", "codex:broken", "codex:two"}, "public")
	if err != nil {
		t.Fatalf("SetVisibility() = %v", err)
	}
	if result.Changed != 2 || result.Failed != 1 {
		t.Errorf("changed=%d failed=%d, want 2 and 1", result.Changed, result.Failed)
	}
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "REGISTRY_ERROR") {
		t.Errorf("errors = %v, want one REGISTRY_ERROR entry", result.Errors)
	}
	if len(published) != 2 {
		t.Errorf("node received %d writes, want 2", len(published))
	}
}

func TestOverviewReportsUnreachableNodeWithoutPanicking(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	server.Close() // refuse connections

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	overview := app.Overview()
	if overview.Reachable {
		t.Error("Reachable = true for a closed node")
	}
	if overview.Error == "" {
		t.Error("Error is empty for a closed node")
	}
	if overview.Sessions == nil {
		t.Error("Sessions is nil; the UI expects an empty array")
	}
}

func TestOverviewLoadsAllPages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/node":
			_ = json.NewEncoder(w).Encode(NodeIdentity{ID: "node_test", DisplayName: "test", Platform: "darwin/arm64"})
		case r.URL.Path == "/v1/sessions":
			page := r.URL.Query().Get("page")
			session := Session{ID: "claude:page" + page, Provider: "claude", Status: "idle", Visibility: "private"}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sessions":   []Session{session},
				"pagination": map[string]int{"page": 1, "totalPages": 3},
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	overview := app.Overview()
	if !overview.Reachable {
		t.Fatalf("Reachable = false: %s", overview.Error)
	}
	if len(overview.Sessions) != 3 {
		t.Errorf("loaded %d sessions, want 3 (one per page)", len(overview.Sessions))
	}
	if overview.Counts["total"] != 3 || overview.Counts["private"] != 3 {
		t.Errorf("counts = %v, want total and private of 3", overview.Counts)
	}
}
