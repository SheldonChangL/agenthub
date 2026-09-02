package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"agenthub.local/agenthub/internal/mcpserver"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const localNode = "node_0123456789abcdef0123"

// fakeNode serves the three endpoints the view reads, so a test can say exactly
// what the node would have returned.
type fakeNode struct {
	local []map[string]any
	peers []map[string]any
}

func (f *fakeNode) client(t *testing.T) *mcpserver.Client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/node":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": localNode})
		case r.URL.Path == "/v1/peers":
			_ = json.NewEncoder(w).Encode(map[string]any{"peers": f.peers})
		case r.URL.Path == "/v1/sessions":
			// Paged, so LocalSessions' loop is exercised rather than assumed.
			// A client that ignored totalPages would return only the first page.
			perPage := 1
			pages := len(f.local)
			if pages == 0 {
				pages = 1
			}
			page := 1
			if v := r.URL.Query().Get("page"); v != "" {
				if n, err := strconv.Atoi(v); err == nil {
					page = n
				}
			}
			var window []map[string]any
			if start := (page - 1) * perPage; start < len(f.local) {
				end := start + perPage
				if end > len(f.local) {
					end = len(f.local)
				}
				window = f.local[start:end]
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sessions":   window,
				"pagination": map[string]int{"totalPages": pages},
			})
		case strings.HasPrefix(r.URL.Path, "/v1/sessions/"):
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "x"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	client, err := mcpserver.NewClient(server.URL)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	return client
}

func session(id, provider, status, cwd string) map[string]any {
	return map[string]any{
		"id": id, "provider": provider, "status": status, "cwd": cwd,
		"management": "unmanaged", "visibility": "private",
		"lastSeenAt": "2026-09-02T00:00:00Z",
	}
}

// connectTo runs the server against a fake node and returns an MCP client.
func connectTo(t *testing.T, node *fakeNode) *mcp.ClientSession {
	t.Helper()
	client := node.client(t)
	binding, err := mcpserver.Bind(context.Background(), client, "codex:mine")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	built, err := mcpserver.New(client, binding, localNode)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := built.MCPServer().Connect(context.Background(), serverTransport, nil); err != nil {
		t.Fatalf("connect: %v", err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).
		Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func call(t *testing.T, s *mcp.ClientSession, tool string, args map[string]any) (string, bool) {
	t.Helper()
	result, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	text := ""
	for _, content := range result.Content {
		if c, ok := content.(*mcp.TextContent); ok {
			text += c.Text
		}
	}
	return text, result.IsError
}

// A smoke test: an authorised remote session and a local one both arrive, in
// the shape an agent will read. The negative half — that nothing else arrives —
// is TestNothingAppearsForAPeerThatAuthorisedNothing, which is where the
// guarantee actually lives.
func TestBothLocalAndAuthorisedRemoteSessionsArrive(t *testing.T) {
	node := &fakeNode{
		local: []map[string]any{session("codex:mine", "codex", "active", "/home/me")},
		peers: []map[string]any{{
			"nodeId": "node_peer000000000000", "displayName": "peer", "online": true,
			"sessions": []map[string]any{session("node_peer000000000000/claude:shared", "claude", "idle", "")},
		}},
	}
	text, isErr := call(t, connectTo(t, node), "agent_list", map[string]any{})
	if isErr {
		t.Fatalf("agent_list errored: %s", text)
	}
	if !strings.Contains(text, "node_peer000000000000/claude:shared") {
		t.Errorf("the authorised remote session is missing: %s", text)
	}
	if !strings.Contains(text, "codex:mine") {
		t.Errorf("the local session is missing: %s", text)
	}
}

// Nothing may appear for a peer beyond what that peer authorised.
//
// The earlier version of this test used an empty peer list, so it passed for
// the trivial reason that the loop never ran — injecting a fabricated session
// into visible() did not fail it. A paired peer that has authorised nothing is
// the case that actually distinguishes "reads presence" from "invents rows".
func TestNothingAppearsForAPeerThatAuthorisedNothing(t *testing.T) {
	node := &fakeNode{
		local: []map[string]any{session("codex:mine", "codex", "active", "")},
		peers: []map[string]any{{
			"nodeId": "node_peer000000000000", "displayName": "peer", "online": true,
			"sessions": []map[string]any{},
		}},
	}
	text, isErr := call(t, connectTo(t, node), "agent_list", map[string]any{})
	if isErr {
		t.Fatalf("agent_list errored: %s", text)
	}
	if strings.Contains(text, "node_peer000000000000/") {
		t.Errorf("a session appeared for a peer that authorised none: %s", text)
	}
	// The peer is paired and online, so its absence must be about authorisation
	// rather than about the peer being missing from presence altogether.
	if !strings.Contains(text, "codex:mine") {
		t.Errorf("the local session should still be listed: %s", text)
	}
}

// The working directory is a separate opt-in from publishing the session. A
// remote summary carries it only where its owner chose to.
func TestARemoteWorkingDirectoryAppearsOnlyWhenExported(t *testing.T) {
	withCWD := &fakeNode{peers: []map[string]any{{
		"nodeId": "node_peer000000000000", "displayName": "peer", "online": true,
		"sessions": []map[string]any{session("node_peer000000000000/claude:a", "claude", "idle", "/peer/work")},
	}}}
	text, _ := call(t, connectTo(t, withCWD), "agent_list", map[string]any{})
	if !strings.Contains(text, "/peer/work") {
		t.Errorf("an exported cwd should be shown: %s", text)
	}

	without := &fakeNode{peers: []map[string]any{{
		"nodeId": "node_peer000000000000", "displayName": "peer", "online": true,
		"sessions": []map[string]any{session("node_peer000000000000/claude:a", "claude", "idle", "")},
	}}}
	text, _ = call(t, connectTo(t, without), "agent_list", map[string]any{})
	if strings.Contains(text, "cwd") {
		t.Errorf("a cwd appeared for a session that did not export one: %s", text)
	}
}

// The view is read fresh on every call, never cached.
//
// This is what makes revocation take effect without a restart — /v1/peers
// iterates the trust store, so a revoked node's sessions are gone from the next
// answer — but the revocation itself belongs to internal/api and is tested
// there. What is asserted here is only the absence of caching, which is the
// part this package owns.
func TestTheViewIsNotCachedBetweenCalls(t *testing.T) {
	node := &fakeNode{peers: []map[string]any{{
		"nodeId": "node_peer000000000000", "displayName": "peer", "online": true,
		"sessions": []map[string]any{session("node_peer000000000000/claude:a", "claude", "idle", "")},
	}}}
	session := connectTo(t, node)
	if text, _ := call(t, session, "agent_list", map[string]any{}); !strings.Contains(text, "claude:a") {
		t.Fatalf("setup: the peer session should be visible first: %s", text)
	}
	node.peers = []map[string]any{}
	text, _ := call(t, session, "agent_list", map[string]any{})
	if strings.Contains(text, "claude:a") {
		t.Errorf("a session survived on the same connection after the node stopped serving it: %s", text)
	}
}

// "No such session", "that node is not paired" and "that session exists but its
// owner did not authorise you" must be one answer. Three answers would make
// this tool a way to discover what another machine runs.
func TestAnUnauthorisedAddressIsIndistinguishableFromAMissingOne(t *testing.T) {
	node := &fakeNode{
		local: []map[string]any{session("codex:mine", "codex", "active", "")},
		peers: []map[string]any{{
			"nodeId": "node_peer000000000000", "displayName": "peer", "online": true,
			"sessions": []map[string]any{},
		}},
	}
	session := connectTo(t, node)
	answers := map[string]string{}
	for _, agentID := range []string{
		"node_peer000000000000/claude:secret", // paired node, not authorised
		"node_other00000000000/claude:secret", // unpaired node
		"codex:nosuchsession",                 // local, absent
	} {
		text, isErr := call(t, session, "agent_status", map[string]any{"agentId": agentID})
		if !isErr {
			t.Errorf("agent_status(%q) succeeded; it must not confirm the session", agentID)
		}
		answers[agentID] = text
	}
	seen := map[string]bool{}
	for _, text := range answers {
		seen[text] = true
	}
	if len(seen) != 1 {
		t.Errorf("the three cases gave %d different answers, which distinguishes them: %v", len(seen), answers)
	}
}

// Remote sessions must come from presence and from nowhere else.
//
// The node applies each peer's audience when it accepts that peer's heartbeat,
// so presence already IS the authorised view. A second source would be a second
// implementation of that filter, free to disagree with the first — and the one
// that disagreed would be the one an agent sees.
//
// This is asserted structurally rather than behaviourally: a node that served
// /v1/peers as an error must produce no remote sessions at all, because there is
// no other path they could have come from.
func TestRemoteSessionsHaveNoSourceButPresence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/node":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": localNode})
		case r.URL.Path == "/v1/peers":
			// Presence unavailable. Anything remote appearing now came from
			// somewhere it should not have.
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": map[string]string{"code": "PEERS_FAILED", "message": "presence is down"},
			})
		case r.URL.Path == "/v1/sessions":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sessions":   []map[string]any{session("codex:mine", "codex", "active", "")},
				"pagination": map[string]int{"totalPages": 1},
			})
		default:
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "x"})
		}
	}))
	t.Cleanup(server.Close)
	client, err := mcpserver.NewClient(server.URL)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	binding, err := mcpserver.Bind(context.Background(), client, "codex:mine")
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	built, err := mcpserver.New(client, binding, localNode)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := built.MCPServer().Connect(context.Background(), serverTransport, nil); err != nil {
		t.Fatalf("connect: %v", err)
	}
	mcpSession, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).
		Connect(context.Background(), clientTransport, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = mcpSession.Close() })

	text, isErr := call(t, mcpSession, "agent_list", map[string]any{})
	if !isErr {
		t.Fatalf("agent_list succeeded with presence unavailable, so remote sessions have another source: %s", text)
	}
	// And the failure must say what went wrong, not silently degrade to local.
	if !strings.Contains(text, "presence") {
		t.Errorf("the error does not name presence: %s", text)
	}
}

// A peer describes its own sessions and nothing else.
//
// The node authenticates who sent a heartbeat but does not check that the ids
// inside name that sender, so a paired peer can claim any id it likes. Believed,
// that lets it attribute a session to a third node which authorised nothing —
// and, worse, send a bare local-form id that collides with one of this machine's
// own sessions, after which asking about that session could return the peer's
// fabrication instead of the truth.
func TestAPeerCannotClaimSessionsItDoesNotOwn(t *testing.T) {
	cases := map[string]string{
		"a session attributed to another node": "node_other00000000000/claude:x",
		"a bare id colliding with a local one": "codex:mine",
		"a bare id of its own":                 "claude:whatever",
		"an id with no provider":               "node_peer000000000000/nonsense",
		"a nested separator":                   "node_peer000000000000/node_other00000000000/claude:x",
		"its own id nested inside another":     "node_other00000000000/node_peer000000000000/claude:x",
		"an empty id":                          "",
		"a session id containing a separator":  "node_peer000000000000/claude:a/extra",
	}
	for name, claimed := range cases {
		t.Run(name, func(t *testing.T) {
			node := &fakeNode{
				local: []map[string]any{session("codex:mine", "codex", "active", "/real/path")},
				peers: []map[string]any{{
					"nodeId": "node_peer000000000000", "displayName": "peer", "online": true,
					"sessions": []map[string]any{session(claimed, "claude", "idle", "/peer/fabricated")},
				}},
			}
			text, isErr := call(t, connectTo(t, node), "agent_list", map[string]any{})
			if isErr {
				t.Fatalf("the call failed instead of ignoring the peer: %s", text)
			}
			if strings.Contains(text, "/peer/fabricated") {
				t.Errorf("the claim %q was served: %s", claimed, text)
			}
			// The lying peer costs the caller nothing else: their own machine
			// is still visible. A peer that can blank the owner's view of their
			// own sessions has been given more power than withholding its rows.
			if !strings.Contains(text, "/real/path") {
				t.Errorf("the caller's own session went missing too: %s", text)
			}
		})
	}
}

// One bad row costs that peer its whole snapshot, and costs nobody else
// anything.
//
// Refusing only the bad row would keep serving a peer that has just been caught
// claiming other people's sessions. Refusing the entire call would let any
// single paired peer blank the owner's view of their own machine, which is a
// larger loss than the rows being withheld.
func TestOneBadRowCostsThatPeerItsSnapshotAndNoOneElseTheirs(t *testing.T) {
	node := &fakeNode{
		local: []map[string]any{session("codex:mine", "codex", "active", "/real/path")},
		peers: []map[string]any{
			{
				"nodeId": "node_liar00000000000", "displayName": "liar", "online": true,
				"sessions": []map[string]any{
					session("node_liar00000000000/claude:honest", "claude", "idle", ""),
					session("node_other00000000000/claude:stolen", "claude", "idle", ""),
				},
			},
			{
				"nodeId": "node_good00000000000", "displayName": "good", "online": true,
				"sessions": []map[string]any{
					session("node_good00000000000/claude:fine", "claude", "idle", ""),
				},
			},
		},
	}
	text, isErr := call(t, connectTo(t, node), "agent_list", map[string]any{})
	if isErr {
		t.Fatalf("the call failed: %s", text)
	}
	if strings.Contains(text, "claude:stolen") {
		t.Error("the stolen row was served")
	}
	if strings.Contains(text, "claude:honest") {
		t.Error("the liar's other row was served; its whole snapshot should go")
	}
	if !strings.Contains(text, "claude:fine") {
		t.Errorf("the honest peer's session went with it: %s", text)
	}
	if !strings.Contains(text, "codex:mine") {
		t.Errorf("the caller's own session went with it: %s", text)
	}
}

// The filters narrow the visible set and nothing more. In particular, naming
// this node must select its local sessions: local rows carry no Node, so
// without translating the caller's id there was no way to ask for them at all.
func TestFiltersNarrowTheVisibleSet(t *testing.T) {
	node := &fakeNode{
		local: []map[string]any{
			session("codex:mine", "codex", "active", ""),
			session("claude:also-mine", "claude", "idle", ""),
		},
		peers: []map[string]any{{
			"nodeId": "node_peer000000000000", "displayName": "peer", "online": true,
			"sessions": []map[string]any{
				session("node_peer000000000000/claude:theirs", "claude", "active", ""),
			},
		}},
	}
	session := connectTo(t, node)
	cases := []struct {
		name  string
		args  map[string]any
		want  []string
		avoid []string
	}{
		{"provider", map[string]any{"provider": "codex"}, []string{"codex:mine"}, []string{"claude:also-mine", "claude:theirs"}},
		{"status", map[string]any{"status": "idle"}, []string{"claude:also-mine"}, []string{"codex:mine", "claude:theirs"}},
		{"remote node", map[string]any{"node": "node_peer000000000000"}, []string{"claude:theirs"}, []string{"codex:mine", "claude:also-mine"}},
		{"this node", map[string]any{"node": localNode}, []string{"codex:mine", "claude:also-mine"}, []string{"claude:theirs"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text, isErr := call(t, session, "agent_list", c.args)
			if isErr {
				t.Fatalf("errored: %s", text)
			}
			for _, want := range c.want {
				if !strings.Contains(text, want) {
					t.Errorf("missing %s: %s", want, text)
				}
			}
			for _, avoid := range c.avoid {
				if strings.Contains(text, avoid) {
					t.Errorf("should not contain %s: %s", avoid, text)
				}
			}
			// Total is the visible set before filtering, so an agent that
			// narrowed too far can tell "nothing matched" from "nothing is
			// visible" without a second call.
			if !strings.Contains(text, `"total":3`) {
				t.Errorf("total should stay 3 regardless of the filter: %s", text)
			}
		})
	}
}

// The provider a caller filters on is derived from the validated id, not copied
// from the peer's own field, so the two cannot disagree.
func TestAPeersProviderFieldCannotContradictItsID(t *testing.T) {
	node := &fakeNode{peers: []map[string]any{{
		"nodeId": "node_peer000000000000", "displayName": "peer", "online": true,
		"sessions": []map[string]any{
			session("node_peer000000000000/claude:x", "codex", "idle", ""),
		},
	}}}
	text, isErr := call(t, connectTo(t, node), "agent_list", map[string]any{"provider": "codex"})
	if isErr {
		t.Fatalf("errored: %s", text)
	}
	if strings.Contains(text, "claude:x") {
		t.Errorf("a row whose id says claude matched a codex filter: %s", text)
	}
	text, _ = call(t, connectTo(t, node), "agent_list", map[string]any{"provider": "claude"})
	if !strings.Contains(text, "claude:x") {
		t.Errorf("the row should match the provider its id names: %s", text)
	}
}

// A peer claiming this node's own id would produce rows shadowing local ones.
// Pairing refuses it, so this covers a trust store that is already wrong.
func TestAPeerClaimingToBeThisNodeIsIgnored(t *testing.T) {
	node := &fakeNode{
		local: []map[string]any{session("codex:mine", "codex", "active", "/real/path")},
		peers: []map[string]any{{
			"nodeId": localNode, "displayName": "impostor", "online": true,
			"sessions": []map[string]any{session(localNode+"/codex:mine", "codex", "idle", "/fabricated")},
		}},
	}
	text, isErr := call(t, connectTo(t, node), "agent_list", map[string]any{})
	if isErr {
		t.Fatalf("errored: %s", text)
	}
	if strings.Contains(text, "/fabricated") {
		t.Errorf("a peer claiming to be this node was served: %s", text)
	}
	if !strings.Contains(text, "/real/path") {
		t.Errorf("the real local session went missing: %s", text)
	}
}
