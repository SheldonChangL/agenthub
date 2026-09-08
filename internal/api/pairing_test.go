package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"math"
	"net/http"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/discovery"
	"agenthub.local/agenthub/internal/identity"
	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/pairing"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
)

// fakeAnnouncer stands in for the announce loop. The handlers need three
// answers from it, and building a live announcer to check an error message
// would mean joining a multicast group in a unit test.
type fakeAnnouncer struct {
	mu     sync.Mutex
	reason string
	status pairing.Status
	wakes  int
}

func (f *fakeAnnouncer) Status() pairing.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeAnnouncer) Unannounceable() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reason
}

func (f *fakeAnnouncer) Wake() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wakes++
}

func (f *fakeAnnouncer) wakeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.wakes
}

func pairingServer(t *testing.T) (http.Handler, *pairing.Mode, *discovery.Candidates) {
	handler, mode, candidates, _ := pairingServerWithAnnouncer(t)
	return handler, mode, candidates
}

func pairingServerWithAnnouncer(t *testing.T) (http.Handler, *pairing.Mode, *discovery.Candidates, *fakeAnnouncer) {
	mode := pairing.NewMode()
	handler, candidates, announcer := pairingHandler(t, mode)
	return handler, mode, candidates, announcer
}

func pairingServerWithMode(t *testing.T, mode *pairing.Mode) (http.Handler, *fakeAnnouncer) {
	handler, _, announcer := pairingHandler(t, mode)
	return handler, announcer
}

func pairingHandler(t *testing.T, mode *pairing.Mode) (http.Handler, *discovery.Candidates, *fakeAnnouncer) {
	t.Helper()
	ctx := context.Background()
	store, err := registry.Open(ctx, filepath.Join(t.TempDir(), "pairing.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	node := model.NodeIdentity{ID: testNodeID, DisplayName: "test", Platform: "test"}
	candidates := discovery.NewCandidates(node.ID, store.IsPaired, func(string) error { return nil })
	announcer := &fakeAnnouncer{}
	server := NewServer(store, nil, protocol.NewHeartbeatBuilder(store, node, apiTestSigner{}), node,
		WithPairing(mode, candidates, announcer))
	return server.Handler(), candidates, announcer
}

// Without -discover this node can neither advertise nor see anyone. Answering
// with an empty list would tell the owner to keep waiting for something that is
// never coming.
func TestPairingEndpointsSayWhenDiscoveryIsOff(t *testing.T) {
	_, handler := testServer(t)
	for name, request := range map[string]struct {
		method, path string
	}{
		"state":      {http.MethodGet, "/v1/pairing"},
		"open":       {http.MethodPost, "/v1/pairing"},
		"close":      {http.MethodDelete, "/v1/pairing"},
		"candidates": {http.MethodGet, "/v1/pairing/candidates"},
	} {
		t.Run(name, func(t *testing.T) {
			response := perform(t, handler, request.method, request.path, nil)
			if response.Code != http.StatusConflict {
				t.Fatalf("response = %d %s; want 409", response.Code, response.Body.String())
			}
			for _, want := range []string{"-discover", "-allow-lan", "ah pair"} {
				if !strings.Contains(response.Body.String(), want) {
					t.Errorf("the refusal does not mention %q: %s", want, response.Body.String())
				}
			}
		})
	}
}

// Closed until asked, open for a bounded time, and closed again by the clock.
func TestPairingModeOpensAndCloses(t *testing.T) {
	handler, mode, _ := pairingServer(t)

	state := perform(t, handler, http.MethodGet, "/v1/pairing", nil)
	if state.Code != http.StatusOK {
		t.Fatalf("state = %d %s", state.Code, state.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(state.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["open"] != false {
		t.Errorf("a fresh node reports pairing mode %v", body["open"])
	}

	opened := perform(t, handler, http.MethodPost, "/v1/pairing", map[string]int{"seconds": 60})
	if opened.Code != http.StatusOK {
		t.Fatalf("open = %d %s", opened.Code, opened.Body.String())
	}
	if err := json.Unmarshal(opened.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["open"] != true || body["expiresAt"] == nil {
		t.Errorf("open response = %v", body)
	}
	if !mode.IsOpen() {
		t.Error("the endpoint answered open but the mode is closed")
	}

	closed := perform(t, handler, http.MethodDelete, "/v1/pairing", nil)
	if closed.Code != http.StatusOK {
		t.Fatalf("close = %d %s", closed.Code, closed.Body.String())
	}
	if mode.IsOpen() {
		t.Error("close did not close")
	}
}

// The pairing state carries the name this node announces, and where it came
// from.
//
// It travels here rather than being read once at startup because it is what a
// UI warns about, and it changes when the node restarts under a different
// -display-name — which is what such a warning tells an owner to do. Asserted
// on the wire, by key: the desktop reads these names, and renaming either side
// alone would leave every other test green.
func TestThePairingStateCarriesTheAnnouncedName(t *testing.T) {
	mode := pairing.NewMode()
	handler, _, _ := pairingHandler(t, mode)

	for _, path := range []string{"GET", "POST"} {
		method := http.MethodGet
		var payload any
		if path == "POST" {
			method, payload = http.MethodPost, map[string]int{"seconds": 60}
		}
		response := perform(t, handler, method, "/v1/pairing", payload)
		var body map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["displayName"] != "test" {
			t.Errorf("%s /v1/pairing displayName = %v, want the node's own name", path, body["displayName"])
		}
		if _, ok := body["nameIsChosen"]; !ok {
			t.Errorf("%s /v1/pairing carries no nameIsChosen, so a UI cannot say which remedy applies", path)
		}
		if body["nameIsChosen"] != false {
			t.Errorf("%s /v1/pairing nameIsChosen = %v for a name nobody picked", path, body["nameIsChosen"])
		}
	}
}

// An owner who asks for an hour is told the answer, rather than given fifteen
// minutes and left believing they have an hour.
func TestAWindowOutsideTheBoundsIsRefused(t *testing.T) {
	handler, mode, _ := pairingServer(t)
	for name, seconds := range map[string]int{
		"an hour":  3600,
		"a second": 1,
		"negative": -1,
	} {
		t.Run(name, func(t *testing.T) {
			response := perform(t, handler, http.MethodPost, "/v1/pairing", map[string]int{"seconds": seconds})
			if response.Code != http.StatusBadRequest {
				t.Fatalf("response = %d %s; want 400", response.Code, response.Body.String())
			}
			if mode.IsOpen() {
				t.Error("a refused window opened anyway")
			}
		})
	}
	// And no body at all is the common case: whatever the default is.
	if response := perform(t, handler, http.MethodPost, "/v1/pairing", nil); response.Code != http.StatusOK {
		t.Errorf("opening without a duration = %d %s", response.Code, response.Body.String())
	}
}

// The list is claims, and the answer has to say so — it is read by a person
// about to decide which machine to trust.
func TestCandidatesAreServedWithTheirProvenance(t *testing.T) {
	handler, mode, candidates := pairingServer(t)
	if _, err := mode.Open(time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := candidates.ObserveAll(context.Background(), netip.MustParseAddr("192.168.1.9"),
		[]discovery.Announcement{{
			NodeID: "node_candidate000000", Address: "192.168.1.9:7463",
			DisplayName: "their laptop", Platform: "linux/amd64",
			Fingerprint: "1223 03EA 5E96 543A 2DD8 BFEA",
		}}); err != nil {
		t.Fatal(err)
	}

	response := perform(t, handler, http.MethodGet, "/v1/pairing/candidates", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("candidates = %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Candidates []struct {
			NodeID      string `json:"nodeId"`
			Fingerprint string `json:"fingerprint"`
		} `json:"candidates"`
		Full   bool   `json:"full"`
		Notice string `json:"notice"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Candidates) != 1 || body.Candidates[0].NodeID != "node_candidate000000" {
		t.Fatalf("candidates = %+v", body.Candidates)
	}
	if body.Full {
		t.Error("a list of one reports itself full")
	}
	// The notice is the difference between a list of machines and a list of
	// claims, and it is what a reader needs before picking a row.
	for _, want := range []string{"chosen by whoever sent the packet", "never as proof", "both machines"} {
		if !strings.Contains(body.Notice, want) {
			t.Errorf("the notice does not say %q: %s", want, body.Notice)
		}
	}
}

// A duration is nanoseconds in an int64. Multiplying a large number of seconds
// by time.Second wraps, and the wrapped value lands inside the allowed range —
// so a request for 18446744104 seconds was answered with a thirty-second
// window and reported as open, which is not what was asked for.
func TestAnUnrepresentableWindowIsRefusedRatherThanWrapped(t *testing.T) {
	handler, mode, _, _ := pairingServerWithAnnouncer(t)
	for _, seconds := range []int64{
		// Just past the point where the multiplication overflows.
		int64(math.MaxInt64/int64(time.Second)) + 1,
		// The value that produced a plausible-looking window.
		18446744104,
		math.MaxInt64,
		// And the same thing downwards, which the first version of this fix
		// missed by bounding one side only: -18446744043 wrapped to
		// 30.709551616s, and math.MinInt64 to exactly zero, which Open reads as
		// "no preference" and answers with a five-minute window.
		-18446744043,
		math.MinInt64,
		-1,
	} {
		response := perform(t, handler, http.MethodPost, "/v1/pairing", map[string]int64{"seconds": seconds})
		if response.Code != http.StatusBadRequest {
			t.Errorf("seconds=%d gave %d %s, want 400", seconds, response.Code, response.Body.String())
		}
		if mode.IsOpen() {
			t.Fatalf("seconds=%d opened a window this node cannot represent", seconds)
		}
	}
	// The usable bounds still work, so the check above is not simply refusing
	// everything. 900 is the maximum and is exercised nowhere else through the
	// handler, which is where an off-by-one in the comparison would hide.
	for _, seconds := range []int{int(pairing.MinWindow / time.Second), 900} {
		response := perform(t, handler, http.MethodPost, "/v1/pairing", map[string]int{"seconds": seconds})
		if response.Code != http.StatusOK {
			t.Errorf("seconds=%d gave %d %s, want 200", seconds, response.Code, response.Body.String())
		}
		mode.Close()
	}
}

// The failure this makes visible: -discover on a node whose peer listener is on
// loopback has no address a peer could reach, so the window opens, announces
// nothing, and the owner waits at the other machine for a candidate that will
// never appear.
func TestOpeningIsRefusedWhenThereIsNoAddressToAnnounce(t *testing.T) {
	handler, mode, _, announcer := pairingServerWithAnnouncer(t)
	// The reason a real node gives, remedy included — see reachableAt. The
	// handler adds nothing to it, so the string has to carry the fix itself.
	announcer.reason = "the peer listener is on loopback, which no other machine can reach. " +
		"Restart the node with -allow-lan and -peer-listen on one of this machine's network addresses"

	response := perform(t, handler, http.MethodPost, "/v1/pairing", nil)
	if response.Code != http.StatusConflict {
		t.Fatalf("response = %d %s, want 409", response.Code, response.Body.String())
	}
	// The refusal carries the node's own reason, so an owner is told which of
	// several possible causes applies, and that reason carries its own remedy —
	// nothing in the UI can restart the node.
	for _, want := range []string{"loopback", "-peer-listen"} {
		if !strings.Contains(response.Body.String(), want) {
			t.Errorf("the refusal does not name %q: %s", want, response.Body.String())
		}
	}
	if mode.IsOpen() {
		t.Error("the window opened on a node that cannot announce")
	}
	if announcer.wakeCount() != 0 {
		t.Error("a refused open still asked the announcer to announce")
	}

	// And no remedy is appended to every reason. The fixes differ: a loopback
	// listener needs a restart with a different address, while an address on a
	// VPN tunnel is configured correctly and needs the machine on a network —
	// telling that owner to change -peer-listen sends them to change the one
	// thing that is right.
	announcer.reason = "utun3 holds 10.8.0.2 but is a point-to-point interface — a tunnel — " +
		"which has no local network segment for a peer to answer on"
	tunnel := perform(t, handler, http.MethodPost, "/v1/pairing", nil)
	if tunnel.Code != http.StatusConflict {
		t.Fatalf("response = %d %s, want 409", tunnel.Code, tunnel.Body.String())
	}
	if strings.Contains(tunnel.Body.String(), "-peer-listen") {
		t.Errorf("a tunnel refusal tells the owner to change -peer-listen, which is correct "+
			"as configured: %s", tunnel.Body.String())
	}
	if !strings.Contains(tunnel.Body.String(), "tunnel") {
		t.Errorf("the refusal lost the node's reason: %s", tunnel.Body.String())
	}
}

// Opening the window has to put a packet on the wire now. The interval is
// twenty seconds and the shortest window is thirty, so waiting for the next
// tick would spend most of a short window silent while the UI counted down.
func TestOpeningAnnouncesImmediately(t *testing.T) {
	handler, _, _, announcer := pairingServerWithAnnouncer(t)
	if response := perform(t, handler, http.MethodPost, "/v1/pairing", nil); response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	if announcer.wakeCount() != 1 {
		t.Errorf("the announcer was woken %d times, want 1", announcer.wakeCount())
	}
}

// "Open" and "advertising" are different facts, and an owner whose candidate
// never appears on the other machine needs to be able to tell which side the
// problem is on without reading this node's log.
func TestTheStateSaysWhatTheAnnouncerIsActuallyDoing(t *testing.T) {
	handler, _, _, announcer := pairingServerWithAnnouncer(t)
	announced := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	announcer.mu.Lock()
	announcer.status = pairing.Status{
		Addresses:   0,
		LastAttempt: announced.Add(time.Minute),
		LastSuccess: announced,
		LastError:   "no address a peer could reach",
	}
	announcer.mu.Unlock()

	for _, path := range []string{"/v1/pairing", "/v1/pairing/candidates"} {
		response := perform(t, handler, http.MethodGet, path, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, response.Code, response.Body.String())
		}
	}
	var body struct {
		Open       bool           `json:"open"`
		Announcing pairing.Status `json:"announcing"`
	}
	response := perform(t, handler, http.MethodGet, "/v1/pairing", nil)
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Open {
		t.Error("a fresh node reports the window open")
	}
	if body.Announcing.LastError == "" {
		t.Errorf("the state does not carry the reason nothing is being announced: %s", response.Body.String())
	}
	if !body.Announcing.LastSuccess.Equal(announced) {
		t.Errorf("lastAnnouncedAt = %v, want %v", body.Announcing.LastSuccess, announced)
	}
	if body.Announcing.Addresses != 0 {
		t.Errorf("announceableAddresses = %d, want 0", body.Announcing.Addresses)
	}
}

// The countdown and the expiry beside it are read by the same UI. Subtracting
// from a different clock than the one the expiry was computed against makes
// them disagree.
func TestTheCountdownComesFromTheWindowsOwnClock(t *testing.T) {
	// A clock an hour behind the wall clock. The countdown must follow the
	// expiry this mode computed, not time.Now() read beside it — which would
	// come out as zero on a window that has just opened.
	mode := pairing.NewModeWithClock(func() time.Time { return time.Now().Add(-time.Hour) })
	handler, _ := pairingServerWithMode(t, mode)

	response := perform(t, handler, http.MethodPost, "/v1/pairing", map[string]int{"seconds": 60})
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	var body struct {
		Open      bool `json:"open"`
		Remaining int  `json:"remainingSeconds"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.Open {
		t.Fatalf("the window did not open: %s", response.Body.String())
	}
	if body.Remaining < 55 || body.Remaining > 60 {
		t.Errorf("remainingSeconds = %d on a 60s window, want about 60; the countdown is using another clock",
			body.Remaining)
	}
}

// The three pairing pieces are wired together or not at all. Half-wired, the
// handlers would answer some questions and panic on others, and a panic in an
// HTTP handler is a 200 with an empty body to whoever asked.
func TestPairingIsRefusedRatherThanHalfWired(t *testing.T) {
	ctx := context.Background()
	store, err := registry.Open(ctx, filepath.Join(t.TempDir(), "half.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	node := model.NodeIdentity{ID: testNodeID, DisplayName: "test", Platform: "test"}
	mode := pairing.NewMode()
	candidates := discovery.NewCandidates(node.ID, store.IsPaired, func(string) error { return nil })
	server := NewServer(store, nil, protocol.NewHeartbeatBuilder(store, node, apiTestSigner{}), node,
		WithPairing(mode, candidates, nil))

	for _, request := range []struct{ method, path string }{
		{http.MethodGet, "/v1/pairing"},
		{http.MethodPost, "/v1/pairing"},
		{http.MethodDelete, "/v1/pairing"},
		{http.MethodGet, "/v1/pairing/candidates"},
	} {
		response := perform(t, server.Handler(), request.method, request.path, nil)
		if response.Code != http.StatusConflict {
			t.Errorf("%s %s = %d %s, want 409", request.method, request.path,
				response.Code, response.Body.String())
		}
	}
	if mode.IsOpen() {
		t.Error("a half-wired node opened a pairing window")
	}
}

// Pairing with a machine takes it off the list of machines to pair with.
//
// The candidate list asks the trust store once, when a row is created, and
// never again on a refresh — an optimisation whose safety rests entirely on
// this call. Without it the node the owner has just paired with keeps being
// offered to them as something still to pair with, for as long as it keeps
// announcing. Three comments in the discovery package said pairing did this,
// and nothing did.
func TestPairingWithACandidateTakesItOffTheList(t *testing.T) {
	handler, _, candidates, _ := pairingServerWithAnnouncer(t)

	// A machine announcing itself, listed the ordinary way.
	source := netip.MustParseAddr("192.168.1.77")
	if _, err := candidates.ObserveAll(context.Background(), source, []discovery.Announcement{{
		NodeID:      peerNodeID,
		Address:     "192.168.1.77:7463",
		DisplayName: "the machine on the next desk",
		Platform:    "darwin/arm64",
		Fingerprint: "1223 03EA 5E96 543A 2DD8 BFEA",
	}}); err != nil {
		t.Fatal(err)
	}
	if len(candidates.List()) != 1 {
		t.Fatalf("the candidate was not listed to begin with: %+v", candidates.List())
	}

	// The owner pairs with it, by hand, having compared the fingerprint. The
	// key is the peer's real one — the announcement never carried it, which is
	// the whole point of the handshake being a separate step.
	public, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	response := perform(t, handler, http.MethodPost, "/v1/nodes", map[string]string{
		"nodeId":               peerNodeID,
		"displayName":          "the machine on the next desk",
		"platform":             "darwin/arm64",
		"publicKey":            identity.EncodePublicKey(public),
		"confirmedFingerprint": identity.Fingerprint(public),
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("pairing = %d %s", response.Code, response.Body.String())
	}

	if rows := candidates.List(); len(rows) != 0 {
		t.Errorf("a node the owner has just paired with is still offered as a candidate: %+v", rows)
	}
}
