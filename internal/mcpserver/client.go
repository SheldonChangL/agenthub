package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	return &Client{
		baseURL: strings.TrimRight(parsed.String(), "/"),
		http:    &http.Client{Timeout: 15 * time.Second},
	}, nil
}

// SessionExists reports whether the node has this local session.
func (c *Client) SessionExists(ctx context.Context, sessionID string) error {
	status, _, err := c.get(ctx, "/v1/sessions/"+url.PathEscape(sessionID))
	if err != nil {
		return err
	}
	if status == http.StatusNotFound {
		return ErrSessionNotFound
	}
	if status != http.StatusOK {
		return fmt.Errorf("node answered %d for that session", status)
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
		return "", fmt.Errorf("node answered %d for its identity", status)
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
