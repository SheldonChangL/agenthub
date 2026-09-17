package api

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	response := perform(n.t, n.owner, http.MethodGet, "/v1/pair/requests?all=true", nil)
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
func (n *pairNode) startRequest(address string) *httptest.ResponseRecorder {
	n.t.Helper()
	return perform(n.t, n.owner, http.MethodPost, "/v1/pair/requests",
		map[string]string{"address": address})
}

// viewOf is one request as the owner API renders it: the ordered fingerprints,
// the next step and the notice, which is what a person actually reads.
func (n *pairNode) viewOf(id string) pairRequestView {
	n.t.Helper()
	response := perform(n.t, n.owner, http.MethodGet, "/v1/pair/requests?all=true", nil)
	if response.Code != http.StatusOK {
		n.t.Fatalf("list pairing requests = %d %s", response.Code, response.Body.String())
	}
	var decoded struct {
		Requests []pairRequestView `json:"requests"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		n.t.Fatal(err)
	}
	for _, row := range decoded.Requests {
		if row.ID == id {
			return row
		}
	}
	n.t.Fatalf("node %s has no pairing request %s", n.node.DisplayName, id)
	return pairRequestView{}
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

	started := requester.startRequest(receiver.address())
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
	// A node is called what it calls itself, on both machines. A local rename
	// here would put a different name on each screen while the exchange asks
	// the owner to check that the two screens agree.
	if name := requester.trusted()[0].DisplayName; name != receiver.node.DisplayName {
		t.Errorf("stored display name = %q, want the peer's own name %q",
			name, receiver.node.DisplayName)
	}
}

// TestPairingRejectionWritesNothingAnywhere covers the acceptance bullet that
// a refusal on either side leaves both trust stores empty, and that the
// requester can tell a refusal from a timeout.
func TestPairingRejectionWritesNothingAnywhere(t *testing.T) {
	requester := newPairNode(t, "asks")
	receiver := newPairNode(t, "decides")
	receiver.openWindow()

	started := requester.startRequest(receiver.address())
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

	started := requester.startRequest(receiver.address())
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

	started := requester.startRequest(receiver.address())
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

	started := requester.startRequest(middle)
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

	started := requester.startRequest(older)
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

// The command the owner was told to run has to work when run exactly as told.
//
// `ah pair request` prints "run ah pair confirm <id> here", and an owner who
// does that without listing first used to be answered "it is pending, not
// awaiting-confirm" — for an approval the other machine had already given.
// Learning of the approval happened only in the list handler. This test does
// not list.
func TestConfirmWorksWithoutListingFirst(t *testing.T) {
	requester := newPairNode(t, "asks")
	receiver := newPairNode(t, "decides")
	receiver.openWindow()

	started := requester.startRequest(receiver.address())
	if started.Code != http.StatusCreated {
		t.Fatalf("start pairing request = %d %s", started.Code, started.Body.String())
	}
	var outgoing pairing.Request
	if err := json.Unmarshal(started.Body.Bytes(), &outgoing); err != nil {
		t.Fatal(err)
	}
	// The receiving owner approves. The requesting owner has read nothing since
	// starting the request, so this node still believes the row is pending.
	receiver.decide(t, outgoing.ID, "approve", http.StatusOK)
	if row, _ := requester.server.pairRequests.Get(outgoing.ID); row.State != pairing.StatePending {
		t.Fatalf("the requester already knows the answer (%q); this test is not testing anything", row.State)
	}

	requester.decide(t, outgoing.ID, "confirm", http.StatusOK)

	trusted := requester.trusted()
	if len(trusted) != 1 || trusted[0].NodeID != receiver.node.ID {
		t.Fatalf("confirming without listing first trusted %v", trusted)
	}
}

// Both machines print the same two fingerprints in the same order, so two
// people reading two screens compare line with line. Each machine printing its
// own first means the order is reversed between them, and a notice claiming
// otherwise is worse than none.
func TestBothMachinesShowTheFingerprintsInOneOrder(t *testing.T) {
	requester := newPairNode(t, "asks")
	receiver := newPairNode(t, "decides")
	receiver.openWindow()

	started := requester.startRequest(receiver.address())
	if started.Code != http.StatusCreated {
		t.Fatalf("start pairing request = %d %s", started.Code, started.Body.String())
	}
	var outgoing pairing.Request
	if err := json.Unmarshal(started.Body.Bytes(), &outgoing); err != nil {
		t.Fatal(err)
	}

	here := requester.viewOf(outgoing.ID)
	there := receiver.viewOf(outgoing.ID)
	if len(here.Fingerprints) != 2 || len(there.Fingerprints) != 2 {
		t.Fatalf("each machine must show both fingerprints: %v / %v", here.Fingerprints, there.Fingerprints)
	}
	for i, want := range []struct {
		role        string
		machine     string
		fingerprint string
	}{
		{"requester", requester.node.DisplayName, identity.Fingerprint(requester.keypair.Public)},
		{"receiver", receiver.node.DisplayName, identity.Fingerprint(receiver.keypair.Public)},
	} {
		for name, got := range map[string]pairFingerprintView{
			"requesting machine": here.Fingerprints[i],
			"receiving machine":  there.Fingerprints[i],
		} {
			if got.Role != want.role || got.Machine != want.machine || got.Fingerprint != want.fingerprint {
				t.Errorf("%s line %d = %+v, want role %q, machine %q, fingerprint %q",
					name, i, got, want.role, want.machine, want.fingerprint)
			}
		}
	}
	// Each machine still says which of the two lines is its own.
	if here.Fingerprints[0].Whose != whoseThis || there.Fingerprints[0].Whose != whoseOther {
		t.Errorf("the lines are not labelled from each machine's own position: %q / %q",
			here.Fingerprints[0].Whose, there.Fingerprints[0].Whose)
	}
	// The notice describes what is on the screen, and each screen says which
	// machine runs the next command.
	if !strings.Contains(here.Notice, "same two values in the same order") {
		t.Errorf("notice does not describe what is printed: %q", here.Notice)
	}
	// It also names the words that label the lines, and the command that shows
	// the other screen: "read both screens" is not an instruction anybody can
	// follow without knowing what to type on the other machine.
	for _, want := range []string{"requester", "receiver", "ah pair pending"} {
		if !strings.Contains(here.Notice, want) {
			t.Errorf("notice does not mention %q: %q", want, here.Notice)
		}
	}
	if !strings.Contains(here.NextStep, "On "+receiver.node.DisplayName) ||
		!strings.Contains(here.NextStep, "ah pair approve") {
		t.Errorf("the requester is not told which machine approves: %q", here.NextStep)
	}
	// And that a confirm is coming. Without it a requester who follows only
	// the CLI stops after the far side approves, with nothing trusted here.
	if !strings.Contains(here.NextStep, "ah pair confirm "+outgoing.ID) {
		t.Errorf("the requester is not told a confirm step follows: %q", here.NextStep)
	}
	if !strings.Contains(there.NextStep, "on this machine") {
		t.Errorf("the receiver is not told it is the one to approve: %q", there.NextStep)
	}

	// And a finished row stops repeating the instruction: there is nothing left
	// to compare, and a notice there sends the owner looking for something to do.
	receiver.decide(t, outgoing.ID, "reject", http.StatusOK)
	if done := receiver.viewOf(outgoing.ID); done.Notice != "" {
		t.Errorf("a decided row still carries the compare notice: %q", done.Notice)
	}
}

// The fingerprint shown is derived from the key that arrived, never read from
// the descriptor beside it. A substituted key that carried the fingerprint of
// the key it replaced would otherwise pass the only check there is.
//
// The wire here is honest about the key and lies about the fingerprint, which
// is the mutation the display must not follow.
func TestAReceivedFingerprintIsNeverTheOneOnTheWire(t *testing.T) {
	receiver := newPairNode(t, "decides")
	receiver.openWindow()

	liar, liarID := throwawayIdentity(t)
	lying := model.NodeIdentity{
		ID: liarID, DisplayName: "not what it seems", Platform: "test",
		PublicKey: identity.EncodePublicKey(liar.Public),
		// The lie: a fingerprint of some other key entirely.
		Fingerprint: "0000 0000 0000 0000 0000 0000",
	}
	envelope, err := protocol.NewHeartbeatBuilder(receiver.store, lying, liar).
		BuildPairRequest(time.Now(), "")
	if err != nil {
		t.Fatal(err)
	}
	posted := perform(t, receiver.server.PeerHandler(), http.MethodPost, "/v1/pair/requests", envelope)
	if posted.Code != http.StatusAccepted {
		t.Fatalf("pair request = %d %s", posted.Code, posted.Body.String())
	}

	rows := receiver.requests()
	if len(rows) != 1 {
		t.Fatalf("rows = %v", rows)
	}
	want := identity.Fingerprint(liar.Public)
	if rows[0].Fingerprint != want {
		t.Fatalf("the receiver shows %q, want %q derived from the key that arrived",
			rows[0].Fingerprint, want)
	}
	// And what a decision writes is that same locally derived value.
	receiver.decide(t, rows[0].ID, "approve", http.StatusOK)
	trusted := receiver.trusted()
	if len(trusted) != 1 || trusted[0].Fingerprint != want {
		t.Fatalf("stored %v, want fingerprint %q", trusted, want)
	}
}

// The same rule on the answer path: the requester derives the receiver's
// fingerprint from the key that terminated the TLS connection, not from the
// string in the descriptor that came back with it.
func TestTheAnswersFingerprintIsDerivedFromTheKeyThatAnswered(t *testing.T) {
	requester := newPairNode(t, "asks")
	keypair, nodeID := throwawayIdentity(t)
	address := fakePeerWithKey(t, keypair, nodeID, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusAccepted, map[string]any{
			"requestId": "pair_liar0000000000000000",
			"node": protocol.NodeDescriptor{
				NodeID: nodeID, DisplayName: "answers", Platform: "test",
				PublicKey: identity.EncodePublicKey(keypair.Public),
				// The lie again, this time from the machine being asked.
				Fingerprint: "0000 0000 0000 0000 0000 0000",
			},
		})
	})

	started := requester.startRequest(address)
	if started.Code != http.StatusCreated {
		t.Fatalf("start pairing request = %d %s", started.Code, started.Body.String())
	}
	var outgoing pairing.Request
	if err := json.Unmarshal(started.Body.Bytes(), &outgoing); err != nil {
		t.Fatal(err)
	}
	if want := identity.Fingerprint(keypair.Public); outgoing.Fingerprint != want {
		t.Fatalf("the requester shows %q, want %q", outgoing.Fingerprint, want)
	}
}

// An approval carries the peer's descriptor a second time, signed. The key in
// it must be the key this exchange is pinned to: a peer that signs an approval
// naming a different key is describing a machine other than the one whose
// fingerprint the owner is comparing, and the approval is discarded.
func TestAnApprovalNamingAnotherKeyIsDiscarded(t *testing.T) {
	requester := newPairNode(t, "asks")
	keypair, nodeID := throwawayIdentity(t)
	elsewhere, _ := throwawayIdentity(t)
	requestID := "pair_swapped000000000000"

	address := fakePeerWithKey(t, keypair, nodeID, func(w http.ResponseWriter, r *http.Request) {
		descriptor := protocol.NodeDescriptor{
			NodeID: nodeID, DisplayName: "answers", Platform: "test",
			PublicKey:   identity.EncodePublicKey(keypair.Public),
			Fingerprint: identity.Fingerprint(keypair.Public),
		}
		if r.Method == http.MethodPost {
			writeJSON(w, http.StatusAccepted, map[string]any{
				"requestId": requestID, "node": descriptor,
			})
			return
		}
		// Signed by the pinned key, directed at the requester, naming the right
		// request — and carrying somebody else's public key.
		swapped := descriptor
		swapped.PublicKey = identity.EncodePublicKey(elsewhere.Public)
		swapped.Fingerprint = identity.Fingerprint(elsewhere.Public)
		envelope, err := protocol.NewDirectedEnvelope(nodeID, requester.node.ID,
			protocol.TypePairApprove, protocol.At(time.Now()),
			protocol.PairApprovePayload{Node: swapped, RequestID: requestID}, keypair)
		if err != nil {
			t.Error(err)
			writeError(w, http.StatusInternalServerError, "TEST", err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"requestId": requestID, "state": string(pairing.StateApproved), "envelope": envelope,
		})
	})

	started := requester.startRequest(address)
	if started.Code != http.StatusCreated {
		t.Fatalf("start pairing request = %d %s", started.Code, started.Body.String())
	}

	// Reading the list polls, which is where the approval is checked.
	row := requester.request(requestID)
	if row.State != pairing.StatePending {
		t.Fatalf("state = %q; an approval carrying another key was accepted", row.State)
	}
	requester.decide(t, requestID, "confirm", http.StatusConflict)
	if len(requester.trusted()) != 0 {
		t.Fatalf("a swapped approval trusted %v", requester.trusted())
	}
}

// Sixteen fresh node ids from one machine used to fill the incoming list, and
// the owner's real machine was answered PAIRING_BUSY during the very window
// they had opened to pair it. Node ids are the sender's to choose; the address
// it dials from is not.
func TestOneAddressCannotFillTheIncomingList(t *testing.T) {
	receiver := newPairNode(t, "decides")
	receiver.openWindow()
	peer := receiver.server.PeerHandler()

	accepted := 0
	for range pairing.MaxPending {
		keypair, nodeID := throwawayIdentity(t)
		node := model.NodeIdentity{
			ID: nodeID, DisplayName: "flood", Platform: "test",
			PublicKey: identity.EncodePublicKey(keypair.Public), Fingerprint: keypair.Fingerprint(),
		}
		envelope, err := protocol.NewHeartbeatBuilder(receiver.store, node, keypair).
			BuildPairRequest(time.Now(), "")
		if err != nil {
			t.Fatal(err)
		}
		if posted := perform(t, peer, http.MethodPost, "/v1/pair/requests", envelope); posted.Code == http.StatusAccepted {
			accepted++
		}
	}
	if accepted > pairing.MaxPendingPerSource {
		t.Fatalf("one address got %d pending requests in, want at most %d",
			accepted, pairing.MaxPendingPerSource)
	}
	// And the owner's own machine still gets in, which is the whole point.
	requester := newPairNode(t, "asks")
	started := requester.startRequest(receiver.address())
	if started.Code != http.StatusCreated {
		t.Fatalf("the real machine was turned away: %d %s", started.Code, started.Body.String())
	}
}

// A refusal on the requesting side reaches the other machine.
//
// The worst case is the one this covers: the owner refused because the
// fingerprints did not match, and the other owner had already approved. Without
// the push, that machine went on trusting the key its owner had been told to
// refuse, and nothing on either screen said so.
func TestARequesterRejectRevokesAnApprovalAlreadyGiven(t *testing.T) {
	requester := newPairNode(t, "asks")
	receiver := newPairNode(t, "decides")
	receiver.openWindow()

	started := requester.startRequest(receiver.address())
	if started.Code != http.StatusCreated {
		t.Fatalf("start pairing request = %d %s", started.Code, started.Body.String())
	}
	var outgoing pairing.Request
	if err := json.Unmarshal(started.Body.Bytes(), &outgoing); err != nil {
		t.Fatal(err)
	}
	receiver.decide(t, outgoing.ID, "approve", http.StatusOK)
	if len(receiver.trusted()) != 1 {
		t.Fatalf("the approval wrote %v", receiver.trusted())
	}

	requester.decide(t, outgoing.ID, "reject", http.StatusOK)

	if trusted := receiver.trusted(); len(trusted) != 0 {
		t.Fatalf("the refused machine is still trusted: %v", trusted)
	}
	if state := receiver.request(outgoing.ID).State; state != pairing.StateRejected {
		t.Fatalf("the receiver sees %q after the refusal, want %q", state, pairing.StateRejected)
	}
	if len(requester.trusted()) != 0 {
		t.Fatalf("the refusing machine trusted something: %v", requester.trusted())
	}
	// And the approver's row says what became of the trust it wrote. "Refused"
	// alone left an owner who had approved with no way to know a trust row had
	// existed here at all, let alone that it is gone.
	step := receiver.viewOf(outgoing.ID).NextStep
	if !strings.Contains(step, "had trusted") || !strings.Contains(step, "withdrawn") {
		t.Errorf("the approver's row does not say the trust it wrote was undone: %q", step)
	}
}

// The same push before any approval: the other owner is told, and approving
// afterwards is refused in words they can act on.
func TestARequesterRejectBlocksALaterApprove(t *testing.T) {
	requester := newPairNode(t, "asks")
	receiver := newPairNode(t, "decides")
	receiver.openWindow()

	started := requester.startRequest(receiver.address())
	if started.Code != http.StatusCreated {
		t.Fatalf("start pairing request = %d %s", started.Code, started.Body.String())
	}
	var outgoing pairing.Request
	if err := json.Unmarshal(started.Body.Bytes(), &outgoing); err != nil {
		t.Fatal(err)
	}

	requester.decide(t, outgoing.ID, "reject", http.StatusOK)

	if state := receiver.request(outgoing.ID).State; state != pairing.StateRejected {
		t.Fatalf("the receiver sees %q, want %q", state, pairing.StateRejected)
	}
	refused := perform(t, receiver.owner, http.MethodPost,
		"/v1/pair/requests/"+outgoing.ID+"/approve", map[string]string{})
	if refused.Code != http.StatusConflict {
		t.Fatalf("approving a withdrawn request = %d %s, want 409", refused.Code, refused.Body.String())
	}
	if code := errorCode(t, refused.Body.Bytes()); code != "PAIRING_STATE" {
		t.Errorf("error code = %q, want PAIRING_STATE", code)
	}
	// One sentence with one remedy, not the same fact said three ways.
	message := errorMessage(t, refused.Body.Bytes())
	if strings.Count(message, "pairing request") != 1 || !strings.Contains(message, "was refused") {
		t.Errorf("message stutters or does not say what happened: %q", message)
	}
	if len(receiver.trusted()) != 0 {
		t.Fatalf("a withdrawn request trusted %v", receiver.trusted())
	}
}

// approvedRequest walks requester → receiver as far as the receiving owner's
// approval, which is the only state from which a refusal revokes anything.
//
// The tests below need to be past that point: a refusal of a request still
// pending never reaches the guards on revoking at all.
func approvedRequest(t *testing.T, requester, receiver *pairNode) pairing.Request {
	t.Helper()
	started := requester.startRequest(receiver.address())
	if started.Code != http.StatusCreated {
		t.Fatalf("start pairing request = %d %s", started.Code, started.Body.String())
	}
	var outgoing pairing.Request
	if err := json.Unmarshal(started.Body.Bytes(), &outgoing); err != nil {
		t.Fatal(err)
	}
	receiver.decide(t, outgoing.ID, "approve", http.StatusOK)
	if len(receiver.trusted()) != 1 {
		t.Fatalf("approval did not write the trust row the revoke tests are about: %v",
			receiver.trusted())
	}
	return outgoing
}

// A refusal may only take back what the refused request itself wrote. A node
// trusted by some other route — `ah pair` by hand — is not revoked because a
// pairing request naming it was refused.
//
// The row is the stand-in for that other route: trust under this node id that
// this request did not write is exactly what TrustedByRequest being false
// means, and clearing it here is the only way to put an approved request in
// that state, because approving always sets it.
func TestARejectDoesNotRevokeATrustItDidNotWrite(t *testing.T) {
	requester := newPairNode(t, "asks")
	receiver := newPairNode(t, "decides")
	receiver.openWindow()
	outgoing := approvedRequest(t, requester, receiver)

	if _, err := receiver.server.pairRequests.Update(outgoing.ID, func(row *pairing.Request) {
		row.TrustedByRequest = false
	}); err != nil {
		t.Fatal(err)
	}

	requester.decide(t, outgoing.ID, "reject", http.StatusOK)

	trusted := receiver.trusted()
	if len(trusted) != 1 {
		t.Fatalf("a trust this request did not write was revoked by its refusal: %v", trusted)
	}
	if state := receiver.request(outgoing.ID).State; state != pairing.StateRejected {
		t.Errorf("the refused request is %q, want %q", state, pairing.StateRejected)
	}
}

// The same guard from the other side: the request did write a trust row, but
// what is stored under that node id now is a different key. Some later act
// replaced it, and replacing is not this refusal's to undo — revoking would
// delete a pairing the owner made after this one.
func TestARejectDoesNotRevokeATrustStoredUnderAnotherKey(t *testing.T) {
	requester := newPairNode(t, "asks")
	receiver := newPairNode(t, "decides")
	receiver.openWindow()
	outgoing := approvedRequest(t, requester, receiver)

	// The owner re-paired that node id by hand, against a new key. The store
	// will not overwrite a key in place, so this is the revoke-and-pair-again
	// the owner would have done.
	replacement, _ := throwawayIdentity(t)
	if err := receiver.store.RevokeNode(context.Background(), requester.node.ID); err != nil {
		t.Fatal(err)
	}
	if err := receiver.store.TrustNode(context.Background(), registry.TrustedNode{
		NodeID: requester.node.ID, DisplayName: "paired again by hand", Platform: "test",
		PublicKey:   identity.EncodePublicKey(replacement.Public),
		Fingerprint: identity.Fingerprint(replacement.Public),
	}); err != nil {
		t.Fatal(err)
	}

	requester.decide(t, outgoing.ID, "reject", http.StatusOK)

	trusted := receiver.trusted()
	if len(trusted) != 1 {
		t.Fatalf("the later pairing was revoked by a refusal of the earlier one: %v", trusted)
	}
	if trusted[0].PublicKey != identity.EncodePublicKey(replacement.Public) {
		t.Errorf("the stored key changed: %q", trusted[0].PublicKey)
	}
}

// A refusal that is not signed by the machine whose request it names changes
// nothing. The peer surface is unauthenticated in the sense that the caller is
// not in the trust store, and authenticated in the sense that matters.
func TestAForgedRejectIsRefused(t *testing.T) {
	requester := newPairNode(t, "asks")
	receiver := newPairNode(t, "decides")
	receiver.openWindow()

	started := requester.startRequest(receiver.address())
	if started.Code != http.StatusCreated {
		t.Fatalf("start pairing request = %d %s", started.Code, started.Body.String())
	}
	var outgoing pairing.Request
	if err := json.Unmarshal(started.Body.Bytes(), &outgoing); err != nil {
		t.Fatal(err)
	}
	receiver.decide(t, outgoing.ID, "approve", http.StatusOK)

	forger, forgerID := throwawayIdentity(t)
	envelope, err := protocol.NewDirectedEnvelope(forgerID, receiver.node.ID,
		protocol.TypePairReject, protocol.At(time.Now()),
		protocol.PairRejectPayload{RequestID: outgoing.ID, Reason: pairing.ReasonDeclined}, forger)
	if err != nil {
		t.Fatal(err)
	}
	response := perform(t, receiver.server.PeerHandler(), http.MethodPost,
		"/v1/pair/requests/"+outgoing.ID+"/reject", envelope)
	if response.Code != http.StatusForbidden {
		t.Fatalf("a forged refusal = %d %s, want 403", response.Code, response.Body.String())
	}
	if len(receiver.trusted()) != 1 {
		t.Fatalf("a forged refusal revoked a real pairing: %v", receiver.trusted())
	}
}

// Pending outgoing rows are polled at once, not one after another. Serially, at
// the delivery timeout each, a handful of machines that have gone away was
// minutes of silence at a prompt.
func TestPendingPollsOutgoingRequestsConcurrently(t *testing.T) {
	requester := newPairNode(t, "asks")
	// Listeners that accept and never answer: the request runs to its timeout.
	for i := range 4 {
		silent, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = silent.Close() })
		go func() {
			for {
				connection, err := silent.Accept()
				if err != nil {
					return
				}
				t.Cleanup(func() { _ = connection.Close() })
			}
		}()
		keypair, nodeID := throwawayIdentity(t)
		now := requester.server.pairRequests.Now()
		if err := requester.server.pairRequests.Add(pairing.Request{
			ID: fmt.Sprintf("pair_silent%013d", i), Direction: pairing.Outgoing,
			NodeID: nodeID, DisplayName: "gone", Platform: "test",
			PublicKey:   identity.EncodePublicKey(keypair.Public),
			Fingerprint: identity.Fingerprint(keypair.Public),
			Address:     silent.Addr().String(),
			State:       pairing.StatePending,
			CreatedAt:   now.UTC(), ExpiresAt: now.Add(pairing.RequestTTL).UTC(),
		}); err != nil {
			t.Fatal(err)
		}
	}

	started := time.Now()
	requester.requests()
	elapsed := time.Since(started)
	// Four rows, serially, would be four timeouts. Two is slack for a slow
	// machine and still nowhere near serial.
	if elapsed > 2*pairPollTimeout {
		t.Fatalf("listing took %s for four unreachable rows; the poll is serial", elapsed)
	}
}

// throwawayIdentity is a keypair and a node id for a machine that exists only
// for the length of one test.
func throwawayIdentity(t *testing.T) (identity.Keypair, string) {
	t.Helper()
	keypair, err := identity.LoadOrCreateKeypair(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	nodeID, err := id.New("node_")
	if err != nil {
		t.Fatal(err)
	}
	return keypair, nodeID
}

// fakePeerWithKey is fakePeer against a key the test holds, so it can sign what
// the peer would sign.
func fakePeerWithKey(t *testing.T, keypair identity.Keypair, nodeID string, handler http.HandlerFunc) string {
	t.Helper()
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
