package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
