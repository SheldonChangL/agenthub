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

// The whole record comes back from a write, not just the fields that were sent.
//
// Turning allowLan off pulls peerListen back to loopback in the same write, and
// after #134 any write does it when the stored allowLan is already false. A
// caller that merged only what it sent would leave a LAN address on screen that
// the node no longer has, which is the one thing this endpoint's own message
// exists to prevent.
func TestSaveNodeSettingsCarriesTheWholeRecordBack(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/v1/node/settings" {
			t.Errorf("asked %s %s, want PUT /v1/node/settings", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &received)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"settings":{"peerListen":"127.0.0.1:7463","allowLan":false,"discover":true,
				"treatAsPrivate":["10.0.0.0/8"],"autoWake":false},
			"sources":{"peerListen":"default","allowLan":"remembered","discover":"remembered",
				"treatAsPrivate":"remembered","autoWake":"default"},
			"saved":{"peerListen":"127.0.0.1:7463","allowLan":false,"discover":true,
				"treatAsPrivate":["10.0.0.0/8"],"autoWake":false},
			"restartRequired":true,
			"peerListenWithdrawn":true,
			"message":"allowLan is off, so peerListen was pulled back to 127.0.0.1:7463"
		}`))
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	off := false
	view := app.SaveNodeSettings(NodeSettingsPatch{AllowLAN: &off})
	if view.Error != "" {
		t.Fatalf("SaveNodeSettings: %s", view.Error)
	}
	// Only the field the owner changed was sent.
	if len(received) != 1 {
		t.Errorf("sent %v, want only allowLan", received)
	}
	if got, ok := received["allowLan"].(bool); !ok || got {
		t.Errorf("sent allowLan=%v, want false", received["allowLan"])
	}
	// And a field nobody sent came back changed.
	if view.Settings.PeerListen != "127.0.0.1:7463" {
		t.Errorf("peerListen = %q, want the loopback the node pulled it back to", view.Settings.PeerListen)
	}
	if !view.RestartRequired {
		t.Error("restartRequired is false; settings only take effect at start-up")
	}
	if !view.PeerListenWithdrawn {
		t.Error("peerListenWithdrawn was dropped, so the form cannot tell the owner to re-enter the address")
	}
	if view.Message == "" {
		t.Error("the node's sentence about what the write did was dropped")
	}
	if view.Sources["peerListen"] != "default" {
		t.Errorf("sources.peerListen = %q, want default", view.Sources["peerListen"])
	}
}

// An absent peerListenWithdrawn means no withdrawal stands. The field is
// omitempty at the node, so "absent" and "false" are the same bytes; what must
// not happen is the view inventing a true.
func TestNodeSettingsWithoutAWithdrawalSaysSo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"settings":{"peerListen":"192.168.1.20:7463","allowLan":true},
			"sources":{"peerListen":"remembered"},"saved":{"peerListen":"192.168.1.20:7463"},
			"restartRequired":false}`))
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	view := app.NodeSettings()
	if view.Error != "" {
		t.Fatalf("NodeSettings: %s", view.Error)
	}
	if view.PeerListenWithdrawn {
		t.Error("an absent peerListenWithdrawn was read as a standing withdrawal")
	}
	if view.Settings.TreatAsPrivate == nil || view.Saved.TreatAsPrivate == nil {
		t.Error("treatAsPrivate came across nil; the form iterates it without checking")
	}
}

// A refusal keeps the node's own words. The 400 names the address that was sent
// and what to send with it, which is the whole value of showing it at all.
func TestNodeSettingsRefusalKeepsTheNodesOwnWords(t *testing.T) {
	const refusal = "allowLan is off, so peerListen has to be a loopback address, " +
		"and it was sent as 192.168.1.20:7463; to serve that address, send allowLan true in the same write"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"INVALID_REQUEST","message":` +
			quoteJSON(refusal) + `}}`))
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	address := "192.168.1.20:7463"
	view := app.SaveNodeSettings(NodeSettingsPatch{PeerListen: &address})
	if view.Error == "" {
		t.Fatal("a refused write reported no error")
	}
	if !strings.Contains(view.Error, "to serve that address, send allowLan true in the same write") {
		t.Errorf("error = %q, want the node's own sentence", view.Error)
	}
	if len(view.Settings.TreatAsPrivate) != 0 || view.Settings.PeerListen != "" {
		t.Error("a refused write returned values as though they had been saved")
	}
}

// A node that cannot reach its settings store answers 409; the view carries it
// rather than throwing, like every other read here.
func TestNodeSettingsUnavailableIsCarriedNotThrown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{"error":{"code":"SETTINGS_UNAVAILABLE","message":"settings are not available"}}`))
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	view := app.NodeSettings()
	if view.Error == "" {
		t.Fatal("a 409 reported no error")
	}
	if view.Sources == nil {
		t.Error("sources came across nil on a failed read")
	}
}

// quoteJSON renders a string as a JSON string literal, so a refusal with
// punctuation in it can be embedded in the fixture above.
func quoteJSON(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// An empty treatAsPrivate has to reach the node as [], not as an absent key.
//
// The two mean opposite things: [] withdraws every declared range, an absent
// key leaves them alone. The field is a *[]string with omitempty, and omitempty
// on a pointer asks whether the pointer is nil — not whether the slice behind
// it is empty — so a non-nil pointer to an empty slice is encoded. That is the
// behaviour a withdrawal depends on, and it is one struct tag away from
// silently becoming "leave it alone".
func TestWithdrawnRangesReachTheNodeAsAnEmptyArray(t *testing.T) {
	var body map[string]json.RawMessage
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"settings":{},"sources":{},"saved":{},"restartRequired":true}`))
	}))
	defer server.Close()

	app := &App{client: newClient(server.URL), url: server.URL, ctx: context.Background()}
	empty := []string{}
	if view := app.SaveNodeSettings(NodeSettingsPatch{TreatAsPrivate: &empty}); view.Error != "" {
		t.Fatalf("SaveNodeSettings: %s", view.Error)
	}
	raw, ok := body["treatAsPrivate"]
	if !ok {
		t.Fatal("treatAsPrivate was omitted, which tells the node to leave the ranges alone")
	}
	if string(raw) != "[]" {
		t.Errorf("treatAsPrivate = %s, want []", raw)
	}

	// And a patch that does not mention them leaves the key out entirely.
	body = nil
	on := true
	if view := app.SaveNodeSettings(NodeSettingsPatch{AllowLAN: &on}); view.Error != "" {
		t.Fatalf("SaveNodeSettings: %s", view.Error)
	}
	if _, present := body["treatAsPrivate"]; present {
		t.Error("a patch that never mentioned the ranges still sent them")
	}
}
