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

// sendNode serves what agent_send reads and records what it was asked.
type sendNode struct {
	outbound bool
	// accept is the acceptMessages flag of local sessions other than the bound one.
	accept bool
	peers  []map[string]any
	local  []map[string]any
	posted []map[string]string
	seen   []string
}

func (n *sendNode) connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.seen = append(n.seen, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/v1/node":
			_ = json.NewEncoder(w).Encode(map[string]string{"id": localNode})
		case r.URL.Path == "/v1/peers":
			_ = json.NewEncoder(w).Encode(map[string]any{"peers": n.peers})
		case r.URL.Path == "/v1/sessions":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"sessions": n.local, "pagination": map[string]int{"totalPages": 1},
			})
		case r.URL.Path == "/v1/messages":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			n.posted = append(n.posted, body)
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "msg_new", "state": "pending"})
		case strings.HasSuffix(r.URL.Path, "/audience"):
			bound := strings.Contains(r.URL.Path, "codex:mine")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"mode":           "none",
				"allowOutbound":  bound && n.outbound,
				"acceptMessages": !bound && n.accept,
			})
		case strings.HasPrefix(r.URL.Path, "/v1/sessions/"):
			_ = json.NewEncoder(w).Encode(map[string]string{"id": "codex:mine"})
		default:
			w.WriteHeader(http.StatusNotFound)
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
	st, ct := mcp.NewInMemoryTransports()
	if _, err := built.MCPServer().Connect(context.Background(), st, nil); err != nil {
		t.Fatalf("connect: %v", err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func peerWith(sessionID string) []map[string]any {
	return []map[string]any{{
		"nodeId": "node_peer000000000000", "displayName": "peer", "online": true,
		"sessions": []map[string]any{session("node_peer000000000000/"+sessionID, "claude", "idle", "")},
	}}
}

// The gate is the whole point: without the owner opening it, nothing leaves.
func TestNothingIsSentUntilTheOwnerOpensOutbound(t *testing.T) {
	node := &sendNode{outbound: false, peers: peerWith("claude:theirs")}
	text, isErr := call(t, node.connect(t), "agent_send", map[string]any{
		"agentId": "node_peer000000000000/claude:theirs", "message": "hello",
	})
	if !isErr {
		t.Fatalf("a message was sent with outbound closed: %s", text)
	}
	if len(node.posted) != 0 {
		t.Errorf("the node was asked to send anyway: %v", node.posted)
	}
	// The refusal must be actionable and say why the default is closed.
	for _, want := range []string{"--outbound", "another machine"} {
		if !strings.Contains(text, want) {
			t.Errorf("the refusal does not mention %q: %s", want, text)
		}
	}
}

// A closed session must not be usable to find out what is visible. The gate is
// checked before the destination is resolved, so every destination gives the
// same answer.
func TestAClosedSessionCannotProbeForDestinations(t *testing.T) {
	node := &sendNode{outbound: false, peers: peerWith("claude:real")}
	session := node.connect(t)
	answers := map[string]string{}
	for _, target := range []string{
		"node_peer000000000000/claude:real",    // exists and is visible
		"node_peer000000000000/claude:missing", // paired node, not authorised
		"node_nope00000000000/claude:x",        // unpaired node
	} {
		text, isErr := call(t, session, "agent_send", map[string]any{"agentId": target, "message": "x"})
		if !isErr {
			t.Fatalf("send succeeded with outbound closed: %s", text)
		}
		answers[target] = text
	}
	distinct := map[string]bool{}
	for _, text := range answers {
		distinct[text] = true
	}
	if len(distinct) != 1 {
		t.Errorf("a closed session got %d different answers, which distinguishes destinations: %v",
			len(distinct), answers)
	}
}

// With the gate open, a visible destination is queued — and the answer says
// plainly that queued is not delivered.
func TestAnOpenSessionQueuesToAVisibleDestination(t *testing.T) {
	node := &sendNode{outbound: true, peers: peerWith("claude:theirs")}
	text, isErr := call(t, node.connect(t), "agent_send", map[string]any{
		"agentId": "node_peer000000000000/claude:theirs", "message": "hello",
	})
	if isErr {
		t.Fatalf("send failed: %s", text)
	}
	if len(node.posted) != 1 {
		t.Fatalf("want 1 post, got %d", len(node.posted))
	}
	if got := node.posted[0]["to"]; got != "node_peer000000000000/claude:theirs" {
		t.Errorf("sent to %q", got)
	}
	// The sender is this node's bound session, qualified, so the recipient can
	// tell who it was without trusting a self-chosen label.
	if got := node.posted[0]["from"]; got != localNode+"/codex:mine" {
		t.Errorf("from = %q, want the bound session qualified by this node", got)
	}
	if !strings.Contains(text, "not mean delivered") {
		t.Errorf("the answer does not say queued is not delivered: %s", text)
	}
}

// An open gate does not make invisible destinations reachable.
func TestAnOpenSessionStillCannotReachWhatItCannotSee(t *testing.T) {
	node := &sendNode{outbound: true, peers: peerWith("claude:real")}
	text, isErr := call(t, node.connect(t), "agent_send", map[string]any{
		"agentId": "node_peer000000000000/claude:invisible", "message": "x",
	})
	if !isErr {
		t.Fatalf("sent to an unauthorised destination: %s", text)
	}
	if !strings.Contains(text, "unknown node or session") {
		t.Errorf("the refusal distinguishes it from a missing one: %s", text)
	}
	if len(node.posted) != 0 {
		t.Errorf("the node was asked to send anyway: %v", node.posted)
	}
}

// A local destination's own inbox flag is still honoured.
func TestALocalDestinationMustAcceptMessages(t *testing.T) {
	local := []map[string]any{
		session("codex:mine", "codex", "active", ""),
		session("claude:neighbour", "claude", "idle", ""),
	}
	closed := &sendNode{outbound: true, accept: false, local: local}
	text, isErr := call(t, closed.connect(t), "agent_send", map[string]any{
		"agentId": "claude:neighbour", "message": "x",
	})
	if !isErr {
		t.Fatalf("sent to a session that does not accept messages: %s", text)
	}
	if !strings.Contains(text, "does not accept messages") {
		t.Errorf("unhelpful refusal: %s", text)
	}

	open := &sendNode{outbound: true, accept: true, local: local}
	text, isErr = call(t, open.connect(t), "agent_send", map[string]any{
		"agentId": "claude:neighbour", "message": "x",
	})
	if isErr {
		t.Fatalf("a local send was refused: %s", text)
	}
	// A local message names a local sender: the node refuses a `from` claiming
	// another node for a local destination.
	if got := open.posted[0]["from"]; got != "codex:mine" {
		t.Errorf("from = %q, want the bare local session", got)
	}
}
