package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sessions":   f.local,
				"pagination": map[string]int{"totalPages": 1},
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
			if !isErr {
				t.Fatalf("the claim %q was accepted: %s", claimed, text)
			}
			if !strings.Contains(text, "does not own") {
				t.Errorf("refused, but not for the claim: %s", text)
			}
		})
	}
}

// The refusal must not be quietly partial: a snapshot with one bad row is
// refused whole, rather than serving the rows that happened to be well-formed
// alongside a peer that has just been caught lying.
func TestOneBadRowRefusesTheWholeSnapshot(t *testing.T) {
	node := &fakeNode{
		peers: []map[string]any{{
			"nodeId": "node_peer000000000000", "displayName": "peer", "online": true,
			"sessions": []map[string]any{
				session("node_peer000000000000/claude:honest", "claude", "idle", ""),
				session("node_other00000000000/claude:stolen", "claude", "idle", ""),
			},
		}},
	}
	text, isErr := call(t, connectTo(t, node), "agent_list", map[string]any{})
	if !isErr {
		t.Fatalf("a snapshot with a stolen row was served: %s", text)
	}
	if strings.Contains(text, "claude:honest") {
		t.Errorf("the well-formed row was served alongside the refusal: %s", text)
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
