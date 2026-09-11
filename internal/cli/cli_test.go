package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
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
		{"no flags", nil, map[string]bool{
			"exportCwd": false, "acceptMessages": false, "allowOutbound": false, "autoWake": false}},
		{"outbound only", []string{"--outbound"}, map[string]bool{
			"exportCwd": false, "acceptMessages": false, "allowOutbound": true, "autoWake": false}},
		// Separately from the others, because it is a different decision:
		// willing to receive is not willing to be woken.
		{"auto-wake only", []string{"--auto-wake"}, map[string]bool{
			"exportCwd": false, "acceptMessages": false, "allowOutbound": false, "autoWake": true}},
		{"all four", []string{"--cwd", "--messages", "--outbound", "--auto-wake"}, map[string]bool{
			"exportCwd": true, "acceptMessages": true, "allowOutbound": true, "autoWake": true}},
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

// The first question asked of a binary that is misbehaving is which build it
// is, so the answer must not need a node, a valid URL, or anything else that
// might be the thing that is broken.
func TestVersionAnswersWithoutANode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	// A URL that resolves to nothing, and no node running anywhere.
	code := Run(context.Background(), []string{"--url", "http://127.0.0.1:1", "--version"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("--version wrote to stderr: %q", stderr.String())
	}
	printed := stdout.String()
	for _, want := range []string{"ah ", "go1."} {
		if !strings.Contains(printed, want) {
			t.Errorf("--version = %q, missing %q", printed, want)
		}
	}
	// Whether a revision is recorded depends on how the binary was built — a
	// test binary carries none — so what is asserted here is that the answer
	// says which of the two it is rather than leaving a reader guessing. CI
	// checks the real `go build` output against the commit it built.
	if !strings.Contains(printed, "unreleased") && !strings.Contains(printed, "revision") {
		t.Errorf("--version = %q; it names neither a release nor a revision", printed)
	}
}

// A malformed URL must not stop it answering, for the same reason.
func TestVersionAnswersEvenWithAnUnusableURL(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--url", "not a url at all", "--version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "ah ") {
		t.Errorf("--version = %q", stdout.String())
	}
}

// `ah pairing` is how an owner turns local-network advertising on and off, so
// each spelling has to reach the right method: reading must not open a window,
// and "off" must not be sent as a request to open one.
func TestRunPairingMapsEachSpellingToAMethod(t *testing.T) {
	for name, testCase := range map[string]struct {
		args       []string
		wantMethod string
		wantBody   map[string]int
	}{
		"read":            {[]string{"pairing"}, http.MethodGet, nil},
		"open by default": {[]string{"pairing", "on"}, http.MethodPost, map[string]int{}},
		"open for a while": {[]string{"pairing", "on", "90"}, http.MethodPost,
			map[string]int{"seconds": 90}},
		"close": {[]string{"pairing", "off"}, http.MethodDelete, nil},
	} {
		t.Run(name, func(t *testing.T) {
			var gotMethod string
			var gotBody map[string]int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/pairing" {
					t.Errorf("path = %s, want /v1/pairing", r.URL.Path)
				}
				gotMethod = r.Method
				if r.Method == http.MethodPost {
					if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
						t.Errorf("decode body: %v", err)
					}
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"open":false,"announcing":{"announceableAddresses":0}}`))
			}))
			defer server.Close()

			var stdout, stderr bytes.Buffer
			args := append([]string{"--url", server.URL}, testCase.args...)
			if exit := Run(context.Background(), args, &stdout, &stderr); exit != 0 {
				t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
			}
			if gotMethod != testCase.wantMethod {
				t.Errorf("method = %s, want %s", gotMethod, testCase.wantMethod)
			}
			if testCase.wantBody == nil {
				return
			}
			if len(gotBody) != len(testCase.wantBody) {
				t.Fatalf("body = %v, want %v", gotBody, testCase.wantBody)
			}
			for key, want := range testCase.wantBody {
				if gotBody[key] != want {
					t.Errorf("body[%q] = %d, want %d", key, gotBody[key], want)
				}
			}
		})
	}
}

// A duration the CLI cannot use must not become a request. Zero is the one that
// matters: the API reads it as "no preference" and opens its default window, so
// forwarding it would give five minutes to someone who asked for none.
func TestRunPairingRefusesADurationItCannotSend(t *testing.T) {
	for name, testCase := range map[string]struct {
		args      []string
		wantInErr string
	}{
		"zero":       {[]string{"pairing", "on", "0"}, "0 seconds"},
		"negative":   {[]string{"pairing", "on", "-30"}, "-30 seconds"},
		"not-number": {[]string{"pairing", "on", "5m"}, `"5m"`},
		"too many":   {[]string{"pairing", "on", "60", "90"}, "60 90"},
		"unknown":    {[]string{"pairing", "sometimes"}, `"sometimes"`},
		"candidates": {[]string{"candidates", "all"}, "all"},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				t.Errorf("a refused argument still reached the node: %s %s", r.Method, r.URL.Path)
			}))
			defer server.Close()

			var stdout, stderr bytes.Buffer
			args := append([]string{"--url", server.URL}, testCase.args...)
			if exit := Run(context.Background(), args, &stdout, &stderr); exit == 0 {
				t.Fatalf("exit = 0 on %v; stdout = %s", testCase.args, stdout.String())
			}
			// The value has to appear, because it came from a shell and that is
			// usually where the mistake is visible.
			if !strings.Contains(stderr.String(), testCase.wantInErr) {
				t.Errorf("stderr = %q, want it to name %s", stderr.String(), testCase.wantInErr)
			}
			if !strings.Contains(stderr.String(), "usage:") {
				t.Errorf("stderr = %q, want a usage line", stderr.String())
			}
		})
	}
}

// `ah peers` answers "who can I send to, and what did they publish".
//
// It exists because that answer had no command. A remote session never appears
// in `ah list`, which is owner-local; it appears only in the presence endpoint,
// addressed as <node-id>/<session-id>. Without this the only way to find the id
// was to read that endpoint with curl — and `ah send` to a bare session id
// answers "session not found", which is true and unhelpful. It cost time in the
// two-host run before it existed.
func TestRunPeersShowsTheAddressToSendTo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/peers" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"peers":[
			{"nodeId":"node_aaaa","displayName":"the other desk","online":true,
			 "receivedAt":"2026-09-08T04:00:00Z",
			 "sessions":[{"id":"node_aaaa/codex:abc","provider":"codex","status":"inactive"}]}
		]}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if exit := Run(context.Background(), []string{"--url", server.URL, "peers"}, &stdout, &stderr); exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
	out := stdout.String()
	// The whole point: the qualified address, not the bare session id.
	if !strings.Contains(out, "node_aaaa/codex:abc") {
		t.Errorf("the address to send to is missing: %q", out)
	}
	// The row as a whole, not substrings that another column already satisfies:
	// asserting "codex" was met by the SEND TO id, so blanking PROVIDER passed,
	// and STATUS was never asserted at all.
	fields := strings.Fields(out[strings.Index(out, "node_aaaa/codex:abc"):])
	want := []string{"node_aaaa/codex:abc", "codex", "inactive", "node_aaaa"}
	for i, field := range want {
		if i >= len(fields) || fields[i] != field {
			t.Errorf("column %d = %q, want %q; whole row: %q",
				i, func() string {
					if i < len(fields) {
						return fields[i]
					}
					return "(missing)"
				}(), field, out)
		}
	}
	// And the display name is present as a quoted label rather than an
	// identifier, because it is not one.
	if !strings.Contains(out, `"the other desk"`) {
		t.Errorf("the display name is not shown as a quoted label: %q", out)
	}
}

// The three states a peer can be in are different facts, and an owner waiting
// for a machine to appear needs to know which one they are looking at. An empty
// row for all three would say the same thing about a peer that has published
// nothing, one this node refused, and one never heard from.
func TestRunPeersDistinguishesSilenceFromRefusalAndFromNeverHeard(t *testing.T) {
	for name, testCase := range map[string]struct {
		peer      string
		wantInOut string
		notInOut  string
	}{
		"published nothing": {
			`{"nodeId":"node_a","displayName":"quiet","online":true,
			  "receivedAt":"2026-09-08T04:00:00Z","sessions":[]}`,
			"nothing published", "refused",
		},
		"this node refused what it sent": {
			`{"nodeId":"node_b","displayName":"refused one","online":true,
			  "receivedAt":"2026-09-08T04:00:00Z","sessions":[],"sessionsWithheld":true}`,
			"refused", "nothing published",
		},
		"never heard from": {
			`{"nodeId":"node_c","displayName":"silent","online":false,"sessions":[]}`,
			"never heard from", "offline since",
		},
		// The fourth state, and the one every sleeping machine is in. The node
		// stops serving what an expired snapshot held, so the empty list is
		// this node withholding stale state — saying the peer published nothing
		// is a claim about the peer that nothing supports. It may have
		// published five sessions two minutes ago.
		"offline with a lapsed snapshot": {
			`{"nodeId":"node_d","displayName":"asleep","online":false,
			  "receivedAt":"2026-09-08T04:00:00Z","sessions":[]}`,
			"offline since", "nothing published",
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"peers":[` + testCase.peer + `]}`))
			}))
			defer server.Close()

			var stdout, stderr bytes.Buffer
			if exit := Run(context.Background(),
				[]string{"--url", server.URL, "peers"}, &stdout, &stderr); exit != 0 {
				t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
			}
			out := stdout.String()
			if !strings.Contains(out, testCase.wantInOut) {
				t.Errorf("output does not say %q: %q", testCase.wantInOut, out)
			}
			if strings.Contains(out, testCase.notInOut) {
				t.Errorf("output says %q, which is a different fact: %q", testCase.notInOut, out)
			}
		})
	}
}

// With nothing paired, the answer is what to do next rather than an empty table.
func TestRunPeersSaysWhatToDoWhenNothingIsPaired(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"peers":[]}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if exit := Run(context.Background(), []string{"--url", server.URL, "peers"}, &stdout, &stderr); exit != 0 {
		t.Fatalf("exit = %d, stderr = %s", exit, stderr.String())
	}
	for _, want := range []string{"ah pair", "ah candidates"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("the empty answer does not point at %q: %q", want, stdout.String())
		}
	}
}

// Every command the switch implements has to appear in the usage summary.
//
// `peers` did not: it was added to the detail lines below the summary but not to
// the enumeration a reader scans first, and nothing noticed. The summary is
// three separate Fprintln calls, so an edit that looks like it covers them can
// miss one.
func TestUsageListsEveryCommandTheSwitchImplements(t *testing.T) {
	source, err := os.ReadFile("cli.go")
	if err != nil {
		t.Fatalf("read cli.go: %v", err)
	}
	body := string(source)
	start := strings.Index(body, "switch args[0] {")
	if start < 0 {
		t.Fatal("the command switch is not where this test expects it")
	}
	end := strings.Index(body[start:], "\n\tdefault:")
	if end < 0 {
		t.Fatal("could not find the end of the command switch")
	}

	// No arguments prints the usage, to stderr.
	var stdout, stderr bytes.Buffer
	Run(context.Background(), nil, &stdout, &stderr)
	usage := stdout.String() + stderr.String()

	// The enumeration only, not the detail lines below it. Checking the whole
	// output let a command satisfy this from its own detail line: removing
	// `peers` from the summary passed, because "ah peers   what paired nodes
	// have published…" was still there.
	from := strings.Index(usage, "commands:")
	if from < 0 {
		t.Fatalf("this test is not reading the usage output: %q", usage)
	}
	summary := usage[from:]
	if end := strings.Index(summary, "\n  ah "); end > 0 {
		summary = summary[:end]
	}
	if strings.Contains(summary, "  ah ") {
		t.Fatalf("the summary was not separated from the detail lines: %q", summary)
	}

	implemented := regexp.MustCompile(`\n\tcase "([a-z-]+)"`).FindAllStringSubmatch(body[start:start+end], -1)
	if len(implemented) == 0 {
		t.Fatal("found no commands in the switch; this test would pass vacuously")
	}
	for _, match := range implemented {
		name := match[1]
		if !strings.Contains(summary, name) {
			t.Errorf("`ah %s` is implemented but absent from the commands summary: %q", name, summary)
		}
	}
	t.Logf("checked %d commands", len(implemented))
}

// `ah nodes address` is the repair for the quietest failure in the system: a
// paired node with no recorded address is skipped without a word, and the
// sender's `ah send` still answers `queued`. Until this subcommand existed the
// only way to fix it was a hand-written `curl -X PUT`.
func TestNodesAddressRecordsTheAddress(t *testing.T) {
	var method, path, contentType string
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path, contentType = r.Method, r.URL.Path, r.Header.Get("Content-Type")
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"nodeId":"node_ubuntu000000000","address":"192.168.1.20:7463"}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(),
		[]string{"--url", server.URL, "nodes", "address", "node_ubuntu000000000", "192.168.1.20:7463"},
		&stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
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
	// The address as typed. Trimming or rewriting it here would mean the CLI
	// and the node disagree about what was recorded.
	if sent.Address != "192.168.1.20:7463" {
		t.Errorf("body address = %q, want 192.168.1.20:7463", sent.Address)
	}
	if !strings.Contains(stdout.String(), "192.168.1.20:7463") {
		t.Errorf("the node's answer was not printed: %q", stdout.String())
	}
}

// A node id is a path segment, so it is escaped rather than pasted in.
func TestNodesAddressEscapesTheNodeID(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(),
		[]string{"--url", server.URL, "nodes", "address", "a/../b", "10.0.0.2:7463"},
		&stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if path != "/v1/nodes/a%2F..%2Fb/address" {
		t.Errorf("path = %s; the node id was not escaped into one segment", path)
	}
}

// The node decides whether an address is one it will deliver to, and its
// refusal is what the owner needs to read — not a message this CLI invented.
func TestNodesAddressPassesTheNodesRefusalThrough(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"INVALID_REQUEST","message":"address must be host:port"}}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	code := Run(context.Background(),
		[]string{"--url", server.URL, "nodes", "address", "node_a00000000000000", "not-an-address"},
		&stdout, &stderr)
	if code == 0 {
		t.Fatalf("exit = 0 for a refused address; stdout = %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "address must be host:port") {
		t.Errorf("the node's reason did not reach the owner: %q", stderr.String())
	}
}

// Wrong arity must fail before any request: half a command must not record
// half an address.
func TestNodesRejectsIncoherentInput(t *testing.T) {
	cases := map[string][]string{
		"address with no arguments": {"nodes", "address"},
		"address with no address":   {"nodes", "address", "node_a00000000000000"},
		"address with a spare word": {"nodes", "address", "node_a00000000000000", "10.0.0.2:7463", "extra"},
		"an unknown subcommand":     {"nodes", "addr", "node_a00000000000000", "10.0.0.2:7463"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			// Unreachable on purpose: these must fail before a request.
			code := Run(context.Background(), append([]string{"--url", "http://127.0.0.1:1"}, args...), &stdout, &stderr)
			if code == 0 {
				t.Errorf("Run(%v) = 0; want a non-zero exit", args)
			}
			if stdout.Len() != 0 {
				t.Errorf("Run(%v) wrote to stdout: %s", args, stdout.String())
			}
			if !strings.Contains(stderr.String(), "ah nodes") {
				t.Errorf("Run(%v) did not name the command in its usage: %q", args, stderr.String())
			}
		})
	}
}

// `ah nodes` with nothing after it still lists, which is what it always did.
func TestNodesStillLists(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"nodes":[{"nodeId":"node_a00000000000000"}]}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--url", server.URL, "nodes"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if path != "/v1/nodes" || !strings.Contains(stdout.String(), "node_a00000000000000") {
		t.Errorf("path = %s, stdout = %q", path, stdout.String())
	}
}

// The usage has to name the subcommand, or it is a command nobody finds.
func TestUsageNamesTheAddressSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	Run(context.Background(), nil, &stdout, &stderr)
	usage := stdout.String() + stderr.String()
	if !strings.Contains(usage, "ah nodes address <node-id> <host:port>") {
		t.Errorf("usage does not describe `ah nodes address`: %q", usage)
	}
}

// TestOutboundWithoutAnIDListsTheQueue covers the command an owner reaches for
// when they no longer have the id — the terminal that printed it is closed, or
// an agent sent the message rather than them.
func TestOutboundWithoutAnIDListsTheQueue(t *testing.T) {
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[{"id":"msg_1","to":"codex:theirs","state":"refused","attempts":2,"lastError":"nowhere to deliver to"}]}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--url", server.URL, "outbound"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if len(asked) != 1 || asked[0] != "GET /v1/outbound" {
		t.Fatalf("requests = %v; want the listing endpoint, not the single lookup", asked)
	}
	// The refusal and its reason are the whole point of looking.
	for _, want := range []string{"msg_1", "refused", "nowhere to deliver to"} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout = %q; want it to contain %q", stdout.String(), want)
		}
	}
}

// TestOutboundWithAnIDStillAsksAboutThatMessage keeps the listing from taking
// over the lookup that already existed.
func TestOutboundWithAnIDStillAsksAboutThatMessage(t *testing.T) {
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_1","state":"pending"}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--url", server.URL, "outbound", "msg_1"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if len(asked) != 1 || asked[0] != "/v1/outbound/msg_1" {
		t.Fatalf("requests = %v", asked)
	}

	// And a second argument is still refused, before anything is sent.
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(),
		[]string{"--url", "http://127.0.0.1:1", "outbound", "msg_1", "msg_2"}, &stdout, &stderr); code == 0 {
		t.Fatal("two message ids were accepted")
	}
}

// `/agenthub-watch` deletes a message every tick, and until this existed the
// skill told the agent to run `curl -X DELETE`: the CLI could already do it,
// under a name nobody looks for. The path is the thing to hold — a wrong one
// deletes somebody else's message or nothing at all.
func TestInboxDeleteDropsOneMessage(t *testing.T) {
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.EscapedPath()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(),
		[]string{"--url", server.URL, "inbox", "delete", "claude:abc", "msg_01"},
		&stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if method != http.MethodDelete || path != "/v1/inbox/claude:abc/msg_01" {
		t.Errorf("request = %s %s, want DELETE /v1/inbox/claude:abc/msg_01", method, path)
	}
}

// Both arguments are path segments. A message id is chosen by whatever wrote
// the message, so it is escaped rather than pasted in.
func TestInboxDeleteEscapesBothSegments(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(),
		[]string{"--url", server.URL, "inbox", "delete", "codex:a/b", "m/../x"},
		&stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if path != "/v1/inbox/codex:a%2Fb/m%2F..%2Fx" {
		t.Errorf("path = %s; a segment was not escaped", path)
	}
}

// Wrong arity, or a second argument that is not a session id, must fail before
// any request: `ah inbox delete claude:abc` with the message id forgotten must
// not become "empty this whole inbox".
func TestInboxDeleteRejectsIncoherentInput(t *testing.T) {
	cases := map[string][]string{
		"no arguments":           {"inbox", "delete"},
		"session but no message": {"inbox", "delete", "claude:abc"},
		"a spare word":           {"inbox", "delete", "claude:abc", "msg_01", "extra"},
		"no provider prefix":     {"inbox", "delete", "abc", "msg_01"},
		"arguments reversed":     {"inbox", "delete", "msg_01", "claude:abc"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			// Unreachable on purpose: these must fail before a request.
			code := Run(context.Background(), append([]string{"--url", "http://127.0.0.1:1"}, args...), &stdout, &stderr)
			if code == 0 {
				t.Errorf("Run(%v) = 0; want a non-zero exit", args)
			}
			if stdout.Len() != 0 {
				t.Errorf("Run(%v) wrote to stdout: %s", args, stdout.String())
			}
			if !strings.Contains(stderr.String(), "ah inbox delete") {
				t.Errorf("Run(%v) did not name the command in its usage: %q", args, stderr.String())
			}
		})
	}
}

// The subcommand must not have eaten the command it was added to: `ah inbox
// <session-id>` still reads, and reads the session it was given.
func TestInboxStillReadsWithASessionID(t *testing.T) {
	var method, path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":[],"count":0}`))
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(),
		[]string{"--url", server.URL, "inbox", "claude:abc"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	if method != http.MethodGet || path != "/v1/inbox/claude:abc" {
		t.Errorf("request = %s %s, want GET /v1/inbox/claude:abc", method, path)
	}
}

// The usage has to name the subcommand, or it is a command nobody finds — the
// failure that made the skill reach for curl in the first place.
func TestUsageNamesInboxDelete(t *testing.T) {
	var stdout, stderr bytes.Buffer
	Run(context.Background(), nil, &stdout, &stderr)
	usage := stdout.String() + stderr.String()
	if !strings.Contains(usage, "ah inbox delete <session-id> <message-id>") {
		t.Errorf("usage does not describe `ah inbox delete`: %q", usage)
	}
}
