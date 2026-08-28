package cli

import (
	"bytes"
	"context"
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
