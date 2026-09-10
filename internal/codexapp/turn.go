package codexapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// ResumeResult is the part of thread/resume this package uses.
type ResumeResult struct {
	Thread struct {
		ID string `json:"id"`
	} `json:"thread"`
	CWD   string `json:"cwd"`
	Model string `json:"model"`
}

// ResumeThread loads a thread and, if it is already running, rejoins it.
//
// Verified against codex-cli 0.153.4, whose ThreadResumeParams says: "If
// thread_id identifies a running thread, app-server rejoins that thread." That
// is what keeps waking from forking a second conversation beside the one the
// owner is looking at — the failure that would make this feature worse than
// useless, because the reply would land somewhere they never see.
//
// excludeTurns is set because nothing here reads the history. Hydrating every
// turn of a long thread to append one message is work whose only effect is
// latency.
func (c *Client) ResumeThread(ctx context.Context, threadID string) (ResumeResult, error) {
	if threadID == "" {
		return ResumeResult{}, errors.New("thread id is required")
	}
	var result ResumeResult
	if err := c.call(ctx, "thread/resume", map[string]any{
		"threadId":     threadID,
		"excludeTurns": true,
	}, &result); err != nil {
		return ResumeResult{}, err
	}
	return result, nil
}

// TurnInput is one piece of what the agent is asked to read.
type TurnInput struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ContextEntry is a fragment handed to the turn beside its input.
//
// Kind is "untrusted" or "application" — Codex's own words, and the reason
// this is used at all. A peer's message can be marked as what it is rather
// than relying on prose framing inside the input to hold, which is the one
// defence this project cannot test and cannot enforce.
type ContextEntry struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}

// ContextUntrusted marks a fragment as written by somebody else.
const ContextUntrusted = "untrusted"

// StartTurnParams is what AgentHub sends to start one turn.
type StartTurnParams struct {
	ThreadID          string                  `json:"threadId"`
	Input             []TurnInput             `json:"input"`
	AdditionalContext map[string]ContextEntry `json:"additionalContext,omitempty"`
	// TurnTrigger classifies the caller. Sent, and not visible afterwards:
	// grepped for on a real woken thread's rollout file, "agenthub-wake"
	// appears zero times and so does "turnTrigger", while the turn itself is
	// plainly there. codex-cli 0.153.4 does not persist it, whatever it does
	// with it in flight.
	//
	// Kept because the field is part of the API and costs nothing to send.
	// What an owner can actually find a woken turn by is the
	// <external_agenthub:message> element Codex wraps the untrusted fragment
	// in, and the notice text inside it.
	TurnTrigger string `json:"turnTrigger,omitempty"`
}

// TurnResult is the part of turn/start this package uses.
type TurnResult struct {
	Turn struct {
		ID string `json:"id"`
	} `json:"turn"`
}

// StartTurn sends one turn to a resumed thread.
func (c *Client) StartTurn(ctx context.Context, params StartTurnParams) (TurnResult, error) {
	if params.ThreadID == "" {
		return TurnResult{}, errors.New("thread id is required")
	}
	if len(params.Input) == 0 {
		return TurnResult{}, errors.New("a turn needs input")
	}
	var result TurnResult
	if err := c.call(ctx, "turn/start", params, &result); err != nil {
		return TurnResult{}, err
	}
	return result, nil
}

// RefuseUnattendedApprovals answers every request Codex makes of the client by
// refusing it.
//
// This is the boundary that makes waking safe to have at all. A turn nobody
// asked for is running with nobody present; Codex will ask before it runs a
// command, edits a file, or widens its own permissions, and there is no one
// here to say yes. Every one of those is refused.
//
// Refused, not ignored. Leaving the request unanswered stalls the turn until
// the owner comes back and finds a session wedged on a prompt they never saw.
// A refusal lets the agent carry on and say what it could not do.
//
// The typed decline is used where the response has one, and a JSON-RPC error
// where it does not. Only two have no refusal in their result shape:
// PermissionsRequestApprovalResponse, whose only shape is a granted profile,
// and ToolRequestUserInputResponse, which is a map of answers. An error is the
// only answer to those that cannot be read as a grant.
//
// Everything not named — including anything added to the protocol after this
// was written — falls to the same error. A request this node does not
// recognise is one nobody is present to answer.
func RefuseUnattendedApprovals(reason string) RequestHandler {
	if reason == "" {
		reason = "this turn was started automatically by AgentHub and nobody is present to approve anything"
	}
	return func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		switch method {
		case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
			// "decline", not "cancel": cancel stops the turn, decline refuses
			// this one thing and lets the agent report what it could not do.
			return map[string]any{"decision": "decline"}, nil
		case "execCommandApproval", "applyPatchApproval":
			// ReviewDecision's refusal is `denied`, not `abort`: denied means
			// "do not execute it, but continue the session", while abort halts
			// the turn until the user's next command — which for a turn with
			// no user is a wedge. An earlier version used abort and said
			// ReviewDecision had no decline; it does.
			return map[string]any{"decision": map[string]any{
				"denied": map[string]any{"rejection": reason},
			}}, nil
		case "mcpServer/elicitation/request":
			// It has a typed decline too.
			return map[string]any{"action": "decline"}, nil
		case "item/permissions/requestApproval", "item/tool/requestUserInput":
			// These have no refusal in their result shape.
			// PermissionsRequestApprovalResponse requires a granted profile —
			// its only shape is a grant — so an error is the only answer that
			// cannot be read as one.
			return nil, fmt.Errorf("%s refused: %s", method, reason)
		}
		return nil, fmt.Errorf("%s refused: %s", method, reason)
	}
}
