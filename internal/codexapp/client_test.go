package codexapp

import (
	"context"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
)

func TestClientInitializesAndMapsLiveThreadStatus(t *testing.T) {
	server := newFakeServer(t)
	client := NewClient(server.transport)
	t.Cleanup(func() { _ = client.Close() })

	done := make(chan error, 1)
	var threads ThreadListResult
	go func() {
		if _, err := client.Initialize(context.Background()); err != nil {
			done <- err
			return
		}
		var err error
		threads, err = client.ListThreads(context.Background(), "")
		done <- err
	}()

	id, method := server.nextCall(t)
	if method != "initialize" {
		t.Fatalf("first call = %q, want initialize", method)
	}
	server.reply(t, id, map[string]any{
		"codexHome": "/tmp/codex", "platformFamily": "unix",
		"platformOs": "macos", "userAgent": "codex",
	})
	// A notification between the two calls, which is the normal case and used
	// to be skipped only because nothing was listening for it.
	server.send(t, map[string]any{"method": "thread/status/changed", "params": map[string]any{}})
	id, method = server.nextCall(t)
	if method != "thread/list" {
		t.Fatalf("second call = %q, want thread/list", method)
	}
	server.reply(t, id, map[string]any{
		"data": []map[string]any{{
			"id":  "01a045ef-7f39-76a1-a638-e72b3153571d",
			"cwd": "/work/demo", "status": map[string]any{"type": "active"},
			"updatedAt": 1787882400,
		}},
		"nextCursor": nil,
	})
	if err := <-done; err != nil {
		t.Fatalf("client error = %v", err)
	}

	if len(threads.Data) != 1 || threads.Data[0].Status.Type != "active" {
		t.Fatalf("threads = %#v", threads)
	}
	now := time.Date(2026, 8, 28, 10, 1, 0, 0, time.UTC)
	sessions := NormalizeThreads(threads.Data, now)
	if len(sessions) != 1 || sessions[0].Status != model.StatusActive ||
		sessions[0].Visibility != model.VisibilityPrivate {
		t.Fatalf("sessions = %#v", sessions)
	}

	sent := map[string]bool{}
	for _, frame := range server.seen() {
		if method, ok := frame["method"].(string); ok {
			sent[method] = true
		}
	}
	for _, expected := range []string{"initialize", "initialized", "thread/list"} {
		if !sent[expected] {
			t.Fatalf("the client never sent %s; sent %v", expected, sent)
		}
	}
}

func TestNormalizeThreadsMapsNonRunnableStatesConservatively(t *testing.T) {
	now := time.Now().UTC()
	threads := []Thread{
		{ID: "not-loaded", Status: ThreadStatus{Type: "notLoaded"}, UpdatedAt: now.Unix()},
		{ID: "error", Status: ThreadStatus{Type: "systemError"}, UpdatedAt: now.Unix()},
	}

	sessions := NormalizeThreads(threads, now)
	if sessions[0].Status != model.StatusInactive || sessions[1].Status != model.StatusUnknown {
		t.Fatalf("statuses = %q, %q", sessions[0].Status, sessions[1].Status)
	}
}
