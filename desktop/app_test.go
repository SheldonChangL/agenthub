package main

import (
	"context"
	"encoding/json"
	"io"
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

// "This node is not looking", "this node did not answer" and "nobody is
// advertising" are three different facts. A panel that renders any of them as
// another tells the owner to keep waiting for something that is not coming, or
// to change a setting that is not the problem.
func TestPairingDistinguishesOffFromUnreachable(t *testing.T) {
	discoveryOff := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
			"code":    "DISCOVERY_DISABLED",
			"message": "this node is not listening on the local network. Start it with -discover",
		}})
	}))
	defer discoveryOff.Close()

	app := &App{client: newClient(discoveryOff.URL), url: discoveryOff.URL, ctx: context.Background()}
	pairing := app.Pairing()
	if pairing.Availability != pairingOff {
		t.Errorf("availability = %q, want %q", pairing.Availability, pairingOff)
	}
	if !strings.Contains(pairing.Error, "DISCOVERY_DISABLED") {
		t.Errorf("error = %q, want the node's own code", pairing.Error)
	}
	// And never a bare empty list, which reads as "nobody is out there".
	if pairing.Candidates == nil {
		t.Error("candidates is nil, which marshals as null rather than an empty list")
	}
	if len(pairing.Candidates) != 0 {
		t.Errorf("candidates = %v on a node that is not looking", pairing.Candidates)
	}

	// A node that is not there at all is a third answer, not the second one.
	unreachable := &App{client: newClient("http://127.0.0.1:1"), url: "http://127.0.0.1:1",
		ctx: context.Background()}
	if got := unreachable.Pairing(); got.Availability != pairingUnknown {
		t.Errorf("availability = %q on an unreachable node, want %q", got.Availability, pairingUnknown)
	} else if got.Error == "" {
		t.Error("an unreachable node produced no error")
	}
}

// The candidate list is the one view whose every field was chosen by whoever
// sent the packet. It has to arrive intact — flags included — because the flags
// are how an impersonation attempt is visible at all, and a field this struct
// does not carry is one the UI can never render.
func TestPairingCarriesEveryClaimAndFlagThroughToTheUI(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/pairing":
			_, _ = w.Write([]byte(`{"open":true,"openedAt":"2026-09-07T07:00:00Z",
				"expiresAt":"2026-09-07T07:05:00Z","remainingSeconds":240,
				"announcing":{"announceableAddresses":1,"lastAnnouncedAt":"2026-09-07T07:00:20Z"}}`))
		case "/v1/pairing/candidates":
			_, _ = w.Write([]byte(`{"candidates":[
				{"nodeId":"node_a","address":"192.168.1.5:7463","displayName":"laptop",
				 "platform":"darwin/arm64","fingerprint":"1223 03EA 5E96 543A 2DD8 BFEA",
				 "firstSeen":"2026-09-07T07:00:00Z","lastSeen":"2026-09-07T07:01:00Z",
				 "duplicate":true,"contested":true}],
				"full":true,"notice":"nothing here has been verified"}`))
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	pairing := app.Pairing()
	if pairing.Availability != pairingOn {
		t.Fatalf("availability = %q, want %q (error %q)", pairing.Availability, pairingOn, pairing.Error)
	}
	if !pairing.State.Open || pairing.State.Remaining != 240 {
		t.Errorf("state = %+v, want an open window with 240s left", pairing.State)
	}
	// The countdown comes from the node, not from a subtraction here, so it
	// cannot disagree with the expiry beside it.
	if pairing.State.Announcing.Addresses != 1 || pairing.State.Announcing.LastSuccess.IsZero() {
		t.Errorf("announcing = %+v, want one address and a last success", pairing.State.Announcing)
	}
	if !pairing.Full {
		t.Error("full was dropped; the owner would not learn the machine they want may be missing")
	}
	if pairing.Notice == "" {
		t.Error("the node's notice was dropped, so the UI would have to invent its own")
	}
	if len(pairing.Candidates) != 1 {
		t.Fatalf("candidates = %v, want one", pairing.Candidates)
	}
	candidate := pairing.Candidates[0]
	if !candidate.Duplicate || !candidate.Contested {
		t.Errorf("candidate = %+v; duplicate and contested are how impersonation is visible", candidate)
	}
	for field, got := range map[string]string{
		"nodeId":      candidate.NodeID,
		"address":     candidate.Address,
		"displayName": candidate.DisplayName,
		"platform":    candidate.Platform,
		"fingerprint": candidate.Fingerprint,
	} {
		if got == "" {
			t.Errorf("candidate %s was dropped in decoding", field)
		}
	}
}

// A window the node refuses must surface as a refusal. Reporting success and
// then showing a closed window would read as the node ignoring the button.
func TestOpenPairingSurfacesTheNodesRefusal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
			"code":    "NO_ANNOUNCEABLE_ADDRESS",
			"message": "this node has no address a peer on the local network could reach",
		}})
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	state, err := app.OpenPairing(60)
	if err == nil {
		t.Fatalf("OpenPairing reported success against a node that refused: %+v", state)
	}
	if !strings.Contains(err.Error(), "NO_ANNOUNCEABLE_ADDRESS") {
		t.Errorf("error = %q, want the node's own code so the panel can explain it", err)
	}
	if state.Open {
		t.Error("a refused window came back open")
	}
}

// Zero means "no preference" and must not be sent as a duration: the node reads
// a zero window as its default, so forwarding one asked-for-nothing would be
// indistinguishable from asking for five minutes.
func TestOpenPairingSendsADurationOnlyWhenItHasOne(t *testing.T) {
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		bodies = append(bodies, strings.TrimSpace(string(data)))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"open":true,"remainingSeconds":300,"announcing":{"announceableAddresses":1}}`))
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	if _, err := app.OpenPairing(0); err != nil {
		t.Fatal(err)
	}
	if _, err := app.OpenPairing(90); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("bodies = %v", bodies)
	}
	if bodies[0] != "" {
		t.Errorf("OpenPairing(0) sent %q, want no body at all", bodies[0])
	}
	if !strings.Contains(bodies[1], `"seconds":90`) {
		t.Errorf("OpenPairing(90) sent %q", bodies[1])
	}
}
