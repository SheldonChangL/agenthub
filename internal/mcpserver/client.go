package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"agenthub.local/agenthub/internal/address"
)

// maxNodeResponse bounds what this client will read from the node in one
// answer. Reached only by a response a peer made large; see inboxBatch.
const maxNodeResponse = 8 << 20

// ErrSessionNotFound marks an address the node does not have.
var ErrSessionNotFound = errors.New("no such session on this node")

// Client talks to the owner's local HTTP API.
//
// This server deliberately has no other way in. It does not open the SQLite
// file, so agenthub-node stays the only writer, and it cannot read anything the
// owner's API would not already hand to the desktop app or the CLI. That also
// means the audience filtering applied on the way out of the node applies here
// too, rather than being a second implementation that could drift.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient targets a node's loopback API.
func NewClient(baseURL string) (*Client, error) {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return nil, fmt.Errorf("parse node URL %q: %w", baseURL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("node URL %q must use http or https", baseURL)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("node URL %q must include a host", baseURL)
	}
	// A query or fragment survives String() and would land in the middle of
	// every path this client builds, producing requests that fail in ways that
	// point nowhere near the cause.
	if parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return nil, fmt.Errorf("node URL %q must be a bare scheme://host:port", baseURL)
	}
	// Loopback only. This is a guardrail against misconfiguration, not a trust
	// boundary — any local port-forward defeats it, and legitimately: the
	// project's own two-host testing reached a remote node through an SSH
	// tunnel whose local end is loopback. What it prevents is pointing this
	// process at a remote node by accident, which matters because the node's
	// owner API is loopback-only anyway and the attempt would otherwise fail
	// far from its cause.
	if err := requireLoopback(parsed.Hostname()); err != nil {
		return nil, fmt.Errorf("node URL %q: %w", baseURL, err)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return &Client{
		baseURL: parsed.String(),
		http:    &http.Client{Timeout: 15 * time.Second},
	}, nil
}

// SessionExists reports whether the node has this local session.
func (c *Client) SessionExists(ctx context.Context, sessionID string) error {
	status, body, err := c.get(ctx, "/v1/sessions/"+url.PathEscape(sessionID))
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return ErrSessionNotFound
	}
	if status != http.StatusOK {
		return fmt.Errorf("node answered %s for that session", describe(status, body))
	}
	return nil
}

// NodeID returns this node's own identifier.
func (c *Client) NodeID(ctx context.Context) (string, error) {
	status, body, err := c.get(ctx, "/v1/node")
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("node answered %s for its identity", describe(status, body))
	}
	var decoded struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", fmt.Errorf("decode node identity: %w", err)
	}
	if decoded.ID == "" {
		return "", errors.New("node reported no identifier")
	}
	return decoded.ID, nil
}

// requireLoopback accepts only names that cannot leave this machine.
func requireLoopback(host string) error {
	// DNS is case-insensitive, and "localhost" is accepted on trust: Go does not
	// pin it to loopback, it goes through the resolver like any other name.
	// Consistent with the guardrail this is, not with a boundary it is not.
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errors.New("must be loopback; this server reads other machines' messages into an agent's context")
	}
	return nil
}

// truncate bounds a string that came from somewhere else. A heartbeat body may
// be a megabyte, and none of it belongs in one line an operator or an agent
// reads.
func truncate(value string, limit int) string {
	if runes := []rune(value); len(runes) > limit {
		return string(runes[:limit]) + "…"
	}
	return value
}

// describe turns the node's error envelope into something an operator can act
// on. Reporting only the status code makes them guess at a message the node
// already wrote down.
func describe(status int, body []byte) string {
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Error.Message != "" {
		return fmt.Sprintf("%d %s: %s", status, envelope.Error.Code, truncate(envelope.Error.Message, 200))
	}
	return fmt.Sprintf("%d", status)
}

func (c *Client) get(ctx context.Context, path string) (int, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return 0, nil, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("reach the node at %s: %w", c.baseURL, err)
	}
	defer func() { _ = response.Body.Close() }()
	// Bounded: this client trusts the node, but a bug or a wrong URL should not
	// be able to exhaust memory here.
	body, err := io.ReadAll(io.LimitReader(response.Body, maxNodeResponse))
	if err != nil {
		return response.StatusCode, nil, err
	}
	return response.StatusCode, body, nil
}

// Session is one entry in the view an agent may see.
//
// Local and remote sessions are the same shape on purpose: an agent asking
// "what is running" should not have to know which side of the network a session
// is on to read the answer. Node distinguishes them.
type Session struct {
	// ID is bare <provider>:<id> for a local session and
	// <node-id>/<provider>:<id> for one on a paired node.
	ID       string `json:"id"`
	Node     string `json:"node,omitempty"`
	Provider string `json:"provider"`
	Status   string `json:"status"`
	// CWD is present for a remote session only where its owner opted in.
	CWD        string    `json:"cwd,omitempty"`
	Management string    `json:"management,omitempty"`
	Visibility string    `json:"visibility,omitempty"`
	LastSeenAt time.Time `json:"lastSeenAt"`
}

// LocalSessions returns every session on this node, including private ones.
//
// The caller is an agent running on this machine, on the owner's behalf. Hiding
// local sessions from it would protect nothing: it can already read the
// provider's files directly.
func (c *Client) LocalSessions(ctx context.Context) ([]Session, error) {
	var all []Session
	for page := 1; ; page++ {
		status, body, err := c.get(ctx, fmt.Sprintf("/v1/sessions?page=%d&pageSize=200", page))
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("node answered %s listing sessions", describe(status, body))
		}
		var decoded struct {
			Sessions []struct {
				ID         string    `json:"id"`
				Provider   string    `json:"provider"`
				Status     string    `json:"status"`
				CWD        string    `json:"cwd"`
				Management string    `json:"management"`
				Visibility string    `json:"visibility"`
				LastSeenAt time.Time `json:"lastSeenAt"`
			} `json:"sessions"`
			Pagination struct {
				TotalPages int `json:"totalPages"`
			} `json:"pagination"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			return nil, fmt.Errorf("decode session list: %w", err)
		}
		for _, s := range decoded.Sessions {
			all = append(all, Session{
				ID: s.ID, Provider: s.Provider, Status: s.Status, CWD: s.CWD,
				Management: s.Management, Visibility: s.Visibility, LastSeenAt: s.LastSeenAt,
			})
		}
		if decoded.Pagination.TotalPages == 0 || page >= decoded.Pagination.TotalPages {
			return all, nil
		}
	}
}

// Peer is a paired node and what it has authorised this node to see.
type Peer struct {
	NodeID      string
	DisplayName string
	Online      bool
	Sessions    []Session
}

// Peers returns presence: the paired nodes and their authorised sessions.
//
// This is the only source for remote sessions, and deliberately so. The node
// already applied each peer's audience when it accepted that peer's heartbeat;
// reading anything else here would be a second implementation of that filter,
// free to disagree with the first.
func (c *Client) Peers(ctx context.Context) ([]Peer, error) {
	status, body, err := c.get(ctx, "/v1/peers")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("node answered %s reading presence", describe(status, body))
	}
	var decoded struct {
		Peers []struct {
			NodeID      string `json:"nodeId"`
			DisplayName string `json:"displayName"`
			Online      bool   `json:"online"`
			Sessions    []struct {
				ID         string    `json:"id"`
				Provider   string    `json:"provider"`
				Status     string    `json:"status"`
				CWD        string    `json:"cwd"`
				Management string    `json:"management"`
				Visibility string    `json:"visibility"`
				LastSeenAt time.Time `json:"lastSeenAt"`
			} `json:"sessions"`
		} `json:"peers"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode presence: %w", err)
	}
	peers := make([]Peer, 0, len(decoded.Peers))
	for _, p := range decoded.Peers {
		peer := Peer{NodeID: p.NodeID, DisplayName: p.DisplayName, Online: p.Online}
		refused, refusedID := false, ""
		for _, s := range p.Sessions {
			// A peer says what its own sessions are called, and nothing more.
			//
			// The node checks this too, when it accepts the heartbeat, which is
			// where every reader benefits. Kept here as well because this is the
			// layer that hands the answer to an agent, and a row that reached
			// the store before that check existed is still a row. If believed,
			// a peer could attribute a session to a third node that authorised
			// nothing, or send a bare local-form id colliding with one of this
			// machine's own sessions.
			nodeID, sessionID, qualified := address.SplitQualifiedID(s.ID)
			if !qualified || nodeID != p.NodeID || address.ValidateLocalSessionID(sessionID) != nil {
				// A bool, not the id itself: an empty claimed id would set a
				// string sentinel to "" and slip past the guard below, keeping
				// the peer with a silently truncated snapshot and no log line.
				refused, refusedID = true, s.ID
				break
			}
			peer.Sessions = append(peer.Sessions, Session{
				ID: s.ID, Node: p.NodeID,
				// Derived from the validated id, not copied from the peer's own
				// field, so the provider a caller filters on cannot disagree
				// with the provider the id names.
				Provider:   providerOf(sessionID),
				Status:     s.Status,
				CWD:        s.CWD,
				Management: s.Management,
				Visibility: s.Visibility,
				LastSeenAt: s.LastSeenAt,
			})
		}
		if refused {
			// This peer's whole snapshot goes, not just the bad row: one that
			// has started claiming other people's sessions is not a source
			// whose remaining rows are worth serving.
			//
			// But only this peer's. Failing the entire call would let any
			// single paired peer blank the owner's view of their own machine,
			// which is a bigger loss than the rows being withheld.
			log.Printf("ignoring presence from %s: it claimed a session id it does not own (%s)",
				p.NodeID, truncate(refusedID, 200))
			continue
		}
		peers = append(peers, peer)
	}
	return peers, nil
}

// TrustedNode is a paired node's identity as this node recorded it at pairing.
type TrustedNode struct {
	NodeID      string `json:"nodeId"`
	DisplayName string `json:"displayName"`
	Fingerprint string `json:"fingerprint"`
}

// TrustedNodes returns the pairing records, which carry the fingerprints.
//
// A message's sender is established by the envelope's signature, so the node id
// on a stored message is already proven. The fingerprint is what a person can
// compare out of band, and it is the only part of a sender's identity a reader
// can check for themselves.
func (c *Client) TrustedNodes(ctx context.Context) (map[string]TrustedNode, error) {
	status, body, err := c.get(ctx, "/v1/nodes")
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("node answered %s listing paired nodes", describe(status, body))
	}
	var decoded struct {
		Nodes []TrustedNode `json:"nodes"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode paired nodes: %w", err)
	}
	byID := make(map[string]TrustedNode, len(decoded.Nodes))
	for _, node := range decoded.Nodes {
		byID[node.NodeID] = node
	}
	return byID, nil
}

// StoredMessage is one message as the node holds it.
type StoredMessage struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}

// Inbox reads the messages held for one local session.
type Inbox struct {
	Messages []StoredMessage
	Held     int
	Capacity int
	Full     bool
	// Next is where the following page begins when the read stopped at the
	// caller's limit with more held; empty when it reached the end.
	Next string
}

// inboxBatch is how many messages are asked for at once.
//
// Bodies are capped at 32 KiB decoded, but JSON escaping expands "<", "&" and
// every control byte sixfold, so a batch is sized by what the response can
// become rather than by what the messages contain: ten worst-case bodies are
// about 2 MiB, comfortably inside the read cap. Asking for fifty at once put a
// peer within reach of exceeding it, after which nothing decoded and the tool
// returned no messages at all.
const inboxBatch = 10

// ReadInbox returns what the node holds for a session.
//
// Read in batches rather than in one request. The node will return as many
// messages as it is asked for, and a peer chooses the size of what it sends, so
// a single large request is a size a peer controls.
func (c *Client) ReadInbox(ctx context.Context, sessionID string, limit int) (Inbox, error) {
	// Clamped here as well as in the tool: an exported method on an exported
	// type should neither panic on a negative count nor read nothing for zero.
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	inbox := Inbox{Messages: make([]StoredMessage, 0, limit)}
	after := ""
	for len(inbox.Messages) < limit {
		want := limit - len(inbox.Messages)
		if want > inboxBatch {
			want = inboxBatch
		}
		batch, err := c.readInboxBatch(ctx, sessionID, after, want)
		if err != nil {
			return Inbox{}, err
		}
		inbox.Held, inbox.Capacity, inbox.Full = batch.Held, batch.Capacity, batch.Full
		if len(batch.Messages) > want {
			// A node that over-delivers does not get to exceed the caller's limit.
			batch.Messages = batch.Messages[:want]
		}
		inbox.Messages = append(inbox.Messages, batch.Messages...)
		if batch.Next == "" || len(batch.Messages) == 0 {
			// The node said this page was the last — or gave nothing, which
			// must not become a loop.
			return inbox, nil
		}
		after = batch.Next
	}
	// Stopped at the caller's limit with the node saying there is more.
	inbox.Next = after
	return inbox, nil
}

func (c *Client) readInboxBatch(ctx context.Context, sessionID string, after string, limit int) (Inbox, error) {
	path := fmt.Sprintf("/v1/inbox/%s?limit=%d&after=%s", url.PathEscape(sessionID), limit, url.QueryEscape(after))
	status, body, err := c.get(ctx, path)
	if err != nil {
		return Inbox{}, err
	}
	if status != http.StatusOK {
		return Inbox{}, fmt.Errorf("node answered %s reading the inbox", describe(status, body))
	}
	var decoded struct {
		Messages []StoredMessage `json:"messages"`
		Held     int             `json:"held"`
		Capacity int             `json:"capacity"`
		Full     bool            `json:"full"`
		Next     string          `json:"next"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		// A truncated answer decodes as badly as a malformed one, and the
		// difference matters: one is a bug, the other is a message too large to
		// read. Say which, because "unexpected end of JSON input" sends the
		// reader nowhere.
		if len(body) >= maxNodeResponse {
			return Inbox{}, fmt.Errorf(
				"the node's answer reached the %d byte limit and was cut off; "+
					"one of these messages is too large to read. The owner can list what is there with "+
					"`ah inbox <session>`, which reads in small pages, and remove one message with "+
					"`ah inbox-clear <session> <message-id>` or all of them with `ah inbox-clear <session>`", maxNodeResponse)
		}
		return Inbox{}, fmt.Errorf("decode inbox: %w", err)
	}
	return Inbox{
		Messages: decoded.Messages, Held: decoded.Held,
		Capacity: decoded.Capacity, Full: decoded.Full, Next: decoded.Next,
	}, nil
}

// SessionAudience is the part of a session's policy this server needs.
type SessionAudience struct {
	AcceptMessages bool
	AllowOutbound  bool
}

// Audience reads one local session's export policy.
func (c *Client) Audience(ctx context.Context, sessionID string) (SessionAudience, error) {
	status, body, err := c.get(ctx, "/v1/sessions/"+url.PathEscape(sessionID)+"/audience")
	if err != nil {
		return SessionAudience{}, err
	}
	if status != http.StatusOK {
		return SessionAudience{}, fmt.Errorf("node answered %s reading the audience", describe(status, body))
	}
	var decoded struct {
		AcceptMessages bool `json:"acceptMessages"`
		AllowOutbound  bool `json:"allowOutbound"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return SessionAudience{}, fmt.Errorf("decode audience: %w", err)
	}
	return SessionAudience{
		AcceptMessages: decoded.AcceptMessages,
		AllowOutbound:  decoded.AllowOutbound,
	}, nil
}

// QueuedMessage is what the node answers when it takes a message.
type QueuedMessage struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Note  string `json:"note"`
}

// SendMessage hands a message to the node for delivery.
func (c *Client) SendMessage(ctx context.Context, to, from, body string) (QueuedMessage, error) {
	payload, err := json.Marshal(map[string]string{"to": to, "from": from, "body": body})
	if err != nil {
		return QueuedMessage{}, err
	}
	status, response, err := c.post(ctx, "/v1/messages", payload)
	if err != nil {
		return QueuedMessage{}, err
	}
	if status != http.StatusOK && status != http.StatusCreated && status != http.StatusAccepted {
		return QueuedMessage{}, fmt.Errorf("node answered %s sending the message", describe(status, response))
	}
	var queued QueuedMessage
	if err := json.Unmarshal(response, &queued); err != nil {
		return QueuedMessage{}, fmt.Errorf("decode the queued message: %w", err)
	}
	return queued, nil
}

func (c *Client) post(ctx context.Context, path string, body []byte) (int, []byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("reach the node at %s: %w", c.baseURL, err)
	}
	defer func() { _ = response.Body.Close() }()
	answer, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return response.StatusCode, nil, err
	}
	return response.StatusCode, answer, nil
}

// providerOf reads the provider from a validated session id.
//
// Cut rather than Index-and-slice: the validation two lines above guarantees a
// colon today, and a panic on peer-controlled input is too high a price for
// that guarantee ever being reordered or relaxed.
func providerOf(sessionID string) string {
	provider, _, _ := strings.Cut(sessionID, ":")
	return provider
}
