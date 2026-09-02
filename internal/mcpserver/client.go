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
	// Loopback only, and not merely because the owner's API binds there.
	// This process writes message bodies authored on other machines into an
	// agent's reasoning context. Pointing it at a node someone else controls
	// would let that party choose what the agent reads, and the instructions
	// this server sends at initialize would stop being true.
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
	if host == "localhost" {
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
		return fmt.Sprintf("%d %s: %s", status, envelope.Error.Code, envelope.Error.Message)
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
