package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A page that came back full carries the cursor for the next one. Without it
// the view can only show the newest fifty and has no way to ask for more, which
// is indistinguishable from a node that has only ever sent fifty messages.
func TestOutboundCarriesTheCursorToTheNextPage(t *testing.T) {
	var path, query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, query = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[
			{"id":"out_1","destinationNodeId":"node_b","to":"claude:x","from":"codex:y",
			 "state":"refused","attempts":3,"createdAt":"2026-09-08T04:00:00Z",
			 "updatedAt":"2026-09-08T04:05:00Z","lastError":"peer refused it","wakeHops":2}
		],"next":"cursor_2"}`))
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	view := app.Outbound(1, "")
	if view.Error != "" {
		t.Fatalf("Outbound: %s", view.Error)
	}
	if path != "/v1/outbound" {
		t.Errorf("path = %s, want /v1/outbound", path)
	}
	if query != "limit=1" {
		t.Errorf("query = %q, want limit=1 and no cursor", query)
	}
	if view.Next != "cursor_2" {
		t.Errorf("next = %q, want the cursor the node issued", view.Next)
	}
	if len(view.Messages) != 1 {
		t.Fatalf("messages = %+v", view.Messages)
	}
	// Every field the row is judged by travels: a refusal with no attempt count
	// and no reason is a row an owner can do nothing with.
	got := view.Messages[0]
	if got.ID != "out_1" || got.DestinationNodeID != "node_b" || got.To != "claude:x" ||
		got.From != "codex:y" || got.State != "refused" || got.Attempts != 3 ||
		got.LastError != "peer refused it" || got.WakeHops != 2 {
		t.Errorf("message = %+v, want every wire field carried through", got)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Errorf("timestamps did not decode: %+v", got)
	}
}

// The cursor is sent back as the node issued it, verbatim.
func TestOutboundForwardsTheCursorItWasGiven(t *testing.T) {
	var after string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		after = r.URL.Query().Get("after")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[]}`))
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	if view := app.Outbound(50, "  cursor_2  "); view.Error != "" {
		t.Fatalf("Outbound: %s", view.Error)
	}
	if after != "cursor_2" {
		t.Errorf("after = %q, want the cursor the node issued", after)
	}
}

// Nothing queued is a fact. It has no cursor and no error, and it must not be
// rendered as either an end-of-list failure or a page with more behind it.
func TestOutboundEmptyPageIsNeitherAnErrorNorMore(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[]}`))
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	view := app.Outbound(0, "")
	if view.Error != "" {
		t.Errorf("an empty log reported an error: %q", view.Error)
	}
	if view.Next != "" {
		t.Errorf("next = %q, want none", view.Next)
	}
	if view.Messages == nil {
		t.Error("messages is nil, which marshals as null rather than an empty list")
	}
	if len(view.Messages) != 0 {
		t.Errorf("messages = %+v", view.Messages)
	}
}

// A node that refuses, and a node that is not there, are both failures to read
// — not proof that nothing was sent. Reported in Error, never thrown, and never
// as an empty table that reads as an answer.
func TestOutboundAndWakesReportAFailedReadRatherThanAnEmptyList(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
			"code": "REGISTRY_ERROR", "message": "registry unavailable",
		}})
	}))
	defer failing.Close()

	app := &App{client: newClient(failing.URL), url: failing.URL, ctx: context.Background()}
	outbound := app.Outbound(50, "")
	if outbound.Error == "" {
		t.Error("a refused outbound read reported no error")
	}
	if outbound.Messages == nil || len(outbound.Messages) != 0 {
		t.Errorf("messages = %+v, want an empty list beside the error", outbound.Messages)
	}
	wakes := app.Wakes("", 50)
	if wakes.Error == "" {
		t.Error("a refused wake read reported no error")
	}
	if wakes.Wakes == nil || len(wakes.Wakes) != 0 {
		t.Errorf("wakes = %+v, want an empty list beside the error", wakes.Wakes)
	}

	// And a node that is not listening at all, which is the ordinary case: the
	// owner started the window without starting the node.
	unreachable := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	gone := unreachable.URL
	unreachable.Close()

	app = &App{client: newClient(gone), url: gone, ctx: context.Background()}
	if view := app.Outbound(50, ""); view.Error == "" || len(view.Messages) != 0 {
		t.Errorf("an unreachable node gave %+v, want an error and no messages", view)
	}
	if view := app.Wakes("", 50); view.Error == "" || len(view.Wakes) != 0 {
		t.Errorf("an unreachable node gave %+v, want an error and no wakes", view)
	}
}

// A refusal is only legible beside the rule that produced it, so the limits
// travel with the trail.
func TestWakesCarriesTheLimitsAndTheSessionFilter(t *testing.T) {
	var path, session string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path, session = r.URL.Path, r.URL.Query().Get("session")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"wakes":[
			{"id":"wake_1","messageId":"msg_1","sourceNodeId":"node_b","sourceSession":"codex:y",
			 "destinationSession":"claude:x","hops":3,"outcome":"refused_pair_rate",
			 "detail":"3 wakes from this pair in the last hour","at":"2026-09-08T04:00:00Z"}
		],"limits":{"hops":4,"pair":3,"pairWindow":"1h0m0s","session":10,
			"sessionWindow":"1h0m0s","node":30,"nodeWindow":"1h0m0s"}}`))
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	view := app.Wakes("claude:x", 50)
	if view.Error != "" {
		t.Fatalf("Wakes: %s", view.Error)
	}
	if path != "/v1/wakes" {
		t.Errorf("path = %s, want /v1/wakes", path)
	}
	// Forwarded as given: the node resolves it and refuses one that is not
	// local, which beats an empty list that reads as "nothing happened".
	if session != "claude:x" {
		t.Errorf("session = %q, want the id it was given, verbatim", session)
	}
	if view.Limits == nil {
		t.Fatal("limits missing")
	}
	if view.Limits.Hops != 4 || view.Limits.Pair != 3 || view.Limits.PairWindow != "1h0m0s" ||
		view.Limits.Session != 10 || view.Limits.SessionWindow != "1h0m0s" ||
		view.Limits.Node != 30 || view.Limits.NodeWindow != "1h0m0s" {
		t.Errorf("limits = %+v, want every rule carried through", view.Limits)
	}
	if len(view.Wakes) != 1 {
		t.Fatalf("wakes = %+v", view.Wakes)
	}
	event := view.Wakes[0]
	if event.ID != "wake_1" || event.MessageID != "msg_1" || event.SourceNodeID != "node_b" ||
		event.SourceSession != "codex:y" || event.DestinationSession != "claude:x" ||
		event.Hops != 3 || event.Outcome != "refused_pair_rate" || event.Detail == "" {
		t.Errorf("wake = %+v, want every wire field carried through", event)
	}
	if event.At.IsZero() {
		t.Error("the timestamp did not decode")
	}
}

// No session means no filter at all, rather than a session named "".
func TestWakesOmitsTheFilterWhenThereIsNone(t *testing.T) {
	var query string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"wakes":[],"limits":{}}`))
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	view := app.Wakes("   ", 50)
	if view.Error != "" {
		t.Fatalf("Wakes: %s", view.Error)
	}
	if query != "limit=50" {
		t.Errorf("query = %q, want limit=50 and no session", query)
	}
	if view.Wakes == nil {
		t.Error("wakes is nil, which marshals as null rather than an empty list")
	}
}

// The node refuses a limit outside 1..200 with HTTP 400 rather than clamping,
// so an out-of-range ask has to be fixed here — otherwise a view that asks for
// everything gets an error page instead of the most the node will give.
func TestPageLimitsAreClampedBeforeTheyReachTheNode(t *testing.T) {
	var limit string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit = r.URL.Query().Get("limit")
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/wakes" {
			_, _ = w.Write([]byte(`{"wakes":[],"limits":{}}`))
			return
		}
		_, _ = w.Write([]byte(`{"messages":[]}`))
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	for _, testCase := range []struct {
		asked int
		want  string
	}{{0, "50"}, {-1, "50"}, {500, "200"}, {200, "200"}, {1, "1"}, {50, "50"}} {
		limit = ""
		if view := app.Outbound(testCase.asked, ""); view.Error != "" {
			t.Fatalf("Outbound(%d): %s", testCase.asked, view.Error)
		}
		if limit != testCase.want {
			t.Errorf("Outbound(%d) asked for limit=%s, want %s", testCase.asked, limit, testCase.want)
		}
		limit = ""
		if view := app.Wakes("", testCase.asked); view.Error != "" {
			t.Fatalf("Wakes(%d): %s", testCase.asked, view.Error)
		}
		if limit != testCase.want {
			t.Errorf("Wakes(%d) asked for limit=%s, want %s", testCase.asked, limit, testCase.want)
		}
	}
}
