package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ChannelMethod is the notification Claude Code listens for.
//
// It arrives in the agent's context as
//
//	<channel source="agenthub" key="value">content</channel>
//
// and is treated as new input: the agent takes a turn, rather than the text
// sitting in context until somebody types.
const ChannelMethod = "notifications/claude/channel"

// ChannelCapability is what a server declares to register the listener.
//
// Presence is what registers it; the value is always an empty object.
const ChannelCapability = "claude/channel"

// channelParams is one push.
//
// Meta keys must match [A-Za-z0-9_]: Claude Code silently drops a key with a
// hyphen in it, which would take a message's provenance with it and leave the
// body looking like something this machine said.
type channelParams struct {
	Content string            `json:"content"`
	Meta    map[string]string `json:"meta,omitempty"`
}

// GetProgressToken and SetProgressToken make channelParams satisfy the SDK's
// Params interface. Neither is meaningful here — a channel push is one-way and
// has nothing to report progress against.
func (*channelParams) GetProgressToken() any { return nil }
func (*channelParams) SetProgressToken(any)  {}

// injectingTransport wraps a transport so the server can send a notification
// the SDK has no API for.
//
// go-sdk 1.7.0 has AddSendingCustomMethod and CallCustomMethod on the *client*
// only; a server's sendable methods are a fixed whitelist, and there is no
// exported way past it. What is exported is Connection, whose Write the SDK
// documents as safe to call concurrently — so this keeps the connection the
// SDK is using and writes one more frame onto it.
//
// The alternative, writing to stdout directly, would interleave with the SDK's
// own frames and corrupt the stream.
type injectingTransport struct {
	inner mcp.Transport

	mu   sync.Mutex
	conn mcp.Connection
}

func (t *injectingTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	conn, err := t.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	t.conn = conn
	t.mu.Unlock()
	return conn, nil
}

// notify writes one notification onto the connection the SDK is using.
func (t *injectingTransport) notify(ctx context.Context, method string, params any) error {
	t.mu.Lock()
	conn := t.conn
	t.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("cannot send %s: the client has not connected", method)
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("encode %s params: %w", method, err)
	}
	// No ID: a JSON-RPC request without one is a notification, and a channel
	// push is not answered.
	return conn.Write(ctx, &jsonrpc.Request{Method: method, Params: encoded})
}

// ChannelPush is a message on its way into a running agent's context.
// The tags are explicit rather than left to Go's case-insensitive fallback.
// That fallback happens to match every field here today, and it is not a
// contract: the node writes `senderNodeId`, and a field renamed on either side
// would stop matching with nothing to say so. What goes missing is the
// provenance — a stranger's words arriving with no attribution, which is the
// one failure that turns a marked message into an anonymous one.
type ChannelPush struct {
	MessageID    string `json:"messageId"`
	Body         string `json:"body"`
	SenderNodeID string `json:"senderNodeId"`
	SenderLabel  string `json:"senderLabel"`
	Fingerprint  string `json:"fingerprint"`
	Hops         int    `json:"hops"`
	Notice       string `json:"notice"`
}

// channelContent is what the agent reads.
//
// The notice comes first and the body last, with the body in a section of its
// own. This is weaker than the Codex path, and the difference is worth naming:
// Codex has a first-class "untrusted" kind for a context fragment, so a peer's
// words never share a string with this node's. A channel push is one string,
// so the separation here is typographic and the provenance is carried out of
// it, in meta, where Claude Code renders it as attributes of the <channel>
// element rather than as part of the message.
func channelContent(push ChannelPush) string {
	return push.Notice + "\n\n--- the message, written by someone else ---\n" + push.Body
}

// channelMeta is the provenance, as attributes.
//
// Keys are [A-Za-z0-9_] because Claude Code drops anything else without
// saying so, and losing these silently would leave the body with no attribution
// at all.
func channelMeta(push ChannelPush) map[string]string {
	meta := map[string]string{"agenthub_message": push.MessageID}
	if push.SenderNodeID != "" {
		meta["agenthub_sender_node"] = push.SenderNodeID
	}
	if push.Fingerprint != "" {
		meta["agenthub_fingerprint"] = push.Fingerprint
	}
	if push.SenderLabel != "" {
		meta["agenthub_sender_label"] = push.SenderLabel
	}
	if push.Hops > 0 {
		meta["agenthub_wake_hops"] = fmt.Sprintf("%d", push.Hops)
	}
	return meta
}
