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

// boolSetting is the pointer a Partial needs: absent and false are different
// answers here, so every stored switch is addressed.
func boolSetting(value bool) *bool { return &value }

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

// Turning allowLan off is the one write that must never be refused for being
// inconsistent with the listener, because the node it is refused on is the one
// serving a network. Before this, a saved peerListen of 192.168.1.10:7463 made
// {"allowLan": false} a 400 telling the owner to pass -allow-lan — the switch
// they were closing — and the README told them to type exactly that.
func TestTurningAllowLANOffTakesTheListenerOffTheNetwork(t *testing.T) {
	store, handler := settingsServer(t, nodeconfig.DefaultSettings(), map[string]string{})
	opened := perform(t, handler, http.MethodPut, "/v1/node/settings",
		map[string]any{"peerListen": "192.168.1.10:7463", "allowLan": true})
	if opened.Code != http.StatusOK {
		t.Fatalf("response = %d %s", opened.Code, opened.Body.String())
	}

	closed := perform(t, handler, http.MethodPut, "/v1/node/settings", map[string]any{"allowLan": false})
	if closed.Code != http.StatusOK {
		t.Fatalf("closing the switch = %d %s", closed.Code, closed.Body.String())
	}
	view := readSettings(t, closed.Body.Bytes())
	if view.Saved.AllowLAN || view.Saved.PeerListen != nodeconfig.DefaultPeerListen {
		t.Fatalf("the next start would use %+v", view.Saved)
	}
	// Said out loud: the owner asked about one field and two moved, and the
	// one that moved on its own is where this node listens.
	if !strings.Contains(view.Message, nodeconfig.DefaultPeerListen) ||
		!strings.Contains(view.Message, "peerListen") {
		t.Errorf("the response does not say the listener moved: %q", view.Message)
	}
	stored, err := store.GetNodeSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stored.PeerListen == nil || *stored.PeerListen != nodeconfig.DefaultPeerListen {
		t.Fatalf("peerListen was stored as %v; the next start would still bind the network", stored.PeerListen)
	}
	if stored.AllowLAN == nil || *stored.AllowLAN {
		t.Fatalf("allowLan was stored as %v", stored.AllowLAN)
	}
}

// The withdrawal is narrow: it never widens anything, and it does not move a
// listener that is already off the network. A loopback port somebody chose is
// a choice, not a consequence of allowLan.
func TestTurningAllowLANOffLeavesALoopbackListenerAlone(t *testing.T) {
	store, handler := settingsServer(t, nodeconfig.DefaultSettings(), map[string]string{})
	const chosen = "127.0.0.1:9463"
	if opened := perform(t, handler, http.MethodPut, "/v1/node/settings",
		map[string]any{"peerListen": chosen}); opened.Code != http.StatusOK {
		t.Fatalf("response = %d %s", opened.Code, opened.Body.String())
	}
	closed := perform(t, handler, http.MethodPut, "/v1/node/settings", map[string]any{"allowLan": false})
	if closed.Code != http.StatusOK {
		t.Fatalf("response = %d %s", closed.Code, closed.Body.String())
	}
	stored, err := store.GetNodeSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stored.PeerListen == nil || *stored.PeerListen != chosen {
		t.Fatalf("peerListen = %v; a loopback listener was moved for no reason", stored.PeerListen)
	}
	if view := readSettings(t, closed.Body.Bytes()); strings.Contains(view.Message, "went back to") {
		t.Errorf("the response claims a listener moved: %q", view.Message)
	}
}

// The withdrawal is for a caller who said nothing about the listener. A caller
// who names a LAN address in the same write that closes the switch has given
// two contradictory halves, and the only honest answer is to refuse — silently
// storing loopback would be the API deciding which half they meant and
// answering 200 to a request it did not carry out.
//
// Pinned because the guard that does this is one clause: dropping
// `requested.PeerListen != nil` from withdrawLANListener turns this 400 into a
// 200 that stores an address the caller never sent, and every other test here
// still passes.
func TestNamingALANListenerWhileClosingTheSwitchIsRefusedNotQuietlyWithdrawn(t *testing.T) {
	store, handler := settingsServer(t, nodeconfig.DefaultSettings(), map[string]string{})
	// The node already serves the LAN, which is the only state in which the
	// guard is reachable: the withdrawal looks at the saved listener, so with
	// nothing saved there is nothing for a missing guard to withdraw.
	if opened := perform(t, handler, http.MethodPut, "/v1/node/settings",
		map[string]any{"peerListen": "192.168.1.10:7463", "allowLan": true}); opened.Code != http.StatusOK {
		t.Fatalf("response = %d %s", opened.Code, opened.Body.String())
	}

	const named = "192.168.1.20:7463"
	response := perform(t, handler, http.MethodPut, "/v1/node/settings",
		map[string]any{"allowLan": false, "peerListen": named})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("a contradictory write = %d %s", response.Code, response.Body.String())
	}
	// Answered in the caller's own terms. The node's validator says "pass
	// -allow-lan", which is the switch this write is closing, and on its own it
	// reads as the API contradicting the request.
	body := response.Body.String()
	for _, want := range []string{"allowLan", "loopback", named} {
		if !strings.Contains(body, want) {
			t.Errorf("the refusal never says %q: %s", want, body)
		}
	}

	stored, err := store.GetNodeSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stored.PeerListen == nil || *stored.PeerListen != "192.168.1.10:7463" {
		t.Fatalf("a refused write moved the listener to %v; neither half of it was carried out",
			stored.PeerListen)
	}
	if stored.AllowLAN == nil || !*stored.AllowLAN {
		t.Fatalf("a refused write closed the switch: %v", stored.AllowLAN)
	}
}

// A withdrawn peer listener is a value in the database that no source name can
// describe.
//
// A node that started with allowLan off beside a remembered LAN listener moved
// that listener to the default and stored it. Of the three provenances the
// desktop contract allows, "default" is the only true one — and it reads as
// "nothing is stored", which is the one thing that is no longer the case. The
// boolean is the difference, and it lasts exactly as long as this process: the
// next start finds the default in the database and calls it remembered.
func TestAWithdrawnPeerListenerIsReportedBesideItsDefaultSource(t *testing.T) {
	ctx := context.Background()
	store, err := registry.Open(ctx, filepath.Join(t.TempDir(), "agenthub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	// What the start-up withdrawal leaves behind: the default, stored.
	withdrawnTo := nodeconfig.DefaultPeerListen
	if err := store.SaveNodeSettings(ctx, nodeconfig.Partial{
		PeerListen: &withdrawnTo, AllowLAN: boolSetting(false),
	}); err != nil {
		t.Fatal(err)
	}
	node := model.NodeIdentity{ID: testNodeID, DisplayName: "test", Platform: "test"}
	heartbeats := protocol.NewHeartbeatBuilder(store, node, apiTestSigner{})
	running := nodeconfig.Settings{PeerListen: nodeconfig.DefaultPeerListen}
	sources := map[string]string{nodeconfig.SettingPeerListen: nodeconfig.SourceDefault}
	handler := NewServer(store, nil, heartbeats, model.NodeIdentity{ID: testNodeID},
		WithNodeSettings(running, sources), WithPeerListenWithdrawn()).Handler()

	response := perform(t, handler, http.MethodGet, "/v1/node/settings", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	view := readSettings(t, response.Body.Bytes())
	if !view.PeerListenWithdrawn {
		t.Fatalf("a withdrawn listener was reported as an ordinary default: %s", response.Body.String())
	}
	// The source stays one of the three the desktop knows.
	if view.Sources[nodeconfig.SettingPeerListen] != nodeconfig.SourceDefault {
		t.Fatalf("sources = %v; the contract is flag/remembered/default", view.Sources)
	}
	if !strings.Contains(view.Message, "withdrawn") {
		t.Errorf("a read of a withdrawn listener explains nothing: %q", view.Message)
	}

	// A write still answers about the write. The boolean carries the fact for
	// anything that wants to render it.
	written := perform(t, handler, http.MethodPut, "/v1/node/settings", map[string]any{"discover": true})
	if written.Code != http.StatusOK {
		t.Fatalf("write = %d %s", written.Code, written.Body.String())
	}
	afterWrite := readSettings(t, written.Body.Bytes())
	if !afterWrite.PeerListenWithdrawn || !strings.Contains(afterWrite.Message, "take effect") {
		t.Fatalf("the write answered %+v", afterWrite)
	}
}

// The next start is an ordinary one, and must not keep explaining a withdrawal
// that happened before the last restart. Absent from the JSON entirely, so a
// reader never has to tell false from missing.
func TestAStartThatWithdrewNothingSaysNothingAboutWithdrawal(t *testing.T) {
	_, handler := settingsServer(t, nodeconfig.Settings{PeerListen: nodeconfig.DefaultPeerListen},
		map[string]string{nodeconfig.SettingPeerListen: nodeconfig.SourceRemembered})
	response := perform(t, handler, http.MethodGet, "/v1/node/settings", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "peerListenWithdrawn") {
		t.Fatalf("an ordinary start reported a withdrawal: %s", response.Body.String())
	}
	if readSettings(t, response.Body.Bytes()).Message != "" {
		t.Fatalf("an ordinary read carried a message: %s", response.Body.String())
	}
}
