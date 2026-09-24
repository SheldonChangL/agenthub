package main

import (
	"crypto/tls"
	"net"
	"net/http"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/identity"
	"agenthub.local/agenthub/internal/nodeconfig"
)

// The serving loop run() uses, end to end: a configured address that was busy
// at start-up binds on a retry, the set retires the loopback fallback, and the
// node goes on serving the configured address on the same TLS server without
// reporting the retired listener's end as a failure. Reported, it would end
// run() — the node would exit at the moment it became reachable.
func TestRetiringTheFallbackLeavesTheConfiguredAddressServing(t *testing.T) {
	configured := freeLoopback(t)
	busy, err := net.Listen("tcp", configured)
	if err != nil {
		t.Fatal(err)
	}
	set, err := bindPeerListeners([]string{configured}, freeLoopback(t), func(string, ...any) {})
	if err != nil {
		_ = busy.Close()
		t.Fatalf("bindPeerListeners: %v", err)
	}
	defer set.Close()
	problem := set.Problem()
	if problem == nil || problem.Reason != nodeconfig.ListenPortInUse {
		_ = busy.Close()
		t.Fatalf("a busy address did not degrade the node: %+v", problem)
	}
	fallback := problem.RunningOn

	keypair, err := identity.LoadOrCreateKeypair(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	rotating := identity.NewRotatingCertificate(keypair, "node_1234567890123456")
	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("peer"))
		}),
		ReadHeaderTimeout: 5 * time.Second,
		TLSConfig:         &tls.Config{GetCertificate: rotating.GetCertificate, MinVersion: tls.VersionTLS13},
	}
	defer server.Close()
	reported := make(chan error, 4)
	servePeerListeners(set, server, func(err error) { reported <- err })

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			// The node's certificate is pinned by key elsewhere; this test asks
			// only whether the listener is served.
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS13}, // #nosec G402 -- test client
		},
	}
	defer client.CloseIdleConnections()
	get := func(address string) error {
		response, err := client.Get("https://" + address + "/")
		if err != nil {
			return err
		}
		_ = response.Body.Close()
		return nil
	}
	if err := get(fallback); err != nil {
		t.Fatalf("the fallback %s is not served: %v", fallback, err)
	}

	// The address comes free, and the next retry takes it.
	if err := busy.Close(); err != nil {
		t.Fatal(err)
	}
	set.Retry()
	if set.Problem() != nil {
		t.Fatalf("still degraded after the configured address bound: %+v", set.Problem())
	}
	if got := set.Bound(); len(got) != 1 || got[0] != configured {
		t.Fatalf("bound = %q, want [%s]", got, configured)
	}
	// The fallback is closed: nobody was told about it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		connection, err := net.DialTimeout("tcp", fallback, time.Second)
		if err != nil {
			break
		}
		_ = connection.Close()
		if time.Now().After(deadline) {
			t.Fatalf("the retired fallback %s still accepts connections", fallback)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := get(configured); err != nil {
		t.Fatalf("the configured address %s is not served after the retry: %v", configured, err)
	}
	// The retired listener's Serve has returned by now (its socket refuses),
	// so a report it was going to make is already on the channel.
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-reported:
		t.Fatalf("retiring the fallback was reported as a failure: %v", err)
	default:
	}
	// And the configured listener still serves once the other one is gone.
	if err := get(configured); err != nil {
		t.Fatalf("the configured address stopped serving: %v", err)
	}
}
