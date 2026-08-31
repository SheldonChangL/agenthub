package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/registry"
)

// What the HTTP endpoint actually serves must satisfy the published contract.
// The builder has its own conformance test; this one closes the gap between
// the builder and the wire.
func TestHeartbeatEndpointServesSchemaConformantJSON(t *testing.T) {
	store, handler := testServer(t)
	now := time.Now().UTC()
	_, err := store.UpsertSession(context.Background(), model.Session{
		ID: "claude:private", Provider: model.ProviderClaude, ProviderSessionID: "private",
		Management: model.Unmanaged, Status: model.StatusIdle, StatusSource: "test",
		CWD: "/tmp/example", Source: "claude-code", MetadataPath: "/never/exported.jsonl",
		LastSeenAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}

	publish := perform(t, handler, http.MethodPut, "/v1/sessions/claude:private/visibility",
		map[string]string{"visibility": string(model.VisibilityPublic)})
	if publish.Code != http.StatusOK {
		t.Fatalf("publish response = %d %s", publish.Code, publish.Body.String())
	}

	response := perform(t, handler, http.MethodGet, "/v1/heartbeat", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("heartbeat response = %d %s", response.Code, response.Body.String())
	}

	document, err := jsonschema.UnmarshalJSON(strings.NewReader(response.Body.String()))
	if err != nil {
		t.Fatalf("decode heartbeat body: %v", err)
	}

	absolute, err := filepath.Abs("../../docs/broker-protocol.schema.json")
	if err != nil {
		t.Fatalf("resolve schema: %v", err)
	}
	file, err := os.Open(absolute)
	if err != nil {
		t.Fatalf("open schema: %v", err)
	}
	defer file.Close()
	schemaDocument, err := jsonschema.UnmarshalJSON(file)
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("broker.json", schemaDocument); err != nil {
		t.Fatalf("add schema: %v", err)
	}
	schema, err := compiler.Compile("broker.json")
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}

	if err := schema.Validate(document); err != nil {
		t.Fatalf("GET /v1/heartbeat does not satisfy the broker schema: %v\n%s", err, response.Body.String())
	}

	body := response.Body.String()
	for _, forbidden := range []string{"providerSessionId", "metadataPath", "updatedAt", "\"source\""} {
		if strings.Contains(body, forbidden) {
			t.Errorf("heartbeat endpoint exported owner-local field %q\n%s", forbidden, body)
		}
	}

	// The endpoint serves the owner's preview, which is a union of everything
	// published anywhere. It is addressed to this node, so a peer that obtained
	// a copy would have to reject it: the recipient it names is not that peer.
	var served struct {
		RecipientNodeID string `json:"recipientNodeId"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &served); err != nil {
		t.Fatalf("decode heartbeat envelope: %v", err)
	}
	if served.RecipientNodeID != testNodeID {
		t.Errorf("served heartbeat recipient = %q, want the local node %q", served.RecipientNodeID, testNodeID)
	}
}

// Internal failures must not describe the machine to the caller. This is the
// listener a LAN mode would expose.
//
// An earlier version of this test posted to /v1/discover on a server with no
// discoverer, which answers 501 before ever reaching the error path, and
// asserted only that the status was >= 400. It passed even when the handler
// returned the full internal error, so it proved nothing.
func TestInternalErrorsDoNotDescribeTheHost(t *testing.T) {
	store, handler := testServer(t)
	now := time.Now().UTC()
	if _, err := store.UpsertSession(context.Background(), model.Session{
		ID: "claude:target", Provider: model.ProviderClaude, ProviderSessionID: "target",
		Management: model.Unmanaged, Status: model.StatusIdle, StatusSource: "test",
		CWD: "/Users/someone/Projects/secret-product", MetadataPath: "/Users/someone/.claude/projects/x.jsonl",
		LastSeenAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	// Closing the store makes every query fail the way a busy, missing or
	// corrupt database would, which is the condition whose detail must not
	// reach the caller.
	if err := store.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	cases := map[string]struct {
		method, path string
		body         any
		wantCode     int
		wantMessage  string
	}{
		"list sessions": {http.MethodGet, "/v1/sessions", nil,
			http.StatusInternalServerError, "registry unavailable"},
		"read session": {http.MethodGet, "/v1/sessions/claude:target", nil,
			http.StatusInternalServerError, "registry unavailable"},
		"heartbeat": {http.MethodGet, "/v1/heartbeat", nil,
			http.StatusInternalServerError, "could not build heartbeat"},
		"send message": {http.MethodPost, "/v1/messages",
			map[string]string{"to": "claude:target", "body": "hello"},
			http.StatusInternalServerError, "registry unavailable"},
		"read inbox": {http.MethodGet, "/v1/inbox/claude:target", nil,
			http.StatusInternalServerError, "registry unavailable"},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			response := perform(t, handler, testCase.method, testCase.path, testCase.body)
			if response.Code != testCase.wantCode {
				t.Errorf("status = %d, want %d: %s", response.Code, testCase.wantCode, response.Body.String())
			}

			var decoded struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
				t.Fatalf("decode error body: %v (%s)", err, response.Body.String())
			}
			// Assert the exact message. Checking only for the absence of a few
			// strings would pass again the moment a new detail leaks.
			if decoded.Error.Message != testCase.wantMessage {
				t.Errorf("message = %q, want exactly %q", decoded.Error.Message, testCase.wantMessage)
			}

			body := response.Body.String()
			for _, leak := range []string{"/Users/", ".claude/projects", "secret-product", "sql:", "database", "claude:target"} {
				if strings.Contains(body, leak) {
					t.Errorf("error body leaked %q: %s", leak, body)
				}
			}
		})
	}
}

// writeRegistryError classifies by sentinel, never by looking for the word
// "invalid" in the message.
//
// The substring form was an escape hatch: a driver error such as
// "converting driver.Value ... invalid syntax" matched it and was returned to
// the caller as a 400, carrying the column and the stored value. This test
// exists so removing the sentinel check, or restoring the substring check, is
// not silent.
func TestWriteRegistryErrorClassifiesBySentinel(t *testing.T) {
	cases := map[string]struct {
		err         error
		wantCode    int
		wantMessage string
	}{
		"driver error that happens to say invalid": {
			errors.New(`converting driver.Value type string ("not-a-number") to a int64: invalid syntax`),
			http.StatusInternalServerError, "registry unavailable",
		},
		"validation error": {
			fmt.Errorf("%w: visibility %q is not private or public", registry.ErrInvalidSession, "exposed"),
			http.StatusBadRequest, "invalid session: visibility \"exposed\" is not private or public",
		},
		"missing session": {
			fmt.Errorf("get session: %w", registry.ErrNotFound),
			http.StatusNotFound, "session not found",
		},
	}

	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			writeRegistryError(recorder, testCase.err)

			if recorder.Code != testCase.wantCode {
				t.Errorf("status = %d, want %d: %s", recorder.Code, testCase.wantCode, recorder.Body.String())
			}
			var decoded struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
				t.Fatalf("decode body: %v (%s)", err, recorder.Body.String())
			}
			if decoded.Error.Message != testCase.wantMessage {
				t.Errorf("message = %q, want exactly %q", decoded.Error.Message, testCase.wantMessage)
			}
		})
	}
}

// The node endpoint publishes what a peer needs to verify this node, and
// nothing that would let it impersonate one.
func TestNodeEndpointNeverExposesPrivateKeyMaterial(t *testing.T) {
	_, handler := testServer(t)
	response := perform(t, handler, http.MethodGet, "/v1/node", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, forbidden := range []string{"private", "seed", "secret", "node.key", "BEGIN"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(forbidden)) {
			t.Errorf("node endpoint mentioned %q: %s", forbidden, body)
		}
	}
}
