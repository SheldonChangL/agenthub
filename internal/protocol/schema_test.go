package protocol_test

import (
	"context"
	"encoding/json"
	"errors"
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
	envelope, err := protocol.NewHeartbeatBuilder(store, node, newTestKeypair(t)).Build(ctx, time.Now())
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
	envelope, err := protocol.NewHeartbeatBuilder(store, node, newTestKeypair(t)).Build(ctx, time.Now())
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
	envelope, err := protocol.NewHeartbeatBuilder(store, node, newTestKeypair(t)).Build(ctx, time.Now())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	payload, err := protocol.DecodePayload[protocol.HeartbeatPayload](envelope)
	if err != nil {
		t.Fatal(err)
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
			"signature":       "c2lnbmF0dXJl",
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
		"unsigned envelope": func(doc map[string]any) {
			delete(doc, "signature")
		},
		"hello payload carrying sessions": func(doc map[string]any) {
			doc["type"] = "node.hello"
			doc["payload"] = map[string]any{
				"node": map[string]any{
					"nodeId": "node_0123456789abcdef0123", "displayName": "peer",
					"platform": "linux/amd64", "publicKey": "AAAA",
					"fingerprint": "2DCF 9604 DBA9 778A 6DDD 035B",
				},
				"sessions": []any{},
			}
		},
		"malformed fingerprint": func(doc map[string]any) {
			doc["type"] = "pair.request"
			doc["payload"] = map[string]any{
				"node": map[string]any{
					"nodeId": "node_0123456789abcdef0123", "displayName": "peer",
					"platform": "linux/amd64", "publicKey": "AAAA",
					"fingerprint": "not-a-fingerprint",
				},
			}
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

// A "selected" session must reach only the nodes its owner named.
//
// Without a recipient every peer would receive the same envelope, and
// "selected" would quietly mean "all paired".
func TestBuildForFiltersBySelectedAudience(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	forA := session("only-node-a", model.ProviderClaude)
	forEveryone := session("all-paired", model.ProviderCodex)
	for _, s := range []model.Session{forA, forEveryone} {
		if _, err := store.UpsertSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	// Both peers are paired: BuildFor refuses an unpaired recipient outright, so
	// the audience filter is only reachable for a node this owner trusts.
	for _, nodeID := range []string{"node_a00000000000000", "node_b00000000000000"} {
		if err := store.TrustNode(ctx, registry.TrustedNode{
			NodeID: nodeID, DisplayName: "peer " + nodeID, Platform: "linux/amd64",
			PublicKey: "key-" + nodeID, Fingerprint: "2DCF 9604 DBA9 778A 6DDD 035B",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetAudience(ctx, forA.ID, model.Audience{
		Mode: model.AudienceSelected, Nodes: []string{"node_a00000000000000"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAudience(ctx, forEveryone.ID, model.Audience{
		Mode: model.AudienceAllPaired,
	}); err != nil {
		t.Fatal(err)
	}

	node := model.NodeIdentity{ID: "node_0123456789abcdef0123", DisplayName: "test", Platform: "darwin/arm64"}
	builder := protocol.NewHeartbeatBuilder(store, node, newTestKeypair(t))
	schema := compileSchema(t)

	ids := func(envelope protocol.Envelope) []string {
		payload, err := protocol.DecodePayload[protocol.HeartbeatPayload](envelope)
		if err != nil {
			t.Fatal(err)
		}
		out := make([]string, 0, len(payload.Sessions))
		for _, summary := range payload.Sessions {
			out = append(out, summary.ID)
		}
		return out
	}

	forNodeA, err := builder.BuildFor(ctx, time.Now(), "node_a00000000000000")
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(forNodeA); len(got) != 2 {
		t.Errorf("node_a received %v, want both sessions", got)
	}
	if err := schema.Validate(roundTrip(t, forNodeA)); err != nil {
		t.Errorf("per-peer envelope does not satisfy the schema: %v", err)
	}

	forNodeB, err := builder.BuildFor(ctx, time.Now(), "node_b00000000000000")
	if err != nil {
		t.Fatal(err)
	}
	got := ids(forNodeB)
	if len(got) != 1 {
		t.Fatalf("node_b received %v, want only the all-paired session", got)
	}
	if !strings.Contains(got[0], "all-paired") {
		t.Errorf("node_b received %v; the selected session leaked", got)
	}

	// The owner preview is not a recipient view and must say so.
	if _, err := builder.BuildFor(ctx, time.Now(), ""); err == nil {
		t.Error("BuildFor accepted an empty recipient")
	}
	preview, err := builder.Build(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids(preview)) != 2 {
		t.Errorf("the owner preview showed %v, want everything that leaves the host", ids(preview))
	}
}

// Revocation is expressed by omission, so a consumer that merges snapshots
// never sees one. The test pins the behaviour the documentation relies on.
func TestRevocationIsExpressedByOmission(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	published := session("revocable", model.ProviderClaude)
	if _, err := store.UpsertSession(ctx, published); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAudience(ctx, published.ID, model.Audience{Mode: model.AudienceAllPaired}); err != nil {
		t.Fatal(err)
	}
	const peer = "node_peer0000000000000"
	if err := store.TrustNode(ctx, registry.TrustedNode{
		NodeID: peer, DisplayName: "peer", Platform: "linux/amd64",
		PublicKey: "key-peer", Fingerprint: "2DCF 9604 DBA9 778A 6DDD 035B",
	}); err != nil {
		t.Fatal(err)
	}

	node := model.NodeIdentity{ID: "node_0123456789abcdef0123", DisplayName: "test", Platform: "darwin/arm64"}
	builder := protocol.NewHeartbeatBuilder(store, node, newTestKeypair(t))

	count := func(envelope protocol.Envelope) int {
		payload, err := protocol.DecodePayload[protocol.HeartbeatPayload](envelope)
		if err != nil {
			t.Fatal(err)
		}
		return len(payload.Sessions)
	}

	before, err := builder.BuildFor(ctx, time.Now(), peer)
	if err != nil {
		t.Fatal(err)
	}
	if count(before) != 1 {
		t.Fatalf("published session absent from the snapshot")
	}

	if err := store.SetAudience(ctx, published.ID, model.Audience{Mode: model.AudienceNone}); err != nil {
		t.Fatal(err)
	}
	after, err := builder.BuildFor(ctx, time.Now(), peer)
	if err != nil {
		t.Fatal(err)
	}
	if count(after) != 0 {
		t.Errorf("a revoked session is still present; revocation must be an omission")
	}

	// The sequence must advance so a consumer can tell a newer empty snapshot
	// from a stale one it should keep.
	beforePayload, err := protocol.DecodePayload[protocol.HeartbeatPayload](before)
	if err != nil {
		t.Fatal(err)
	}
	afterPayload, err := protocol.DecodePayload[protocol.HeartbeatPayload](after)
	if err != nil {
		t.Fatal(err)
	}
	beforeSeq, afterSeq := beforePayload.Sequence, afterPayload.Sequence
	if afterSeq <= beforeSeq {
		t.Errorf("sequence went from %d to %d; a consumer cannot order these", beforeSeq, afterSeq)
	}
}

// "All paired" has to mean paired.
//
// The audience filter alone cannot enforce that: Audience.PublishesTo returns
// true for an all_paired session and any non-empty string, so a builder that
// accepted an arbitrary recipient would publish every all_paired session to
// anyone who supplied a node id. BuildFor therefore refuses a recipient that is
// not currently in trusted_nodes, and revoking a node takes effect immediately
// without the owner revisiting each session's audience.
func TestBuildForRequiresATrustedRecipient(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)

	const (
		trusted  = "node_trusted00000000"
		selected = "node_selected0000000"
		stranger = "node_stranger0000000"
	)

	shared := session("shared-with-all-paired", model.ProviderClaude)
	targeted := session("shared-with-one", model.ProviderCodex)
	for _, s := range []model.Session{shared, targeted} {
		if _, err := store.UpsertSession(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	for _, nodeID := range []string{trusted, selected} {
		if err := store.TrustNode(ctx, registry.TrustedNode{
			NodeID: nodeID, DisplayName: "peer " + nodeID, Platform: "linux/amd64",
			PublicKey: "key-" + nodeID, Fingerprint: "2DCF 9604 DBA9 778A 6DDD 035B",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetAudience(ctx, shared.ID, model.Audience{Mode: model.AudienceAllPaired}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAudience(ctx, targeted.ID, model.Audience{
		Mode: model.AudienceSelected, Nodes: []string{selected},
	}); err != nil {
		t.Fatal(err)
	}

	node := model.NodeIdentity{ID: "node_0123456789abcdef0123", DisplayName: "test", Platform: "darwin/arm64"}
	builder := protocol.NewHeartbeatBuilder(store, node, newTestKeypair(t))

	count := func(t *testing.T, envelope protocol.Envelope) int {
		t.Helper()
		payload, err := protocol.DecodePayload[protocol.HeartbeatPayload](envelope)
		if err != nil {
			t.Fatal(err)
		}
		return len(payload.Sessions)
	}

	t.Run("unknown node is refused", func(t *testing.T) {
		_, err := builder.BuildFor(ctx, time.Now(), stranger)
		if err == nil {
			t.Fatal("built a heartbeat for a node this owner never paired with")
		}
		if !errors.Is(err, protocol.ErrPeerNotTrusted) {
			t.Errorf("error = %v; want ErrPeerNotTrusted so a caller can tell this from a store failure", err)
		}
	})

	t.Run("empty node is refused", func(t *testing.T) {
		if _, err := builder.BuildFor(ctx, time.Now(), ""); err == nil {
			t.Error("BuildFor accepted an empty recipient")
		}
	})

	t.Run("trusted node receives all_paired only", func(t *testing.T) {
		envelope, err := builder.BuildFor(ctx, time.Now(), trusted)
		if err != nil {
			t.Fatal(err)
		}
		if got := count(t, envelope); got != 1 {
			t.Errorf("a trusted node received %d sessions, want only the all_paired one", got)
		}
	})

	t.Run("selected node also receives the session naming it", func(t *testing.T) {
		envelope, err := builder.BuildFor(ctx, time.Now(), selected)
		if err != nil {
			t.Fatal(err)
		}
		if got := count(t, envelope); got != 2 {
			t.Errorf("the selected node received %d sessions, want both", got)
		}
	})

	t.Run("revoked node is refused", func(t *testing.T) {
		if err := store.RevokeNode(ctx, trusted); err != nil {
			t.Fatal(err)
		}
		_, err := builder.BuildFor(ctx, time.Now(), trusted)
		if err == nil {
			t.Fatal("a revoked node still received a heartbeat")
		}
		if !errors.Is(err, protocol.ErrPeerNotTrusted) {
			t.Errorf("error = %v; want ErrPeerNotTrusted", err)
		}
		// The other peer is unaffected: revoking one node must not silence the
		// rest.
		if _, err := builder.BuildFor(ctx, time.Now(), selected); err != nil {
			t.Errorf("revoking one node broke another peer's heartbeat: %v", err)
		}
	})

	// The owner preview is not a recipient view. It is a union and stays one, so
	// the owner can still see everything that leaves this host at all.
	t.Run("owner preview remains a union", func(t *testing.T) {
		preview, err := builder.Build(ctx, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if got := count(t, preview); got != 2 {
			t.Errorf("the owner preview showed %d sessions, want everything that leaves the host", got)
		}
	})
}
