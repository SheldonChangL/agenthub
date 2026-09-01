package transport

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
)

const localNodeID = "node_local000000000000"

func openStore(t *testing.T) *registry.Registry {
	t.Helper()
	store, err := registry.Open(context.Background(), filepath.Join(t.TempDir(), "agenthub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// capture stands in for a peer's POST /v1/heartbeat. It records the envelopes
// it receives so a test can assert on what was actually put on the wire, rather
// than on what the builder was asked for.
type capture struct {
	server    *httptest.Server
	envelopes []protocol.Envelope
	status    int
}

func newCapture(t *testing.T) *capture {
	t.Helper()
	c := &capture{status: http.StatusNoContent}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			t.Error(err)
			return
		}
		var envelope protocol.Envelope
		if err := json.Unmarshal(body, &envelope); err != nil {
			t.Errorf("peer received something that is not an envelope: %v", err)
			return
		}
		c.envelopes = append(c.envelopes, envelope)
		w.WriteHeader(c.status)
	}))
	t.Cleanup(c.server.Close)
	return c
}

func (c *capture) address(t *testing.T) string {
	t.Helper()
	parsed, err := url.Parse(c.server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Host
}

func (c *capture) sessionIDs(t *testing.T, index int) []string {
	t.Helper()
	if index >= len(c.envelopes) {
		t.Fatalf("peer received %d envelopes; wanted index %d", len(c.envelopes), index)
	}
	var payload protocol.HeartbeatPayload
	if err := json.Unmarshal(c.envelopes[index].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(payload.Sessions))
	for _, session := range payload.Sessions {
		ids = append(ids, session.ID)
	}
	return ids
}

func publisherFor(t *testing.T, store *registry.Registry) *Publisher {
	t.Helper()
	node := model.NodeIdentity{ID: localNodeID, DisplayName: "local", Platform: "test"}
	builder := protocol.NewHeartbeatBuilder(store, node, stubSigner{})
	return NewPublisher(store, builder, LoopbackOnly, time.Hour)
}

type stubSigner struct{}

func (stubSigner) Sign(message []byte) []byte {
	signature := make([]byte, 64)
	copy(signature, message)
	return signature
}

func trust(t *testing.T, store *registry.Registry, nodeID, address string) {
	t.Helper()
	ctx := context.Background()
	if err := store.TrustNode(ctx, registry.TrustedNode{
		NodeID: nodeID, DisplayName: nodeID, Platform: "test",
		PublicKey: "key-" + nodeID, Fingerprint: "2DCF 9604 DBA9 778A 6DDD 035B",
	}); err != nil {
		t.Fatal(err)
	}
	if address != "" {
		if err := store.SetNodeAddress(ctx, nodeID, address); err != nil {
			t.Fatal(err)
		}
	}
}

func publish(t *testing.T, store *registry.Registry, sessionID string, audience model.Audience) {
	t.Helper()
	ctx := context.Background()
	session := model.Session{
		ID: sessionID, Provider: model.ProviderClaude,
		ProviderSessionID: sessionID[len("claude:"):],
		Management:        model.Unmanaged, Visibility: model.VisibilityPublic,
		Status: model.StatusIdle, StatusSource: "test",
		LastSeenAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if _, err := store.UpsertSession(ctx, session); err != nil {
		t.Fatalf("UpsertSession(%q) error = %v", sessionID, err)
	}
	// Discovery deliberately cannot set an audience — a rescan must never alter
	// the owner's sharing decisions — so the grant is made through the same
	// call the owner's tooling uses.
	if err := store.SetAudience(ctx, sessionID, audience); err != nil {
		t.Fatalf("SetAudience(%q) error = %v", sessionID, err)
	}
}

// TestEachPeerReceivesOnlyItsOwnGrants is issue #14's remaining acceptance
// item, checked on the bytes each peer actually received rather than on what
// the builder was asked to produce.
func TestEachPeerReceivesOnlyItsOwnGrants(t *testing.T) {
	store := openStore(t)
	peerA, peerB := "node_peeraaaaaaaaaaaa", "node_peerbbbbbbbbbbbb"
	captureA, captureB := newCapture(t), newCapture(t)
	trust(t, store, peerA, captureA.address(t))
	trust(t, store, peerB, captureB.address(t))

	publish(t, store, "claude:for-a-only", model.Audience{Mode: model.AudienceSelected, Nodes: []string{peerA}})
	publish(t, store, "claude:for-everyone", model.Audience{Mode: model.AudienceAllPaired})

	result, err := publisherFor(t, store).PublishOnce(context.Background())
	if err != nil {
		t.Fatalf("PublishOnce() error = %v", err)
	}
	if result.Delivered != 2 {
		t.Fatalf("result = %+v; want both peers delivered to", result)
	}

	gotA := captureA.sessionIDs(t, 0)
	gotB := captureB.sessionIDs(t, 0)
	if len(gotA) != 2 {
		t.Errorf("peer A received %v; want both the selected and the all-paired session", gotA)
	}
	for _, id := range gotB {
		if id == localNodeID+"/claude:for-a-only" || id == "claude:for-a-only" {
			t.Fatalf("peer B received a session published only to peer A: %v", gotB)
		}
	}
	if len(gotB) != 1 {
		t.Errorf("peer B received %v; want only the all-paired session", gotB)
	}
}

// TestEachPeerGetsAnEnvelopeAddressedToItself pins that the publisher builds per
// peer instead of building once and fanning out. Fanning out would produce
// envelopes naming the wrong recipient, which every receiver would then refuse.
func TestEachPeerGetsAnEnvelopeAddressedToItself(t *testing.T) {
	store := openStore(t)
	peerA, peerB := "node_peeraaaaaaaaaaaa", "node_peerbbbbbbbbbbbb"
	captureA, captureB := newCapture(t), newCapture(t)
	trust(t, store, peerA, captureA.address(t))
	trust(t, store, peerB, captureB.address(t))
	publish(t, store, "claude:shared", model.Audience{Mode: model.AudienceAllPaired})

	if _, err := publisherFor(t, store).PublishOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := captureA.envelopes[0].RecipientNodeID; got != peerA {
		t.Errorf("peer A received an envelope addressed to %q", got)
	}
	if got := captureB.envelopes[0].RecipientNodeID; got != peerB {
		t.Errorf("peer B received an envelope addressed to %q", got)
	}
	if captureA.envelopes[0].MessageID == captureB.envelopes[0].MessageID {
		t.Error("both peers received the same envelope; it was built once and fanned out")
	}
}

// TestASleepingPeerDoesNotStopTheOthers pins that one unreachable peer cannot
// stop this node publishing to every other peer.
func TestASleepingPeerDoesNotStopTheOthers(t *testing.T) {
	store := openStore(t)
	live := newCapture(t)
	// An address nothing is listening on. Sorted before the live peer's node id
	// so it is attempted first and would abort the round if failures propagated.
	trust(t, store, "node_aaaadeadaaaadead0", "127.0.0.1:1")
	trust(t, store, "node_zzzzliveliveliv0", live.address(t))
	publish(t, store, "claude:shared", model.Audience{Mode: model.AudienceAllPaired})

	result, err := publisherFor(t, store).PublishOnce(context.Background())
	if err != nil {
		t.Fatalf("PublishOnce() error = %v; one dead peer must not fail the round", err)
	}
	if result.Delivered != 1 || result.Failed != 1 {
		t.Fatalf("result = %+v; want one delivered and one failed", result)
	}
	if len(live.envelopes) != 1 {
		t.Fatalf("the reachable peer received %d envelopes", len(live.envelopes))
	}
}

// TestANonLoopbackAddressIsNotDeliveredTo pins the boundary from
// docs/multinode-plan.md: no session metadata leaves this host in this build.
func TestANonLoopbackAddressIsNotDeliveredTo(t *testing.T) {
	store := openStore(t)
	trust(t, store, "node_remote0000000000", "192.0.2.10:7462")
	publish(t, store, "claude:shared", model.Audience{Mode: model.AudienceAllPaired})

	result, err := publisherFor(t, store).PublishOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Delivered != 0 || result.Skipped != 1 {
		t.Fatalf("result = %+v; a routable address must be skipped, not delivered to", result)
	}
	if err := LoopbackOnly("192.0.2.10:7462"); err == nil {
		t.Error("LoopbackOnly accepted a routable address")
	}
}

// TestAPeerWithNoAddressIsSkippedNotFailed keeps "paired but not located" out of
// the failure count: it is the normal state before discovery has run.
func TestAPeerWithNoAddressIsSkippedNotFailed(t *testing.T) {
	store := openStore(t)
	trust(t, store, "node_unlocated0000000", "")
	publish(t, store, "claude:shared", model.Audience{Mode: model.AudienceAllPaired})

	result, err := publisherFor(t, store).PublishOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != 1 || result.Failed != 0 || result.Delivered != 0 {
		t.Fatalf("result = %+v; want the peer skipped", result)
	}
}

// TestARevokedPeerIsNoLongerPublishedTo pins that revocation stops delivery.
func TestARevokedPeerIsNoLongerPublishedTo(t *testing.T) {
	store := openStore(t)
	peerID := "node_peeraaaaaaaaaaaa"
	peer := newCapture(t)
	trust(t, store, peerID, peer.address(t))
	publish(t, store, "claude:shared", model.Audience{Mode: model.AudienceAllPaired})
	publisher := publisherFor(t, store)

	if _, err := publisher.PublishOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(peer.envelopes) != 1 {
		t.Fatalf("peer received %d envelopes before revocation", len(peer.envelopes))
	}

	if err := store.RevokeNode(context.Background(), peerID); err != nil {
		t.Fatal(err)
	}
	result, err := publisher.PublishOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Delivered != 0 {
		t.Errorf("result = %+v; a revoked peer must not be published to", result)
	}
	if len(peer.envelopes) != 1 {
		t.Errorf("peer received %d envelopes; it was published to after revocation", len(peer.envelopes))
	}
}

// TestARedirectingPeerCannotMoveTheDeliveryOffHost is the boundary test the
// loopback rule actually needs, and it was missing: checking the configured
// address proves nothing about where the bytes end up if the peer is allowed
// to name a new destination after the check.
//
// The request body is a bytes.Reader, so net/http populates GetBody and replays
// the body on a 307. Without CheckRedirect this delivered the entire signed
// heartbeat — session ids, statuses, any granted cwd — to a routable host, and
// still counted the delivery as a success.
func TestARedirectingPeerCannotMoveTheDeliveryOffHost(t *testing.T) {
	offHost := newCapture(t)

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, offHost.server.URL+"/v1/heartbeat", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	redirectorHost, err := url.Parse(redirector.URL)
	if err != nil {
		t.Fatal(err)
	}

	store := openStore(t)
	trust(t, store, "node_redirector00000", redirectorHost.Host)
	publish(t, store, "claude:secret", model.Audience{Mode: model.AudienceAllPaired})

	result, err := publisherFor(t, store).PublishOnce(context.Background())
	if err != nil {
		t.Fatalf("PublishOnce() error = %v", err)
	}
	if len(offHost.envelopes) != 0 {
		t.Fatalf("session metadata was redirected off-host: %s", offHost.envelopes[0].Payload)
	}
	if result.Delivered != 0 {
		t.Errorf("result = %+v; a refused redirect must not count as delivered", result)
	}
	if result.Failed != 1 {
		t.Errorf("result = %+v; want the redirect reported as a failure", result)
	}
}

// TestLoopbackOnlyResolvesNamesRatherThanTrustingThem pins the stricter rule
// this policy applies compared with the listen-address check it builds on: a
// name decides where bytes are sent, so every address it resolves to must be
// loopback.
func TestLoopbackOnlyResolvesNamesRatherThanTrustingThem(t *testing.T) {
	for name, address := range map[string]string{
		"a loopback literal":                          "127.0.0.1:7462",
		"the IPv6 loopback":                           "[::1]:7462",
		"localhost, which should resolve to loopback": "localhost:7462",
	} {
		t.Run(name, func(t *testing.T) {
			if err := LoopbackOnly(address); err != nil {
				t.Errorf("LoopbackOnly(%q) = %v; want it accepted", address, err)
			}
		})
	}
	for name, address := range map[string]string{
		"a routable literal":                       "192.0.2.10:7462",
		"a routable IPv6":                          "[2001:db8::1]:7462",
		"a name that does not resolve to loopback": "example.com:7462",
		"no port at all":                           "127.0.0.1",
	} {
		t.Run(name, func(t *testing.T) {
			if err := LoopbackOnly(address); err == nil {
				t.Errorf("LoopbackOnly(%q) = nil; want it refused", address)
			}
		})
	}
}
