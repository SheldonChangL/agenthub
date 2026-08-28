package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
)

func TestPublishControlsHeartbeatVisibility(t *testing.T) {
	store, handler := testServer(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_, err := store.UpsertSession(ctx, model.Session{
		ID: "claude:private", Provider: model.ProviderClaude, ProviderSessionID: "private",
		Management: model.Unmanaged, Status: model.StatusIdle, StatusSource: "test",
		LastSeenAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	before := perform(t, handler, http.MethodGet, "/v1/heartbeat", nil)
	if before.Code != http.StatusOK || bytes.Contains(before.Body.Bytes(), []byte("claude:private")) {
		t.Fatalf("private heartbeat response = %d %s", before.Code, before.Body.String())
	}

	publish := perform(t, handler, http.MethodPut, "/v1/sessions/claude:private/visibility", map[string]string{"visibility": "public"})
	if publish.Code != http.StatusOK {
		t.Fatalf("publish response = %d %s", publish.Code, publish.Body.String())
	}
	after := perform(t, handler, http.MethodGet, "/v1/heartbeat", nil)
	if after.Code != http.StatusOK || !bytes.Contains(after.Body.Bytes(), []byte("claude:private")) {
		t.Fatalf("public heartbeat response = %d %s", after.Code, after.Body.String())
	}
}

func TestSendAndInboxRoundTrip(t *testing.T) {
	store, handler := testServer(t)
	ctx := context.Background()
	now := time.Now().UTC()
	_, err := store.UpsertSession(ctx, model.Session{
		ID: "codex:target", Provider: model.ProviderCodex, ProviderSessionID: "target",
		Management: model.Managed, Status: model.StatusIdle, StatusSource: "test",
		LastSeenAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	sent := perform(t, handler, http.MethodPost, "/v1/messages", map[string]string{"to": "codex:target", "from": "claude:source", "body": "check tests"})
	if sent.Code != http.StatusCreated {
		t.Fatalf("send response = %d %s", sent.Code, sent.Body.String())
	}
	inbox := perform(t, handler, http.MethodGet, "/v1/inbox/codex:target", nil)
	if inbox.Code != http.StatusOK || !bytes.Contains(inbox.Body.Bytes(), []byte("check tests")) {
		t.Fatalf("inbox response = %d %s", inbox.Code, inbox.Body.String())
	}
}

func testServer(t *testing.T) (*registry.Registry, http.Handler) {
	t.Helper()
	ctx := context.Background()
	store, err := registry.Open(ctx, filepath.Join(t.TempDir(), "agenthub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	heartbeats := protocol.NewHeartbeatBuilder(store, model.NodeIdentity{ID: "node_1234567890123456", DisplayName: "test", Platform: "test"})
	return store, NewServer(store, nil, heartbeats, model.NodeIdentity{ID: "node_1234567890123456"}).Handler()
}

func perform(t *testing.T, handler http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var encoded bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&encoded).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &encoded)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}
