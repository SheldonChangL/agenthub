package api

import (
	"context"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
	"agenthub.local/agenthub/internal/transport"
)

// lanOwner is an owner API on a LAN delivery policy with one paired peer.
func lanOwner(t *testing.T) (*registry.Registry, http.Handler) {
	t.Helper()
	ctx := context.Background()
	store, err := registry.Open(ctx, filepath.Join(t.TempDir(), "agenthub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	node := model.NodeIdentity{ID: testNodeID, DisplayName: "test", Platform: "test"}
	server := NewServer(store, nil, protocol.NewHeartbeatBuilder(store, node, apiTestSigner{}), node,
		WithDeliveryPolicy(transport.PrivateNetworks(nil)))
	if err := store.TrustNode(ctx, registry.TrustedNode{
		NodeID: peerNodeID, DisplayName: "peer", Platform: "test",
		PublicKey: "key", Fingerprint: "FP",
	}); err != nil {
		t.Fatal(err)
	}
	return store, server.Handler()
}

func addressesAt(t *testing.T, store *registry.Registry) (string, []string) {
	t.Helper()
	node, err := store.TrustedNode(context.Background(), peerNodeID)
	if err != nil {
		t.Fatal(err)
	}
	return node.Address, node.Alternates
}

// PUT /v1/nodes/{id}/addresses replaces the set, the first preferred, and
// GET /v1/nodes — what `ah nodes` prints — shows the alternates.
func TestPutAddressesReplacesTheSetAndTheListShowsIt(t *testing.T) {
	store, owner := lanOwner(t)
	path := "/v1/nodes/" + peerNodeID + "/addresses"
	response := perform(t, owner, http.MethodPut, path,
		map[string][]string{"addresses": {"192.168.1.20:7463", "10.0.0.5:7463"}})
	if response.Code != http.StatusNoContent {
		t.Fatalf("PUT = %d %s", response.Code, response.Body.String())
	}
	preferred, alternates := addressesAt(t, store)
	if preferred != "192.168.1.20:7463" || !reflect.DeepEqual(alternates, []string{"10.0.0.5:7463"}) {
		t.Fatalf("stored %q %q", preferred, alternates)
	}
	listed := perform(t, owner, http.MethodGet, "/v1/nodes", nil)
	if !strings.Contains(listed.Body.String(), `"alternateAddresses":["10.0.0.5:7463"]`) {
		t.Fatalf("GET /v1/nodes does not show the alternates: %s", listed.Body.String())
	}

	// An empty list clears them all.
	response = perform(t, owner, http.MethodPut, path, map[string][]string{"addresses": {}})
	if response.Code != http.StatusNoContent {
		t.Fatalf("PUT [] = %d %s", response.Code, response.Body.String())
	}
	if preferred, alternates := addressesAt(t, store); preferred != "" || len(alternates) != 0 {
		t.Fatalf("after [] stored %q %q", preferred, alternates)
	}
}

func TestPutAddressesRefusesWhatCannotBeDelivered(t *testing.T) {
	cases := map[string]struct {
		body any
		code string
	}{
		"five addresses": {map[string][]string{"addresses": {
			"10.0.0.1:1", "10.0.0.2:1", "10.0.0.3:1", "10.0.0.4:1", "10.0.0.5:1"}}, "INVALID_REQUEST"},
		"a public address":     {map[string][]string{"addresses": {"10.0.0.1:1", "203.0.113.9:1"}}, "ADDRESS_NOT_ALLOWED"},
		"not host:port":        {map[string][]string{"addresses": {"10.0.0.1"}}, "INVALID_REQUEST"},
		"no list at all":       {map[string]string{}, "INVALID_REQUEST"},
		"the one-address body": {map[string]string{"address": "10.0.0.1:1"}, "INVALID_REQUEST"},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			store, owner := lanOwner(t)
			if err := store.SetNodeAddress(context.Background(), peerNodeID, "10.9.9.9:7463"); err != nil {
				t.Fatal(err)
			}
			response := perform(t, owner, http.MethodPut, "/v1/nodes/"+peerNodeID+"/addresses", test.body)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), test.code) {
				t.Fatalf("PUT = %d %s; want 400 %s", response.Code, response.Body.String(), test.code)
			}
			if preferred, alternates := addressesAt(t, store); preferred != "10.9.9.9:7463" || len(alternates) != 0 {
				t.Fatalf("a refused PUT changed the addresses to %q %q", preferred, alternates)
			}
		})
	}
}

func TestPutAddressesOnAnUnknownNodeIsNotFound(t *testing.T) {
	_, owner := lanOwner(t)
	response := perform(t, owner, http.MethodPut, "/v1/nodes/node_stranger00000000/addresses",
		map[string][]string{"addresses": {"10.0.0.1:7463"}})
	if response.Code != http.StatusNotFound {
		t.Fatalf("PUT = %d %s; want 404", response.Code, response.Body.String())
	}
}

// The one-address route is unchanged in what it takes, and now keeps the
// address it replaces as the first alternate.
func TestPutAddressKeepsTheOldPreferredBehindTheNewOne(t *testing.T) {
	store, owner := lanOwner(t)
	for _, address := range []string{"10.0.0.1:7463", "192.168.1.20:7463"} {
		response := perform(t, owner, http.MethodPut, "/v1/nodes/"+peerNodeID+"/address",
			map[string]string{"address": address})
		if response.Code != http.StatusNoContent {
			t.Fatalf("PUT = %d %s", response.Code, response.Body.String())
		}
	}
	preferred, alternates := addressesAt(t, store)
	if preferred != "192.168.1.20:7463" || !reflect.DeepEqual(alternates, []string{"10.0.0.1:7463"}) {
		t.Fatalf("stored %q %q", preferred, alternates)
	}
}
