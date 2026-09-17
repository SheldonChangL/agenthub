package api

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"agenthub.local/agenthub/internal/id"
	"agenthub.local/agenthub/internal/identity"
	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/pairing"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
	"agenthub.local/agenthub/internal/transport"
)

// pairNode is one whole node for these tests: a store, an owner API, and a real
// TLS peer listener presenting the node's own identity key.
//
// Real TLS rather than a handler called directly, because half of what this
// exchange checks is which key terminated the connection. A test that skipped
// the handshake would pass while the one assertion that catches a machine in
// the middle was never made.
type pairNode struct {
	t       *testing.T
	store   *registry.Registry
	server  *Server
	owner   http.Handler
	peer    *httptest.Server
	dbPath  string
	keypair identity.Keypair
	node    model.NodeIdentity
}

func newPairNode(t *testing.T, name string) *pairNode {
	t.Helper()
	ctx := context.Background()
	directory := t.TempDir()
	keypair, err := identity.LoadOrCreateKeypair(directory)
	if err != nil {
		t.Fatal(err)
	}
	nodeID, err := id.New("node_")
	if err != nil {
		t.Fatal(err)
	}
	node := model.NodeIdentity{
		ID: nodeID, DisplayName: name, Platform: "test",
		PublicKey: identity.EncodePublicKey(keypair.Public), Fingerprint: keypair.Fingerprint(),
	}
	dbPath := filepath.Join(directory, "agenthub.db")
	store, err := registry.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// The peer listener has to exist before the server, because the server
	// claims its address, and the server has to exist before the listener can
	// serve anything. The indirection is that knot, untied once.
	var peerHandler http.Handler
	peer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peerHandler.ServeHTTP(w, r)
	}))
	certificate, err := keypair.TLSCertificate(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	peer.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13}
	peer.StartTLS()
	t.Cleanup(peer.Close)

	heartbeats := protocol.NewHeartbeatBuilder(store, node, keypair)
	server := NewServer(store, nil, heartbeats, node,
		WithPairing(pairing.NewMode(), nil, nil),
		WithPairExchange(pairing.NewRequests(), transport.NewPairDialer(transport.LoopbackOnly),
			peer.Listener.Addr().String()),
	)
	peerHandler = server.PeerHandler()
	return &pairNode{
		t: t, store: store, server: server, owner: server.Handler(),
		peer: peer, dbPath: dbPath, keypair: keypair, node: node,
	}
}

func (n *pairNode) address() string { return n.peer.Listener.Addr().String() }

// openWindow is the owner consenting to be asked.
func (n *pairNode) openWindow() {
	n.t.Helper()
	if _, err := n.server.pairing.Open(pairing.DefaultWindow); err != nil {
		n.t.Fatal(err)
	}
}

func (n *pairNode) closeWindow() { n.server.pairing.Close() }

// requests reads this node's own view of the exchange, through the owner API,
// which is also what refreshes an outgoing request.
func (n *pairNode) requests() []pairing.Request {
	n.t.Helper()
	response := perform(n.t, n.owner, http.MethodGet, "/v1/pair/requests", nil)
	if response.Code != http.StatusOK {
		n.t.Fatalf("list pairing requests = %d %s", response.Code, response.Body.String())
	}
	var decoded struct {
		Requests []pairing.Request `json:"requests"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		n.t.Fatal(err)
	}
	return decoded.Requests
}

func (n *pairNode) request(id string) pairing.Request {
	n.t.Helper()
	for _, row := range n.requests() {
		if row.ID == id {
			return row
		}
	}
	n.t.Fatalf("node %s has no pairing request %s", n.node.DisplayName, id)
	return pairing.Request{}
}

func (n *pairNode) trusted() []registry.TrustedNode {
	n.t.Helper()
	nodes, err := n.store.TrustedNodes(context.Background())
	if err != nil {
		n.t.Fatal(err)
	}
	return nodes
}

// audienceRows counts the grants in this node's database.
//
// Read straight out of the table rather than through an endpoint, because the
// claim being tested is about the table: pairing writes identity and nothing
// else, and a grant that appeared here would be a peer able to see sessions
// nobody published to it.
func (n *pairNode) audienceRows() int {
	n.t.Helper()
	db, err := sql.Open("sqlite", n.dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		n.t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM session_audience`).Scan(&count); err != nil {
		n.t.Fatal(err)
	}
	return count
}

// startRequest is `ah pair request <address>` on this node.
func (n *pairNode) startRequest(address, name string) *httptest.ResponseRecorder {
	n.t.Helper()
	return perform(n.t, n.owner, http.MethodPost, "/v1/pair/requests",
		map[string]string{"address": address, "name": name})
}

func (n *pairNode) decide(t *testing.T, requestID, verb string, wantCode int) {
	t.Helper()
	response := perform(t, n.owner, http.MethodPost,
		"/v1/pair/requests/"+requestID+"/"+verb, map[string]string{})
	if response.Code != wantCode {
		t.Fatalf("%s %s on %s = %d %s; want %d",
			verb, requestID, n.node.DisplayName, response.Code, response.Body.String(), wantCode)
	}
}

// TestPairingExchangeTrustsBothSidesAfterTwoConfirmations walks the whole
// exchange between two nodes and checks what each of them ends up holding.
//
// The assertions that matter are the two fingerprints: each side must display
// the fingerprint of the key the *other* side actually holds, derived here from
// that node's keypair rather than from anything that travelled. That is the
// value a person compares, and it is the only thing standing between this
// exchange and trusting whoever answered.
func TestPairingExchangeTrustsBothSidesAfterTwoConfirmations(t *testing.T) {
	requester := newPairNode(t, "asks")
	receiver := newPairNode(t, "decides")
	receiver.openWindow()

	started := requester.startRequest(receiver.address(), "the other laptop")
	if started.Code != http.StatusCreated {
		t.Fatalf("start pairing request = %d %s", started.Code, started.Body.String())
	}
	var outgoing pairing.Request
	if err := json.Unmarshal(started.Body.Bytes(), &outgoing); err != nil {
		t.Fatal(err)
	}

	// What each owner is shown, before anybody has decided anything.
	incoming := receiver.request(outgoing.ID)
	checks := []struct {
		name string
		got  string
		want string
	}{
		{"requester shows the receiver's fingerprint", outgoing.Fingerprint, identity.Fingerprint(receiver.keypair.Public)},
		{"requester shows its own", outgoing.LocalFingerprint, identity.Fingerprint(requester.keypair.Public)},
		{"receiver shows the requester's fingerprint", incoming.Fingerprint, identity.Fingerprint(requester.keypair.Public)},
		{"receiver shows its own", incoming.LocalFingerprint, identity.Fingerprint(receiver.keypair.Public)},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Errorf("%s: got %q, want %q", check.name, check.got, check.want)
		}
	}
	if incoming.Direction != pairing.Incoming || outgoing.Direction != pairing.Outgoing {
		t.Fatalf("directions = %q and %q", incoming.Direction, outgoing.Direction)
	}
	// Nothing is trusted yet. Both owners have only been shown something.
	if len(requester.trusted()) != 0 || len(receiver.trusted()) != 0 {
		t.Fatalf("something was trusted before anyone confirmed: %v %v",
			requester.trusted(), receiver.trusted())
	}

	receiver.decide(t, outgoing.ID, "approve", http.StatusOK)
	// The receiving owner's approval writes the receiving machine's trust store
	// and nothing else. The requester has still compared nothing.
	if len(requester.trusted()) != 0 {
		t.Fatalf("an approval on the other machine trusted something here: %v", requester.trusted())
	}
	if state := requester.request(outgoing.ID).State; state != pairing.StateAwaitingConfirm {
		t.Fatalf("after the approval the requester is %q, want %q", state, pairing.StateAwaitingConfirm)
	}
	requester.decide(t, outgoing.ID, "confirm", http.StatusOK)

	for _, pair := range []struct {
		name   string
		holder *pairNode
		other  *pairNode
	}{
		{"receiver", receiver, requester},
		{"requester", requester, receiver},
	} {
		trusted := pair.holder.trusted()
		if len(trusted) != 1 {
			t.Fatalf("%s trusts %d nodes, want 1", pair.name, len(trusted))
		}
		stored := trusted[0]
		if stored.NodeID != pair.other.node.ID {
			t.Errorf("%s trusts %q, want %q", pair.name, stored.NodeID, pair.other.node.ID)
		}
		if stored.PublicKey != identity.EncodePublicKey(pair.other.keypair.Public) {
			t.Errorf("%s stored a key that is not %s's", pair.name, pair.other.node.DisplayName)
		}
		if stored.Fingerprint != identity.Fingerprint(pair.other.keypair.Public) {
			t.Errorf("%s stored fingerprint %q, want %q",
				pair.name, stored.Fingerprint, identity.Fingerprint(pair.other.keypair.Public))
		}
		if stored.Address != pair.other.address() {
			t.Errorf("%s stored address %q, want %q", pair.name, stored.Address, pair.other.address())
		}
		// Pairing is identity and nothing else. A grant here would mean two
		// machines that pair can see each other's sessions without anybody
		// having published one.
		if rows := pair.holder.audienceRows(); rows != 0 {
			t.Errorf("%s has %d session_audience rows after pairing, want 0", pair.name, rows)
		}
	}
	// The name the owner typed is the name they see, not the one the far side
	// chose for itself.
	if name := requester.trusted()[0].DisplayName; name != "the other laptop" {
		t.Errorf("stored display name = %q, want the name the owner gave", name)
	}
}

// TestPairingRejectionWritesNothingAnywhere covers the acceptance bullet that
// a refusal on either side leaves both trust stores empty, and that the
// requester can tell a refusal from a timeout.
func TestPairingRejectionWritesNothingAnywhere(t *testing.T) {
	requester := newPairNode(t, "asks")
	receiver := newPairNode(t, "decides")
	receiver.openWindow()

	started := requester.startRequest(receiver.address(), "")
	if started.Code != http.StatusCreated {
		t.Fatalf("start pairing request = %d %s", started.Code, started.Body.String())
	}
	var outgoing pairing.Request
	if err := json.Unmarshal(started.Body.Bytes(), &outgoing); err != nil {
		t.Fatal(err)
	}

	receiver.decide(t, outgoing.ID, "reject", http.StatusOK)
	refused := requester.request(outgoing.ID)
	if refused.State != pairing.StateRejected {
		t.Fatalf("requester sees %q after a refusal, want %q", refused.State, pairing.StateRejected)
	}
	if refused.Reason != pairing.ReasonDeclined {
		t.Errorf("reason = %q, want %q: a refusal and a timeout must not read the same",
			refused.Reason, pairing.ReasonDeclined)
	}
	// Confirming a refused request is not a way around the refusal.
	requester.decide(t, outgoing.ID, "confirm", http.StatusConflict)
	if len(requester.trusted()) != 0 || len(receiver.trusted()) != 0 {
		t.Fatalf("a refusal trusted something: %v %v", requester.trusted(), receiver.trusted())
	}
}

// TestPairingExpiresWithTheWindowAndWritesNothing covers the other half of that
// bullet: the owner who closes their pairing window has withdrawn consent, and
// a request collected under it cannot still be approved afterwards.
func TestPairingExpiresWithTheWindowAndWritesNothing(t *testing.T) {
	requester := newPairNode(t, "asks")
	receiver := newPairNode(t, "decides")
	receiver.openWindow()

	started := requester.startRequest(receiver.address(), "")
	if started.Code != http.StatusCreated {
		t.Fatalf("start pairing request = %d %s", started.Code, started.Body.String())
	}
	var outgoing pairing.Request
	if err := json.Unmarshal(started.Body.Bytes(), &outgoing); err != nil {
		t.Fatal(err)
	}

	receiver.closeWindow()
	if state := receiver.request(outgoing.ID).State; state != pairing.StateExpired {
		t.Fatalf("after the window closed the request is %q, want %q", state, pairing.StateExpired)
	}
	// Approving it now is refused, and the refusal says what state it is in.
	receiver.decide(t, outgoing.ID, "approve", http.StatusConflict)

	expired := requester.request(outgoing.ID)
	if expired.State != pairing.StateExpired {
		t.Fatalf("requester sees %q, want %q", expired.State, pairing.StateExpired)
	}
	if expired.Reason != pairing.ReasonExpired {
		t.Errorf("reason = %q, want %q", expired.Reason, pairing.ReasonExpired)
	}
	requester.decide(t, outgoing.ID, "confirm", http.StatusConflict)
	if len(requester.trusted()) != 0 || len(receiver.trusted()) != 0 {
		t.Fatalf("an expired request trusted something: %v %v",
			requester.trusted(), receiver.trusted())
	}
}

// TestPairingRefusedWhileTheWindowIsClosed covers the acceptance bullet that a
// node which is not pairing stores nothing at all.
func TestPairingRefusedWhileTheWindowIsClosed(t *testing.T) {
	requester := newPairNode(t, "asks")
	receiver := newPairNode(t, "decides")
	// No openWindow: the receiving owner has not consented to anything.

	started := requester.startRequest(receiver.address(), "")
	if started.Code != http.StatusConflict {
		t.Fatalf("start against a closed window = %d %s", started.Code, started.Body.String())
	}
	if code := errorCode(t, started.Body.Bytes()); code != "PEER_PAIRING_CLOSED" {
		t.Errorf("error code = %q, want PEER_PAIRING_CLOSED", code)
	}
	if rows := receiver.requests(); len(rows) != 0 {
		t.Fatalf("a closed node stored %d pairing requests, want 0", len(rows))
	}
	if rows := requester.requests(); len(rows) != 0 {
		t.Fatalf("a refused request was recorded here: %v", rows)
	}
}

// TestPeerSurfaceCannotApproveOrConfirm is the boundary the whole design rests
// on: the decision is the owner's, and a peer that could reach it would be
// approving itself.
func TestPeerSurfaceCannotApproveOrConfirm(t *testing.T) {
	node := newPairNode(t, "decides")
	peer := node.server.PeerHandler()
	for _, path := range []string{
		"/v1/pair/requests/pair_anything/approve",
		"/v1/pair/requests/pair_anything/confirm",
		"/v1/pair/requests/pair_anything/reject",
		"/v1/nodes",
	} {
		response := perform(t, peer, http.MethodPost, path, map[string]string{})
		if response.Code != http.StatusNotFound && response.Code != http.StatusMethodNotAllowed {
			t.Errorf("peer surface answered %s with %d %s; it must not be routed at all",
				path, response.Code, response.Body.String())
		}
	}
}

// TestPairingAbortsWhenTheTLSKeyIsNotTheDescribedKey is the machine in the
// middle, simulated: something terminates the TLS connection with its own key
// and passes on the real machine's descriptor.
//
// The requester must abort. If it did not, the fingerprint it showed its owner
// would be the real machine's while the key it was actually talking to was the
// attacker's — a comparison that succeeds and proves nothing.
func TestPairingAbortsWhenTheTLSKeyIsNotTheDescribedKey(t *testing.T) {
	requester := newPairNode(t, "asks")
	// The descriptor names a key nobody on this connection holds.
	elsewhere, err := identity.LoadOrCreateKeypair(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	victimID, err := id.New("node_")
	if err != nil {
		t.Fatal(err)
	}
	middle := fakePeer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"requestId": "pair_relayed",
			"node": protocol.NodeDescriptor{
				NodeID: victimID, DisplayName: "the machine you meant", Platform: "test",
				PublicKey:   identity.EncodePublicKey(elsewhere.Public),
				Fingerprint: identity.Fingerprint(elsewhere.Public),
			},
		})
	})

	started := requester.startRequest(middle, "")
	if started.Code != http.StatusBadGateway {
		t.Fatalf("relayed pairing = %d %s; want 502", started.Code, started.Body.String())
	}
	if code := errorCode(t, started.Body.Bytes()); code != "PEER_KEY_MISMATCH" {
		t.Errorf("error code = %q, want PEER_KEY_MISMATCH", code)
	}
	if rows := requester.requests(); len(rows) != 0 {
		t.Fatalf("a relayed exchange was recorded: %v", rows)
	}
	if len(requester.trusted()) != 0 {
		t.Fatalf("a relayed exchange trusted something: %v", requester.trusted())
	}
}

// TestPairingAgainstAnOlderNodeSaysSo covers the wire-compatibility bullet: a
// node from before this route 404s, and "404" is not something an owner can act
// on.
func TestPairingAgainstAnOlderNodeSaysSo(t *testing.T) {
	requester := newPairNode(t, "asks")
	older := fakePeer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "no such route")
	})

	started := requester.startRequest(older, "")
	if started.Code != http.StatusConflict {
		t.Fatalf("pairing with an older node = %d %s", started.Code, started.Body.String())
	}
	if code := errorCode(t, started.Body.Bytes()); code != "PEER_TOO_OLD" {
		t.Fatalf("error code = %q, want PEER_TOO_OLD", code)
	}
	message := errorMessage(t, started.Body.Bytes())
	for _, want := range []string{"before the pairing exchange existed", "ah pair"} {
		if !strings.Contains(message, want) {
			t.Errorf("message %q does not say %q", message, want)
		}
	}
}

// fakePeer is a TLS listener that is not an AgentHub node, presenting a key of
// its own, and returns its address.
func fakePeer(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	keypair, err := identity.LoadOrCreateKeypair(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	nodeID, err := id.New("node_")
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := keypair.TLSCertificate(nodeID)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server.Listener.Addr().String()
}

func errorCode(t *testing.T, body []byte) string {
	t.Helper()
	return decodeError(t, body).Error.Code
}

func errorMessage(t *testing.T, body []byte) string {
	t.Helper()
	return decodeError(t, body).Error.Message
}

func decodeError(t *testing.T, body []byte) struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
} {
	t.Helper()
	var decoded struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode error body %q: %v", body, err)
	}
	return decoded
}
