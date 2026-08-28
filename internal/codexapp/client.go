package codexapp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
	"unicode"

	"agenthub.local/agenthub/internal/model"
)

type Client struct {
	transport io.ReadWriter
	reader    *bufio.Scanner
	mu        sync.Mutex
	nextID    int64
}

func NewClient(transport io.ReadWriter) *Client {
	scanner := bufio.NewScanner(transport)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	return &Client{transport: transport, reader: scanner}
}

type InitializeResult struct {
	CodexHome      string `json:"codexHome"`
	PlatformFamily string `json:"platformFamily"`
	PlatformOS     string `json:"platformOs"`
	UserAgent      string `json:"userAgent"`
}

func (c *Client) Initialize(ctx context.Context) (InitializeResult, error) {
	var result InitializeResult
	params := map[string]any{
		"clientInfo": map[string]string{"name": "agenthub", "version": "0.1.0"},
	}
	if err := c.call(ctx, "initialize", params, &result); err != nil {
		return InitializeResult{}, err
	}
	if result.CodexHome == "" || result.PlatformOS == "" {
		return InitializeResult{}, errors.New("Codex App Server returned incomplete initialize result")
	}
	if err := c.notify(ctx, "initialized"); err != nil {
		return InitializeResult{}, err
	}
	return result, nil
}

type ThreadStatus struct {
	Type string `json:"type"`
}

type Thread struct {
	ID        string       `json:"id"`
	CWD       string       `json:"cwd"`
	Status    ThreadStatus `json:"status"`
	UpdatedAt int64        `json:"updatedAt"`
}

type ThreadListResult struct {
	Data       []Thread `json:"data"`
	NextCursor *string  `json:"nextCursor"`
}

func (c *Client) ListThreads(ctx context.Context, cursor string) (ThreadListResult, error) {
	params := map[string]any{
		"limit":          200,
		"sortKey":        "updated_at",
		"sortDirection":  "desc",
		"useStateDbOnly": true,
	}
	if cursor != "" {
		params["cursor"] = cursor
	}
	var result ThreadListResult
	if err := c.call(ctx, "thread/list", params, &result); err != nil {
		return ThreadListResult{}, err
	}
	if result.Data == nil {
		result.Data = make([]Thread, 0)
	}
	return result, nil
}

func (c *Client) call(ctx context.Context, method string, params any, destination any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := c.nextID
	request := struct {
		ID     int64  `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params"`
	}{ID: id, Method: method, Params: params}
	if err := json.NewEncoder(c.transport).Encode(request); err != nil {
		return fmt.Errorf("write Codex App Server %s request: %w", method, err)
	}

	for skipped := 0; skipped < 1000 && c.reader.Scan(); skipped++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		var response struct {
			ID     int64           `json:"id"`
			Method string          `json:"method"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(c.reader.Bytes(), &response); err != nil || response.ID == 0 {
			continue
		}
		if response.Method != "" {
			return fmt.Errorf("unsupported Codex App Server request %q", response.Method)
		}
		if response.ID != id {
			return fmt.Errorf("Codex App Server response id %d does not match request id %d", response.ID, id)
		}
		if response.Error != nil {
			return fmt.Errorf("Codex App Server error %d: %s", response.Error.Code, response.Error.Message)
		}
		if len(response.Result) == 0 {
			return errors.New("Codex App Server response has no result")
		}
		if err := json.Unmarshal(response.Result, destination); err != nil {
			return fmt.Errorf("decode Codex App Server %s result: %w", method, err)
		}
		return nil
	}
	if err := c.reader.Err(); err != nil {
		return fmt.Errorf("read Codex App Server response: %w", err)
	}
	return errors.New("Codex App Server closed before sending a matching response")
}

func (c *Client) notify(ctx context.Context, method string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := json.NewEncoder(c.transport).Encode(map[string]string{"method": method}); err != nil {
		return fmt.Errorf("write Codex App Server %s notification: %w", method, err)
	}
	return nil
}

func NormalizeThreads(threads []Thread, observedAt time.Time) []model.Session {
	sessions := make([]model.Session, 0, len(threads))
	for _, thread := range threads {
		id := strings.TrimSpace(thread.ID)
		if !validID(id) {
			continue
		}
		status := model.StatusUnknown
		switch thread.Status.Type {
		case "active":
			status = model.StatusActive
		case "idle":
			status = model.StatusIdle
		case "notLoaded":
			status = model.StatusInactive
		case "systemError":
			status = model.StatusUnknown
		}
		lastSeen := observedAt.UTC()
		if thread.UpdatedAt > 0 {
			lastSeen = time.Unix(thread.UpdatedAt, 0).UTC()
		}
		sessions = append(sessions, model.Session{
			ID:                model.SessionID(model.ProviderCodex, id),
			Provider:          model.ProviderCodex,
			ProviderSessionID: id,
			Management:        model.Unmanaged,
			Visibility:        model.VisibilityPrivate,
			Status:            status,
			StatusSource:      "codex_app_server",
			CWD:               thread.CWD,
			Source:            "codex-app-server",
			LastSeenAt:        lastSeen,
			UpdatedAt:         observedAt.UTC(),
		})
	}
	return sessions
}

func validID(id string) bool {
	return id != "" && len(id) <= 256 && !strings.ContainsFunc(id, unicode.IsControl)
}
