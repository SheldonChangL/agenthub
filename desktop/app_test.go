package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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

// A store failure must surface as an error rather than as a silent no-op.
func TestSetAudienceSurfacesTransportFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{"code": "REGISTRY_ERROR", "message": "registry unavailable"},
		})
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	if _, err := app.SetAudience([]string{"claude:a"}, Audience{Mode: "none"}); err == nil {
		t.Error("SetAudience reported success against a failing node")
	} else if !strings.Contains(err.Error(), "REGISTRY_ERROR") {
		t.Errorf("error = %v; want the node's error code", err)
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

func TestSetAudienceRejectsIncoherentPolicies(t *testing.T) {
	app := &App{client: newClient(defaultNodeURL), url: defaultNodeURL, ctx: context.Background()}
	cases := map[string]struct {
		ids      []string
		audience Audience
	}{
		"no sessions":            {nil, Audience{Mode: "none"}},
		"unknown mode":           {[]string{"claude:a"}, Audience{Mode: "everyone"}},
		"selected without nodes": {[]string{"claude:a"}, Audience{Mode: "selected"}},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := app.SetAudience(testCase.ids, testCase.audience); err == nil {
				t.Errorf("SetAudience accepted %+v", testCase.audience)
			}
		})
	}
}

// A batch must reach the node as one request and report per-session outcomes.
func TestSetAudienceBatchesAndReportsFailures(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/sessions/audience" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		requests++
		var payload struct {
			IDs      []string `json:"ids"`
			Audience Audience `json:"audience"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload.Audience.Mode != "selected" || len(payload.Audience.Nodes) != 1 {
			t.Errorf("audience reached the node as %+v", payload.Audience)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"changed": 2, "failed": 1,
			"results": []map[string]string{
				{"id": "claude:one"},
				{"id": "codex:broken", "error": "invalid session: boom"},
				{"id": "codex:two"},
			},
		})
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	result, err := app.SetAudience(
		[]string{"claude:one", "codex:broken", "codex:two"},
		Audience{Mode: "selected", Nodes: []string{"node_a"}, ExportCWD: true},
	)
	if err != nil {
		t.Fatalf("SetAudience() = %v", err)
	}
	if requests != 1 {
		t.Errorf("made %d requests, want one batch", requests)
	}
	if result.Changed != 2 || result.Failed != 1 {
		t.Errorf("changed/failed = %d/%d", result.Changed, result.Failed)
	}
	if len(result.Errors) != 1 || !strings.Contains(result.Errors[0], "codex:broken") {
		t.Errorf("errors = %v", result.Errors)
	}
}

// Publishing through the simple path means the explicit all-paired choice and
// nothing more: it says who may see the session, not how much of it. The export
// flags are turned on in the picker, never as a side effect.
func TestSetVisibilityMapsOntoAudience(t *testing.T) {
	var seen Audience
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Audience Audience `json:"audience"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		seen = payload.Audience
		_ = json.NewEncoder(w).Encode(map[string]any{"changed": 1, "failed": 0})
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	if _, err := app.SetVisibility([]string{"claude:a"}, "public"); err != nil {
		t.Fatal(err)
	}
	if seen.Mode != "all_paired" {
		t.Errorf("publish sent mode %q, want all_paired", seen.Mode)
	}
	if seen.ExportCWD || seen.AcceptMessages {
		t.Errorf("publish opened an export flag without being asked: %+v", seen)
	}
	if _, err := app.SetVisibility([]string{"claude:a"}, "private"); err != nil {
		t.Fatal(err)
	}
	if seen.Mode != "none" {
		t.Errorf("unpublish sent %+v", seen)
	}
}

// The node distinguishes "this peer published nothing" from "this node refused
// what it published". Both are an online peer with an empty session list, so a
// field this struct does not name is a field the UI cannot render — an unknown
// JSON key is dropped in decoding and gone when the overview is re-encoded.
func TestARefusedPeerSnapshotReachesTheUI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/peers"):
			_, _ = w.Write([]byte(`{"peers":[{"nodeId":"node_peer0000000000000","displayName":"p",` +
				`"online":true,"sessions":[],"sessionsWithheld":true}]}`))
		case strings.HasPrefix(r.URL.Path, "/v1/sessions"):
			_, _ = w.Write([]byte(`{"sessions":[],"total":0}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer server.Close()

	peers, err := newClient(server.URL).peers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(peers) != 1 {
		t.Fatalf("peers = %+v", peers)
	}
	if !peers[0].SessionsWithheld {
		t.Error("the node said it withheld this peer's sessions; the desktop dropped that")
	}
	// And it survives the encoding the UI actually reads.
	encoded, err := json.Marshal(peers[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"sessionsWithheld":true`) {
		t.Errorf("the field does not reach the UI: %s", encoded)
	}
}
