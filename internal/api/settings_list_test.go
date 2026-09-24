package api

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"agenthub.local/agenthub/internal/nodeconfig"
)

const (
	cableAddress = "192.168.1.10:7463"
	wifiAddress  = "10.0.0.5:7463"
)

// lanListOnDisk stores the two-address configuration an owner with a cable and
// Wi-Fi saves.
func lanListOnDisk(t *testing.T) (nodeconfig.Settings, func(*testing.T, http.Handler) settingsResponse) {
	t.Helper()
	running := nodeconfig.Settings{
		PeerListen: cableAddress, PeerListens: []string{cableAddress, wifiAddress}, AllowLAN: true,
	}
	return running, func(t *testing.T, handler http.Handler) settingsResponse {
		t.Helper()
		response := perform(t, handler, http.MethodGet, "/v1/node/settings", nil)
		if response.Code != http.StatusOK {
			t.Fatalf("GET = %d %s", response.Code, response.Body.String())
		}
		return readSettings(t, response.Body.Bytes())
	}
}

// The safety core, through the endpoint every older window uses: a write that
// carries only peerListen replaces the whole list. An older window choosing
// "this machine only" must close the Wi-Fi address too, or it has told its
// owner something false about the one setting that lets data leave.
func TestAPeerListenWriteReplacesTheWholeList(t *testing.T) {
	running, _ := lanListOnDisk(t)
	store, handler := settingsServer(t, running, map[string]string{})
	storeSettings(t, store, nodeconfig.Partial{
		PeerListens: &[]string{cableAddress, wifiAddress}, AllowLAN: boolSetting(true),
	})

	response := perform(t, handler, http.MethodPut, "/v1/node/settings",
		map[string]any{"peerListen": wifiAddress})
	if response.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", response.Code, response.Body.String())
	}
	view := readSettings(t, response.Body.Bytes())
	if !slices.Equal(view.Saved.PeerListens, []string{wifiAddress}) || view.Saved.PeerListen != wifiAddress {
		t.Fatalf("saved = %q / %v; a peerListen write has to be the whole list", view.Saved.PeerListen, view.Saved.PeerListens)
	}
	stored, err := store.GetNodeSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if stored.PeerListens == nil || !slices.Equal(*stored.PeerListens, []string{wifiAddress}) {
		t.Fatalf("the database holds %v; the next start would still serve the cable address", stored.PeerListens)
	}

	// The same address as the list's first entry is the case no other rule
	// catches: the stored scalar would still match the list, so the read-side
	// downgrade rule would believe a list this write meant to close.
	storeSettings(t, store, nodeconfig.Partial{PeerListens: &[]string{cableAddress, wifiAddress}})
	response = perform(t, handler, http.MethodPut, "/v1/node/settings",
		map[string]any{"peerListen": cableAddress})
	if response.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", response.Code, response.Body.String())
	}
	stored, err = store.GetNodeSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(*stored.PeerListens, []string{cableAddress}) {
		t.Fatalf("peerListen = the first entry left the list %v; Wi-Fi is still open", *stored.PeerListens)
	}

	// And "this machine only", which is the write that matters most.
	response = perform(t, handler, http.MethodPut, "/v1/node/settings",
		map[string]any{"peerListen": nodeconfig.DefaultPeerListen, "allowLan": false})
	if response.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", response.Code, response.Body.String())
	}
	if view := readSettings(t, response.Body.Bytes()); !slices.Equal(view.Saved.PeerListens,
		[]string{nodeconfig.DefaultPeerListen}) {
		t.Fatalf("after 'this machine only' the saved list is %v", view.Saved.PeerListens)
	}
}

func TestAPeerListensWriteIsAccepted(t *testing.T) {
	store, handler := settingsServer(t, nodeconfig.DefaultSettings(), map[string]string{})
	response := perform(t, handler, http.MethodPut, "/v1/node/settings", map[string]any{
		"peerListens": []string{cableAddress, wifiAddress}, "allowLan": true,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", response.Code, response.Body.String())
	}
	view := readSettings(t, response.Body.Bytes())
	if !slices.Equal(view.Saved.PeerListens, []string{cableAddress, wifiAddress}) || view.Saved.PeerListen != cableAddress {
		t.Fatalf("saved = %q / %v", view.Saved.PeerListen, view.Saved.PeerListens)
	}
	if !view.RestartRequired {
		t.Error("a new list is not live until a restart, and the answer did not say so")
	}
	stored, err := store.GetNodeSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if *stored.PeerListen != cableAddress || len(*stored.PeerListens) != 2 {
		t.Fatalf("stored %q / %v", *stored.PeerListen, *stored.PeerListens)
	}
}

// Everything the node would refuse at start-up is refused here, before it is
// stored, and the owner's API still refuses a field it does not know — which is
// how a window tells an older node from this one.
func TestAPeerListensWriteIsRefusedWhereTheNodeWouldRefuseIt(t *testing.T) {
	for name, body := range map[string]map[string]any{
		// Beside a field it knows, so the refusal is about the unknown one
		// and not about a body that says nothing.
		"unknown field": {"peerListenz": []string{cableAddress}, "discover": true},
		"unspecified":   {"peerListens": []string{cableAddress, "0.0.0.0:7463"}, "allowLan": true},
		"unspecified v6": {
			"peerListens": []string{cableAddress, "[::ffff:0.0.0.0]:7463"}, "allowLan": true,
		},
		"five": {"peerListens": []string{
			"192.168.1.10:7463", "192.168.1.11:7463", "10.0.0.5:7463", "172.16.0.1:7463", "10.0.0.6:7463",
		}, "allowLan": true},
		"duplicate":         {"peerListens": []string{cableAddress, cableAddress}, "allowLan": true},
		"two ports":         {"peerListens": []string{cableAddress, "10.0.0.5:7464"}, "allowLan": true},
		"LAN without allow": {"peerListens": []string{cableAddress, wifiAddress}},
		"mixed":             {"peerListens": []string{"127.0.0.1:7463", cableAddress}, "allowLan": true},
		"spellings disagree": {
			"peerListen": "127.0.0.1:7463", "peerListens": []string{cableAddress, wifiAddress}, "allowLan": true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			store, handler := settingsServer(t, nodeconfig.DefaultSettings(), map[string]string{})
			response := perform(t, handler, http.MethodPut, "/v1/node/settings", body)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("PUT %v = %d %s; want 400", body, response.Code, response.Body.String())
			}
			stored, err := store.GetNodeSettings(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !stored.Empty() {
				t.Fatalf("a refused write stored %+v", stored)
			}
		})
	}
}

// Closing allowLan withdraws every LAN address, and the answer names them all.
func TestClosingAllowLANWithdrawsTheWholeList(t *testing.T) {
	running, _ := lanListOnDisk(t)
	store, handler := settingsServer(t, running, map[string]string{})
	storeSettings(t, store, nodeconfig.Partial{
		PeerListens: &[]string{cableAddress, wifiAddress}, AllowLAN: boolSetting(true),
	})
	response := perform(t, handler, http.MethodPut, "/v1/node/settings", map[string]any{"allowLan": false})
	if response.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", response.Code, response.Body.String())
	}
	view := readSettings(t, response.Body.Bytes())
	if !slices.Equal(view.Saved.PeerListens, []string{nodeconfig.DefaultPeerListen}) {
		t.Fatalf("saved list = %v", view.Saved.PeerListens)
	}
	for _, address := range []string{cableAddress, wifiAddress} {
		if !strings.Contains(view.Message, address) {
			t.Errorf("the message does not name %s: %q", address, view.Message)
		}
	}
}

// fakeListeners states a listener set's answers.
type fakeListeners struct {
	states  []nodeconfig.ListenerState
	problem *nodeconfig.ListenProblem
	running string
}

func (f fakeListeners) States() []nodeconfig.ListenerState { return f.states }
func (f fakeListeners) Problem() *nodeconfig.ListenProblem { return f.problem }
func (f fakeListeners) Running() string                    { return f.running }

// One address of two failing is not a degraded node: peerListenProblem stays
// absent, so an older window does not say "only this machine" about a node
// that is on the network. The failed address is still reported, per address.
// And no restart is asked for: the configuration is what is saved, and a
// restart cannot bring an address back.
func TestOneFailedAddressOfTwoIsReportedWithoutAProblem(t *testing.T) {
	running, get := lanListOnDisk(t)
	listeners := fakeListeners{
		running: cableAddress,
		states: []nodeconfig.ListenerState{
			{Address: cableAddress, State: nodeconfig.ListenerBound},
			{Address: wifiAddress, State: nodeconfig.ListenerFailed, Reason: nodeconfig.ListenAddressGone,
				Detail: "bind: can't assign requested address", Message: "gone"},
		},
	}
	store, handler := settingsServer(t, running, map[string]string{
		nodeconfig.SettingPeerListen: nodeconfig.SourceRemembered,
	}, WithPeerListeners(listeners))
	storeSettings(t, store, nodeconfig.Partial{
		PeerListens: &[]string{cableAddress, wifiAddress}, AllowLAN: boolSetting(true),
	})

	view := get(t, handler)
	if view.PeerListenProblem != nil {
		t.Fatalf("one address bound and peerListenProblem = %+v", view.PeerListenProblem)
	}
	if len(view.PeerListeners) != 2 || view.PeerListeners[1].State != nodeconfig.ListenerFailed ||
		view.PeerListeners[1].Reason != nodeconfig.ListenAddressGone {
		t.Fatalf("peerListeners = %+v", view.PeerListeners)
	}
	if view.RestartRequired {
		t.Error("restartRequired with the configuration equal to the saved one; a restart cannot bring back an address")
	}
	if !slices.Equal(view.Settings.PeerListens, []string{cableAddress, wifiAddress}) ||
		view.Settings.PeerListen != cableAddress {
		t.Errorf("running = %q / %v", view.Settings.PeerListen, view.Settings.PeerListens)
	}
	if view.Sources[nodeconfig.SettingPeerListen] != nodeconfig.SourceRemembered {
		t.Errorf("source = %q", view.Sources[nodeconfig.SettingPeerListen])
	}
}

// Every address failing is the degraded node, and it is reported the way it
// always was: the running peerListen is the fallback, its source is the
// default, and the problem names the first configured address.
func TestEveryAddressFailingIsTheProblemOlderReadersKnow(t *testing.T) {
	running, get := lanListOnDisk(t)
	fallback := "127.0.0.1:53418"
	listeners := fakeListeners{
		running: fallback,
		problem: &nodeconfig.ListenProblem{
			Address: cableAddress, Reason: nodeconfig.ListenAddressGone, RunningOn: fallback, Message: "gone",
		},
		states: []nodeconfig.ListenerState{
			{Address: cableAddress, State: nodeconfig.ListenerFailed, Reason: nodeconfig.ListenAddressGone},
			{Address: wifiAddress, State: nodeconfig.ListenerFailed, Reason: nodeconfig.ListenAddressGone},
		},
	}
	store, handler := settingsServer(t, running, map[string]string{
		nodeconfig.SettingPeerListen: nodeconfig.SourceRemembered,
	}, WithPeerListeners(listeners))
	storeSettings(t, store, nodeconfig.Partial{
		PeerListens: &[]string{cableAddress, wifiAddress}, AllowLAN: boolSetting(true),
	})

	view := get(t, handler)
	if view.PeerListenProblem == nil || view.PeerListenProblem.Address != cableAddress {
		t.Fatalf("peerListenProblem = %+v", view.PeerListenProblem)
	}
	if view.Settings.PeerListen != fallback {
		t.Errorf("running peerListen = %q, want the fallback %q", view.Settings.PeerListen, fallback)
	}
	if view.Sources[nodeconfig.SettingPeerListen] != nodeconfig.SourceDefault {
		t.Errorf("source = %q; nobody chose the fallback", view.Sources[nodeconfig.SettingPeerListen])
	}
	if view.RestartRequired {
		t.Error("restartRequired while degraded; restarting into the same addresses is the loop this avoids")
	}
	// The saved list is the owner's and stays on the form.
	if !slices.Equal(view.Saved.PeerListens, []string{cableAddress, wifiAddress}) {
		t.Errorf("saved = %v", view.Saved.PeerListens)
	}
}

// A window tells a node that knows the list by finding peerListens in both
// halves of the reply.
func TestTheSettingsReplyCarriesTheListInBothHalves(t *testing.T) {
	_, handler := settingsServer(t, nodeconfig.Settings{PeerListen: nodeconfig.DefaultPeerListen}, map[string]string{})
	response := perform(t, handler, http.MethodGet, "/v1/node/settings", nil)
	body := response.Body.String()
	if strings.Count(body, `"peerListens":["127.0.0.1:7463"]`) != 2 {
		t.Fatalf("the reply does not carry peerListens in settings and saved: %s", body)
	}
}
