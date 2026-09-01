package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

	// A session accepts messages only when its owner said so.
	allow := perform(t, handler, http.MethodPut, "/v1/sessions/codex:target/audience",
		map[string]any{"mode": "none", "acceptMessages": true})
	if allow.Code != http.StatusOK {
		t.Fatalf("opt in response = %d %s", allow.Code, allow.Body.String())
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

func TestForeignBrowserOriginIsRejected(t *testing.T) {
	_, handler := testServer(t)
	request := httptest.NewRequest(http.MethodGet, "/v1/sessions", nil)
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("response code = %d; want 403", response.Code)
	}
}

func TestJSONLikeContentTypeIsRejected(t *testing.T) {
	_, handler := testServer(t)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"to":"codex:x","body":"hello"}`))
	request.Header.Set("Content-Type", "application/jsonp")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "Content-Type must") {
		t.Fatalf("response = %d %s; want Content-Type rejection", response.Code, response.Body.String())
	}
}

func TestListSessionsIsPaginated(t *testing.T) {
	store, handler := testServer(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, id := range []string{"claude:one", "claude:two", "claude:three"} {
		_, err := store.UpsertSession(ctx, model.Session{
			ID: id, Provider: model.ProviderClaude, ProviderSessionID: id[len("claude:"):],
			Management: model.Unmanaged, Status: model.StatusIdle, StatusSource: "test",
			LastSeenAt: now, UpdatedAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	response := perform(t, handler, http.MethodGet, "/v1/sessions?page=2&pageSize=2", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Sessions   []model.Session `json:"sessions"`
		Pagination struct {
			Page       int `json:"page"`
			PageSize   int `json:"pageSize"`
			TotalItems int `json:"totalItems"`
			TotalPages int `json:"totalPages"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Sessions) != 1 || body.Pagination.Page != 2 || body.Pagination.TotalItems != 3 || body.Pagination.TotalPages != 2 {
		t.Fatalf("paginated body = %#v", body)
	}
}

const testNodeID = "node_1234567890123456"

func testServer(t *testing.T) (*registry.Registry, http.Handler) {
	t.Helper()
	ctx := context.Background()
	store, err := registry.Open(ctx, filepath.Join(t.TempDir(), "agenthub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	heartbeats := protocol.NewHeartbeatBuilder(store, model.NodeIdentity{ID: testNodeID, DisplayName: "test", Platform: "test"}, apiTestSigner{})
	return store, NewServer(store, nil, heartbeats, model.NodeIdentity{ID: testNodeID}).Handler()
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

// apiTestSigner signs heartbeats in tests with a throwaway key.
type apiTestSigner struct{}

func (apiTestSigner) Sign(message []byte) []byte {
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		panic(err)
	}
	return ed25519.Sign(private, message)
}

// TestQualifiedAddressesReachTheRightAnswer covers issue #7's acceptance list
// directly. Each case is one of its bullets, and the distinction between them
// is the point: "I do not know that node" and "that is not an address" call for
// different reactions from whoever typed it.
func TestQualifiedAddressesReachTheRightAnswer(t *testing.T) {
	store, handler := testServer(t)
	ctx := context.Background()
	const session = "codex:local-target"
	now := time.Now().UTC()
	if _, err := store.UpsertSession(ctx, model.Session{
		ID: session, Provider: model.ProviderCodex, ProviderSessionID: "local-target",
		Management: model.Managed, Status: model.StatusIdle, StatusSource: "test",
		LastSeenAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	allow := perform(t, handler, http.MethodPut, "/v1/sessions/"+session+"/audience",
		map[string]any{"mode": "none", "acceptMessages": true})
	if allow.Code != http.StatusOK {
		t.Fatalf("opt in = %d %s", allow.Code, allow.Body.String())
	}

	t.Run("a qualified address for an unknown node reports the node, not a parse failure", func(t *testing.T) {
		response := perform(t, handler, http.MethodPost, "/v1/messages", map[string]string{
			"to": "node_somewhereelse00/claude:abc", "body": "hello",
		})
		if response.Code != http.StatusNotFound {
			t.Fatalf("response = %d %s; want 404", response.Code, response.Body.String())
		}
		if !strings.Contains(response.Body.String(), "UNKNOWN_NODE") {
			t.Fatalf("body = %s; want UNKNOWN_NODE", response.Body.String())
		}
	})

	t.Run("a bare address still works and records this node as the destination", func(t *testing.T) {
		response := perform(t, handler, http.MethodPost, "/v1/messages", map[string]string{
			"to": session, "body": "hello",
		})
		if response.Code != http.StatusCreated && response.Code != http.StatusOK {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
		inbox, err := store.Inbox(ctx, session, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(inbox) != 1 {
			t.Fatalf("inbox = %#v", inbox)
		}
		if inbox[0].DestinationNodeID != testNodeID {
			t.Fatalf("destination = %q; a local message must still record where it went",
				inbox[0].DestinationNodeID)
		}
	})

	t.Run("a malformed address is refused with context", func(t *testing.T) {
		// "With context" means a named error code, not merely a non-empty body:
		// asserting the latter would pass on any error at all, including one
		// about something other than the address.
		for _, address := range []string{"", "not-an-address", "claude:", ":abc", "node_x/", "//claude:a"} {
			response := perform(t, handler, http.MethodPost, "/v1/messages", map[string]string{
				"to": address, "body": "hello",
			})
			if response.Code == http.StatusCreated || response.Code == http.StatusOK {
				t.Fatalf("address %q was accepted", address)
			}
			var decoded struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
				t.Fatalf("address %q was refused with a body that is not an error object: %s",
					address, response.Body.String())
			}
			if decoded.Error.Code == "" || decoded.Error.Message == "" {
				t.Fatalf("address %q was refused without a code and message: %s",
					address, response.Body.String())
			}
		}
	})
}
