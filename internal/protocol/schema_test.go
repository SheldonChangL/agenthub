package protocol_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
)

const schemaPath = "../../docs/broker-protocol.schema.json"

func compileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	absolute, err := filepath.Abs(schemaPath)
	if err != nil {
		t.Fatalf("resolve schema path: %v", err)
	}
	file, err := os.Open(absolute)
	if err != nil {
		t.Fatalf("open schema: %v", err)
	}
	defer file.Close()

	document, err := jsonschema.UnmarshalJSON(file)
	if err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	if err := compiler.AddResource("broker.json", document); err != nil {
		t.Fatalf("add schema resource: %v", err)
	}
	schema, err := compiler.Compile("broker.json")
	if err != nil {
		t.Fatalf("compile schema: %v", err)
	}
	return schema
}

func roundTrip(t *testing.T, value any) any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	decoded, err := jsonschema.UnmarshalJSON(strings.NewReader(string(encoded)))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return decoded
}

func newStore(t *testing.T) *registry.Registry {
	t.Helper()
	store, err := registry.Open(context.Background(), filepath.Join(t.TempDir(), "agenthub.db"))
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func session(providerSessionID string, provider model.Provider) model.Session {
	return model.Session{
		ID:                model.SessionID(provider, providerSessionID),
		Provider:          provider,
		ProviderSessionID: providerSessionID,
		Management:        model.Unmanaged,
		Visibility:        model.VisibilityPrivate,
		Status:            model.StatusIdle,
		StatusSource:      "metadata_process_heuristic",
		CWD:               "/tmp/example",
		Source:            "claude-code",
		MetadataPath:      "/should/never/be/exported.jsonl",
		LastSeenAt:        time.Now().UTC().Add(-time.Minute),
		UpdatedAt:         time.Now().UTC(),
	}
}

// The generated heartbeat must satisfy the contract the repository publishes.
// Before this test the builder emitted owner-local model.Session values that
// the same schema rejected.
func TestHeartbeatEnvelopeMatchesPublishedSchema(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	public := session("public-one", model.ProviderClaude)
	if _, err := store.UpsertSession(ctx, public); err != nil {
		t.Fatalf("upsert public session: %v", err)
	}
	if err := store.SetVisibility(ctx, public.ID, model.VisibilityPublic); err != nil {
		t.Fatalf("publish session: %v", err)
	}
	if _, err := store.UpsertSession(ctx, session("private-one", model.ProviderCodex)); err != nil {
		t.Fatalf("upsert private session: %v", err)
	}

	node := model.NodeIdentity{ID: "node_0123456789abcdef0123", DisplayName: "test", Platform: "darwin/arm64"}
	envelope, err := protocol.NewHeartbeatBuilder(store, node).Build(ctx, time.Now())
	if err != nil {
		t.Fatalf("build heartbeat: %v", err)
	}

	if err := compileSchema(t).Validate(roundTrip(t, envelope)); err != nil {
		t.Fatalf("heartbeat does not satisfy the broker schema: %v", err)
	}
}

// The export view must never carry owner-local fields, whatever the schema
// says, so assert on the serialized bytes as well.
func TestHeartbeatOmitsOwnerLocalFields(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	public := session("public-two", model.ProviderClaude)
	if _, err := store.UpsertSession(ctx, public); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := store.SetVisibility(ctx, public.ID, model.VisibilityPublic); err != nil {
		t.Fatalf("publish: %v", err)
	}

	node := model.NodeIdentity{ID: "node_0123456789abcdef0123", DisplayName: "test", Platform: "darwin/arm64"}
	envelope, err := protocol.NewHeartbeatBuilder(store, node).Build(ctx, time.Now())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	body := string(encoded)

	for _, forbidden := range []string{
		"providerSessionId", "metadataPath", "should/never/be/exported",
		"\"source\"", "updatedAt", "displayName", "platform",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("heartbeat exported owner-local value %q\n%s", forbidden, body)
		}
	}
	if !strings.Contains(body, "node_0123456789abcdef0123/claude:public-two") {
		t.Errorf("heartbeat did not use a qualified session address\n%s", body)
	}
}

// A private session must be absent even though the projection would otherwise
// happily render it.
func TestHeartbeatExcludesPrivateSessions(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	if _, err := store.UpsertSession(ctx, session("private-only", model.ProviderCodex)); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	node := model.NodeIdentity{ID: "node_0123456789abcdef0123", DisplayName: "test", Platform: "linux/amd64"}
	envelope, err := protocol.NewHeartbeatBuilder(store, node).Build(ctx, time.Now())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	payload, ok := envelope.Payload.(protocol.HeartbeatPayload)
	if !ok {
		t.Fatalf("payload is %T, want HeartbeatPayload", envelope.Payload)
	}
	if len(payload.Sessions) != 0 {
		t.Errorf("private session leaked into heartbeat: %+v", payload.Sessions)
	}
}

// TestSchemaRejectsUnsafeShapes exercises the schema itself against
// hand-built documents. It says nothing about what the projection produces —
// see TestSummarizeRefusesUnpublishedSessions for that. Both are needed: the
// schema is the contract a peer validates against, the projection is what this
// node actually emits.
func TestSchemaRejectsUnsafeShapes(t *testing.T) {
	schema := compileSchema(t)
	base := func() map[string]any {
		return map[string]any{
			"protocolVersion": protocol.Version,
			"messageId":       "msg_1",
			"type":            protocol.TypeNodeHeartbeat,
			"sentAt":          "2026-08-28T02:00:00Z",
			"nodeId":          "node_0123456789abcdef0123",
			"payload": map[string]any{
				"sequence":     1,
				"expiresAt":    "2026-08-28T02:00:30Z",
				"capabilities": []any{"session.list"},
				"sessions": []any{map[string]any{
					"id":           "node_0123456789abcdef0123/claude:abc",
					"provider":     "claude",
					"status":       "idle",
					"statusSource": "metadata_process_heuristic",
					"management":   "unmanaged",
					"visibility":   "public",
					"lastSeenAt":   "2026-08-28T01:59:00Z",
				}},
			},
		}
	}

	if err := schema.Validate(roundTrip(t, base())); err != nil {
		t.Fatalf("the baseline document should be valid: %v", err)
	}

	cases := map[string]func(doc map[string]any){
		"owner-local field on a summary": func(doc map[string]any) {
			sessions := doc["payload"].(map[string]any)["sessions"].([]any)
			sessions[0].(map[string]any)["providerSessionId"] = "abc"
		},
		"private session in the export view": func(doc map[string]any) {
			sessions := doc["payload"].(map[string]any)["sessions"].([]any)
			sessions[0].(map[string]any)["visibility"] = "private"
		},
		"unqualified session address": func(doc map[string]any) {
			sessions := doc["payload"].(map[string]any)["sessions"].([]any)
			sessions[0].(map[string]any)["id"] = "claude:abc"
		},
		"heartbeat payload on the wrong type": func(doc map[string]any) {
			doc["type"] = "node.heartbeat"
			doc["payload"] = map[string]any{"unexpected": true}
		},
		"unknown envelope field": func(doc map[string]any) {
			doc["extra"] = "value"
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			doc := base()
			mutate(doc)
			if err := schema.Validate(roundTrip(t, doc)); err == nil {
				t.Errorf("schema accepted %s", name)
			}
		})
	}
}
