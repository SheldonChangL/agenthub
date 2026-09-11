package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	AllowOutbound  bool     `json:"allowOutbound"`
	AutoWake       bool     `json:"autoWake"`
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

// TrustedNode is a peer this owner has paired with.
type TrustedNode struct {
	NodeID      string    `json:"nodeId"`
	DisplayName string    `json:"displayName"`
	Platform    string    `json:"platform"`
	PublicKey   string    `json:"publicKey"`
	Fingerprint string    `json:"fingerprint"`
	PairedAt    time.Time `json:"pairedAt"`
	LastSeenAt  time.Time `json:"lastSeenAt,omitzero"`
}

// Peer is what this node currently believes about one paired peer.
//
// Online and Sessions are answered together by the node: an expired snapshot
// reports offline and carries no sessions, so a stale view cannot be rendered
// as the current one by a client that forgets to check.
type Peer struct {
	NodeID      string    `json:"nodeId"`
	DisplayName string    `json:"displayName"`
	Online      bool      `json:"online"`
	Sequence    uint64    `json:"sequence,omitempty"`
	ReceivedAt  time.Time `json:"receivedAt,omitzero"`
	ExpiresAt   time.Time `json:"expiresAt,omitzero"`
	Sessions    []Session `json:"sessions"`
	// SessionsWithheld means the node refused what this peer published, rather
	// than the peer having published nothing. The two are the same shape on the
	// wire — an online peer with an empty list — and only this tells them
	// apart. Named here rather than left to pass through: an unknown key is
	// dropped in decoding, so a field this struct does not carry is one the UI
	// can never render.
	SessionsWithheld bool `json:"sessionsWithheld,omitempty"`
}

type NodeIdentity struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"displayName"`
	Platform    string    `json:"platform"`
	CreatedAt   time.Time `json:"createdAt"`
	PublicKey   string    `json:"publicKey,omitempty"`
	Fingerprint string    `json:"fingerprint,omitempty"`
	// AutoWake is the node's own -auto-wake flag, not any session's. Waking
	// needs both switches open, and a session's box does nothing while this is
	// closed — so the dialog offering that box has to be able to say so.
	AutoWake bool `json:"autoWake"`
}

// Candidate is one machine currently advertising that it is willing to pair.
//
// Every field is a claim by whoever sent the packet, on a multicast group
// anyone on the segment can write to. Nothing here has been verified, and
// appearing in this list grants nothing: no trust, no session, no audience.
// Fingerprint especially is the announced one — a hint for finding the right
// row, never evidence of which machine it is.
type Candidate struct {
	NodeID      string    `json:"nodeId"`
	Address     string    `json:"address"`
	DisplayName string    `json:"displayName,omitempty"`
	Platform    string    `json:"platform,omitempty"`
	Fingerprint string    `json:"fingerprint"`
	FirstSeen   time.Time `json:"firstSeen"`
	LastSeen    time.Time `json:"lastSeen"`
	// Duplicate: another row claims this display name or this fingerprint,
	// which is what an impersonation attempt looks like from here.
	Duplicate bool `json:"duplicate,omitempty"`
	// Contested: something has announced different details under this node id
	// since it was first seen.
	Contested bool `json:"contested,omitempty"`
}

// AnnounceStatus is what this node's announce loop last managed to do, which is
// a different question from whether the window is open.
//
// Carried into the UI because the gap between the two is where the one failure
// an owner cannot see from the other machine lives. The node refuses to open a
// window when it has no address to announce, so the gap opens afterwards: an
// interface goes away, or every send starts failing, and the window stays open
// saying nothing while the other machine waits.
type AnnounceStatus struct {
	Addresses   int       `json:"announceableAddresses"`
	LastAttempt time.Time `json:"lastAttemptAt,omitzero"`
	LastSuccess time.Time `json:"lastAnnouncedAt,omitzero"`
	LastError   string    `json:"lastError,omitempty"`
}

// PairingState is the advertising window.
type PairingState struct {
	Open      bool      `json:"open"`
	OpenedAt  time.Time `json:"openedAt,omitzero"`
	ExpiresAt time.Time `json:"expiresAt,omitzero"`
	// Remaining is what a UI counts down. Taken from the node rather than
	// subtracted from ExpiresAt here, because the node measured the window
	// against its own clock and this process need not agree with it.
	Remaining  int            `json:"remainingSeconds"`
	Announcing AnnounceStatus `json:"announcing"`
	// DisplayName is what the node announces itself as. Read from the node on
	// every poll rather than once at startup: it is the string the pairing
	// warning names, and it changes when the node restarts under a different
	// -display-name — which is exactly what that warning tells an owner to do.
	DisplayName string `json:"displayName"`
	// NameIsChosen says whether a person picked that name or the node read it
	// off the machine. The warning names a remedy, and the remedy differs.
	NameIsChosen bool `json:"nameIsChosen"`
}

// responseCap bounds what this app will read from its own node. Large enough
// for a full session list, small enough that a peer cannot exhaust memory here.
const responseCap = 8 * 1024 * 1024

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

func (c *client) trustedNodes(ctx context.Context) ([]TrustedNode, error) {
	body, err := c.request(ctx, http.MethodGet, "/v1/nodes", nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Nodes []TrustedNode `json:"nodes"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode trusted nodes: %w", err)
	}
	return response.Nodes, nil
}

func (c *client) trustNode(ctx context.Context, input map[string]string) (TrustedNode, error) {
	body, err := c.request(ctx, http.MethodPost, "/v1/nodes", input)
	if err != nil {
		return TrustedNode{}, err
	}
	var node TrustedNode
	if err := json.Unmarshal(body, &node); err != nil {
		return TrustedNode{}, fmt.Errorf("decode trusted node: %w", err)
	}
	return node, nil
}

func (c *client) revokeNode(ctx context.Context, nodeID string) error {
	_, err := c.request(ctx, http.MethodDelete, "/v1/nodes/"+url.PathEscape(nodeID), nil)
	return err
}

func (c *client) peers(ctx context.Context) ([]Peer, error) {
	body, err := c.request(ctx, http.MethodGet, "/v1/peers", nil)
	if err != nil {
		return nil, err
	}
	var decoded struct {
		Peers []Peer `json:"peers"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, fmt.Errorf("decode peers: %w", err)
	}
	if decoded.Peers == nil {
		decoded.Peers = []Peer{}
	}
	return decoded.Peers, nil
}

func (c *client) pairingState(ctx context.Context) (PairingState, error) {
	return c.decodePairingState(ctx, http.MethodGet, nil)
}

// openPairing starts the window. Seconds of zero means the node's default.
func (c *client) openPairing(ctx context.Context, seconds int) (PairingState, error) {
	var input any
	if seconds > 0 {
		input = map[string]int{"seconds": seconds}
	}
	return c.decodePairingState(ctx, http.MethodPost, input)
}

func (c *client) closePairing(ctx context.Context) (PairingState, error) {
	return c.decodePairingState(ctx, http.MethodDelete, nil)
}

func (c *client) decodePairingState(ctx context.Context, method string, input any) (PairingState, error) {
	body, err := c.request(ctx, method, "/v1/pairing", input)
	if err != nil {
		return PairingState{}, err
	}
	var state PairingState
	if err := json.Unmarshal(body, &state); err != nil {
		return PairingState{}, fmt.Errorf("decode pairing state: %w", err)
	}
	return state, nil
}

// candidates lists the machines advertising right now, with the node's own
// notice about what the list is worth.
func (c *client) candidates(ctx context.Context) ([]Candidate, bool, string, error) {
	body, err := c.request(ctx, http.MethodGet, "/v1/pairing/candidates", nil)
	if err != nil {
		return nil, false, "", err
	}
	var decoded struct {
		Candidates []Candidate `json:"candidates"`
		// Full means an attacker could be holding the list at its limit, so the
		// machine the owner is looking for may be missing for that reason
		// rather than because it is not advertising.
		Full   bool   `json:"full"`
		Notice string `json:"notice"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, false, "", fmt.Errorf("decode pairing candidates: %w", err)
	}
	if decoded.Candidates == nil {
		decoded.Candidates = []Candidate{}
	}
	return decoded.Candidates, decoded.Full, decoded.Notice, nil
}

// InboxMessage is one message another node queued for a local session.
//
// Every field except the id was written on another machine. `Body` especially:
// it is content, not instruction, and the desktop renders it as such — a
// request inside it is a request from a stranger, and the sender's session name
// is a label they chose. Only NodeID identifies who sent it.
type InboxMessage struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}

// Inbox is what one local session has been sent, and how full it is.
type Inbox struct {
	Messages []InboxMessage `json:"messages"`
	// Held and Capacity are carried so a full inbox is visible as full rather
	// than as a list that has stopped growing for no stated reason.
	Held     int  `json:"held"`
	Capacity int  `json:"capacity"`
	Full     bool `json:"full"`
	// Next is the cursor for the page after this one, empty when there is none.
	Next string `json:"next,omitempty"`
}

func (c *client) inbox(ctx context.Context, sessionID string, limit int) (Inbox, error) {
	path := fmt.Sprintf("/v1/inbox/%s?limit=%d", url.PathEscape(sessionID), limit)
	body, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return Inbox{}, err
	}
	var inbox Inbox
	if err := json.Unmarshal(body, &inbox); err != nil {
		return Inbox{}, fmt.Errorf("decode inbox: %w", err)
	}
	if inbox.Messages == nil {
		inbox.Messages = []InboxMessage{}
	}
	return inbox, nil
}

// clearInbox empties an inbox and reports how many messages went.
//
// The count is the only way an owner learns that something arrived between
// reading the list and confirming, and was destroyed unseen.
func (c *client) clearInbox(ctx context.Context, sessionID string) (int, error) {
	body, err := c.request(ctx, http.MethodDelete, "/v1/inbox/"+url.PathEscape(sessionID), nil)
	if err != nil {
		return 0, err
	}
	var removed struct {
		Removed int `json:"removed"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &removed); err != nil {
			return 0, fmt.Errorf("decode clear result: %w", err)
		}
	}
	return removed.Removed, nil
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
	data, err := io.ReadAll(io.LimitReader(response.Body, responseCap))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(data) >= responseCap {
		// Cut off, so it will not decode, and "unexpected end of JSON input"
		// sends the reader nowhere. Reachable from an inbox: a message body is
		// 32KB and a control character in it escapes to six JSON bytes, so a
		// peer can make a page far larger than it looks. Say what happened and
		// what clears it.
		return nil, fmt.Errorf("the node's answer reached the %d byte limit and was cut off; "+
			"something in it is too large to read here. Emptying an inbox is the way out of one",
			responseCap)
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
