package api

import (
	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/protocol"
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/discovery"
	"agenthub.local/agenthub/internal/pairing"
	"agenthub.local/agenthub/internal/registry"
)

func pairingServer(t *testing.T) (http.Handler, *pairing.Mode, *discovery.Candidates) {
	t.Helper()
	ctx := context.Background()
	store, err := registry.Open(ctx, filepath.Join(t.TempDir(), "pairing.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	node := model.NodeIdentity{ID: testNodeID, DisplayName: "test", Platform: "test"}
	mode := pairing.NewMode()
	candidates := discovery.NewCandidates(node.ID, store.IsPaired, func(string) error { return nil })
	server := NewServer(store, nil, protocol.NewHeartbeatBuilder(store, node, apiTestSigner{}), node,
		WithPairing(mode, candidates))
	return server.Handler(), mode, candidates
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
			for _, want := range []string{"-discover", "ah pair"} {
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
