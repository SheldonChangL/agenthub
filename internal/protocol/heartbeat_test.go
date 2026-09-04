package protocol

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/address"
	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/registry"
	_ "modernc.org/sqlite"
)

func TestHeartbeatContainsPublicSessionsOnly(t *testing.T) {
	ctx := context.Background()
	store, err := registry.Open(ctx, filepath.Join(t.TempDir(), "agenthub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)

	for _, id := range []string{"claude:private", "codex:public"} {
		provider := model.ProviderClaude
		providerID := id[len("claude:"):]
		if id == "codex:public" {
			provider = model.ProviderCodex
			providerID = "public"
		}
		_, err := store.UpsertSession(ctx, model.Session{
			ID: id, Provider: provider, ProviderSessionID: providerID,
			Management: model.Unmanaged, Status: model.StatusIdle, StatusSource: "test",
			LastSeenAt: now, UpdatedAt: now,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SetVisibility(ctx, "codex:public", model.VisibilityPublic); err != nil {
		t.Fatal(err)
	}

	node := model.NodeIdentity{ID: "node-1234567890123456", DisplayName: "test", Platform: "test"}
	builder := NewHeartbeatBuilder(store, node, internalTestSigner{})
	envelope, err := builder.Build(ctx, now)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if envelope.Type != TypeNodeHeartbeat || envelope.NodeID != node.ID {
		t.Fatalf("envelope type/nodeId = %q/%q", envelope.Type, envelope.NodeID)
	}
	got, err := DecodePayload[HeartbeatPayload](envelope)
	if err != nil {
		t.Fatal(err)
	}
	want := address.QualifiedID(node.ID, "codex:public")
	if len(got.Sessions) != 1 || got.Sessions[0].ID != want {
		t.Fatalf("heartbeat sessions = %#v; want only %q", got.Sessions, want)
	}
	if got.Sequence != 1 || !got.ExpiresAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("heartbeat sequence/expiresAt = %d/%v", got.Sequence, got.ExpiresAt)
	}
}

// internalTestSigner keeps the in-package test independent of the external
// test helper while still producing a real signature.
type internalTestSigner struct{}

func (internalTestSigner) Sign(message []byte) []byte {
	_, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		panic(err)
	}
	return ed25519.Sign(private, message)
}

// Whatever this node sends, a node running this code must accept.
//
// The receiving side refuses snapshots whose contents are not what the fields
// are for (ValidateIncomingPayload). If the builder could produce something that
// check refuses, this build would reject its own peers — so the two are pinned
// to each other rather than tested apart.
func TestWhatTheBuilderProducesPassesTheIncomingCheck(t *testing.T) {
	ctx := context.Background()
	store, err := registry.Open(ctx, filepath.Join(t.TempDir(), "heartbeat.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	// Relative to the clock, because the incoming check bounds how far ahead a
	// reported time may be. A hardcoded date drifts into the future and the test
	// starts failing for a reason unrelated to what it asserts.
	now := time.Now().UTC().Add(-time.Hour)

	// One of each provider, one published every way a session can be.
	for _, seed := range []struct {
		id       string
		provider model.Provider
		cwd      string
	}{
		{"claude:one", model.ProviderClaude, "/home/someone/a project with spaces"},
		{"codex:two", model.ProviderCodex, ""},
	} {
		providerID := seed.id[strings.Index(seed.id, ":")+1:]
		if _, err := store.UpsertSession(ctx, model.Session{
			ID: seed.id, Provider: seed.provider, ProviderSessionID: providerID,
			Management: model.Unmanaged, Status: model.StatusActive, StatusSource: "test",
			CWD: seed.cwd, LastSeenAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SetAudience(ctx, seed.id, model.Audience{
			Mode: model.AudienceAllPaired, ExportCWD: true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	node := model.NodeIdentity{ID: "node_1234567890123456", DisplayName: "test", Platform: "test"}
	envelope, err := NewHeartbeatBuilder(store, node, internalTestSigner{}).Build(ctx, now)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	payload, err := DecodePayload[HeartbeatPayload](envelope)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Sessions) != 2 {
		t.Fatalf("want 2 sessions, got %d; the test would not exercise much", len(payload.Sessions))
	}
	if err := ValidateIncomingPayload(node.ID, payload); err != nil {
		t.Errorf("this node's own heartbeat would be refused by a node running this code: %v", err)
	}
}

// A path this build cannot export must cost its owner the directory, not the
// session — and not every other session in the same heartbeat.
//
// A receiver refuses a whole snapshot over one bad field, so a long or
// tab-bearing path (both legal: PATH_MAX is 1024 on macOS, 4096 on Linux) would
// otherwise take the entire node off every peer's view.
func TestAnUnexportableDirectoryCostsOnlyTheDirectory(t *testing.T) {
	ctx := context.Background()
	store, err := registry.Open(ctx, filepath.Join(t.TempDir(), "cwd.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC().Add(-time.Hour)

	seeds := map[string]string{
		"claude:long": "/home/u/" + strings.Repeat("專案", 200), // 1206 bytes, legal
		"claude:tab":  "/home/u/a\tb",
		"claude:fine": "/home/u/ordinary",
	}
	for id, cwd := range seeds {
		if _, err := store.UpsertSession(ctx, model.Session{
			ID: id, Provider: model.ProviderClaude, ProviderSessionID: id[len("claude:"):],
			Management: model.Unmanaged, Status: model.StatusIdle, StatusSource: "test",
			CWD: cwd, LastSeenAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.SetAudience(ctx, id, model.Audience{
			Mode: model.AudienceAllPaired, ExportCWD: true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	node := model.NodeIdentity{ID: "node_1234567890123456", DisplayName: "t", Platform: "t"}
	envelope, err := NewHeartbeatBuilder(store, node, internalTestSigner{}).Build(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := DecodePayload[HeartbeatPayload](envelope)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload.Sessions) != 3 {
		t.Fatalf("want all 3 sessions exported, got %d", len(payload.Sessions))
	}
	if err := ValidateIncomingPayload(node.ID, payload); err != nil {
		t.Fatalf("a receiver would refuse this node's own heartbeat: %v", err)
	}
	byID := map[string]string{}
	for _, s := range payload.Sessions {
		byID[s.ID] = s.CWD
	}
	if got := byID[node.ID+"/claude:fine"]; got != "/home/u/ordinary" {
		t.Errorf("an ordinary path was not exported: %q", got)
	}
	for _, id := range []string{"claude:long", "claude:tab"} {
		if got := byID[node.ID+"/"+id]; got != "" {
			t.Errorf("%s exported a directory a receiver refuses: %q", id, got)
		}
	}
}

// A local session a peer would refuse must not take the whole snapshot with it.
//
// The cwd fix covered one field; the same mismatch survived on the id and on
// lastSeenAt, and the existing tests could not see it because the builder is
// only ever given clean sessions. A receiver refuses a whole snapshot over one
// bad row, so an unexportable id would have taken every other session in that
// heartbeat off every peer's view.
//
// An id cannot be dropped the way a cwd can — it is what the session is — so
// that session is left out. A time is clamped, because the session is still
// worth exporting and the wrong value is only a display detail.
func TestASessionAPeerWouldRefuseIsLeftOutNotFatal(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "mismatch.db")
	store, err := registry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC().Add(-time.Hour)

	seeds := []struct {
		providerSessionID string
		lastSeen          time.Time
	}{
		{"ordinary", now},
		{strings.Repeat("a", MaxProviderSessionIDLength+1), now}, // id no peer accepts
		{"has space here", now},                                  // id no peer accepts
		{"future", time.Now().UTC().Add(72 * time.Hour)},         // a time no clock explains
		{"disagrees", now},                                       // provider will be made to disagree with the id
	}
	for _, seed := range seeds {
		id := "claude:" + seed.providerSessionID
		if _, err := store.UpsertSession(ctx, model.Session{
			ID: id, Provider: model.ProviderClaude, ProviderSessionID: seed.providerSessionID,
			Management: model.Unmanaged, Status: model.StatusIdle, StatusSource: "test",
			LastSeenAt: seed.lastSeen, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed %q: %v", id, err)
		}
		if err := store.SetAudience(ctx, id, model.Audience{Mode: model.AudienceAllPaired}); err != nil {
			t.Fatalf("audience %q: %v", id, err)
		}
	}

	// The registry validates on write, so a row that passes every column
	// check yet would be refused by a peer has to be written behind its back —
	// which is the premise: a store written by something else.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, `UPDATE sessions SET provider = 'codex' WHERE id = 'claude:disagrees'`); err != nil {
		t.Fatalf("corrupt row: %v", err)
	}

	node := model.NodeIdentity{ID: "node_1234567890123456", DisplayName: "t", Platform: "t"}
	envelope, err := NewHeartbeatBuilder(store, node, internalTestSigner{}).Build(ctx, now)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	payload, err := DecodePayload[HeartbeatPayload](envelope)
	if err != nil {
		t.Fatal(err)
	}

	// The two unexportable ids and the disagreeing row are left out; the
	// ordinary one and the future-dated one survive.
	if len(payload.Sessions) != 2 {
		t.Fatalf("want 2 exported sessions, got %d: %+v", len(payload.Sessions), payload.Sessions)
	}
	// And the whole thing is acceptable to a peer, which is the point.
	if err := ValidateIncomingPayload(node.ID, payload); err != nil {
		t.Fatalf("a receiver would refuse this node's own heartbeat: %v", err)
	}
	// Clamped to the builder's now, not to the edge of what a receiver
	// tolerates: that edge is on the receiver's clock, and a sender running
	// ahead would land just past it.
	for _, s := range payload.Sessions {
		if s.LastSeenAt.After(now) {
			t.Errorf("%s exported lastSeenAt %s, after the build's own now %s", s.ID, s.LastSeenAt, now)
		}
	}
}
