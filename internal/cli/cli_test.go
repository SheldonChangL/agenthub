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

// The owner's own view of an inbox must survive what a peer can put in it. One
// request for fifty heavy messages passed the read cap and decoded as nothing —
// on the very command the tool's error told the owner to run. Read in pages.
func TestInboxReadsInPagesAndPrintsTheWhole(t *testing.T) {
	const total = 23
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
	if len(printed.Messages) != total || printed.Held != total || requests != 3 || printed.Next != nil {
		t.Errorf("printed %d messages (held %d) over %d requests, next=%v; want %d over 3 with no next",
			len(printed.Messages), printed.Held, requests, printed.Next, total)
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
