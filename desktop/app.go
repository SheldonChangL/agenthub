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
	Nodes     []TrustedNode  `json:"nodes"`
	Counts    map[string]int `json:"counts"`
	NodeURL   string         `json:"nodeUrl"`
	Reachable bool           `json:"reachable"`
	Error     string         `json:"error,omitempty"`
}

// Overview loads everything the management view needs in one round trip so the
// UI never renders a half-populated table.
func (a *App) Overview() Overview {
	activeClient, nodeURL := a.current()
	result := Overview{NodeURL: nodeURL, Counts: map[string]int{},
		Sessions: []Session{}, Nodes: []TrustedNode{}}

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
	// A pairing list that fails to load must not blank the session view: the
	// two answer different questions and the owner still needs the sessions.
	nodes, err := activeClient.trustedNodes(a.ctx)
	if err != nil {
		result.Error = err.Error()
		nodes = []TrustedNode{}
	}

	result.Reachable = true
	result.Node = identity
	result.Sessions = sessions
	result.Nodes = nodes
	result.Counts = summarize(sessions)
	result.Counts["nodes"] = len(nodes)
	return result
}

func summarize(sessions []Session) map[string]int {
	counts := map[string]int{
		"total": len(sessions), "public": 0, "private": 0,
		"active": 0, "idle": 0, "inactive": 0, "unknown": 0,
		"claude": 0, "codex": 0,
		"none": 0, "all_paired": 0, "selected": 0,
	}
	for _, session := range sessions {
		counts[session.Visibility]++
		counts[session.Status]++
		counts[session.Provider]++
		if session.Audience.Mode != "" {
			counts[string(session.Audience.Mode)]++
		}
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

// SetAudience applies one export policy to many sessions.
//
// This is the operation the CLI could only do one session at a time, and the
// reason the desktop app exists: choosing who may see a session is a decision
// about a list, not about one row.
func (a *App) SetAudience(ids []string, audience Audience) (VisibilityResult, error) {
	if len(ids) == 0 {
		return VisibilityResult{}, fmt.Errorf("select at least one session")
	}
	switch audience.Mode {
	case "none", "all_paired", "selected":
	default:
		return VisibilityResult{}, fmt.Errorf("audience mode must be none, all_paired or selected, got %q", audience.Mode)
	}
	if audience.Mode == "selected" && len(audience.Nodes) == 0 {
		return VisibilityResult{}, fmt.Errorf("selected requires at least one node; use none to publish to nobody")
	}
	if audience.Mode != "selected" {
		audience.Nodes = nil
	}

	activeClient, _ := a.current()
	batch, err := activeClient.setAudienceBatch(a.ctx, ids, audience)
	if err != nil {
		return VisibilityResult{}, err
	}

	failures := make([]string, 0)
	for _, item := range batch.Results {
		if item.Error != "" && len(failures) < 10 {
			failures = append(failures, fmt.Sprintf("%s: %s", item.ID, item.Error))
		}
	}
	sort.Strings(failures)
	return VisibilityResult{Changed: batch.Changed, Failed: batch.Failed, Errors: failures}, nil
}

// SetVisibility keeps the simple publish and unpublish path working. Publishing
// means the explicit "all paired nodes" choice.
func (a *App) SetVisibility(ids []string, visibility string) (VisibilityResult, error) {
	switch visibility {
	case "public":
		// Export flags stay closed; the picker is where they are turned on.
		return a.SetAudience(ids, Audience{Mode: "all_paired"})
	case "private":
		return a.SetAudience(ids, Audience{Mode: "none"})
	default:
		return VisibilityResult{}, fmt.Errorf("visibility must be public or private, got %q", visibility)
	}
}

// TrustNode records a peer whose fingerprint the owner compared on both
// machines. The node refuses the pairing if the fingerprint does not belong to
// the key, so a mistyped or substituted key cannot be trusted by accident.
func (a *App) TrustNode(nodeID, displayName, platform, publicKey, confirmedFingerprint string) (TrustedNode, error) {
	activeClient, _ := a.current()
	return activeClient.trustNode(a.ctx, map[string]string{
		"nodeId":               nodeID,
		"displayName":          displayName,
		"platform":             platform,
		"publicKey":            publicKey,
		"confirmedFingerprint": confirmedFingerprint,
	})
}

// RevokeNode withdraws trust and every session grant the node held.
func (a *App) RevokeNode(nodeID string) error {
	activeClient, _ := a.current()
	return activeClient.revokeNode(a.ctx, nodeID)
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
