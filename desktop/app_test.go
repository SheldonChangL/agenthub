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
				"displayName":"sheldon.chang mac","nameIsChosen":true,
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
	// The countdown comes from the node rather than being subtracted from the
	// expiry here, because the node measured the window against its own clock.
	// The name this node broadcasts, and whether a person picked it. Both reach
	// the warning that tells an owner what the segment can see, and its remedy
	// differs by the second — so a field lost in transit is a UI stating the
	// wrong one confidently.
	if pairing.State.DisplayName != "sheldon.chang mac" {
		t.Errorf("displayName = %q, want the name the node says it announces", pairing.State.DisplayName)
	}
	if !pairing.State.NameIsChosen {
		t.Error("nameIsChosen was lost, so the warning would say a chosen name was read off the machine")
	}
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

// The window read can succeed while the candidate read fails — two requests,
// and the second can fail on its own. That has to arrive as a failed read, not
// as an empty list, or the owner is told nobody is advertising on the strength
// of a request that never completed.
func TestPairingReportsAFailedCandidateReadSeparately(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/pairing" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"open":true,"remainingSeconds":120,
				"announcing":{"announceableAddresses":1}}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
			"code": "REGISTRY_ERROR", "message": "the candidate list could not be read",
		}})
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	pairing := app.Pairing()

	// The window is known, so availability is not in doubt.
	if pairing.Availability != pairingOn {
		t.Errorf("availability = %q, want %q", pairing.Availability, pairingOn)
	}
	if !pairing.State.Open {
		t.Error("the window read succeeded but its result was discarded")
	}
	// The failure is reported as its own fact, not as an empty list and not as
	// an error about the window.
	if pairing.Error != "" {
		t.Errorf("a failed candidate read was reported as a window error: %q", pairing.Error)
	}
	if !strings.Contains(pairing.CandidatesError, "could not be read") {
		t.Errorf("candidatesError = %q, want the node's own reason", pairing.CandidatesError)
	}
	if pairing.Candidates == nil {
		t.Error("candidates is nil, which marshals as null rather than an empty list")
	}
	if len(pairing.Candidates) != 0 {
		t.Errorf("candidates = %v after a failed read", pairing.Candidates)
	}
	// And nothing is claimed about the list itself.
	if pairing.Full {
		t.Error("a failed read reported the list as full")
	}
	if pairing.Notice != "" {
		t.Errorf("a failed read carried a notice about a list it never got: %q", pairing.Notice)
	}
}

// An inbox that could not be read is not an inbox with nothing in it. Told
// apart, because only one of them means the owner should stop looking.
func TestInboxSeparatesAFailedReadFromAnEmptyOne(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
			"code": "REGISTRY_ERROR", "message": "the inbox could not be read",
		}})
	}))
	defer failing.Close()

	app := &App{client: newClient(failing.URL), url: failing.URL, ctx: context.Background()}
	view := app.Inbox("claude:abc")
	if view.Error == "" {
		t.Error("a failed read reported no error")
	}
	if view.Messages == nil {
		t.Error("messages is nil, which marshals as null rather than an empty list")
	}

	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[],"held":0,"capacity":500,"full":false}`))
	}))
	defer empty.Close()

	app = &App{client: newClient(empty.URL), url: empty.URL, ctx: context.Background()}
	if view := app.Inbox("claude:abc"); view.Error != "" {
		t.Errorf("an empty inbox reported an error: %q", view.Error)
	}
}

// A full inbox refuses new messages, so it has to arrive as full rather than as
// a list that stopped growing for no stated reason.
func TestInboxCarriesHowFullItIs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/v1/inbox/claude:abc" {
			t.Errorf("path = %s", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[
			{"id":"msg_1","from":"node_a/codex:x","body":"hello","createdAt":"2026-09-08T04:00:00Z"}
		],"held":500,"capacity":500,"full":true}`))
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	view := app.Inbox("claude:abc")
	if !view.Full || view.Held != 500 || view.Capacity != 500 {
		t.Errorf("view = %+v, want it to carry that the inbox is full", view)
	}
	if len(view.Messages) != 1 || view.Messages[0].Body != "hello" {
		t.Errorf("messages = %+v", view.Messages)
	}
	// The sender travels, because who sent it is the only part the reader can
	// check — and the node id inside it is the only identifying half.
	if view.Messages[0].From != "node_a/codex:x" {
		t.Errorf("the sender was dropped: %+v", view.Messages[0])
	}
}

// Reading and clearing both refuse without a session rather than asking the
// node about an empty path.
func TestInboxRefusesWithoutASession(t *testing.T) {
	var called int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	if view := app.Inbox("  "); view.Error == "" {
		t.Error("reading with no session reported no error")
	}
	if cleared := app.ClearInbox(""); cleared.Error == "" {
		t.Error("clearing with no session reported success")
	}
	if called != 0 {
		t.Errorf("the node was asked %d times about an empty session id", called)
	}
}

// The one destructive operation here, and nothing checked what it sent.
func TestClearInboxDeletesTheSessionItWasGiven(t *testing.T) {
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"removed":3}`))
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	cleared := app.ClearInbox("claude:the-one-asked-for")
	if cleared.Error != "" {
		t.Fatalf("ClearInbox: %s", cleared.Error)
	}
	// The count travels, because it is the only sign that something arrived
	// between the read and the confirm and was destroyed unseen.
	if cleared.Removed != 3 {
		t.Errorf("removed = %d, want 3", cleared.Removed)
	}
	if method != http.MethodDelete {
		t.Errorf("method = %s, want DELETE; a GET would report success having emptied nothing", method)
	}
	if path != "/v1/inbox/claude:the-one-asked-for" {
		t.Errorf("path = %s, want the session it was given", path)
	}
}

// A page, and small enough that a peer cannot make one undecodable.
//
// A body is 32KB and a control character in it escapes to six JSON bytes, so a
// large page can pass this app's read limit — after which nothing decodes and
// the owner cannot see what is jamming the inbox they came to look at. Fifty
// was that size; the CLI's own comment says so.
func TestInboxAsksForAPageSmallEnoughToDecode(t *testing.T) {
	var limit string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit = r.URL.Query().Get("limit")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[],"held":500,"capacity":500,"full":true,"next":"cursor"}`))
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	view := app.Inbox("claude:abc")
	if limit != "10" {
		t.Errorf("limit = %q, want 10", limit)
	}
	// And the view says it is a page, so ten out of five hundred cannot read as
	// an inbox of ten.
	if !view.More {
		t.Error("a paged answer did not say there is more")
	}
	if view.Held != 500 {
		t.Errorf("held = %d, want the node's own count", view.Held)
	}
}

// An answer cut off at the read limit will not decode, and "unexpected end of
// JSON input" sends the owner nowhere. This is reachable from an inbox, which
// is why it is checked here.
func TestATruncatedAnswerSaysSoRatherThanFailingToDecode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// More than the client will read, so it is cut mid-document.
		big := strings.Repeat("a", responseCap+1024)
		_, _ = w.Write([]byte(`{"messages":[{"id":"m","from":"n/c:s","body":"` + big + `"}]}`))
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	view := app.Inbox("claude:abc")
	if view.Error == "" {
		t.Fatal("a truncated answer was not reported as an error")
	}
	if strings.Contains(view.Error, "unexpected end of JSON input") {
		t.Errorf("the owner is told %q, which points nowhere", view.Error)
	}
	if !strings.Contains(view.Error, "cut off") {
		t.Errorf("error = %q, want it to say what happened", view.Error)
	}
}

// A clear that fails has to come back as a fact the dialog can show, not as a
// thrown error: the modal is fixed over the whole window, so a banner behind it
// leaves the owner with an unchanged list and no sign the action did not
// happen.
func TestClearInboxReportsAFailureRatherThanThrowing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{
			"code": "REGISTRY_ERROR", "message": "the inbox could not be emptied",
		}})
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	cleared := app.ClearInbox("claude:abc")
	if cleared.Error == "" {
		t.Fatal("a failed clear reported success")
	}
	if !strings.Contains(cleared.Error, "could not be emptied") {
		t.Errorf("error = %q, want the node's own reason", cleared.Error)
	}
	if cleared.Removed != 0 {
		t.Errorf("removed = %d after a failure", cleared.Removed)
	}
}

// A cursor is not evidence of more. The node issues one whenever a page comes
// back full, so an inbox holding exactly one page answers with one — and taking
// it at face value put "there is more, clear some to see it" beside the
// irreversible button, about messages that do not exist.
func TestAFullPageIsNotTakenAsProofOfMore(t *testing.T) {
	for name, testCase := range map[string]struct {
		body     string
		wantMore bool
	}{
		"a full page that is the whole inbox": {
			`{"messages":[{"id":"m1"},{"id":"m2"}],"held":2,"capacity":500,"next":"cursor"}`, false,
		},
		"a full page with more behind it": {
			`{"messages":[{"id":"m1"},{"id":"m2"}],"held":500,"capacity":500,"next":"cursor"}`, true,
		},
		"a short page": {
			`{"messages":[{"id":"m1"}],"held":1,"capacity":500}`, false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(testCase.body))
			}))
			defer server.Close()

			app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
			if got := app.Inbox("claude:abc").More; got != testCase.wantMore {
				t.Errorf("more = %v, want %v", got, testCase.wantMore)
			}
		})
	}
}

// SetNodeAddress is the window's repair for the quietest failure in the
// system: a paired node with no recorded address is skipped without a word and
// the sender's `ah send` still answers `queued`. With no `--discover`
// broadcast on the segment, nothing else in this app could supply one.
func TestSetNodeAddressRecordsWhereThePeerAnswers(t *testing.T) {
	var method, path, contentType string
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, contentType = r.Method, r.URL.EscapedPath(), r.Header.Get("Content-Type")
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	if err := app.SetNodeAddress("node_ubuntu000000000", "192.168.1.20:7463"); err != nil {
		t.Fatalf("SetNodeAddress: %v", err)
	}
	if method != http.MethodPut {
		t.Errorf("method = %s, want PUT", method)
	}
	if path != "/v1/nodes/node_ubuntu000000000/address" {
		t.Errorf("path = %s", path)
	}
	if contentType != "application/json" {
		t.Errorf("Content-Type = %q", contentType)
	}
	var sent struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("decode request body %q: %v", body, err)
	}
	// Exactly what the owner typed. Rewriting it here would mean this window
	// and the node disagree about what was recorded.
	if sent.Address != "192.168.1.20:7463" {
		t.Errorf("address sent = %q, want 192.168.1.20:7463", sent.Address)
	}
}

// A node id becomes one path segment, whatever is in it.
func TestSetNodeAddressEscapesTheNodeID(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	if err := app.SetNodeAddress("a/../b", "10.0.0.2:7463"); err != nil {
		t.Fatalf("SetNodeAddress: %v", err)
	}
	if path != "/v1/nodes/a%2F..%2Fb/address" {
		t.Errorf("path = %s; the node id was not escaped into one segment", path)
	}
}

// What is wrong with an address is something only the node knows — it holds the
// ranges this build will deliver to — so its words are what the banner shows.
// Replacing them with a message invented here would send the owner looking for
// the wrong fault.
func TestSetNodeAddressPassesTheNodesRefusalThrough(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"INVALID_REQUEST","message":"address must be host:port"}}`))
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	err := app.SetNodeAddress("node_a00000000000000", "not-an-address")
	if err == nil {
		t.Fatal("SetNodeAddress returned nil for an address the node refused")
	}
	if !strings.Contains(err.Error(), "address must be host:port") {
		t.Errorf("the node's reason did not reach the window: %v", err)
	}
}

// The address the node already has must reach the window, or the warning that
// there is none cannot tell the two apart. An unknown key is dropped in
// decoding, so this is a fact about the struct, not about the wire.
func TestOverviewCarriesEachNodesRecordedAddress(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/node":
			_, _ = w.Write([]byte(`{"id":"node_self0000000000","displayName":"self","platform":"darwin","publicKey":"AAAA","fingerprint":"2DCF 9604"}`))
		case "/v1/nodes":
			_, _ = w.Write([]byte(`{"nodes":[` +
				`{"nodeId":"node_with000000000","displayName":"has one","platform":"linux","address":"192.168.1.20:7463"},` +
				`{"nodeId":"node_without000000","displayName":"has none","platform":"linux"}]}`))
		default:
			_, _ = w.Write([]byte(`{"sessions":[],"peers":[],"pagination":{"totalPages":1}}`))
		}
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	overview := app.Overview()
	if len(overview.Nodes) != 2 {
		t.Fatalf("nodes = %d, want 2 (error: %s)", len(overview.Nodes), overview.Error)
	}
	if overview.Nodes[0].Address != "192.168.1.20:7463" {
		t.Errorf("recorded address = %q, want 192.168.1.20:7463", overview.Nodes[0].Address)
	}
	if overview.Nodes[1].Address != "" {
		t.Errorf("a node with no address reported %q", overview.Nodes[1].Address)
	}
	// The local public key is what the peer types into its own dialog, and the
	// window had no way to show it: main.js never read the field.
	if overview.Node.PublicKey != "AAAA" {
		t.Errorf("local public key = %q, want AAAA", overview.Node.PublicKey)
	}
}

// TestOutboundAndWakesAskTheRightQuestions pins both records reads: the path
// and query each sends, and that every field an owner needs survives decoding.
//
// The fields are the whole point. A `refused` with no reason and no attempt
// count is a row that says something went wrong and nothing about what, which
// is the state the CLI-only view already left them in.
func TestOutboundAndWakesAskTheRightQuestions(t *testing.T) {
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path+"?"+r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/outbound":
			_, _ = io.WriteString(w, `{"messages":[{"id":"msg_1","destinationNodeId":"node_peer",`+
				`"to":"codex:theirs","from":"claude:mine","state":"refused","attempts":3,`+
				`"createdAt":"2026-09-10T01:00:00Z","updatedAt":"2026-09-10T01:05:00Z",`+
				`"lastError":"nowhere to deliver to"}]}`)
		case "/v1/wakes":
			_, _ = io.WriteString(w, `{"wakes":[{"id":"wake_1","messageId":"msg_9",`+
				`"sourceNodeId":"node_peer","sourceSession":"codex:theirs",`+
				`"destinationSession":"claude:mine","hops":2,"outcome":"refused_pair_rate",`+
				`"detail":"3 in the last 10m0s, at the limit of 3","at":"2026-09-10T01:00:00Z"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}

	outbound := app.Outbound()
	if outbound.Error != "" {
		t.Fatalf("Outbound() error = %q", outbound.Error)
	}
	if len(outbound.Messages) != 1 {
		t.Fatalf("Outbound() messages = %#v", outbound.Messages)
	}
	message := outbound.Messages[0]
	if message.ID != "msg_1" || message.To != "codex:theirs" || message.From != "claude:mine" ||
		message.State != "refused" || message.Attempts != 3 ||
		message.LastError != "nowhere to deliver to" {
		t.Errorf("message = %#v; a field the dialog renders was dropped in decoding", message)
	}
	if message.CreatedAt.IsZero() {
		t.Error("the queued time did not decode, so no row can say when it was sent")
	}

	wakes := app.Wakes("claude:mine")
	if wakes.Error != "" {
		t.Fatalf("Wakes() error = %q", wakes.Error)
	}
	if len(wakes.Wakes) != 1 {
		t.Fatalf("Wakes() = %#v", wakes.Wakes)
	}
	event := wakes.Wakes[0]
	if event.DestinationSession != "claude:mine" || event.SourceSession != "codex:theirs" ||
		event.Hops != 2 || event.Outcome != "refused_pair_rate" ||
		!strings.Contains(event.Detail, "at the limit of 3") {
		t.Errorf("wake = %#v; a field the dialog renders was dropped in decoding", event)
	}

	// The session filter travels as a query parameter, and no filter means no
	// parameter — not an empty one, which the node resolves as a session id and
	// refuses.
	if unfiltered := app.Wakes(""); unfiltered.Error != "" {
		t.Errorf("unfiltered Wakes() failed: %q", unfiltered.Error)
	}
	want := []string{
		"/v1/outbound?limit=50",
		"/v1/wakes?limit=50&session=claude%3Amine",
		"/v1/wakes?limit=50",
	}
	if strings.Join(asked, "|") != strings.Join(want, "|") {
		t.Fatalf("requests = %v; want %v", asked, want)
	}
}

// TestRecordsReadsCarryTheirFailureIntoTheDialog covers the unreachable node.
//
// Thrown, the error would reach a banner the dialog is drawn over, and the
// dialog would show an empty list — which reads as "this node has never sent
// anything", the one answer that must not be invented.
func TestRecordsReadsCarryTheirFailureIntoTheDialog(t *testing.T) {
	app := &App{client: newClient("http://127.0.0.1:1"), url: "http://127.0.0.1:1",
		ctx: context.Background()}
	outbound := app.Outbound()
	if outbound.Error == "" {
		t.Error("Outbound() from an unreachable node reported no error")
	}
	if outbound.Messages == nil {
		t.Error("Outbound() returned a nil list, which the frontend cannot iterate")
	}
	wakes := app.Wakes("")
	if wakes.Error == "" {
		t.Error("Wakes() from an unreachable node reported no error")
	}
	if wakes.Wakes == nil {
		t.Error("Wakes() returned a nil list, which the frontend cannot iterate")
	}
}
