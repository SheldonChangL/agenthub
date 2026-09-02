package mcpserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agenthub.local/agenthub/internal/mcpserver"
)

// nodeStub answers the two endpoints binding needs.
func nodeStub(t *testing.T, known map[string]bool) *mcpserver.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/node":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "node_0123456789abcdef0123"})
		case strings.HasPrefix(r.URL.Path, "/v1/sessions/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1/sessions/")
			if known[id] {
				_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
				return
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client, err := mcpserver.NewClient(server.URL)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
}

// A server that guessed its own identity would let any agent on this machine
// read any session's inbox, so refusing is the whole point of the flag.
func TestAnUnboundServerRefusesToStart(t *testing.T) {
	client := nodeStub(t, nil)
	for _, raw := range []string{"", "   ", "\t"} {
		if _, err := mcpserver.Bind(context.Background(), client, raw); !errors.Is(err, mcpserver.ErrNoBinding) {
			t.Errorf("Bind(%q) = %v, want ErrNoBinding", raw, err)
		}
	}
}

// A well-formed id for a session that is not there would otherwise produce a
// server that answers every call with an empty result, which reads as "no
// messages" rather than "you pointed this at nothing".
func TestAnUnknownSessionIsRefused(t *testing.T) {
	client := nodeStub(t, map[string]bool{"codex:real": true})
	_, err := mcpserver.Bind(context.Background(), client, "codex:missing")
	if !errors.Is(err, mcpserver.ErrSessionNotFound) {
		t.Fatalf("Bind(unknown) = %v, want ErrSessionNotFound", err)
	}
	if _, err := mcpserver.Bind(context.Background(), client, "codex:real"); err != nil {
		t.Fatalf("Bind(known) = %v, want success", err)
	}
}

// This server acts for a session on the node it connects to. A node-id prefix
// is wrong even when it names that same node: the inbox it would read lives
// here, and reading another node's inbox is not something this process can do.
func TestAQualifiedAddressIsRefused(t *testing.T) {
	client := nodeStub(t, map[string]bool{"codex:real": true})
	for _, raw := range []string{
		"node_0123456789abcdef0123/codex:real",
		"node_ffffffffffffffffffff/codex:real",
	} {
		_, err := mcpserver.Bind(context.Background(), client, raw)
		if err == nil {
			t.Errorf("Bind(%q) succeeded; a qualified address must be refused", raw)
			continue
		}
		if !strings.Contains(err.Error(), "another node") {
			t.Errorf("Bind(%q) = %v; the error should say why a node prefix is wrong", raw, err)
		}
	}
}

func TestTheBoundSessionIsTheOneAsked(t *testing.T) {
	client := nodeStub(t, map[string]bool{"claude:abc": true})
	binding, err := mcpserver.Bind(context.Background(), client, "  claude:abc  ")
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if binding.SessionID() != "claude:abc" {
		t.Fatalf("bound to %q, want claude:abc", binding.SessionID())
	}
}

// A Binding that never went through Bind must not produce a running server.
// Without this, mcpserver.New(client, mcpserver.Binding{}, id) would serve a
// session named "" and every later tool would trust it.
func TestAnUnvalidatedBindingCannotBuildAServer(t *testing.T) {
	client := nodeStub(t, map[string]bool{"codex:demo": true})
	if _, err := mcpserver.New(client, mcpserver.Binding{}, "node_0123456789abcdef0123"); !errors.Is(err, mcpserver.ErrUnboundServer) {
		t.Fatalf("New(zero binding) = %v, want ErrUnboundServer", err)
	}
	binding, err := mcpserver.Bind(context.Background(), client, "codex:demo")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if _, err := mcpserver.New(client, binding, "node_0123456789abcdef0123"); err != nil {
		t.Fatalf("New(valid binding) = %v, want success", err)
	}
}
