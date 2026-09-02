package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

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
		message := envelope.Error.Message
		// Bounded: get() allows 8 MB, and none of it belongs in one error line.
		if runes := []rune(message); len(runes) > 200 {
			message = string(runes[:200]) + "…"
		}
		return fmt.Sprintf("%d %s: %s", status, envelope.Error.Code, message)
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
	body, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
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
		for _, s := range p.Sessions {
			peer.Sessions = append(peer.Sessions, Session{
				ID: s.ID, Node: p.NodeID, Provider: s.Provider, Status: s.Status,
				CWD: s.CWD, Management: s.Management, Visibility: s.Visibility,
				LastSeenAt: s.LastSeenAt,
			})
		}
		peers = append(peers, peer)
	}
	return peers, nil
}
