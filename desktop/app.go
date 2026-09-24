package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	runtimepkg "runtime"
	"sort"
	"strings"
	"sync"
)

// HostPlatform is the operating system this window is running on.
//
// The window needs it for one thing the node cannot answer: on macOS the title
// bar is `mac.TitleBarHiddenInset()`, so the traffic lights are drawn over the
// top-left of the page and the content has to leave room for them. Asking the
// node's platform instead would be wrong twice — it is a different fact, and it
// is unavailable exactly when the window has nothing else to show.
func (a *App) HostPlatform() string {
	return runtimepkg.GOOS
}

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
	// NoticeCode names Notice, so the window can say it in its own language
	// and fall back to Notice for a code it does not know — or for a node that
	// predates the code and sends none.
	NoticeCode string `json:"noticeCode,omitempty"`
	// Availability is "on", "off", "openNotAnnouncing" or "unknown", and it is
	// four values rather than a boolean because the panel has four different
	// things to say. "off" is a node started without -discover and with no
	// window open, which the owner can change by restarting it.
	// "openNotAnnouncing" is that same node with a window open: nobody will
	// find it on the network, and a request sent to its address still arrives.
	// "unknown" is a node that did not answer, where saying anything about the
	// network would be a claim with nothing behind it. A boolean would have to
	// render one of those as another.
	Availability string `json:"availability"`
	// WindowAvailable says the pairing window itself can be opened and closed
	// from here, which is a different question from Availability.
	//
	// They used to be one answer, and that was right while being found on the
	// network was the only way to pair. It is not any more: the window is a
	// node-level state, a node started without -discover opens one all the
	// same, and the other machine pairs by typing this one's address. Left as
	// one answer, the panel greys out the button on exactly the node whose
	// owner has no other way in.
	WindowAvailable bool `json:"windowAvailable"`
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
	// pairingOpenNotAnnouncing is a node with a window open that cannot be
	// found on the network: it was started without -discover, so the candidate
	// list is refused, but requests sent to its address still arrive.
	//
	// Its own value because the two halves have different remedies. Rendering
	// it as "off" said the window was shut while it was open and collecting
	// requests, and sent the owner to reopen something that was already there
	// instead of to type this machine's address on the other one.
	pairingOpenNotAnnouncing = "openNotAnnouncing"
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
	// The window endpoints answered, so the window is this owner's to open —
	// whatever the candidate list says below.
	result.WindowAvailable = true

	list, err := activeClient.candidates(a.ctx)
	if err != nil {
		// A node without -discover now answers the window endpoints — the
		// window is a node-level state and the pairing exchange needs only that
		// — and refuses the candidate list. Not a failure to read, so it is not
		// reported as one; what it says about this node depends on the window,
		// which was read first and is authoritative here.
		if isDiscoveryDisabled(err) {
			result.Availability = pairingOff
			if state.Open {
				result.Availability = pairingOpenNotAnnouncing
			}
			return result
		}
		result.CandidatesError = err.Error()
		return result
	}
	result.Candidates = list.Candidates
	result.Full = list.Full
	result.Notice = list.Notice
	result.NoticeCode = list.NoticeCode
	return result
}

// OpenPairing starts advertising for a while. Seconds of zero asks the node for
// its default window.
//
// A window the node cannot announce is opened all the same — the other machine
// can be given this one's address to type — and the state that comes back says
// so in its notice. What the node does refuse is a window outside its own
// bounds, rather than clamping it, so an owner who asked for an hour is told
// the limit instead of being given fifteen minutes and believing they have an
// hour. That refusal arrives here as an error and is shown as it is.
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

// PairRequests is what each side of an exchange is waiting for.
//
// `all` adds the rows that finished in the last ten minutes. The default is
// only what still needs somebody to do something, for the same reason `ah pair
// pending` defaults that way: a list where the one row needing a decision sits
// under four decided ones is how an owner misses their own pairing.
//
// Reading is not free at the node — it polls every pending outgoing request at
// the far side first — which is why the window asks only while the pairing
// drawer is open.
func (a *App) PairRequests(all bool) ([]PairRequest, error) {
	activeClient, _ := a.current()
	return activeClient.pairRequests(a.ctx, all)
}

// StartPairRequest asks the machine at that address to trust this one.
//
// The address is not validated here. The node holds the ranges this build will
// talk to and refuses one outside them, in its own words; a second rule in this
// process could only disagree with the one that actually decides.
func (a *App) StartPairRequest(address string) (PairRequest, error) {
	activeClient, _ := a.current()
	return activeClient.startPairRequest(a.ctx, address)
}

// ApprovePairRequest is this owner saying the two fingerprints match, on the
// machine that was asked. It writes the trust row for that machine and nothing
// else: what a peer may see stays a separate decision, per session.
func (a *App) ApprovePairRequest(id string) (PairRequest, error) {
	activeClient, _ := a.current()
	return activeClient.decidePairRequest(a.ctx, id, "approve")
}

// ConfirmPairRequest is the same sentence on the machine that asked.
//
// Both owners say it, and neither says it for the other: an approval over there
// writes that machine's trust store, and the person here has compared nothing
// until they do this.
func (a *App) ConfirmPairRequest(id string) (PairRequest, error) {
	activeClient, _ := a.current()
	return activeClient.decidePairRequest(a.ctx, id, "confirm")
}

// RejectPairRequest refuses one, in either direction. On the requesting side
// the node pushes the refusal to the other machine, so an approval it may
// already have written is taken back.
func (a *App) RejectPairRequest(id string) (PairRequest, error) {
	activeClient, _ := a.current()
	return activeClient.decidePairRequest(a.ctx, id, "reject")
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

// InboxCountsView is every local inbox's depth, for the badges on the list.
//
// Ok is the field the badges are drawn from, and it is why this is a struct
// rather than a bare map. A read that did not reach the node leaves Counts
// empty, and an empty map is indistinguishable from "every inbox is empty" —
// which would paint zero where the truth is "nobody knows". Zero and unknown
// are different facts and the window shows only one of them (issue #146).
type InboxCountsView struct {
	Counts map[string]InboxCount `json:"counts"`
	Ok     bool                  `json:"ok"`
	Error  string                `json:"error,omitempty"`
}

// InboxCounts reads how much every local session is still holding.
//
// A thin wrapper over one node endpoint, called after each Overview() so the
// badges are as current as the rows they sit on. Like Inbox it changes nothing:
// the node has no notion of read, and this is a count of what is still queued —
// what an agent has not taken yet — not of what nobody has looked at.
//
// Never returns an error to the frontend: a rejected binding call and an
// answered one that says "could not reach the node" are handled at the same
// place, and there is only one sensible reaction either way.
func (a *App) InboxCounts() InboxCountsView {
	view := InboxCountsView{Counts: map[string]InboxCount{}}
	activeClient, _ := a.current()
	counts, err := activeClient.inboxCounts(a.ctx)
	if err != nil {
		view.Error = err.Error()
		return view
	}
	view.Counts = counts
	view.Ok = true
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

// NodeAddressesSaved is what SetNodeAddresses did.
type NodeAddressesSaved struct {
	// OlderNode is a node that predates address lists, and with them
	// alternates: it keeps one address per paired node. Only the first
	// address was recorded, through the one-address endpoint, and the window
	// has to say that the rest were not.
	OlderNode bool `json:"olderNode"`
}

// SetNodeAddresses replaces every address a paired node answers on, as
// host:port, the first preferred (ADR-005 §4).
//
// It is the node detail page's editor: the whole set, so an address the owner
// removes — a mistyped one, most often — is gone. SetNodeAddress cannot do
// that, since the node keeps the address it replaces as an alternate. The
// node validates every address and refuses the list whole, verbatim.
//
// On a node too old to take a list, the first address goes through the
// one-address endpoint instead, and OlderNode says that is all it did.
func (a *App) SetNodeAddresses(nodeID string, addresses []string) (NodeAddressesSaved, error) {
	activeClient, _ := a.current()
	err := activeClient.setNodeAddresses(a.ctx, nodeID, addresses)
	if !errors.Is(err, errNoAddressList) {
		return NodeAddressesSaved{}, err
	}
	first := ""
	if len(addresses) > 0 {
		first = addresses[0]
	}
	return NodeAddressesSaved{OlderNode: true}, activeClient.setNodeAddress(a.ctx, nodeID, first)
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

// OutboundView is the send log as the owner sees it: one page, plus whether it
// could be read at all.
//
// Error is separate from an empty list for the same reason it is on InboxView.
// "Nothing has been sent from this machine" and "this node did not answer" are
// different facts, and they arrive on the wire as the same empty table unless
// something says which one happened.
type OutboundView struct {
	OutboundPage
	Error string `json:"error,omitempty"`
}

// NodeSettingsView is NodeSettings plus the reason a read or write failed.
//
// The node's own refusal text is the useful part of a 400 here — it names the
// address that was sent and what to send with it — so it is carried verbatim
// rather than replaced with a sentence of this app's own.
type NodeSettingsView struct {
	NodeSettings
	Error string `json:"error,omitempty"`
}

// NodeSettings reads what the node remembers for its own start-up.
func (a *App) NodeSettings() NodeSettingsView {
	activeClient, _ := a.current()
	settings, err := activeClient.nodeSettings(a.ctx)
	if err != nil {
		return NodeSettingsView{NodeSettings: emptyNodeSettings(), Error: err.Error()}
	}
	return NodeSettingsView{NodeSettings: settings}
}

// SaveNodeSettings writes the fields the owner changed and answers with the
// whole record as the node now sees it.
//
// The answer is the whole record on purpose: turning allowLan off pulls
// peerListen back to loopback in the same write, and after #134 any write can
// do it when the stored allowLan is already false — so a caller that updated
// only the fields it sent would leave a LAN address on screen that the node no
// longer has.
func (a *App) SaveNodeSettings(patch NodeSettingsPatch) NodeSettingsView {
	activeClient, _ := a.current()
	settings, err := activeClient.saveNodeSettings(a.ctx, patch)
	if err != nil {
		return NodeSettingsView{NodeSettings: emptyNodeSettings(), Error: err.Error()}
	}
	return NodeSettingsView{NodeSettings: settings}
}

func emptyNodeSettings() NodeSettings {
	return NodeSettings{
		Sources:  map[string]string{},
		Settings: NodeSettingValues{TreatAsPrivate: []string{}},
		Saved:    NodeSettingValues{TreatAsPrivate: []string{}},
	}
}

// Outbound reads what this node has queued for peers, newest first.
//
// `ah send` answers "queued" and nothing more, so without this an owner who has
// closed that terminal has no way to ask what became of a message. A session
// narrows the list to what that local session sent; empty asks for every
// session on this node. A limit of zero asks for the node's page size; after is
// the `next` cursor from the previous page, empty to start at the newest — and
// a continuation must repeat the same session, or the second page is the
// node-wide one.
//
// A failure is returned in Error rather than thrown, because a thrown error
// reaches a banner while the table below it keeps showing the last good page as
// though it were current.
func (a *App) Outbound(session string, limit int, after string) OutboundView {
	view := OutboundView{OutboundPage: OutboundPage{Messages: []OutboundMessage{}}}
	activeClient, _ := a.current()
	page, err := activeClient.outbound(a.ctx, session, limit, after)
	if err != nil {
		view.Error = err.Error()
		return view
	}
	view.OutboundPage = page
	if view.Messages == nil {
		view.Messages = []OutboundMessage{}
	}
	return view
}

// WakesView is the wake trail plus the limits that produced its refusals, and
// whether the read succeeded.
type WakesView struct {
	WakesPage
	Error string `json:"error,omitempty"`
}

// Wakes reads what has woken agents on this node, newest first, optionally for
// one local session.
//
// The refusals are part of the answer: a quiet trail and a limit doing its job
// look the same from outside and call for different actions. The session id is
// passed to the node as given — the node refuses one that is not local, which
// is a better answer than an empty list that reads as "nothing happened".
func (a *App) Wakes(session string, limit int) WakesView {
	view := WakesView{WakesPage: WakesPage{Wakes: []WakeEvent{}}}
	activeClient, _ := a.current()
	page, err := activeClient.wakes(a.ctx, session, limit)
	if err != nil {
		view.Error = err.Error()
		return view
	}
	view.WakesPage = page
	if view.Wakes == nil {
		view.Wakes = []WakeEvent{}
	}
	return view
}
