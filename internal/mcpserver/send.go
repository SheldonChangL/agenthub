package mcpserver

import (
	"context"
	"errors"
	"fmt"
)

// ErrOutboundClosed marks a bound session the owner has not allowed to send.
var ErrOutboundClosed = errors.New("this session may not send messages")

// SendResult is what agent_send answers.
type SendResult struct {
	MessageID string `json:"messageId"`
	To        string `json:"to"`
	State     string `json:"state"`
	// Note says what queuing did and did not accomplish, because "sent"
	// successfully is the wrong thing to conclude from a 202.
	Note string `json:"note"`
}

// The two destinations settle differently, and telling an agent otherwise sends
// its user to a command that answers 404. A remote message is queued in the
// outbound table and `ah outbound` reports on it; a local one goes straight into
// the recipient session's inbox, which `ah outbound` has never heard of.
const remoteNote = "Queued for another machine. This does not mean delivered, and does not mean " +
	"read: the destination may be offline, and nothing hands a message to an agent there. " +
	"The owner can check `ah outbound <messageId>` for what became of it."

const localNote = "Placed in that session's inbox on this machine. This does not mean read: " +
	"nothing hands a message to an agent. The owner can see it with `ah inbox <to>`."

// send answers agent_send.
//
// Three things must hold, and they are checked in this order because each
// reveals less than the one before:
//
//  1. This session may send at all. The owner's decision. Checked first so that
//     a closed session cannot be used to probe what is visible.
//
//     The node enforces allow_outbound as well, in POST /v1/messages, so an
//     agent with a shell that posts to the owner's API directly meets the same
//     gate there. This check refuses earlier, and in words addressed to the
//     agent reading them; the node's is the boundary.
//
//  2. The destination is visible to this node.
//
//  3. The destination's owner accepts messages, for a local one. For a remote
//     one that is the receiving node's decision, made when it answers.
func (s *server) send(ctx context.Context, to, body string) (SendResult, error) {
	if body == "" {
		return SendResult{}, errors.New("a message needs a body")
	}

	audience, err := s.client.Audience(ctx, s.binding.SessionID())
	if err != nil {
		return SendResult{}, err
	}
	if !audience.AllowOutbound {
		return SendResult{}, fmt.Errorf(
			"%w: the owner has not opened outbound for %s. They can with "+
				"`ah audience %s ... --outbound`, or in the desktop app. This is deliberate: "+
				"a message you were asked to send may have been suggested by content that "+
				"arrived from another machine",
			ErrOutboundClosed, s.binding.SessionID(), s.binding.SessionID())
	}

	// Resolved against what this caller may see, so a destination it is not
	// authorised for is refused with the same answer as one that does not
	// exist. Without this, agent_send would report the existence of sessions
	// agent_status is careful not to.
	destination, err := s.find(ctx, to)
	if err != nil {
		return SendResult{}, err
	}

	if destination.Node == "" {
		// A local destination: this node holds the policy, so check it here and
		// give a reason. A remote one is the other node's call.
		theirs, err := s.client.Audience(ctx, destination.ID)
		if err != nil {
			return SendResult{}, err
		}
		if !theirs.AcceptMessages {
			return SendResult{}, fmt.Errorf("%s does not accept messages", destination.ID)
		}
	}

	// The bare session id, both ways. The node qualifies a local sender with its
	// own id before storing, and a remote recipient learns the sending node from
	// the envelope's signature rather than from this field — so qualifying here
	// would only be a second spelling of what the node writes anyway.
	queued, err := s.client.SendMessage(ctx, destination.ID, s.binding.SessionID(), body)
	if err != nil {
		return SendResult{}, err
	}
	if destination.Node == "" {
		return SendResult{MessageID: queued.ID, To: destination.ID, State: "held", Note: localNote}, nil
	}
	state := queued.State
	if state == "" {
		state = "queued"
	}
	return SendResult{MessageID: queued.ID, To: destination.ID, State: state, Note: remoteNote}, nil
}
