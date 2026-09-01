package api

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/identity"
	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
)

// senderSigner signs as a peer would. The receiver never sees the private key;
// it verifies with the public key the owner recorded when pairing.
type senderSigner struct{ private ed25519.PrivateKey }

func (s senderSigner) Sign(message []byte) []byte { return ed25519.Sign(s.private, message) }

type sender struct {
	nodeID string
	signer senderSigner
	public ed25519.PublicKey
}

func newSender(t *testing.T, nodeID string) sender {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return sender{nodeID: nodeID, signer: senderSigner{private: private}, public: public}
}

// heartbeatTo builds what the peer would put on the wire for one recipient.
func (s sender) heartbeatTo(t *testing.T, recipient string, sequence uint64, expires time.Time, sessions []protocol.SessionSummary) protocol.Envelope {
	t.Helper()
	if sessions == nil {
		sessions = []protocol.SessionSummary{}
	}
	envelope, err := protocol.NewDirectedEnvelope(s.nodeID, recipient, protocol.TypeNodeHeartbeat,
		protocol.At(time.Now()), protocol.HeartbeatPayload{
			Sequence: sequence, ExpiresAt: expires.UTC(),
			Capabilities: []string{"session.list"}, Sessions: sessions,
		}, s.signer)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func (s sender) pairWith(t *testing.T, handler http.Handler) {
	t.Helper()
	encoded := identity.EncodePublicKey(s.public)
	response := perform(t, handler, http.MethodPost, "/v1/nodes", map[string]string{
		"nodeId": s.nodeID, "displayName": "peer", "platform": "linux/amd64",
		"publicKey": encoded, "confirmedFingerprint": identity.Fingerprint(s.public),
	})
	if response.Code != http.StatusCreated && response.Code != http.StatusOK {
		t.Fatalf("pairing failed: %d %s", response.Code, response.Body.String())
	}
}

func summary(id string) protocol.SessionSummary {
	return protocol.SessionSummary{
		ID:         id,
		Provider:   "claude",
		Status:     "idle",
		Visibility: "public",
		LastSeenAt: time.Now().UTC().Truncate(time.Second),
	}
}

// testSurfaces returns the two handlers a node actually serves. They are
// separate listeners in production — the owner's management API and the peer
// surface — so a test that drove both through one handler would not be testing
// the deployed shape.
func testSurfaces(t *testing.T) (*registry.Registry, http.Handler, http.Handler) {
	t.Helper()
	ctx := context.Background()
	store, err := registry.Open(ctx, filepath.Join(t.TempDir(), "agenthub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	node := model.NodeIdentity{ID: testNodeID, DisplayName: "test", Platform: "test"}
	heartbeats := protocol.NewHeartbeatBuilder(store, node, apiTestSigner{})
	server := NewServer(store, nil, heartbeats, node)
	return store, server.Handler(), server.PeerHandler()
}

const peerNodeID = "node_peer0000000000000"

// TestUnpairedNodeGetsNothing is issue #14's "an unpaired node cannot obtain any
// session data". It also checks the weaker property the endpoint must hold: the
// refusal says nothing that distinguishes an unknown sender from a bad
// signature, so it cannot be used to probe who this owner has paired with.
func TestUnpairedNodeGetsNothing(t *testing.T) {
	store, owner, peers := testSurfaces(t)
	stranger := newSender(t, peerNodeID)

	envelope := stranger.heartbeatTo(t, testNodeID, 1, time.Now().Add(time.Minute), []protocol.SessionSummary{summary("node_x/claude:a")})
	response := perform(t, peers, http.MethodPost, "/v1/heartbeat", envelope)
	if response.Code != http.StatusForbidden {
		t.Fatalf("response = %d %s; an unpaired sender must be refused", response.Code, response.Body.String())
	}
	unpairedBody := response.Body.String()

	if _, found, err := store.PeerSnapshotFor(context.Background(), peerNodeID); err != nil || found {
		t.Fatalf("a refused heartbeat was stored (found = %v, err = %v)", found, err)
	}

	// Now pair, then send an envelope signed by a different key. The refusal
	// must be indistinguishable from the unpaired one.
	stranger.pairWith(t, owner)
	impostor := newSender(t, peerNodeID)
	forged := impostor.heartbeatTo(t, testNodeID, 1, time.Now().Add(time.Minute), nil)
	forgedResponse := perform(t, peers, http.MethodPost, "/v1/heartbeat", forged)
	if forgedResponse.Code != http.StatusForbidden {
		t.Fatalf("a forged signature was not refused: %d %s", forgedResponse.Code, forgedResponse.Body.String())
	}
	if forgedResponse.Body.String() != unpairedBody {
		t.Errorf("refusals differ and can be used to probe the trust store:\n unpaired: %s\n forged:   %s",
			unpairedBody, forgedResponse.Body.String())
	}
}

// TestHeartbeatBuiltForAnotherNodeIsRefused is the reason PR #24 put the
// recipient inside the signature: without this check, a snapshot a peer built
// for one node is a valid snapshot for every node that trusts that peer.
func TestHeartbeatBuiltForAnotherNodeIsRefused(t *testing.T) {
	store, owner, peers := testSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)

	misdirected := peer.heartbeatTo(t, "node_somebodyelse0000", 1, time.Now().Add(time.Minute),
		[]protocol.SessionSummary{summary("node_x/claude:secret")})
	response := perform(t, peers, http.MethodPost, "/v1/heartbeat", misdirected)
	if response.Code != http.StatusForbidden {
		t.Fatalf("response = %d %s; an envelope addressed elsewhere must be refused",
			response.Code, response.Body.String())
	}
	if _, found, err := store.PeerSnapshotFor(context.Background(), peerNodeID); err != nil || found {
		t.Fatalf("a redirected heartbeat was stored (found = %v, err = %v)", found, err)
	}
}

// TestSnapshotReplacesAndRevocationByOmissionTakesEffect is issue #17 end to
// end: the second snapshot replaces the first, and a session that vanished from
// the array stops being reported.
func TestSnapshotReplacesAndRevocationByOmissionTakesEffect(t *testing.T) {
	_, owner, peers := testSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	expires := time.Now().Add(time.Minute)

	first := peer.heartbeatTo(t, testNodeID, 1, expires,
		[]protocol.SessionSummary{summary("node_p/claude:kept"), summary("node_p/claude:withdrawn")})
	if response := perform(t, peers, http.MethodPost, "/v1/heartbeat", first); response.Code != http.StatusNoContent {
		t.Fatalf("first heartbeat = %d %s", response.Code, response.Body.String())
	}
	if ids := peerSessionIDs(t, owner, peerNodeID); len(ids) != 2 {
		t.Fatalf("sessions after the first heartbeat = %v; want both", ids)
	}

	second := peer.heartbeatTo(t, testNodeID, 2, expires,
		[]protocol.SessionSummary{summary("node_p/claude:kept")})
	if response := perform(t, peers, http.MethodPost, "/v1/heartbeat", second); response.Code != http.StatusNoContent {
		t.Fatalf("second heartbeat = %d %s", response.Code, response.Body.String())
	}
	ids := peerSessionIDs(t, owner, peerNodeID)
	if len(ids) != 1 || ids[0] != "node_p/claude:kept" {
		t.Fatalf("sessions = %v; the withdrawn session survived, so the consumer merged instead of replacing", ids)
	}
}

// TestReplayedAndExpiredHeartbeatsAreRefused is issue #11's remaining item.
func TestReplayedAndExpiredHeartbeatsAreRefused(t *testing.T) {
	_, owner, peers := testSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)
	expires := time.Now().Add(time.Minute)

	live := peer.heartbeatTo(t, testNodeID, 2, expires, []protocol.SessionSummary{summary("node_p/claude:live")})
	if response := perform(t, peers, http.MethodPost, "/v1/heartbeat", live); response.Code != http.StatusNoContent {
		t.Fatalf("live heartbeat = %d %s", response.Code, response.Body.String())
	}

	t.Run("an exact replay of the accepted delivery", func(t *testing.T) {
		response := perform(t, peers, http.MethodPost, "/v1/heartbeat", live)
		if response.Code != http.StatusConflict {
			t.Fatalf("response = %d %s; want the replay refused", response.Code, response.Body.String())
		}
	})

	t.Run("a rolled-back sequence carrying a stale view", func(t *testing.T) {
		rolledBack := peer.heartbeatTo(t, testNodeID, 1, expires,
			[]protocol.SessionSummary{summary("node_p/claude:stale")})
		if response := perform(t, peers, http.MethodPost, "/v1/heartbeat", rolledBack); response.Code != http.StatusConflict {
			t.Fatalf("response = %d %s; want the rollback refused", response.Code, response.Body.String())
		}
		ids := peerSessionIDs(t, owner, peerNodeID)
		if len(ids) != 1 || ids[0] != "node_p/claude:live" {
			t.Fatalf("sessions = %v; a rolled-back heartbeat overwrote the live snapshot", ids)
		}
	})

	t.Run("an already expired heartbeat", func(t *testing.T) {
		stale := peer.heartbeatTo(t, testNodeID, 3, time.Now().Add(-time.Second), nil)
		if response := perform(t, peers, http.MethodPost, "/v1/heartbeat", stale); response.Code != http.StatusConflict {
			t.Fatalf("response = %d %s; want the expired heartbeat refused", response.Code, response.Body.String())
		}
		ids := peerSessionIDs(t, owner, peerNodeID)
		if len(ids) != 1 || ids[0] != "node_p/claude:live" {
			t.Fatalf("sessions = %v; an expired heartbeat replaced the live snapshot", ids)
		}
	})
}

// TestPeerGoesOfflineWhenItsHeartbeatExpires is issue #14's "the peer shows
// offline once the heartbeat expires", and pins that offline is not the same as
// absent: the peer is still listed, with the moment it went quiet.
func TestPeerGoesOfflineWhenItsHeartbeatExpires(t *testing.T) {
	store, owner, _ := testSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)

	// Store directly so the expiry can be placed in the past without waiting.
	payload, err := json.Marshal(protocol.HeartbeatPayload{
		Sequence: 1, ExpiresAt: time.Now().Add(-time.Second).UTC(),
		Capabilities: []string{"session.list"},
		Sessions:     []protocol.SessionSummary{summary("node_p/claude:gone")},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	past := time.Now().Add(-time.Hour).UTC()
	if err := store.StorePeerSnapshot(ctx, registry.PeerSnapshot{
		NodeID: peerNodeID, Sequence: 1, ExpiresAt: past.Add(time.Minute), Payload: payload,
	}, past); err != nil {
		t.Fatalf("StorePeerSnapshot() error = %v", err)
	}

	views := decodePeers(t, owner)
	if len(views) != 1 {
		t.Fatalf("peers = %#v; an expired peer must still be listed", views)
	}
	if views[0].Online {
		t.Error("a peer whose heartbeat expired is reported online")
	}
	if len(views[0].Sessions) != 0 {
		t.Errorf("sessions = %v; an expired snapshot must not still be shown", views[0].Sessions)
	}
	if views[0].ReceivedAt == nil {
		t.Error("the moment the peer was last heard from is not reported")
	}
}

// TestRevokingAPeerRemovesItsView pins that withdrawing trust withdraws what
// the peer published, not just its ability to publish more.
func TestRevokingAPeerRemovesItsView(t *testing.T) {
	_, owner, peers := testSurfaces(t)
	peer := newSender(t, peerNodeID)
	peer.pairWith(t, owner)

	live := peer.heartbeatTo(t, testNodeID, 1, time.Now().Add(time.Minute),
		[]protocol.SessionSummary{summary("node_p/claude:visible")})
	if response := perform(t, peers, http.MethodPost, "/v1/heartbeat", live); response.Code != http.StatusNoContent {
		t.Fatalf("heartbeat = %d %s", response.Code, response.Body.String())
	}
	if ids := peerSessionIDs(t, owner, peerNodeID); len(ids) != 1 {
		t.Fatalf("sessions before revoke = %v", ids)
	}

	if response := perform(t, owner, http.MethodDelete, "/v1/nodes/"+peerNodeID, nil); response.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", response.Code, response.Body.String())
	}
	views := decodePeers(t, owner)
	if len(views) != 0 {
		t.Fatalf("peers after revoke = %#v; a revoked node must not remain listed", views)
	}
}

type peerView struct {
	NodeID     string                    `json:"nodeId"`
	Online     bool                      `json:"online"`
	Sequence   uint64                    `json:"sequence"`
	ReceivedAt *time.Time                `json:"receivedAt"`
	Sessions   []protocol.SessionSummary `json:"sessions"`
}

func decodePeers(t *testing.T, handler http.Handler) []peerView {
	t.Helper()
	response := perform(t, handler, http.MethodGet, "/v1/peers", nil)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /v1/peers = %d %s", response.Code, response.Body.String())
	}
	var decoded struct {
		Peers []peerView `json:"peers"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("decode peers: %v (body %s)", err, response.Body.String())
	}
	return decoded.Peers
}

func peerSessionIDs(t *testing.T, handler http.Handler, nodeID string) []string {
	t.Helper()
	for _, view := range decodePeers(t, handler) {
		if view.NodeID != nodeID {
			continue
		}
		ids := make([]string, 0, len(view.Sessions))
		for _, session := range view.Sessions {
			ids = append(ids, session.ID)
		}
		return ids
	}
	return nil
}

// TestChallengeEndpointProvesKeyPossession covers the endpoint a sender uses to
// confirm it is talking to the peer it thinks it is.
func TestChallengeEndpointProvesKeyPossession(t *testing.T) {
	_, _, peers := testSurfaces(t)
	nonce, err := protocol.NewChallengeNonce()
	if err != nil {
		t.Fatal(err)
	}
	const challenger = "node_challenger000000"

	response := perform(t, peers, http.MethodPost, "/v1/challenge", map[string]string{
		"nonce": base64.StdEncoding.EncodeToString(nonce), "challengerNodeId": challenger,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	var answer struct {
		NodeID    string `json:"nodeId"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}
	if answer.NodeID != testNodeID {
		t.Errorf("nodeId = %q; want this node's id", answer.NodeID)
	}
	if answer.Signature == "" {
		t.Fatal("the endpoint answered without a signature")
	}
	// The test server signs with a stub, so verifying the bytes here would test
	// the stub. What matters is that the answer is over the challenge form and
	// nothing else, which the protocol tests pin directly.
}

// TestChallengeRefusesAShortNonce keeps the endpoint from signing bytes a
// caller can fully predict.
func TestChallengeRefusesAShortNonce(t *testing.T) {
	_, _, peers := testSurfaces(t)
	for name, nonce := range map[string][]byte{
		"empty":                 {},
		"one byte":              {0x01},
		"one under the minimum": make([]byte, protocol.ChallengeNonceSize-1),
	} {
		t.Run(name, func(t *testing.T) {
			response := perform(t, peers, http.MethodPost, "/v1/challenge", map[string]string{
				"nonce": base64.StdEncoding.EncodeToString(nonce), "challengerNodeId": "node_challenger000000",
			})
			if response.Code != http.StatusBadRequest {
				t.Fatalf("response = %d %s; a short nonce must be refused", response.Code, response.Body.String())
			}
		})
	}
}

// TestChallengeRefusesANonBase64Nonce pins input handling on an endpoint that
// answers anyone.
func TestChallengeRefusesANonBase64Nonce(t *testing.T) {
	_, _, peers := testSurfaces(t)
	response := perform(t, peers, http.MethodPost, "/v1/challenge", map[string]string{
		"nonce": "this is not base64!!", "challengerNodeId": "node_challenger000000",
	})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}

// TestChallengeHandlerSignsTheRealBytesWithTheRealKey closes a gap the shared
// test server leaves open: it signs with a throwaway key, so no test proved the
// HTTP handler signs the correct (nodeId, challengerNodeId, nonce) form with
// the node's actual key. A handler that signed the nonce alone, or swapped the
// two node ids, would satisfy every other test here and be relay-and-replay
// material in production.
func TestChallengeHandlerSignsTheRealBytesWithTheRealKey(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := registry.Open(ctx, filepath.Join(t.TempDir(), "agenthub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	node := model.NodeIdentity{ID: testNodeID, DisplayName: "test", Platform: "test"}
	builder := protocol.NewHeartbeatBuilder(store, node, fixedSigner{private: private})
	peers := NewServer(store, nil, builder, node).PeerHandler()

	nonce, err := protocol.NewChallengeNonce()
	if err != nil {
		t.Fatal(err)
	}
	const challenger = "node_challenger000000"
	response := perform(t, peers, http.MethodPost, "/v1/challenge", map[string]string{
		"nonce": base64.StdEncoding.EncodeToString(nonce), "challengerNodeId": challenger,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	var answer struct {
		NodeID    string `json:"nodeId"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &answer); err != nil {
		t.Fatal(err)
	}

	// The real check: a sender holding only this node's public key must be able
	// to verify the answer.
	if err := protocol.VerifyChallengeAnswer(public, testNodeID, challenger, nonce, answer.Signature); err != nil {
		t.Fatalf("VerifyChallengeAnswer() = %v; the handler did not sign the challenge form", err)
	}
	// And the binding must be real: the same answer must not verify for a
	// different challenger.
	if err := protocol.VerifyChallengeAnswer(public, testNodeID, "node_someoneelse00000", nonce, answer.Signature); err == nil {
		t.Error("the answer verified for a challenger it was not produced for")
	}
}

// fixedSigner signs with one stable key, unlike the shared test signer which
// generates a throwaway per call.
type fixedSigner struct{ private ed25519.PrivateKey }

func (s fixedSigner) Sign(message []byte) []byte { return ed25519.Sign(s.private, message) }

// TestThePeerSurfaceExposesNothingElse is the test that makes opening a port to
// peers a bounded decision.
//
// The owner's API changes who may see a session, revokes peers and sends
// messages. If those lived on the same mux as heartbeats, exposing the peer
// surface would expose them too, and the only thing between a peer and the
// owner's controls would be that nobody had sent the request. This enumerates
// the management routes and requires every one of them to be absent.
func TestThePeerSurfaceExposesNothingElse(t *testing.T) {
	_, _, peers := testSurfaces(t)

	management := []struct{ method, path string }{
		{http.MethodGet, "/v1/sessions"},
		{http.MethodGet, "/v1/sessions/claude:abc"},
		{http.MethodPut, "/v1/sessions/claude:abc/visibility"},
		{http.MethodGet, "/v1/sessions/claude:abc/audience"},
		{http.MethodPut, "/v1/sessions/claude:abc/audience"},
		{http.MethodPost, "/v1/sessions/audience"},
		{http.MethodGet, "/v1/node"},
		{http.MethodGet, "/v1/nodes"},
		{http.MethodPost, "/v1/nodes"},
		{http.MethodDelete, "/v1/nodes/node_peer0000000000000"},
		{http.MethodPut, "/v1/nodes/node_peer0000000000000/address"},
		{http.MethodGet, "/v1/heartbeat"},
		{http.MethodGet, "/v1/peers"},
		{http.MethodPost, "/v1/discover"},
		{http.MethodPost, "/v1/messages"},
		{http.MethodGet, "/v1/inbox/claude:abc"},
	}
	for _, route := range management {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			response := perform(t, peers, route.method, route.path, map[string]string{})
			if response.Code != http.StatusNotFound && response.Code != http.StatusMethodNotAllowed {
				t.Fatalf("the peer surface served %s %s with %d %s",
					route.method, route.path, response.Code, response.Body.String())
			}
		})
	}
}

// TestThePeerSurfaceStillServesItsOwnRoutes keeps the test above honest: a
// handler that served nothing at all would satisfy it.
func TestThePeerSurfaceStillServesItsOwnRoutes(t *testing.T) {
	_, _, peers := testSurfaces(t)
	if response := perform(t, peers, http.MethodGet, "/healthz", nil); response.Code != http.StatusOK {
		t.Errorf("/healthz = %d", response.Code)
	}
	nonce, err := protocol.NewChallengeNonce()
	if err != nil {
		t.Fatal(err)
	}
	response := perform(t, peers, http.MethodPost, "/v1/challenge", map[string]string{
		"nonce": base64.StdEncoding.EncodeToString(nonce), "challengerNodeId": "node_challenger000000",
	})
	if response.Code != http.StatusOK {
		t.Errorf("/v1/challenge = %d %s", response.Code, response.Body.String())
	}
}
