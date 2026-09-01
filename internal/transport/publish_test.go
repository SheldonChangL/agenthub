package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/identity"
	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/nodeconfig"
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
	nodeID    string
	public    ed25519.PublicKey
	private   ed25519.PrivateKey
	// answerAs overrides the node id this peer claims when answering a
	// challenge, so a test can impersonate.
	answerAs string
	// messages records what arrived on the message endpoint.
	messages []protocol.MessagePayload
	// messageStatus overrides the status the message endpoint answers with, so
	// a test can drive the sender's handling of a deferral.
	messageStatus int
	// refuseMessages answers with a refusal ack rather than queueing.
	refuseMessages bool
	// refuseChallenge makes the peer fail the proof while still serving.
	refuseChallenge bool
}

func (c *capture) Sign(message []byte) []byte { return ed25519.Sign(c.private, message) }

func newCapture(t *testing.T, nodeID string) *capture {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &capture{status: http.StatusNoContent, nodeID: nodeID, public: public, private: private}
	c.server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			t.Error(err)
			return
		}
		switch r.URL.Path {
		case "/v1/challenge":
			var input struct {
				Nonce            string `json:"nonce"`
				ChallengerNodeID string `json:"challengerNodeId"`
			}
			if err := json.Unmarshal(body, &input); err != nil {
				t.Errorf("peer received an unreadable challenge: %v", err)
				return
			}
			nonce, err := base64.StdEncoding.DecodeString(input.Nonce)
			if err != nil {
				t.Errorf("peer received a non-base64 nonce: %v", err)
				return
			}
			claimed := c.nodeID
			if c.answerAs != "" {
				claimed = c.answerAs
			}
			answer, err := protocol.AnswerChallenge(claimed, input.ChallengerNodeID, nonce, c)
			if err != nil {
				t.Errorf("peer could not answer: %v", err)
				return
			}
			if c.refuseChallenge {
				// A well-formed answer signed over the wrong nonce: the shape
				// of an impostor that cannot produce the real proof.
				answer, _ = protocol.AnswerChallenge(claimed, input.ChallengerNodeID,
					bytes.Repeat([]byte{9}, protocol.ChallengeNonceSize), c)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"nodeId": claimed, "signature": answer})
		case "/v1/heartbeat":
			var envelope protocol.Envelope
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Errorf("peer received something that is not an envelope: %v", err)
				return
			}
			c.envelopes = append(c.envelopes, envelope)
			w.WriteHeader(c.status)
		case "/v1/messages":
			var envelope protocol.Envelope
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Errorf("peer received something that is not an envelope: %v", err)
				return
			}
			var payload protocol.MessagePayload
			if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
				t.Errorf("peer received an unreadable message payload: %v", err)
				return
			}
			if c.messageStatus != 0 && c.messageStatus != http.StatusOK {
				// A deferral or a failure: no ack body, just the status.
				w.WriteHeader(c.messageStatus)
				return
			}
			c.messages = append(c.messages, payload)
			status := protocol.AckQueued
			if c.refuseMessages {
				status = protocol.AckRefused
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(protocol.AckPayload{
				MessageID: payload.MessageID, Status: status, Reason: "because",
			})
		default:
			t.Errorf("peer received an unexpected path %q", r.URL.Path)
		}
	}))
	// The peer presents its identity key as its TLS certificate, exactly as a
	// real node does. A publisher that pins a different key must fail to
	// handshake, so this is what makes the impostor tests meaningful.
	c.server.TLS = &tls.Config{
		Certificates: []tls.Certificate{certificateFor(t, private, public, nodeID)},
		MinVersion:   tls.VersionTLS13,
	}
	c.server.StartTLS()
	t.Cleanup(c.server.Close)
	return c
}

// certificateFor mirrors identity.Keypair.TLSCertificate for a raw key pair.
func certificateFor(t *testing.T, private ed25519.PrivateKey, public ed25519.PublicKey, nodeID string) tls.Certificate {
	t.Helper()
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: nodeID},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: private}
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
	return NewPublisher(store, builder, localNodeID, LoopbackOnly, time.Hour)
}

type stubSigner struct{}

func (stubSigner) Sign(message []byte) []byte {
	signature := make([]byte, 64)
	copy(signature, message)
	return signature
}

func trust(t *testing.T, store *registry.Registry, nodeID, address string, public ed25519.PublicKey) {
	t.Helper()
	ctx := context.Background()
	encoded := "key-" + nodeID
	if public != nil {
		encoded = identity.EncodePublicKey(public)
	}
	if err := store.TrustNode(ctx, registry.TrustedNode{
		NodeID: nodeID, DisplayName: nodeID, Platform: "test",
		PublicKey: encoded, Fingerprint: "2DCF 9604 DBA9 778A 6DDD 035B",
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
	captureA, captureB := newCapture(t, peerA), newCapture(t, peerB)
	trust(t, store, peerA, captureA.address(t), captureA.public)
	trust(t, store, peerB, captureB.address(t), captureB.public)

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
	captureA, captureB := newCapture(t, peerA), newCapture(t, peerB)
	trust(t, store, peerA, captureA.address(t), captureA.public)
	trust(t, store, peerB, captureB.address(t), captureB.public)
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
	live := newCapture(t, "node_zzzzliveliveliv0")
	// An address nothing is listening on. Sorted before the live peer's node id
	// so it is attempted first and would abort the round if failures propagated.
	trust(t, store, "node_aaaadeadaaaadead0", "127.0.0.1:1", nil)
	trust(t, store, "node_zzzzliveliveliv0", live.address(t), live.public)
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
	trust(t, store, "node_remote0000000000", "192.0.2.10:7462", nil)
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
	trust(t, store, "node_unlocated0000000", "", nil)
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
	peer := newCapture(t, peerID)
	trust(t, store, peerID, peer.address(t), peer.public)
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
	offHost := newCapture(t, "node_offhost00000000")

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, offHost.server.URL+"/v1/heartbeat", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	redirectorHost, err := url.Parse(redirector.URL)
	if err != nil {
		t.Fatal(err)
	}

	store := openStore(t)
	trust(t, store, "node_redirector00000", redirectorHost.Host, nil)
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

// TestAnImpostorAtTheAddressGetsNothing is the reason the challenge exists.
//
// Once addresses come from discovery, anything on the network can claim to be
// at a peer's address. This peer serves correctly and even claims the right
// node id — it simply cannot produce a signature over the nonce, because it
// does not hold the key the owner recorded when pairing. No session metadata
// may reach it.
func TestAnImpostorAtTheAddressGetsNothing(t *testing.T) {
	store := openStore(t)
	peerID := "node_peeraaaaaaaaaaaa"
	impostor := newCapture(t, peerID)

	// The owner paired with the real peer's key; the impostor holds a different
	// one. This is exactly what a spoofed discovery record produces.
	realPublic, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	trust(t, store, peerID, impostor.address(t), realPublic)
	publish(t, store, "claude:secret", model.Audience{Mode: model.AudienceAllPaired})

	result, err := publisherFor(t, store).PublishOnce(context.Background())
	if err != nil {
		t.Fatalf("PublishOnce() error = %v", err)
	}
	if len(impostor.envelopes) != 0 {
		t.Fatalf("session metadata reached an impostor: %s", impostor.envelopes[0].Payload)
	}
	if result.Delivered != 0 || result.Failed != 1 {
		t.Errorf("result = %+v; want the delivery refused", result)
	}
}

// TestAPeerAnsweringAsSomebodyElseGetsNothing covers the discovery record that
// points at a real, honest node that simply is not the peer we meant.
func TestAPeerAnsweringAsSomebodyElseGetsNothing(t *testing.T) {
	store := openStore(t)
	peerID := "node_peeraaaaaaaaaaaa"
	other := newCapture(t, peerID)
	other.answerAs = "node_somebodyelse0000"
	trust(t, store, peerID, other.address(t), other.public)
	publish(t, store, "claude:secret", model.Audience{Mode: model.AudienceAllPaired})

	result, err := publisherFor(t, store).PublishOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(other.envelopes) != 0 {
		t.Fatalf("session metadata reached a node answering as somebody else: %s", other.envelopes[0].Payload)
	}
	if result.Failed != 1 {
		t.Errorf("result = %+v; want the delivery refused", result)
	}
}

// TestAWrongProofGetsNothing covers a peer that holds the right key but signs
// the wrong bytes — a captured answer replayed against a fresh nonce.
func TestAWrongProofGetsNothing(t *testing.T) {
	store := openStore(t)
	peerID := "node_peeraaaaaaaaaaaa"
	peer := newCapture(t, peerID)
	peer.refuseChallenge = true
	trust(t, store, peerID, peer.address(t), peer.public)
	publish(t, store, "claude:secret", model.Audience{Mode: model.AudienceAllPaired})

	result, err := publisherFor(t, store).PublishOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(peer.envelopes) != 0 {
		t.Fatalf("session metadata reached a peer that failed its proof: %s", peer.envelopes[0].Payload)
	}
	if result.Failed != 1 {
		t.Errorf("result = %+v; want the delivery refused", result)
	}
}

// TestTheChallengeHappensBeforeAnythingIsSent pins the ordering. Verifying
// after sending would be decorative: the metadata would already be gone.
func TestTheChallengeHappensBeforeAnythingIsSent(t *testing.T) {
	store := openStore(t)
	peerID := "node_peeraaaaaaaaaaaa"

	var paths []string
	peer := newCapture(t, peerID)
	base := peer.server.Config.Handler
	peer.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		base.ServeHTTP(w, r)
	})
	trust(t, store, peerID, peer.address(t), peer.public)
	publish(t, store, "claude:shared", model.Audience{Mode: model.AudienceAllPaired})

	if _, err := publisherFor(t, store).PublishOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0] != "/v1/challenge" || paths[1] != "/v1/heartbeat" {
		t.Fatalf("request order = %v; want the challenge before the heartbeat", paths)
	}
}

// TestARelayCannotInterceptAPinnedDelivery is the test that changed meaning
// when the transport was pinned, and it is worth saying why.
//
// It was written as a PASSING test reproducing a real weakness: with only a
// challenge-response, an interceptor forwarded this node's challenge to the
// genuine peer, returned the genuine answer, and then received the heartbeat in
// plaintext. Every value the answer bound — responder id, challenger id, nonce
// — travels through a forwarder unchanged, so binding them proved only that the
// real peer was reachable, never that the thing being delivered to was it.
//
// The interceptor below is exactly that: it terminates TLS with its own key,
// forwards the challenge to the genuine peer, and returns the genuine answer.
// The challenge alone cannot tell it apart from the real peer. Pinning can, and
// earlier than the challenge: the handshake fails, so the interceptor never
// receives a request to forward.
//
// Removing the pin makes this test fail with the metadata it captured.
func TestARelayCannotInterceptAPinnedDelivery(t *testing.T) {
	store := openStore(t)
	peerID := "node_peeraaaaaaaaaaaa"
	genuine := newCapture(t, peerID)

	// The interceptor holds its own key — it is not the peer — but it can reach
	// the genuine peer and relay the proof.
	var stolen []protocol.Envelope
	interceptorKey, interceptorPrivate, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	interceptor := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			t.Error(err)
			return
		}
		switch r.URL.Path {
		case "/v1/challenge":
			// Forward verbatim to the genuine peer and return its answer.
			//
			// The attacker does not validate the peer's certificate — it has no
			// reason to. Using a validating client here would make the test pass
			// for the wrong reason: the interception would fail on the
			// attacker's own TLS strictness rather than on this node's pin.
			attacker := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // #nosec G402 -- this is the attacker in a test
				MinVersion:         tls.VersionTLS13,
			}}}
			forwarded, err := attacker.Post(
				genuine.server.URL+"/v1/challenge", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Logf("interceptor could not reach the genuine peer: %v", err)
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			defer func() { _ = forwarded.Body.Close() }()
			answer, err := io.ReadAll(io.LimitReader(forwarded.Body, 1<<20))
			if err != nil {
				t.Error(err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(answer)
		case "/v1/heartbeat":
			var envelope protocol.Envelope
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Error(err)
				return
			}
			stolen = append(stolen, envelope)
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	interceptor.TLS = &tls.Config{
		Certificates: []tls.Certificate{certificateFor(t, interceptorPrivate, interceptorKey, peerID)},
		MinVersion:   tls.VersionTLS13,
	}
	interceptor.StartTLS()
	t.Cleanup(interceptor.Close)
	interceptorHost, err := url.Parse(interceptor.URL)
	if err != nil {
		t.Fatal(err)
	}

	// Discovery points at the interceptor; the trust store holds the genuine key.
	trust(t, store, peerID, interceptorHost.Host, genuine.public)
	publish(t, store, "claude:secret", model.Audience{Mode: model.AudienceAllPaired})

	result, err := publisherFor(t, store).PublishOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(stolen) != 0 {
		t.Fatalf("an interceptor relaying the genuine peer's proof captured session metadata: %s",
			stolen[0].Payload)
	}
	if result.Delivered != 0 {
		t.Errorf("result = %+v; the delivery must not have succeeded", result)
	}
}

// TestPrivateNetworksRefusesNames pins the rule that closes a TOCTOU.
//
// Resolving a name here would check one answer and dial another: this policy
// would resolve it, then net/http would resolve it again when the connection is
// made. A name that answers private at check time and public at dial time sends
// the TCP connection and the TLS ClientHello — carrying the name in SNI — to a
// host the owner never chose. The pinned key stops session metadata following,
// but a check that can be satisfied by one answer and acted on with another is
// not a check.
func TestPrivateNetworksRefusesNames(t *testing.T) {
	for _, address := range []string{
		"peer.local:7463",
		"localhost:7463",
		"my-laptop:7463",
		"example.com:7463",
	} {
		if err := PrivateNetworks(nil)(address); err == nil {
			t.Errorf("PrivateNetworks(%q) = nil; a name must be refused, not resolved", address)
		}
	}
}

// TestPrivateNetworksAcceptsPrivateLiterals is the other half: refusing names
// must not refuse the addresses this policy exists to allow.
func TestPrivateNetworksAcceptsPrivateLiterals(t *testing.T) {
	for _, address := range []string{
		"127.0.0.1:7463",
		"[::1]:7463",
		"192.168.0.73:7463",
		"10.0.0.5:7463",
		"172.16.0.1:7463",
		"[fd00::1]:7463",
		"169.254.10.20:7463",
	} {
		if err := PrivateNetworks(nil)(address); err != nil {
			t.Errorf("PrivateNetworks(%q) = %v", address, err)
		}
	}
}

// TestPrivateNetworksRefusesPublicLiterals keeps the policy from becoming
// "anything that parses".
func TestPrivateNetworksRefusesPublicLiterals(t *testing.T) {
	for _, address := range []string{
		"203.0.113.10:7463",
		"[2001:db8::1]:7463",
		"100.64.0.1:7463",
		"203.0.113.2:7463",
		"[::ffff:203.0.113.1]:7463",
	} {
		if err := PrivateNetworks(nil)(address); err == nil {
			t.Errorf("PrivateNetworks(%q) = nil; a public address must be refused", address)
		}
	}
}

// TestA503LeavesTheMessageQueued is the sender-side half of the full-inbox
// design, and until this test nothing exercised it.
//
// The whole 503 decision rests on the sender treating it as transient: a full
// inbox defers rather than refuses precisely so the message survives. That
// property lived in the interaction between two packages and was enforced by no
// test — an edit classifying 503 as a refusal would have passed the suite while
// silently destroying messages.
func TestA503LeavesTheMessageQueued(t *testing.T) {
	store := openStore(t)
	peerID := "node_peeraaaaaaaaaaaa"
	peer := newCapture(t, peerID)
	peer.messageStatus = http.StatusServiceUnavailable
	trust(t, store, peerID, peer.address(t), peer.public)

	ctx := context.Background()
	if _, err := store.TrustedNode(ctx, peerID); err != nil {
		t.Fatal(err)
	}
	queued, err := store.QueueOutbound(ctx, registry.OutboundMessage{
		DestinationNodeID: peerID, To: "codex:theirs", Body: "please keep me",
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := publisherFor(t, store).DeliverMessages(ctx)
	if err != nil {
		t.Fatalf("DeliverMessages() error = %v", err)
	}
	if result.Delivered != 0 {
		t.Errorf("result = %+v; a 503 is not a delivery", result)
	}

	after, err := store.OutboundFor(ctx, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != registry.OutboundPending {
		t.Fatalf("state = %q; a 503 must leave the message queued, not settle it — "+
			"settling would destroy a message the recipient never took", after.State)
	}
	if after.Attempts == 0 {
		t.Error("the failed attempt was not recorded, so the owner cannot see why it is stuck")
	}
	if after.LastError == "" {
		t.Error("a stuck message does not say why")
	}
}

// TestARefusedAckSettlesTheMessage is the other side of the same distinction:
// a decision the recipient's owner made is terminal, and retrying it forever
// would be pointless traffic.
func TestARefusedAckSettlesTheMessage(t *testing.T) {
	store := openStore(t)
	peerID := "node_peeraaaaaaaaaaaa"
	peer := newCapture(t, peerID)
	peer.refuseMessages = true
	trust(t, store, peerID, peer.address(t), peer.public)

	ctx := context.Background()
	queued, err := store.QueueOutbound(ctx, registry.OutboundMessage{
		DestinationNodeID: peerID, To: "codex:theirs", Body: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := publisherFor(t, store).DeliverMessages(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := store.OutboundFor(ctx, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != registry.OutboundRefused {
		t.Fatalf("state = %q; a refusal is a decision and is terminal", after.State)
	}
}

// TestADeliveredAckSettlesTheMessage keeps the happy path from being the one
// nobody checks.
func TestADeliveredAckSettlesTheMessage(t *testing.T) {
	store := openStore(t)
	peerID := "node_peeraaaaaaaaaaaa"
	peer := newCapture(t, peerID)
	trust(t, store, peerID, peer.address(t), peer.public)

	ctx := context.Background()
	queued, err := store.QueueOutbound(ctx, registry.OutboundMessage{
		DestinationNodeID: peerID, To: "codex:theirs", Body: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := publisherFor(t, store).DeliverMessages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Delivered != 1 {
		t.Fatalf("result = %+v; want one delivery", result)
	}
	after, err := store.OutboundFor(ctx, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != registry.OutboundDelivered {
		t.Fatalf("state = %q; want delivered", after.State)
	}
	if len(peer.messages) != 1 || peer.messages[0].Body != "hello" {
		t.Fatalf("the peer received %#v", peer.messages)
	}
}

// TestDeliveryFollowsTheSameDeclaration keeps the listen side and the delivery
// side from disagreeing about what is private.
//
// If they could disagree, a node could be configured to serve on an address it
// would then refuse to deliver to, or the reverse — and the owner would see a
// peer that is configured, reachable, and silently never contacted.
func TestDeliveryFollowsTheSameDeclaration(t *testing.T) {
	const address = "203.0.113.2:7463"

	if err := PrivateNetworks(nil)(address); err == nil {
		t.Fatal("a public address was deliverable with nothing declared")
	}

	declared, err := nodeconfig.ParsePrivateRanges([]string{"203.0.113.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if err := PrivateNetworks(declared)(address); err != nil {
		t.Fatalf("PrivateNetworks(declared)(%q) = %v", address, err)
	}
	// And the listen side agrees, which is the property that matters.
	if err := nodeconfig.ValidatePeerListen(address, true, declared); err != nil {
		t.Fatalf("the listen side refused what delivery accepts: %v", err)
	}

	// Still no names, declaration or not.
	if err := PrivateNetworks(declared)("peer.local:7463"); err == nil {
		t.Error("a name was accepted because a range was declared")
	}
}
