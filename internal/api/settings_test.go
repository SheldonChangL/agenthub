package api

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/nodeconfig"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
)

// settingsServer builds a node whose running configuration is known, so the
// difference between "in effect" and "saved" can be asserted.
func settingsServer(t *testing.T, running nodeconfig.Settings, sources map[string]string) (*registry.Registry, http.Handler) {
	t.Helper()
	ctx := context.Background()
	store, err := registry.Open(ctx, filepath.Join(t.TempDir(), "agenthub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	node := model.NodeIdentity{ID: testNodeID, DisplayName: "test", Platform: "test"}
	heartbeats := protocol.NewHeartbeatBuilder(store, node, apiTestSigner{})
	return store, NewServer(store, nil, heartbeats, model.NodeIdentity{ID: testNodeID},
		WithNodeSettings(running, sources)).Handler()
}

func readSettings(t *testing.T, body []byte) settingsResponse {
	t.Helper()
	var view settingsResponse
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return view
}

// The owner surface has to say both what is running and what the next start
// will use, because these settings are tied to listeners built once.
func TestGetNodeSettingsReportsWhatIsRunningAndWhereItCameFrom(t *testing.T) {
	running := nodeconfig.Settings{PeerListen: "192.168.1.10:7463", AllowLAN: true}
	_, handler := settingsServer(t, running, map[string]string{
		nodeconfig.SettingPeerListen: nodeconfig.SourceFlag,
		nodeconfig.SettingAllowLAN:   nodeconfig.SourceRemembered,
	})

	response := perform(t, handler, http.MethodGet, "/v1/node/settings", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	view := readSettings(t, response.Body.Bytes())
	if view.Settings.PeerListen != running.PeerListen || !view.Settings.AllowLAN {
		t.Fatalf("settings = %+v", view.Settings)
	}
	if view.Sources[nodeconfig.SettingAllowLAN] != nodeconfig.SourceRemembered {
		t.Fatalf("sources = %v", view.Sources)
	}
	// Nothing is stored, so the next start would fall back to the defaults —
	// which is a real difference and has to be reported as one.
	if view.Saved.PeerListen != nodeconfig.DefaultPeerListen || view.Saved.AllowLAN {
		t.Fatalf("saved = %+v", view.Saved)
	}
	if !view.RestartRequired {
		t.Fatal("a running configuration that differs from the saved one reported restartRequired = false")
	}
	if view.Settings.TreatAsPrivate == nil || view.Saved.TreatAsPrivate == nil {
		t.Fatal("an empty declaration serialised as null rather than []")
	}
}

// A partial write changes what it names and nothing else, and says the change
// is not live yet.
func TestPutNodeSettingsStoresOnlyWhatWasSentAndAsksForARestart(t *testing.T) {
	running := nodeconfig.Settings{PeerListen: nodeconfig.DefaultPeerListen}
	store, handler := settingsServer(t, running, map[string]string{})

	response := perform(t, handler, http.MethodPut, "/v1/node/settings", map[string]any{"discover": true})
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	view := readSettings(t, response.Body.Bytes())
	if !view.RestartRequired {
		t.Fatal("restartRequired = false after a write that is not live")
	}
	if view.Message == "" {
		t.Fatal("a write answered with no sentence a GUI can show")
	}
	if view.Settings.Discover {
		t.Fatal("the running configuration was reported as changed; nothing was restarted")
	}
	if !view.Saved.Discover {
		t.Fatalf("saved = %+v", view.Saved)
	}
	stored, err := store.GetNodeSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stored.Discover == nil || !*stored.Discover {
		t.Fatalf("discover was not written: %v", stored.Discover)
	}
	if stored.AllowLAN != nil || stored.PeerListen != nil {
		t.Fatalf("a one-field write pinned others: %+v", stored)
	}
}

// The refusal is the node's own, in the node's own words, before anything is
// stored: a saved address the node would refuse is a service that crashes on
// every restart with the reason only in a log.
func TestPutNodeSettingsRefusesWhatTheNodeWouldRefuseAtStartup(t *testing.T) {
	store, handler := settingsServer(t, nodeconfig.DefaultSettings(), map[string]string{})

	response := perform(t, handler, http.MethodPut, "/v1/node/settings",
		map[string]any{"peerListen": "192.168.1.10:7463"})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	var failure struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Error.Code != "INVALID_REQUEST" {
		t.Fatalf("code = %q", failure.Error.Code)
	}
	// The node's message names the flag that would permit it. A generic
	// "invalid settings" would leave the owner with nothing to do next.
	if !strings.Contains(failure.Error.Message, "allow-lan") {
		t.Fatalf("message = %q", failure.Error.Message)
	}
	stored, err := store.GetNodeSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !stored.Empty() {
		t.Fatalf("a refused request still wrote %+v", stored)
	}

	// The same address with the switch that permits it, in one request, is
	// accepted: the two fields are one decision.
	accepted := perform(t, handler, http.MethodPut, "/v1/node/settings",
		map[string]any{"peerListen": "192.168.1.10:7463", "allowLan": true})
	if accepted.Code != http.StatusOK {
		t.Fatalf("response = %d %s", accepted.Code, accepted.Body.String())
	}

	// And an empty body changes nothing rather than being a silent success.
	empty := perform(t, handler, http.MethodPut, "/v1/node/settings", map[string]any{})
	if empty.Code != http.StatusBadRequest {
		t.Fatalf("an empty write = %d %s", empty.Code, empty.Body.String())
	}
}

// A write is judged against what the next start will use, not against what is
// running. A node on loopback can hold a saved LAN listener, and withdrawing
// the declaration that makes that address private is the combination that
// starts nothing — checked against the running loopback listener it would be
// waved through, and the node would refuse to start with nobody watching.
func TestPutNodeSettingsJudgesTheSavedConfigurationNotTheRunningOne(t *testing.T) {
	// Running on loopback, as a node started with no flags at all.
	store, handler := settingsServer(t, nodeconfig.DefaultSettings(), map[string]string{})

	saved := perform(t, handler, http.MethodPut, "/v1/node/settings", map[string]any{
		"peerListen": "122.122.0.1:7463", "allowLan": true, "treatAsPrivate": []string{"122.122.0.0/16"},
	})
	if saved.Code != http.StatusOK {
		t.Fatalf("response = %d %s", saved.Code, saved.Body.String())
	}

	withdrawn := perform(t, handler, http.MethodPut, "/v1/node/settings",
		map[string]any{"treatAsPrivate": []string{}})
	if withdrawn.Code != http.StatusBadRequest {
		t.Fatalf("withdrawing the range a saved listener depends on = %d %s",
			withdrawn.Code, withdrawn.Body.String())
	}
	stored, err := store.GetNodeSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stored.TreatAsPrivate == nil || len(*stored.TreatAsPrivate) != 1 {
		t.Fatalf("the refused write still changed the declaration: %v", stored.TreatAsPrivate)
	}
	// Withdrawing both together is coherent and accepted, because the listener
	// goes back to loopback in the same request.
	together := perform(t, handler, http.MethodPut, "/v1/node/settings", map[string]any{
		"treatAsPrivate": []string{}, "peerListen": nodeconfig.DefaultPeerListen, "allowLan": false,
	})
	if together.Code != http.StatusOK {
		t.Fatalf("response = %d %s", together.Code, together.Body.String())
	}
}
