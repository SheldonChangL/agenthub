package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"agenthub.local/agenthub/internal/api"
	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
	"agenthub.local/agenthub/internal/transport"
)

const addressedPeer = "node_ubuntu000000000"

// lanNode is a real owner API on a LAN delivery policy, with one paired peer.
func lanNode(t *testing.T) (*registry.Registry, *httptest.Server) {
	t.Helper()
	ctx := context.Background()
	store, err := registry.Open(ctx, filepath.Join(t.TempDir(), "agenthub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.TrustNode(ctx, registry.TrustedNode{
		NodeID: addressedPeer, DisplayName: "ubuntu", Platform: "linux", PublicKey: "key", Fingerprint: "FP",
	}); err != nil {
		t.Fatal(err)
	}
	node := model.NodeIdentity{ID: "node_1234567890123456", DisplayName: "test", Platform: "test"}
	handler := api.NewServer(store, nil, protocol.NewHeartbeatBuilder(store, node, listTestSigner{}), node,
		api.WithDeliveryPolicy(transport.PrivateNetworks(nil))).Handler()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return store, server
}

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Run(context.Background(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// Several addresses replace the whole set, the first preferred, and `ah nodes`
// shows the alternates.
func TestNodesAddressWithSeveralRecordsThemAll(t *testing.T) {
	store, server := lanNode(t)
	code, _, stderr := runCLI(t, "--url", server.URL, "nodes", "address", addressedPeer,
		"192.168.1.20:7463", "10.0.0.5:7463")
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, stderr)
	}
	node, err := store.TrustedNode(context.Background(), addressedPeer)
	if err != nil {
		t.Fatal(err)
	}
	if node.Address != "192.168.1.20:7463" || !reflect.DeepEqual(node.Alternates, []string{"10.0.0.5:7463"}) {
		t.Fatalf("stored %q %q", node.Address, node.Alternates)
	}
	code, stdout, stderr := runCLI(t, "--url", server.URL, "nodes")
	if code != 0 {
		t.Fatalf("ah nodes: exit = %d, stderr = %s", code, stderr)
	}
	if !strings.Contains(stdout, `"alternateAddresses"`) || !strings.Contains(stdout, "10.0.0.5:7463") {
		t.Fatalf("ah nodes does not show the alternate:\n%s", stdout)
	}
}

// One address still goes to the route every node has, and a node that knows
// alternates keeps the one it replaced behind it.
func TestNodesAddressWithOneUsesTheOneAddressRoute(t *testing.T) {
	store, server := lanNode(t)
	for _, address := range []string{"10.0.0.5:7463", "192.168.1.20:7463"} {
		if code, _, stderr := runCLI(t, "--url", server.URL, "nodes", "address", addressedPeer, address); code != 0 {
			t.Fatalf("exit = %d, stderr = %s", code, stderr)
		}
	}
	node, err := store.TrustedNode(context.Background(), addressedPeer)
	if err != nil {
		t.Fatal(err)
	}
	if node.Address != "192.168.1.20:7463" || !reflect.DeepEqual(node.Alternates, []string{"10.0.0.5:7463"}) {
		t.Fatalf("stored %q %q", node.Address, node.Alternates)
	}
}

// olderNode is a node from before alternates: the one-address route and
// nothing else, answered by the mux the way a real older node answers.
func olderNode(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var hits []string
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /v1/nodes/{id}/address", func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("DELETE /v1/nodes/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, &hits
}

func TestNodesAddressAgainstAnOlderNodeSaysItIsTooOld(t *testing.T) {
	server, hits := olderNode(t)
	code, _, stderr := runCLI(t, "--url", server.URL, "nodes", "address", addressedPeer,
		"192.168.1.20:7463", "10.0.0.5:7463")
	if code == 0 {
		t.Fatal("several addresses against an older node exited 0")
	}
	if !strings.Contains(stderr, "too old for more than one address") {
		t.Fatalf("stderr = %q; want the node named as too old", stderr)
	}
	// One address still works there.
	if code, _, stderr := runCLI(t, "--url", server.URL, "nodes", "address", addressedPeer,
		"192.168.1.20:7463"); code != 0 {
		t.Fatalf("one address against an older node: exit = %d, stderr = %s", code, stderr)
	}
	if len(*hits) != 1 {
		t.Fatalf("the older node's route was hit %d times", len(*hits))
	}
}

// A 404 the node wrote — an id it does not know — is not a node too old for
// the route, and must not be reported as one.
func TestNodesAddressForAnUnknownNodeIsNotCalledTooOld(t *testing.T) {
	_, server := lanNode(t)
	code, _, stderr := runCLI(t, "--url", server.URL, "nodes", "address", "node_stranger00000000",
		"192.168.1.20:7463", "10.0.0.5:7463")
	if code == 0 {
		t.Fatal("an unknown node exited 0")
	}
	if strings.Contains(stderr, "too old") || !strings.Contains(stderr, "NOT_FOUND") {
		t.Fatalf("stderr = %q; want the node's own not-found", stderr)
	}
}

// The CLI's bound is the node's.
func TestTheCLIAddressBoundIsTheRegistrys(t *testing.T) {
	if maxNodeAddresses != registry.MaxNodeAddresses {
		t.Fatalf("the CLI allows %d addresses and the node %d", maxNodeAddresses, registry.MaxNodeAddresses)
	}
}
