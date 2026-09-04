package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agenthub.local/agenthub/internal/model"
)

func TestRunListShowsPrivacyState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/sessions" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sessions":[{"id":"claude:abc","provider":"claude","management":"unmanaged","visibility":"private","status":"idle","statusSource":"test","lastSeenAt":"2026-08-28T01:00:00Z","updatedAt":"2026-08-28T01:00:00Z"}]}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	exitCode := Run(context.Background(), []string{"--url", server.URL, "list"}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("Run() exit = %d, stderr = %s", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "claude:abc") || !strings.Contains(stdout.String(), "private") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunPublishCallsVisibilityEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/v1/sessions/claude:abc/visibility" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"claude:abc","visibility":"public"}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	exitCode := Run(context.Background(), []string{"--url", server.URL, "publish", "claude:abc"}, &stdout, &stderr)
	if exitCode != 0 || !strings.Contains(stdout.String(), "public") {
		t.Fatalf("exit = %d, stdout = %q, stderr = %q", exitCode, stdout.String(), stderr.String())
	}
}

func TestAudienceCommandRejectsIncoherentInput(t *testing.T) {
	cases := map[string][]string{
		"missing session":        {"audience"},
		"unknown mode":           {"audience", "claude:x", "everyone"},
		"selected without nodes": {"audience", "claude:x", "selected"},
		"nodes without selected": {"audience", "claude:x", "all-paired", "node_a"},
		"unknown flag":           {"audience", "claude:x", "all-paired", "--public"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			// The node URL is unreachable on purpose: these must fail before
			// any request is made.
			code := Run(context.Background(), append([]string{"--url", "http://127.0.0.1:1"}, args...), &stdout, &stderr)
			if code == 0 {
				t.Errorf("Run(%v) = 0; want a non-zero exit", args)
			}
			if stdout.Len() != 0 {
				t.Errorf("Run(%v) wrote to stdout: %s", args, stdout.String())
			}
		})
	}
}

func TestDescribeAudience(t *testing.T) {
	cases := map[string]struct {
		audience model.Audience
		want     string
	}{
		"none":            {model.Audience{Mode: model.AudienceNone}, "private"},
		"all paired":      {model.Audience{Mode: model.AudienceAllPaired}, "all paired"},
		"one node":        {model.Audience{Mode: model.AudienceSelected, Nodes: []string{"node_a"}}, "1 node"},
		"several nodes":   {model.Audience{Mode: model.AudienceSelected, Nodes: []string{"node_a", "node_b"}}, "2 nodes"},
		"empty selection": {model.Audience{Mode: model.AudienceSelected}, "0 nodes"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			if got := describeAudience(testCase.audience); got != testCase.want {
				t.Errorf("describeAudience() = %q, want %q", got, testCase.want)
			}
		})
	}
}

// TestRunRevokeAcceptsNoContent pins the CLI against a success the API states
// by saying nothing. DELETE /v1/nodes/{id} answers 204 with an empty body, and
// decoding that as JSON reported "decode response JSON: unexpected end of JSON
// input" with exit 1 for a revocation that had already succeeded. An owner
// reading that would believe a peer still has access it no longer has.
func TestRunRevokeAcceptsNoContent(t *testing.T) {
	const nodeID = "node_0123456789abcdef0123"
	var called bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/nodes/"+nodeID {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	exitCode := Run(context.Background(), []string{"--url", server.URL, "revoke", nodeID}, &stdout, &stderr)
	if exitCode != 0 {
		t.Fatalf("Run() exit = %d, stderr = %q", exitCode, stderr.String())
	}
	if !called {
		t.Fatal("the revoke endpoint was never called")
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q; a no-content success should print nothing", stdout.String())
	}
}

// TestRunStillReportsAnUndecodableBody keeps the fix narrow: only an empty body
// is a silent success. A 2xx carrying bytes that are not JSON is still a
// broken response and must not be reported as success.
func TestRunStillReportsAnUndecodableBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("this is not JSON"))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	exitCode := Run(context.Background(), []string{"--url", server.URL, "nodes"}, &stdout, &stderr)
	if exitCode == 0 {
		t.Fatalf("Run() exit = 0 for a body that is not JSON; stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "decode response JSON") {
		t.Errorf("stderr = %q; want the decode failure reported", stderr.String())
	}
}

// The flags are what an owner uses to open each gate, so each must reach the
// node as the field it names — and the ones not passed must stay closed.
func TestAudienceFlagsReachTheNode(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want map[string]bool
	}{
		{"no flags", nil, map[string]bool{"exportCwd": false, "acceptMessages": false, "allowOutbound": false}},
		{"outbound only", []string{"--outbound"}, map[string]bool{"exportCwd": false, "acceptMessages": false, "allowOutbound": true}},
		{"all three", []string{"--cwd", "--messages", "--outbound"}, map[string]bool{"exportCwd": true, "acceptMessages": true, "allowOutbound": true}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var body map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewDecoder(r.Body).Decode(&body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"claude:abc"}`))
			}))
			defer server.Close()

			args := append([]string{"--url", server.URL, "audience", "claude:abc", "none"}, testCase.args...)
			var stdout, stderr bytes.Buffer
			if code := Run(context.Background(), args, &stdout, &stderr); code != 0 {
				t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
			}
			for field, want := range testCase.want {
				got, present := body[field]
				if !present {
					t.Errorf("%s is absent from the request body: %v", field, body)
					continue
				}
				if got != want {
					t.Errorf("%s = %v, want %v", field, got, want)
				}
			}
		})
	}
}

// An unknown flag must be refused rather than treated as a node id, or a typo
// would silently grant a session to a node called "--outbund".
func TestAnUnknownAudienceFlagIsRefused(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(),
		[]string{"--url", "http://127.0.0.1:1", "audience", "claude:abc", "none", "--outbund"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("a misspelled flag was accepted")
	}
	if !strings.Contains(stderr.String(), "--outbound") {
		t.Errorf("the error does not list the real flag: %q", stderr.String())
	}
}

// A message to another node must be attributed to a local session, because the
// node's outbound gate is per session. --from carries that, and its absence must
// produce a request the node refuses rather than one that quietly omits it.
func TestSendCarriesFromToTheNode(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_x","state":"pending"}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(),
		[]string{"--url", server.URL, "send", "--from", "claude:mine", "node_peer0000000000000/codex:x", "hello", "there"},
		&stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if body["from"] != "claude:mine" {
		t.Errorf("from = %v, want claude:mine", body["from"])
	}
	if body["to"] != "node_peer0000000000000/codex:x" || body["body"] != "hello there" {
		t.Errorf("to/body = %v / %v", body["to"], body["body"])
	}

	// The flag is positional-agnostic, the way audience's flags are: after the
	// destination, after the message, or in `--from=` form.
	for name, args := range map[string][]string{
		"after the destination": {"send", "node_peer0000000000000/codex:x", "--from", "claude:mine", "hello", "there"},
		"after the message":     {"send", "node_peer0000000000000/codex:x", "hello", "there", "--from", "claude:mine"},
		"equals form":           {"send", "--from=claude:mine", "node_peer0000000000000/codex:x", "hello", "there"},
	} {
		body = nil
		code = Run(context.Background(), append([]string{"--url", server.URL}, args...), &stdout, &stderr)
		if code != 0 {
			t.Fatalf("%s: exit = %d, stderr = %q", name, code, stderr.String())
		}
		if body["from"] != "claude:mine" || body["to"] != "node_peer0000000000000/codex:x" || body["body"] != "hello there" {
			t.Errorf("%s: from/to/body = %v / %v / %v", name, body["from"], body["to"], body["body"])
		}
	}
	// After `--` everything is text, so a message may talk about --from
	// without losing words or changing its sender.
	body = nil
	code = Run(context.Background(), []string{"--url", server.URL, "send", "--from", "claude:mine",
		"node_peer0000000000000/codex:x", "--", "please", "pass", "--from", "claude:y", "to", "the", "script"},
		&stdout, &stderr)
	if code != 0 {
		t.Fatalf("terminator: exit = %d, stderr = %q", code, stderr.String())
	}
	if body["from"] != "claude:mine" || body["body"] != "please pass --from claude:y to the script" {
		t.Errorf("terminator: from/body = %v / %v", body["from"], body["body"])
	}

	// A `--` inside the message is prose, not the terminator: it must arrive.
	body = nil
	code = Run(context.Background(), []string{"--url", server.URL, "send", "claude:local", "fixed", "--", "see", "commit"},
		&stdout, &stderr)
	if code != 0 {
		t.Fatalf("dash in prose: exit = %d, stderr = %q", code, stderr.String())
	}
	if body["body"] != "fixed -- see commit" {
		t.Errorf("dash in prose: body = %v, want the dash kept", body["body"])
	}

	// A --from with nothing behind it is an error, not a message and not a
	// silent absence the node then asks the user to fix.
	for name, args := range map[string][]string{
		"dangling": {"send", "claude:local", "hi", "--from"},
		"empty":    {"send", "--from=", "node_peer0000000000000/codex:x", "hi"},
		"a flag":   {"send", "--from", "--from", "x", "claude:local", "hi"},
	} {
		stderr.Reset()
		if code = Run(context.Background(), append([]string{"--url", server.URL}, args...),
			&stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "--from needs a value") {
			t.Errorf("%s --from: exit = %d, stderr = %q", name, code, stderr.String())
		}
	}
	// A --from after a dash in prose is ambiguous: refused, not taken out of
	// the sentence with the sender silently changed.
	stderr.Reset()
	if code = Run(context.Background(), []string{"--url", server.URL, "send", "claude:local", "hey", "--", "use", "--from", "claude:mine"},
		&stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "ambiguous") {
		t.Errorf("--from after a prose dash: exit = %d, stderr = %q", code, stderr.String())
	}
	stderr.Reset()
	if code = Run(context.Background(), []string{"--url", server.URL, "send", "--from", "claude:a", "--from", "claude:b", "claude:local", "hi"},
		&stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "twice") {
		t.Errorf("repeated --from: exit = %d, stderr = %q", code, stderr.String())
	}

	// Without --from the field is simply absent — the node decides.
	body = nil
	code = Run(context.Background(),
		[]string{"--url", server.URL, "send", "claude:local", "hi"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if _, present := body["from"]; present {
		t.Errorf("from was sent without --from: %v", body["from"])
	}
}

// The owner's own view of an inbox must survive what a peer can put in it. One
// request for fifty heavy messages passed the read cap and decoded as nothing —
// on the very command the tool's error told the owner to run. Read in pages.
func TestInboxReadsInPagesAndPrintsTheWhole(t *testing.T) {
	// An inbox that ends inside the first page is the common case, and an empty
	// one is what a reader sees most often. Both must be a plain success: the
	// loop stops early on purpose there, and the report of stopping early must
	// not fire for it.
	for _, total := range []int{0, 5, 23} {
		t.Run(fmt.Sprint(total, " messages"), func(t *testing.T) { inboxPages(t, total) })
	}
}

func inboxPages(t *testing.T, total int) {
	t.Helper()
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("limit") != "10" {
			t.Errorf("limit = %q, want pages of 10", r.URL.Query().Get("limit"))
		}
		start := 0
		_, _ = fmt.Sscanf(r.URL.Query().Get("after"), "%d", &start)
		end := start + 10
		if end > total {
			end = total
		}
		messages := make([]map[string]any, 0, end-start)
		for i := start; i < end; i++ {
			messages = append(messages, map[string]any{"id": fmt.Sprintf("msg_%02d", i), "body": strings.Repeat("<", 32768)})
		}
		page := map[string]any{"messages": messages, "held": total, "capacity": 500, "full": false}
		if end < total {
			page["next"] = fmt.Sprint(end)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(page)
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--url", server.URL, "inbox", "codex:mine"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("a complete inbox wrote to stderr: %q", stderr.String())
	}
	var printed struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
		Held int     `json:"held"`
		Next *string `json:"next"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &printed); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	wantRequests := total/10 + 1
	if len(printed.Messages) != total || printed.Held != total || requests != wantRequests || printed.Next != nil {
		t.Errorf("printed %d messages (held %d) over %d requests, next=%v; want %d over %d with no next",
			len(printed.Messages), printed.Held, requests, printed.Next, total, wantRequests)
	}
}

// A cut-off answer says so. "unexpected end of JSON input" sends the owner nowhere.
func TestACutOffAnswerIsExplained(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[{"body":"`))
		_, _ = w.Write([]byte(strings.Repeat("a", 5<<20)))
		_, _ = w.Write([]byte(`"}]}`))
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"--url", server.URL, "inbox", "codex:mine"}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "cut off") {
		t.Errorf("exit = %d, stderr = %q; want a failure that says the answer was cut off", code, stderr.String())
	}
}

// A node that answers with a cursor that never advances must not spin here.
// The node is trusted, but a bug or a wrong URL is not a reason to fill a
// terminal until the context dies.
func TestInboxStopsOnACursorThatDoesNotAdvance(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"messages": []map[string]any{{"id": "msg_stuck", "body": "x"}},
			"held":     1, "capacity": 500, "full": false, "next": "stuck",
		})
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"--url", server.URL, "inbox", "codex:mine"}, &stdout, &stderr)
	// Two: the first page, then the one that repeats the cursor and stops.
	if requests != 2 {
		t.Errorf("made %d requests against a node whose cursor never advances; want 2", requests)
	}
	// The repeated page's messages are the ones already held, so they must not
	// be printed twice.
	if got := strings.Count(stdout.String(), "msg_stuck"); got != 1 {
		t.Errorf("the repeated page was printed %d times, want 1: %s", got, stdout.String())
	}
	// A node that answers with the same cursor twice is misbehaving too, and
	// the answer may be short. Both ways of stopping early say so.
	if code == 0 || !strings.Contains(stderr.String(), "stopped after") {
		t.Errorf("exit = %d, stderr = %q; a short answer must not read as a complete one", code, stderr.String())
	}
}

// A node that keeps issuing fresh cursors past what an inbox can hold is
// misbehaving, and the answer is truncated. Saying nothing would make a partial
// inbox look like a complete one — the failure this command was just fixed for,
// in a different costume.
func TestInboxSaysSoWhenItStopsShort(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"messages": []map[string]any{{"id": fmt.Sprintf("msg_%d", requests), "body": "x"}},
			"held":     9999, "capacity": 500, "full": false,
			"next": fmt.Sprint(requests),
		})
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), []string{"--url", server.URL, "inbox", "codex:mine"}, &stdout, &stderr)
	if code == 0 {
		t.Error("a truncated inbox exited 0, so it reads as a complete one")
	}
	for _, want := range []string{"stopped after", "misbehaving"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr does not say %q: %s", want, stderr.String())
		}
	}
	// What was read is still printed, and `next` says where it stopped.
	var printed struct {
		Messages []json.RawMessage `json:"messages"`
		Next     string            `json:"next"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &printed); err != nil {
		t.Fatalf("nothing usable was printed: %v", err)
	}
	if len(printed.Messages) != 60 || printed.Next == "" {
		t.Errorf("printed %d messages, next = %q; want 60 and a cursor", len(printed.Messages), printed.Next)
	}
}
