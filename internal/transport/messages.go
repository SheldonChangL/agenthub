package transport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
)

// maxMessagesPerRound bounds one peer's share of a delivery round, so a large
// backlog for one node cannot starve every other node's messages.
const maxMessagesPerRound = 20

// DeliverMessages hands queued messages to the peers they are addressed to.
//
// It runs on the same schedule as heartbeat publishing and through the same
// pinned, challenged connection: a message is content rather than metadata, so
// it must not travel on a weaker path than the metadata does.
//
// The outcome of every attempt is recorded before the next is made. A message
// whose ack was lost is redelivered, and the recipient recognises the repeat by
// its id and answers "duplicate" rather than queueing a second copy — losing an
// ack must not cost the reader two of the same message.
func (p *Publisher) DeliverMessages(ctx context.Context) (Result, error) {
	peers, err := p.store.TrustedNodes(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("list trusted nodes: %w", err)
	}

	var result Result
	for _, peer := range peers {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		pending, err := p.store.PendingOutbound(ctx, peer.NodeID, maxMessagesPerRound)
		if err != nil {
			return result, fmt.Errorf("read queue for %q: %w", peer.NodeID, err)
		}
		if len(pending) == 0 {
			continue
		}
		if peer.Address == "" {
			result.Skipped += len(pending)
			continue
		}
		if err := p.policy(peer.Address); err != nil {
			log.Printf("not delivering %d message(s) to %q at %q: %v",
				len(pending), peer.NodeID, peer.Address, err)
			result.Skipped += len(pending)
			continue
		}
		// The peer proves who it is once per round rather than once per message.
		// The connection is per request, but the proof is about the peer, and
		// nothing between two messages in one round can change who holds the key.
		if err := p.challenge(ctx, peer, p.localNodeID); err != nil {
			log.Printf("not delivering to %q: %v", peer.NodeID, err)
			result.Failed += len(pending)
			continue
		}
		for _, message := range pending {
			if err := ctx.Err(); err != nil {
				return result, err
			}
			if p.deliverMessage(ctx, peer, message) {
				result.Delivered++
			} else {
				result.Failed++
			}
		}
	}
	return result, nil
}

// deliverMessage makes one attempt and records what came back.
func (p *Publisher) deliverMessage(ctx context.Context, peer registry.TrustedNode, message registry.OutboundMessage) bool {
	envelope, err := p.builder.BuildMessage(peer.NodeID, protocol.MessagePayload{
		MessageID: message.ID,
		To:        message.To,
		From:      message.From,
		Body:      message.Body,
		SentAt:    message.CreatedAt,
	})
	if err != nil {
		// The message cannot be built into a valid envelope, so no amount of
		// retrying will send it. Settling it as refused stops the queue holding
		// something that will never move.
		p.settle(ctx, message.ID, registry.OutboundRefused, fmt.Sprintf("cannot be sent: %v", err))
		return false
	}

	body, err := json.Marshal(envelope)
	if err != nil {
		p.settle(ctx, message.ID, registry.OutboundRefused, fmt.Sprintf("cannot be encoded: %v", err))
		return false
	}

	response, err := p.post(ctx, peer, "/v1/messages", body)
	if err != nil {
		// The peer is unreachable, or answered a non-2xx — which now includes a
		// storage failure on its side. Leave the message pending: this is the
		// case retrying exists for, and the attempt is recorded so an owner can
		// see why a message is not moving.
		log.Printf("message %s to %q failed: %v", message.ID, peer.NodeID, err)
		p.noteAttempt(ctx, message.ID, err.Error())
		return false
	}

	var ack protocol.AckPayload
	if err := json.Unmarshal(response, &ack); err != nil {
		log.Printf("message %s to %q: unreadable ack: %v", message.ID, peer.NodeID, err)
		p.noteAttempt(ctx, message.ID, "peer sent an unreadable acknowledgement")
		return false
	}
	switch ack.Status {
	case protocol.AckQueued, protocol.AckDuplicate:
		p.settle(ctx, message.ID, registry.OutboundDelivered, "")
		return true
	case protocol.AckRefused:
		// The recipient decided, and will decide the same way again.
		p.settle(ctx, message.ID, registry.OutboundRefused, ack.Reason)
		return false
	default:
		log.Printf("message %s to %q: unknown ack status %q", message.ID, peer.NodeID, ack.Status)
		p.noteAttempt(ctx, message.ID, fmt.Sprintf("peer answered with an unknown status %q", ack.Status))
		return false
	}
}

// noteAttempt records a delivery that did not settle, so a message stuck in the
// queue can say why rather than reading as though nothing had been tried.
func (p *Publisher) noteAttempt(ctx context.Context, messageID, reason string) {
	if err := p.store.RecordAttempt(ctx, messageID, reason); err != nil {
		log.Printf("could not record the attempt for message %s: %v", messageID, err)
	}
}

func (p *Publisher) settle(ctx context.Context, messageID string, state registry.OutboundState, reason string) {
	if err := p.store.MarkOutbound(ctx, messageID, state, reason); err != nil {
		if errors.Is(err, registry.ErrOutboundNotFound) {
			// Already settled by another attempt. Nothing to do.
			return
		}
		log.Printf("could not record the outcome for message %s: %v", messageID, err)
	}
}
