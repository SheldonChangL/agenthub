package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"agenthub.local/agenthub/internal/api"
	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/nodeconfig"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
)

type listTestSigner struct{}

func (listTestSigner) Sign([]byte) []byte { return make([]byte, ed25519.SignatureSize) }

// realNode is the owner API of a node, over its real database, holding the
// two-address list an owner with a cable and Wi-Fi saved.
func realNode(t *testing.T) (*registry.Registry, *httptest.Server) {
	t.Helper()
	ctx := context.Background()
	store, err := registry.Open(ctx, filepath.Join(t.TempDir(), "agenthub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	both := []string{"192.168.1.10:7463", "10.0.0.5:7463"}
	on := true
	if err := store.SaveNodeSettings(ctx, nodeconfig.Partial{PeerListens: &both, AllowLAN: &on}); err != nil {
		t.Fatal(err)
	}
	node := model.NodeIdentity{ID: "node_1234567890123456", DisplayName: "test", Platform: "test"}
	handler := api.NewServer(store, nil, protocol.NewHeartbeatBuilder(store, node, listTestSigner{}), node,
		api.WithNodeSettings(nodeconfig.Settings{PeerListen: both[0], PeerListens: both, AllowLAN: true},
			map[string]string{})).Handler()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return store, server
}

// `ah settings set --peer-listen X` is the command every how-to in this repo
// gives, and the one an owner types to take a node off one network. Against a
// node that serves a list it has to leave exactly [X] — not X in front of the
// addresses it was serving.
func TestSettingsSetOnePeerListenReplacesTheWholeList(t *testing.T) {
	store, server := realNode(t)
	var stdout, stderr bytes.Buffer
	// The list's own first entry: the write that only the "a scalar write
	// replaces the list" rule closes — the stored scalar would still match.
	if code := Run(context.Background(), []string{"--url", server.URL, "settings", "set",
		"--peer-listen", "192.168.1.10:7463"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	stored, err := store.GetNodeSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stored.PeerListens == nil || !slices.Equal(*stored.PeerListens, []string{"192.168.1.10:7463"}) {
		t.Fatalf("the node stored %v; one --peer-listen has to be the whole list", stored.PeerListens)
	}
}

func TestSettingsSetSeveralPeerListensSendsTheList(t *testing.T) {
	store, server := realNode(t)
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--url", server.URL, "settings", "set",
		"--peer-listen", "10.0.0.5:7463", "--peer-listen", "192.168.1.10:7463"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	stored, err := store.GetNodeSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*stored.PeerListens, []string{"10.0.0.5:7463", "192.168.1.10:7463"}) {
		t.Fatalf("stored %v", *stored.PeerListens)
	}
	// And the node's refusals come back as the node's words.
	stdout.Reset()
	stderr.Reset()
	if code := Run(context.Background(), []string{"--url", server.URL, "settings", "set",
		"--peer-listen", "192.168.1.10:7463", "--peer-listen", "0.0.0.0:7463"}, &stdout, &stderr); code == 0 {
		t.Fatalf("the unspecified address was accepted: %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "every interface") {
		t.Errorf("refusal = %s", stderr.String())
	}
}

// One address is sent the old way, so it works on every node; several are
// sent as the list, and a node that predates the list is named as too old
// rather than answered with a JSON error.
func TestSettingsSetTellsAnOlderNodeApartFromARefusal(t *testing.T) {
	var received []map[string]any
	older := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		received = append(received, body)
		w.Header().Set("Content-Type", "application/json")
		if _, several := body["peerListens"]; several {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":"INVALID_REQUEST","message":"request body is not valid JSON for this endpoint"}}`)
			return
		}
		_, _ = io.WriteString(w, settingsAnswer)
	}))
	defer older.Close()

	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--url", older.URL, "settings", "set",
		"--peer-listen", "10.0.0.5:7463"}, &stdout, &stderr); code != 0 {
		t.Fatalf("one address against an older node: exit %d, %s", code, stderr.String())
	}
	if len(received[0]) != 1 || received[0]["peerListen"] != "10.0.0.5:7463" {
		t.Fatalf("one address was sent as %v; an older node understands only peerListen", received[0])
	}
	stderr.Reset()
	if code := Run(context.Background(), []string{"--url", older.URL, "settings", "set",
		"--peer-listen", "10.0.0.5:7463", "--peer-listen", "192.168.1.10:7463"}, &stdout, &stderr); code == 0 {
		t.Fatal("several addresses against an older node succeeded")
	}
	if !strings.Contains(stderr.String(), "too old for more than one address") {
		t.Errorf("stderr = %s", stderr.String())
	}
}

// Which configured addresses are bound is on screen, and one that is not says
// why, without calling the node degraded.
func TestSettingsShowsEachPeerAddress(t *testing.T) {
	answer := `{
  "settings": {"peerListen":"192.168.1.10:7463","peerListens":["192.168.1.10:7463","10.0.0.5:7463"],"allowLan":true,"discover":false,"treatAsPrivate":[],"autoWake":false},
  "sources": {"peerListen":"remembered","allowLan":"remembered","discover":"default","treatAsPrivate":"default","autoWake":"default"},
  "saved": {"peerListen":"192.168.1.10:7463","peerListens":["192.168.1.10:7463","10.0.0.5:7463"],"allowLan":true,"discover":false,"treatAsPrivate":[],"autoWake":false},
  "restartRequired": false,
  "peerListeners": [
    {"address":"192.168.1.10:7463","state":"bound"},
    {"address":"10.0.0.5:7463","state":"failed","reason":"address_gone","detail":"bind: can't assign requested address","message":"no interface on this machine holds 10.0.0.5:7463 any more"}
  ]
}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, answer)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"--url", server.URL, "settings"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit = %d, %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"peer listener 10.0.0.5:7463 is failed: no interface on this machine holds 10.0.0.5:7463",
		"remembered (1 of 2 bound)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "degraded") || strings.Contains(out, "(not bound)") {
		t.Errorf("one address of two bound was presented as a degraded node:\n%s", out)
	}
	// IN EFFECT is the bound address alone, and NEXT START is empty: the saved
	// list is the configured one, restartRequired is false, and a column that
	// printed the list there would ask for a restart that brings back nothing.
	if got := peerListenRow(t, out); !slices.Equal(got, []string{
		"peer-listen", "192.168.1.10:7463", "remembered", "(1", "of", "2", "bound)",
	}) {
		t.Errorf("peer-listen row = %q:\n%s", got, out)
	}
	if strings.Contains(out, "ah service restart") {
		t.Errorf("a node with nothing to restart for was told to restart:\n%s", out)
	}
}

// peerListenRow is the peer-listen row of `ah settings`, split on spaces.
func peerListenRow(t *testing.T, out string) []string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "peer-listen ") {
			return strings.Fields(line)
		}
	}
	t.Fatalf("no peer-listen row:\n%s", out)
	return nil
}

// A node on this branch that bound nothing retries its addresses, so the
// sentence under the problem says the address will be served when it binds,
// not that the owner has to restart once the network is back. An older node,
// which reports no listeners, binds once, and keeps the older sentence.
func TestSettingsSaysADegradedNodeRetries(t *testing.T) {
	const problem = `"peerListenProblem": {
    "address": "192.168.77.9:7463", "reason": "address_gone",
    "detail": "listen tcp 192.168.77.9:7463: bind: can't assign requested address",
    "runningOn": "127.0.0.1:7463",
    "message": "no interface on this machine holds 192.168.77.9:7463 any more"
  }`
	current := `{
  "settings": {"peerListen":"127.0.0.1:7463","peerListens":["192.168.77.9:7463"],"allowLan":true,"discover":false,"treatAsPrivate":[],"autoWake":false},
  "sources": {"peerListen":"default","allowLan":"remembered","discover":"default","treatAsPrivate":"default","autoWake":"default"},
  "saved": {"peerListen":"192.168.77.9:7463","peerListens":["192.168.77.9:7463"],"allowLan":true,"discover":false,"treatAsPrivate":[],"autoWake":false},
  "restartRequired": false,
  ` + problem + `,
  "peerListeners": [{"address":"192.168.77.9:7463","state":"failed","reason":"address_gone","message":"no interface on this machine holds 192.168.77.9:7463 any more"}]
}`
	older := `{
  "settings": {"peerListen":"127.0.0.1:7463","allowLan":true,"discover":false,"treatAsPrivate":[],"autoWake":false},
  "sources": {"peerListen":"default","allowLan":"remembered","discover":"default","treatAsPrivate":"default","autoWake":"default"},
  "saved": {"peerListen":"192.168.77.9:7463","allowLan":true,"discover":false,"treatAsPrivate":[],"autoWake":false},
  "restartRequired": true,
  ` + problem + `
}`
	for _, testCase := range []struct {
		name, answer, want, refused string
		row                         []string
	}{
		{"a node that retries", current, "tries the configured addresses again every 30s", "restart once the network is back",
			[]string{"peer-listen", "127.0.0.1:7463", "default", "(not", "bound)"}},
		{"an older node", older, "restart once the network is back", "tries the configured addresses again",
			[]string{"peer-listen", "127.0.0.1:7463", "default", "(not", "bound)", "192.168.77.9:7463"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, testCase.answer)
			}))
			defer server.Close()
			var stdout, stderr bytes.Buffer
			if code := Run(context.Background(), []string{"--url", server.URL, "settings"}, &stdout, &stderr); code != 0 {
				t.Fatalf("exit = %d, %s", code, stderr.String())
			}
			out := stdout.String()
			if !strings.Contains(out, testCase.want) {
				t.Errorf("output lacks %q:\n%s", testCase.want, out)
			}
			if strings.Contains(out, testCase.refused) {
				t.Errorf("output says %q:\n%s", testCase.refused, out)
			}
			if !strings.Contains(out, "no interface on this machine holds 192.168.77.9:7463") {
				t.Errorf("the configured address is named nowhere:\n%s", out)
			}
			if got := peerListenRow(t, out); !slices.Equal(got, testCase.row) {
				t.Errorf("peer-listen row = %q, want %q:\n%s", got, testCase.row, out)
			}
		})
	}
}
