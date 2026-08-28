package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Session mirrors the node's public JSON contract. The desktop app is an HTTP
// client of agenthub-node and never reads provider files or SQLite directly.
// Audience is a session's export policy: published to whom, and how much.
type Audience struct {
	Mode           string   `json:"mode"`
	Nodes          []string `json:"nodes,omitempty"`
	ExportCWD      bool     `json:"exportCwd"`
	AcceptMessages bool     `json:"acceptMessages"`
}

type Session struct {
	ID                string    `json:"id"`
	Provider          string    `json:"provider"`
	ProviderSessionID string    `json:"providerSessionId"`
	Management        string    `json:"management"`
	Visibility        string    `json:"visibility"`
	Audience          Audience  `json:"audience"`
	Status            string    `json:"status"`
	StatusSource      string    `json:"statusSource"`
	CWD               string    `json:"cwd,omitempty"`
	Source            string    `json:"source,omitempty"`
	LastSeenAt        time.Time `json:"lastSeenAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

type NodeIdentity struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"displayName"`
	Platform    string    `json:"platform"`
	CreatedAt   time.Time `json:"createdAt"`
}

type client struct {
	baseURL string
	http    *http.Client
}

func newClient(baseURL string) *client {
	return &client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (c *client) listSessions(ctx context.Context) ([]Session, error) {
	type listResponse struct {
		Sessions   []Session `json:"sessions"`
		Pagination struct {
			TotalPages int `json:"totalPages"`
		} `json:"pagination"`
	}
	all := make([]Session, 0, 256)
	for page := 1; ; page++ {
		body, err := c.request(ctx, http.MethodGet, fmt.Sprintf("/v1/sessions?page=%d&pageSize=200", page), nil)
		if err != nil {
			return nil, err
		}
		var response listResponse
		if err := json.Unmarshal(body, &response); err != nil {
			return nil, fmt.Errorf("decode session list: %w", err)
		}
		all = append(all, response.Sessions...)
		if response.Pagination.TotalPages == 0 || page >= response.Pagination.TotalPages {
			break
		}
	}
	return all, nil
}

// setAudienceBatch applies one policy to many sessions in a single request.
//
// The node reports per-session outcomes, so a partial failure stays partial
// rather than being retried as a whole.
func (c *client) setAudienceBatch(ctx context.Context, ids []string, audience Audience) (BatchResult, error) {
	if audience.Nodes == nil {
		audience.Nodes = []string{}
	}
	body, err := c.request(ctx, http.MethodPost, "/v1/sessions/audience", map[string]any{
		"ids":      ids,
		"audience": audience,
	})
	if err != nil {
		return BatchResult{}, err
	}
	var result BatchResult
	if err := json.Unmarshal(body, &result); err != nil {
		return BatchResult{}, fmt.Errorf("decode batch result: %w", err)
	}
	return result, nil
}

type BatchResult struct {
	Changed int `json:"changed"`
	Failed  int `json:"failed"`
	Results []struct {
		ID    string `json:"id"`
		Error string `json:"error,omitempty"`
	} `json:"results"`
}

func (c *client) discover(ctx context.Context) (map[string]int, error) {
	body, err := c.request(ctx, http.MethodPost, "/v1/discover", nil)
	if err != nil {
		return nil, err
	}
	counts := map[string]int{}
	if err := json.Unmarshal(body, &counts); err != nil {
		return nil, fmt.Errorf("decode discover result: %w", err)
	}
	return counts, nil
}

func (c *client) node(ctx context.Context) (NodeIdentity, error) {
	body, err := c.request(ctx, http.MethodGet, "/v1/node", nil)
	if err != nil {
		return NodeIdentity{}, err
	}
	var identity NodeIdentity
	if err := json.Unmarshal(body, &identity); err != nil {
		return NodeIdentity{}, fmt.Errorf("decode node identity: %w", err)
	}
	return identity, nil
}

func (c *client) heartbeat(ctx context.Context) (json.RawMessage, error) {
	body, err := c.request(ctx, http.MethodGet, "/v1/heartbeat", nil)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(body), nil
}

func (c *client) request(ctx context.Context, method, path string, input any) ([]byte, error) {
	var body io.Reader
	if input != nil {
		var encoded bytes.Buffer
		if err := json.NewEncoder(&encoded).Encode(input); err != nil {
			return nil, fmt.Errorf("encode request: %w", err)
		}
		body = &encoded
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("contact node: %w", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 8*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var apiError struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &apiError) == nil && apiError.Error.Message != "" {
			return nil, fmt.Errorf("%s: %s", apiError.Error.Code, apiError.Error.Message)
		}
		return nil, fmt.Errorf("node returned HTTP %d", response.StatusCode)
	}
	return data, nil
}
