package mcpserver

import (
	"context"
	"errors"
	"fmt"

	"agenthub.local/agenthub/internal/address"
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

const queuedNote = "Queued. This does not mean delivered, and does not mean read: " +
	"the destination may be offline, and nothing hands a message to an agent there. " +
	"Ask the owner to check `ah outbound <messageId>` for what became of it."

// send answers agent_send.
//
// Three things must hold, and they are checked in this order because each
// reveals less than the one before:
//
//  1. This session may send at all. The owner's decision, and the only gate
//     between a message written on another machine — which reached the agent
//     through agent_inbox — and this machine's data going out. Checked first so
//     that a closed session cannot be used to probe what is visible.
//  2. The destination is visible to this node.
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

	from := address.QualifiedID(s.nodeID, s.binding.SessionID())
	if destination.Node == "" {
		// A local message names a local sender: the node refuses a `from`
		// claiming another node for a local destination, and this node is not
		// another node to itself.
		from = s.binding.SessionID()
	}
	queued, err := s.client.SendMessage(ctx, destination.ID, from, body)
	if err != nil {
		return SendResult{}, err
	}
	state := queued.State
	if state == "" {
		state = "queued"
	}
	return SendResult{MessageID: queued.ID, To: destination.ID, State: state, Note: queuedNote}, nil
}
