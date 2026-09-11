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

// SetNodeURL repoints the app at another node on this machine. Non-loopback
// hosts are refused: the owner's API has no authentication and stays on loopback
// for that reason, so a remote target would be both unreachable and unsafe to
// encourage.
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
		return fmt.Errorf("the node URL must be loopback: this app speaks to the owner's API, " +
			"which has no authentication and is served on loopback for that reason. This field " +
			"says where this machine's own node is; to reach another machine, pair with it in " +
			"the Network view — its node answers this one over its peer listener")
	}
	trimmed := strings.TrimRight(raw, "/")
	a.mu.Lock()
	a.client = newClient(trimmed)
	a.url = trimmed
	a.mu.Unlock()
	return nil
}

type Overview struct {
	Node     NodeIdentity  `json:"node"`
	Sessions []Session     `json:"sessions"`
	Nodes    []TrustedNode `json:"nodes"`
	// Peers is what each paired node has published to this one. It is separate
	// from Nodes because the two answer different questions: Nodes is who the
	// owner trusts, Peers is who is currently reachable and what they are
	// sharing. A node can be trusted and silent.
	Peers []Peer `json:"peers"`
	// PresenceError is separate from Error because a failure to read presence
	// must not be rendered as a fact about the peers. Without it, an
	// unreachable presence endpoint looks exactly like every peer having gone
	// quiet, which is a confident claim with nothing behind it.
	PresenceError string         `json:"presenceError,omitempty"`
	Counts        map[string]int `json:"counts"`
	// NodeCount is separate from Counts, which is keyed by session attribute
	// values; a provider or status could otherwise collide with it.
	NodeCount int    `json:"nodeCount"`
	NodeURL   string `json:"nodeUrl"`
	Reachable bool   `json:"reachable"`
	Error     string `json:"error,omitempty"`
}

// Overview loads everything the management view needs in one round trip so the
// UI never renders a half-populated table.
func (a *App) Overview() Overview {
	activeClient, nodeURL := a.current()
	result := Overview{NodeURL: nodeURL, Counts: map[string]int{},
		Sessions: []Session{}, Nodes: []TrustedNode{}, Peers: []Peer{}}

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

	// Presence failing must not blank the view either, for the same reason the
	// pairing list must not: the local sessions are still worth showing.
	peers, err := activeClient.peers(a.ctx)
	if err != nil {
		// Reported separately so it cannot overwrite a pairing-list error, and
		// so the view can say "presence is unavailable" rather than inventing a
		// claim about every peer.
		result.PresenceError = err.Error()
		peers = []Peer{}
	}

	result.Reachable = true
	result.Node = identity
	result.Peers = peers
	result.Sessions = sessions
	result.Nodes = nodes
	result.Counts = summarize(sessions)
	result.NodeCount = len(nodes)
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

// Pairing is everything the pairing panel needs, in one call so the panel makes
// one round trip rather than three.
//
// Not one atomic answer: the window and the list are two requests, and the
// second can fail on its own — which is why CandidatesError exists and why the
// panel has to be able to render a window with no list beside it.
type Pairing struct {
	State      PairingState `json:"state"`
	Candidates []Candidate  `json:"candidates"`
	// Full means the list is at its limit, so a machine the owner is looking
	// for may be missing for that reason rather than because it is silent.
	Full bool `json:"full"`
	// Notice is the node's own words about what this list is worth. Taken from
	// the node rather than written here, so the warning cannot drift from the
	// guarantees the node actually makes.
	Notice string `json:"notice"`
	// Availability is "on", "off" or "unknown", and it is three values rather
	// than a boolean because the panel has three different things to say.
	// "off" is a node started without -discover, which the owner can change by
	// restarting it. "unknown" is a node that did not answer, where saying
	// anything about the network would be a claim with nothing behind it. A
	// boolean would have to render one of those as the other.
	Availability string `json:"availability"`
	// Error and CandidatesError are separate, and separate from Availability,
	// because a failure to read must never be rendered as a fact about the
	// network. An unreachable node looks exactly like an empty segment
	// otherwise.
	Error           string `json:"error,omitempty"`
	CandidatesError string `json:"candidatesError,omitempty"`
}

// Pairing availability, as the panel has to talk about it.
const (
	pairingOn      = "on"
	pairingOff     = "off"
	pairingUnknown = "unknown"
)

// Pairing reports whether this machine is advertising and who else is.
func (a *App) Pairing() Pairing {
	activeClient, _ := a.current()
	result := Pairing{Candidates: []Candidate{}}

	state, err := activeClient.pairingState(a.ctx)
	if err != nil {
		result.Error = err.Error()
		if isDiscoveryDisabled(err) {
			result.Availability = pairingOff
		} else {
			result.Availability = pairingUnknown
		}
		return result
	}
	result.Availability = pairingOn
	result.State = state

	candidates, full, notice, err := activeClient.candidates(a.ctx)
	if err != nil {
		result.CandidatesError = err.Error()
		return result
	}
	result.Candidates = candidates
	result.Full = full
	result.Notice = notice
	return result
}

// OpenPairing starts advertising for a while. Seconds of zero asks the node for
// its default window.
//
// The node refuses a window it could not announce, and refuses one outside its
// own bounds, rather than clamping — so an owner who asked for an hour is told
// the limit instead of being given fifteen minutes and believing they have an
// hour. Those refusals arrive here as errors and are shown as they are.
func (a *App) OpenPairing(seconds int) (PairingState, error) {
	activeClient, _ := a.current()
	return activeClient.openPairing(a.ctx, seconds)
}

// ClosePairing stops advertising now. Idempotent, so the button works whatever
// the panel currently believes.
func (a *App) ClosePairing() (PairingState, error) {
	activeClient, _ := a.current()
	return activeClient.closePairing(a.ctx)
}

// isDiscoveryDisabled distinguishes "this node is not looking" from "this node
// could not be reached", which the panel must not conflate: the first is a
// configuration the owner can change, the second is a failure.
func isDiscoveryDisabled(err error) bool {
	return err != nil && strings.Contains(err.Error(), "DISCOVERY_DISABLED")
}

// InboxView is one session's inbox as the owner sees it.
type InboxView struct {
	SessionID string         `json:"sessionId"`
	Messages  []InboxMessage `json:"messages"`
	Held      int            `json:"held"`
	Capacity  int            `json:"capacity"`
	Full      bool           `json:"full"`
	// Showing and More say that this is a page. Without them a view of ten out
	// of five hundred looks like an inbox of ten — and the ten are the oldest,
	// because the node returns them in arrival order.
	Showing int  `json:"showing"`
	More    bool `json:"more"`
	// Error is separate from an empty list, because "nothing has been sent" and
	// "this could not be read" are different facts and only one of them means
	// the owner should stop looking.
	Error string `json:"error,omitempty"`
}

// Inbox reads what other nodes have queued for one of this owner's sessions.
//
// The desktop can show it because the owner may want to see what arrived
// without asking an agent to look. Reading it here changes nothing: the node
// does not mark anything read, and nothing hands a message to an agent — that
// is still the documented boundary.
func (a *App) Inbox(sessionID string) InboxView {
	view := InboxView{SessionID: sessionID, Messages: []InboxMessage{}}
	if strings.TrimSpace(sessionID) == "" {
		view.Error = "select a session first"
		return view
	}
	activeClient, _ := a.current()
	// Ten, not fifty. A body is 32KB and a control character in it escapes to
	// six JSON bytes, so a peer can make fifty messages serialise to more than
	// this app will read — after which nothing decodes and the owner cannot see
	// what is jamming the inbox they came to look at. The CLI reaches the same
	// number for the same reason.
	//
	// The node returns them oldest first, so this is the start of the queue,
	// not the recent end.
	inbox, err := activeClient.inbox(a.ctx, sessionID, inboxPageSize)
	if err != nil {
		view.Error = err.Error()
		return view
	}
	view.Messages = inbox.Messages
	view.Held = inbox.Held
	view.Capacity = inbox.Capacity
	view.Full = inbox.Full
	// Said rather than left to be inferred from a short list: an inbox holding
	// five hundred, shown ten at a time, must not read as an inbox holding ten.
	//
	// The cursor alone does not mean more: the node issues one whenever a page
	// comes back full, so an inbox holding exactly ten answers with one. Taking
	// it at face value put "there is more, clear some to see it" beside the one
	// irreversible button in the dialog, about messages that do not exist.
	view.Showing = len(inbox.Messages)
	view.More = inbox.Next != "" && inbox.Held > len(inbox.Messages)
	return view
}

// inboxPageSize is how many messages one read asks for. See Inbox for why it is
// not larger.
const inboxPageSize = 10

// ClearedInbox is what emptying did, for the dialog to show. Not a banner: the
// modal is fixed over the whole window, so a banner behind it is a message
// nobody reads — the same reason read errors are shown in the dialog.
type ClearedInbox struct {
	Removed int    `json:"removed"`
	Error   string `json:"error,omitempty"`
}

// ClearInbox empties one session's inbox.
//
// Destructive and not undoable, so the frontend asks first. It exists because
// an inbox that only grows is one an owner cannot keep usable, and because a
// full one refuses new messages.
func (a *App) ClearInbox(sessionID string) ClearedInbox {
	if strings.TrimSpace(sessionID) == "" {
		return ClearedInbox{Error: "select a session first"}
	}
	activeClient, _ := a.current()
	removed, err := activeClient.clearInbox(a.ctx, sessionID)
	if err != nil {
		// Returned rather than thrown, so the dialog can say the destructive
		// action did not happen. Thrown, it reached a banner the dialog covers
		// while the list below sat unchanged.
		return ClearedInbox{Error: err.Error()}
	}
	return ClearedInbox{Removed: removed}
}

// OutboundView is what this node has queued for peers, for the records dialog.
//
// Its own answer, separate from the wake trail's, because they are two reads
// of two endpoints and one failing must not blank the other. The error is
// carried rather than thrown: the dialog is fixed over the window, so a
// failure reported in a banner is one nobody sees.
type OutboundView struct {
	Messages []OutboundRecord `json:"messages"`
	Error    string           `json:"error,omitempty"`
}

// WakesView is the wake trail, optionally for one session.
type WakesView struct {
	SessionID string       `json:"sessionId,omitempty"`
	Wakes     []WakeRecord `json:"wakes"`
	Error     string       `json:"error,omitempty"`
}

// recordsPageSize is how many rows one read asks for. Fifty, the node's own
// default: these rows carry no message bodies, so the reason the inbox reads
// ten at a time does not apply.
const recordsPageSize = 50

// Outbound reads what this node has queued for peers.
//
// The GUI could see sessions and inboxes and nothing about what left this
// machine: pending, delivered or refused was `ah outbound` only, and a message
// sitting refused is the failure an owner most needs to see and least likely
// to go looking for.
func (a *App) Outbound() OutboundView {
	view := OutboundView{Messages: []OutboundRecord{}}
	activeClient, _ := a.current()
	messages, err := activeClient.listOutbound(a.ctx, recordsPageSize)
	if err != nil {
		view.Error = err.Error()
		return view
	}
	view.Messages = messages
	return view
}

// Wakes reads what has started a turn on this node with nobody watching. An
// empty sessionID means every session.
//
// The refusals come with it. An owner who sees nothing needs to know whether
// the node was quiet or a rate limit held something back, and those call for
// different actions.
func (a *App) Wakes(sessionID string) WakesView {
	sessionID = strings.TrimSpace(sessionID)
	view := WakesView{SessionID: sessionID, Wakes: []WakeRecord{}}
	activeClient, _ := a.current()
	wakes, err := activeClient.listWakes(a.ctx, recordsPageSize, sessionID)
	if err != nil {
		view.Error = err.Error()
		return view
	}
	view.Wakes = wakes
	return view
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

// SetNodeAddress records where a paired node answers, as host:port.
//
// Trust and address are separate facts and this changes only the second:
// pairing says who a node is, this says where it currently is, which is what
// changes when a laptop moves between networks. It exists because a peer with
// no address is skipped in silence — the sender's `ah send` still answers
// `queued` — and with no broadcast on the segment there was nothing in this
// window that could supply one.
//
// The node validates the address and its refusal is returned verbatim, because
// what is wrong with an address is something only the node knows.
func (a *App) SetNodeAddress(nodeID, address string) error {
	activeClient, _ := a.current()
	return activeClient.setNodeAddress(a.ctx, nodeID, address)
}

// RevokeNode withdraws trust and every session grant the node held.
func (a *App) RevokeNode(nodeID string) error {
	activeClient, _ := a.current()
	return activeClient.revokeNode(a.ctx, nodeID)
}

// Heartbeat returns the owner's own preview of what this node exports: the union
// of every published session, signed the way a peer's copy would be.
//
// It is not what any peer receives. A peer gets the output of BuildFor, which
// applies that peer's audience, so this preview is a superset: a session
// published only to node X appears here, while every other peer's envelope
// omits it. What it does show, and what it is here for, is that a session the
// owner has not published appears in no export at all.
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
