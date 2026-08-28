package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/registry"
)

func seedSession(t *testing.T, store *registry.Registry, providerSessionID string) string {
	t.Helper()
	now := time.Now().UTC()
	session := model.Session{
		ID:                model.SessionID(model.ProviderClaude, providerSessionID),
		Provider:          model.ProviderClaude,
		ProviderSessionID: providerSessionID,
		Management:        model.Unmanaged,
		Status:            model.StatusIdle,
		StatusSource:      "test",
		CWD:               "/tmp/example",
		LastSeenAt:        now,
		UpdatedAt:         now,
	}
	if _, err := store.UpsertSession(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	return session.ID
}

func TestAudienceEndpointRoundTrip(t *testing.T) {
	store, handler := testServer(t)
	id := seedSession(t, store, "round-trip")

	set := perform(t, handler, http.MethodPut, "/v1/sessions/"+id+"/audience", map[string]any{
		"mode": "selected", "nodes": []string{"node_b", "node_a"},
		"exportCwd": true, "acceptMessages": false,
	})
	if set.Code != http.StatusOK {
		t.Fatalf("set audience = %d %s", set.Code, set.Body.String())
	}

	got := perform(t, handler, http.MethodGet, "/v1/sessions/"+id+"/audience", nil)
	if got.Code != http.StatusOK {
		t.Fatalf("get audience = %d %s", got.Code, got.Body.String())
	}
	var audience model.Audience
	if err := json.Unmarshal(got.Body.Bytes(), &audience); err != nil {
		t.Fatal(err)
	}
	if audience.Mode != model.AudienceSelected || len(audience.Nodes) != 2 {
		t.Fatalf("audience = %+v", audience)
	}
	if !audience.ExportCWD || audience.AcceptMessages {
		t.Errorf("export flags = %+v", audience)
	}
}

// A misspelled field must not be ignored: a caller who meant to grant nodes
// would otherwise publish to nobody, or publish to everyone, without being told.
func TestAudienceEndpointRejectsUnknownFields(t *testing.T) {
	store, handler := testServer(t)
	id := seedSession(t, store, "unknown-field")

	response := perform(t, handler, http.MethodPut, "/v1/sessions/"+id+"/audience", map[string]any{
		"mode": "selected", "node": []string{"node_a"},
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("response = %d %s; a misspelled field must be refused", response.Code, response.Body.String())
	}
}

func TestAudienceBatchReportsPerSessionOutcomes(t *testing.T) {
	store, handler := testServer(t)
	first := seedSession(t, store, "batch-one")
	second := seedSession(t, store, "batch-two")

	response := perform(t, handler, http.MethodPost, "/v1/sessions/audience", map[string]any{
		"ids":      []string{first, "claude:missing", second},
		"audience": map[string]any{"mode": "all_paired", "exportCwd": true},
	})
	if response.Code != http.StatusOK {
		t.Fatalf("batch = %d %s", response.Code, response.Body.String())
	}
	var decoded struct {
		Changed int `json:"changed"`
		Failed  int `json:"failed"`
		Results []struct {
			ID    string `json:"id"`
			Error string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Changed != 2 || decoded.Failed != 1 {
		t.Errorf("changed/failed = %d/%d, want 2/1: %s", decoded.Changed, decoded.Failed, response.Body.String())
	}
	if len(decoded.Results) != 3 {
		t.Errorf("results = %d, want one per requested session", len(decoded.Results))
	}

	// The sessions that succeeded must actually be published.
	published, err := store.ListSessions(context.Background(), registry.ListOptions{PublicOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(published) != 2 {
		t.Errorf("published %d sessions, want 2", len(published))
	}
}

// An invalid policy must change nothing rather than the first few sessions.
func TestAudienceBatchValidatesBeforeWriting(t *testing.T) {
	store, handler := testServer(t)
	first := seedSession(t, store, "atomic-one")
	second := seedSession(t, store, "atomic-two")

	response := perform(t, handler, http.MethodPost, "/v1/sessions/audience", map[string]any{
		"ids":      []string{first, second},
		"audience": map[string]any{"mode": "everyone"},
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("batch = %d %s", response.Code, response.Body.String())
	}
	published, err := store.ListSessions(context.Background(), registry.ListOptions{PublicOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(published) != 0 {
		t.Errorf("a rejected batch published %d sessions", len(published))
	}
}

// The pre-audience endpoint keeps working, and publishing through it means the
// explicit "all paired nodes" choice rather than a fourth, implicit mode.
func TestVisibilityEndpointMapsOntoAudience(t *testing.T) {
	store, handler := testServer(t)
	id := seedSession(t, store, "compat")

	if response := perform(t, handler, http.MethodPut, "/v1/sessions/"+id+"/visibility",
		map[string]string{"visibility": "public"}); response.Code != http.StatusOK {
		t.Fatalf("publish = %d %s", response.Code, response.Body.String())
	}
	audience, err := store.GetAudience(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if audience.Mode != model.AudienceAllPaired {
		t.Errorf("mode = %q, want all_paired", audience.Mode)
	}

	if response := perform(t, handler, http.MethodPut, "/v1/sessions/"+id+"/visibility",
		map[string]string{"visibility": "private"}); response.Code != http.StatusOK {
		t.Fatalf("unpublish = %d %s", response.Code, response.Body.String())
	}
	audience, err = store.GetAudience(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if audience.Mode != model.AudienceNone {
		t.Errorf("mode = %q, want none", audience.Mode)
	}
}

// The inbox opt-in is enforced at the API boundary too, and refusing is the
// caller's fault rather than a server failure.
func TestMessagesRequireInboxOptIn(t *testing.T) {
	store, handler := testServer(t)
	id := seedSession(t, store, "closed-inbox")

	refused := perform(t, handler, http.MethodPost, "/v1/messages",
		map[string]string{"to": id, "body": "hello"})
	if refused.Code != http.StatusBadRequest {
		t.Fatalf("response = %d %s, want 400", refused.Code, refused.Body.String())
	}

	if response := perform(t, handler, http.MethodPut, "/v1/sessions/"+id+"/audience",
		map[string]any{"mode": "none", "acceptMessages": true}); response.Code != http.StatusOK {
		t.Fatalf("opt in = %d %s", response.Code, response.Body.String())
	}
	accepted := perform(t, handler, http.MethodPost, "/v1/messages",
		map[string]string{"to": id, "body": "hello"})
	if accepted.Code != http.StatusCreated {
		t.Fatalf("response = %d %s, want 201", accepted.Code, accepted.Body.String())
	}
}
