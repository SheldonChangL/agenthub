package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
)

const defaultNodeURL = "http://127.0.0.1:7462"

// App is the Wails-bound surface. Every method is a thin, explicit wrapper over
// the node's local HTTP API so the desktop app stays a client, not a second
// writer of the registry.
type App struct {
	ctx context.Context

	mu     sync.RWMutex
	client *client
	url    string
}

func NewApp() *App {
	nodeURL := os.Getenv("AGENTHUB_URL")
	if nodeURL == "" {
		nodeURL = defaultNodeURL
	}
	return &App{client: newClient(nodeURL), url: nodeURL}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
}

func (a *App) current() (*client, string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.client, a.url
}

// NodeURL reports the node endpoint the app is pointed at.
func (a *App) NodeURL() string {
	_, nodeURL := a.current()
	return nodeURL
}

// SetNodeURL repoints the app at another node. Non-loopback hosts are refused:
// the local API has no authentication yet, so a remote target would be both
// unreachable and unsafe to encourage.
func (a *App) SetNodeURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("node URL is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse node URL %q: %w", raw, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("node URL must use http or https")
	}
	if parsed.Host == "" {
		return fmt.Errorf("node URL must include a host")
	}
	if !isLoopbackHost(parsed.Hostname()) {
		return fmt.Errorf("only loopback nodes are supported until authenticated LAN pairing exists")
	}
	trimmed := strings.TrimRight(raw, "/")
	a.mu.Lock()
	a.client = newClient(trimmed)
	a.url = trimmed
	a.mu.Unlock()
	return nil
}

type Overview struct {
	Node      NodeIdentity   `json:"node"`
	Sessions  []Session      `json:"sessions"`
	Counts    map[string]int `json:"counts"`
	NodeURL   string         `json:"nodeUrl"`
	Reachable bool           `json:"reachable"`
	Error     string         `json:"error,omitempty"`
}

// Overview loads everything the management view needs in one round trip so the
// UI never renders a half-populated table.
func (a *App) Overview() Overview {
	activeClient, nodeURL := a.current()
	result := Overview{NodeURL: nodeURL, Counts: map[string]int{}, Sessions: []Session{}}

	identity, err := activeClient.node(a.ctx)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	sessions, err := activeClient.listSessions(a.ctx)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Reachable = true
	result.Node = identity
	result.Sessions = sessions
	result.Counts = summarize(sessions)
	return result
}

func summarize(sessions []Session) map[string]int {
	counts := map[string]int{
		"total": len(sessions), "public": 0, "private": 0,
		"active": 0, "idle": 0, "inactive": 0, "unknown": 0,
		"claude": 0, "codex": 0,
	}
	for _, session := range sessions {
		counts[session.Visibility]++
		counts[session.Status]++
		counts[session.Provider]++
	}
	return counts
}

// Discover triggers a provider rescan on the node.
func (a *App) Discover() (map[string]int, error) {
	activeClient, _ := a.current()
	counts, err := activeClient.discover(a.ctx)
	if err != nil {
		return nil, err
	}
	return counts, nil
}

type VisibilityResult struct {
	Changed int      `json:"changed"`
	Failed  int      `json:"failed"`
	Errors  []string `json:"errors,omitempty"`
}

// SetVisibility applies one visibility choice to many sessions. This is the
// operation the CLI could only do one session at a time.
func (a *App) SetVisibility(ids []string, visibility string) (VisibilityResult, error) {
	if visibility != "public" && visibility != "private" {
		return VisibilityResult{}, fmt.Errorf("visibility must be public or private, got %q", visibility)
	}
	if len(ids) == 0 {
		return VisibilityResult{}, fmt.Errorf("select at least one session")
	}
	activeClient, _ := a.current()

	result := VisibilityResult{}
	failures := make([]string, 0)
	for _, id := range ids {
		if err := activeClient.setVisibility(a.ctx, id, visibility); err != nil {
			result.Failed++
			if len(failures) < 10 {
				failures = append(failures, fmt.Sprintf("%s: %v", id, err))
			}
			continue
		}
		result.Changed++
	}
	sort.Strings(failures)
	result.Errors = failures
	return result, nil
}

// Heartbeat returns the exact payload a future broker would receive. It is the
// app's proof to the user that private sessions never leave the host.
func (a *App) Heartbeat() (string, error) {
	activeClient, _ := a.current()
	raw, err := activeClient.heartbeat(a.ctx)
	if err != nil {
		return "", err
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("decode heartbeat: %w", err)
	}
	pretty, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode heartbeat: %w", err)
	}
	return string(pretty), nil
}

func isLoopbackHost(host string) bool {
	switch strings.ToLower(host) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	return strings.HasPrefix(host, "127.")
}
