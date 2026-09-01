package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"agenthub.local/agenthub/internal/id"
	"agenthub.local/agenthub/internal/model"
)

// OutboundState is where one queued message has got to.
type OutboundState string

const (
	// OutboundPending is waiting for a delivery attempt.
	OutboundPending OutboundState = "pending"
	// OutboundDelivered means the recipient acknowledged holding it. It says
	// nothing about anyone having read it.
	OutboundDelivered OutboundState = "delivered"
	// OutboundRefused means the recipient declined and will decline again.
	// Retrying is pointless, so it is a terminal state.
	OutboundRefused OutboundState = "refused"
)

// ErrOutboundNotFound marks a message id this node did not queue.
var ErrOutboundNotFound = errors.New("no such queued message")

// OutboundMessage is one message this node is trying to hand to a peer.
//
// It is stored before any delivery is attempted, which is what lets `ah send`
// answer immediately and honestly: the message is queued here, and queued is
// all that has happened. Delivery is a separate event with its own outcome.
type OutboundMessage struct {
	ID                string        `json:"id"`
	DestinationNodeID string        `json:"destinationNodeId"`
	To                string        `json:"to"`
	From              string        `json:"from,omitempty"`
	Body              string        `json:"body"`
	State             OutboundState `json:"state"`
	Attempts          int           `json:"attempts"`
	CreatedAt         time.Time     `json:"createdAt"`
	UpdatedAt         time.Time     `json:"updatedAt"`
	LastError         string        `json:"lastError,omitempty"`
}

func (r *Registry) migrateOutbox(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS outbound_messages (
    id TEXT PRIMARY KEY,
    destination_node_id TEXT NOT NULL CHECK (length(destination_node_id) BETWEEN 16 AND 128),
    recipient_session TEXT NOT NULL,
    sender_label TEXT NOT NULL DEFAULT '',
    body TEXT NOT NULL CHECK (length(body) BETWEEN 1 AND 32768),
    state TEXT NOT NULL CHECK (state IN ('pending', 'delivered', 'refused')),
    attempts INTEGER NOT NULL DEFAULT 0,
    created_at_ms INTEGER NOT NULL,
    updated_at_ms INTEGER NOT NULL,
    last_error TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_outbound_pending
    ON outbound_messages(state, created_at_ms ASC, id ASC);
`
	if _, err := r.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate outbound messages: %w", err)
	}
	return nil
}

// QueueOutbound records a message for a peer and returns it.
//
// Nothing is delivered here. The separation is the point: a caller learns that
// the message is queued, which is true and useful, rather than waiting on a
// machine that may be asleep.
func (r *Registry) QueueOutbound(ctx context.Context, message OutboundMessage) (OutboundMessage, error) {
	switch {
	case message.DestinationNodeID == "":
		return OutboundMessage{}, fmt.Errorf("%w: destination node is required", ErrInvalidSession)
	case message.To == "":
		return OutboundMessage{}, fmt.Errorf("%w: recipient session is required", ErrInvalidSession)
	case len(message.Body) == 0 || len(message.Body) > 32768:
		return OutboundMessage{}, fmt.Errorf("%w: message body must contain 1 to 32768 bytes", ErrInvalidSession)
	}
	// The destination must be a node this owner paired with. Queueing for an
	// unknown node would produce a message that can never be delivered and an
	// answer that suggested otherwise.
	if _, err := r.TrustedNode(ctx, message.DestinationNodeID); err != nil {
		return OutboundMessage{}, err
	}

	if message.ID == "" {
		generated, err := id.New("msg_")
		if err != nil {
			return OutboundMessage{}, err
		}
		message.ID = generated
	}
	now := time.Now().UTC()
	message.State = OutboundPending
	message.CreatedAt, message.UpdatedAt = now, now

	if _, err := r.db.ExecContext(ctx, `
INSERT INTO outbound_messages
    (id, destination_node_id, recipient_session, sender_label, body, state, attempts, created_at_ms, updated_at_ms)
VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?)`,
		message.ID, message.DestinationNodeID, message.To, message.From, message.Body,
		string(OutboundPending), now.UnixMilli(), now.UnixMilli()); err != nil {
		return OutboundMessage{}, fmt.Errorf("queue outbound message: %w", err)
	}
	return message, nil
}

// PendingOutbound returns queued messages for one node, oldest first.
func (r *Registry) PendingOutbound(ctx context.Context, nodeID string, limit int) ([]OutboundMessage, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT id, destination_node_id, recipient_session, sender_label, body, state, attempts,
       created_at_ms, updated_at_ms, last_error
FROM outbound_messages
WHERE destination_node_id = ? AND state = 'pending'
ORDER BY created_at_ms ASC, id ASC LIMIT ?`, nodeID, limit)
	if err != nil {
		return nil, fmt.Errorf("read outbound queue: %w", err)
	}
	defer rows.Close()
	return scanOutbound(rows)
}

// OutboundFor returns one queued message by id.
func (r *Registry) OutboundFor(ctx context.Context, messageID string) (OutboundMessage, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, destination_node_id, recipient_session, sender_label, body, state, attempts,
       created_at_ms, updated_at_ms, last_error
FROM outbound_messages WHERE id = ?`, messageID)
	if err != nil {
		return OutboundMessage{}, fmt.Errorf("read outbound message: %w", err)
	}
	defer rows.Close()
	messages, err := scanOutbound(rows)
	if err != nil {
		return OutboundMessage{}, err
	}
	if len(messages) == 0 {
		return OutboundMessage{}, fmt.Errorf("%q: %w", messageID, ErrOutboundNotFound)
	}
	return messages[0], nil
}

// MarkOutbound records the outcome of a delivery attempt.
//
// A delivered or refused message is terminal and is never moved back to
// pending: redelivering something the recipient already holds would put a
// second copy in their inbox, and re-offering something they declined is not
// going to be answered differently.
func (r *Registry) MarkOutbound(ctx context.Context, messageID string, state OutboundState, reason string) error {
	switch state {
	case OutboundPending, OutboundDelivered, OutboundRefused:
	default:
		return fmt.Errorf("%w: unknown outbound state %q", ErrInvalidSession, state)
	}
	result, err := r.db.ExecContext(ctx, `
UPDATE outbound_messages
SET state = ?, last_error = ?, attempts = attempts + 1, updated_at_ms = ?
WHERE id = ? AND state = 'pending'`,
		string(state), reason, time.Now().UTC().UnixMilli(), messageID)
	if err != nil {
		return fmt.Errorf("update outbound message: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read outbound update: %w", err)
	}
	if count == 0 {
		// Either the id is unknown or the message has already settled. Both
		// mean this attempt has nothing to record.
		return fmt.Errorf("%q: %w", messageID, ErrOutboundNotFound)
	}
	return nil
}

func scanOutbound(rows rowsScanner) ([]OutboundMessage, error) {
	messages := []OutboundMessage{}
	for rows.Next() {
		var message OutboundMessage
		var state string
		var createdMS, updatedMS int64
		if err := rows.Scan(&message.ID, &message.DestinationNodeID, &message.To, &message.From,
			&message.Body, &state, &message.Attempts, &createdMS, &updatedMS, &message.LastError); err != nil {
			return nil, fmt.Errorf("scan outbound message: %w", err)
		}
		message.State = OutboundState(state)
		message.CreatedAt = time.UnixMilli(createdMS).UTC()
		message.UpdatedAt = time.UnixMilli(updatedMS).UTC()
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read outbound messages: %w", err)
	}
	return messages, nil
}

type rowsScanner interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

// MessageByID returns one stored inbox message.
//
// It exists so a redelivery can be recognised as one. A peer that never got an
// ack will send the same message again, and without this the inbox would grow a
// second copy every time an ack was lost.
func (r *Registry) MessageByID(ctx context.Context, messageID string) (model.Message, error) {
	var message model.Message
	var createdMS int64
	err := r.db.QueryRowContext(ctx, `
SELECT id, sender_id, recipient_id, destination_node_id, body, created_at_ms
FROM messages WHERE id = ?`, messageID).
		Scan(&message.ID, &message.From, &message.To, &message.DestinationNodeID, &message.Body, &createdMS)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Message{}, fmt.Errorf("message %q: %w", messageID, ErrNotFound)
	}
	if err != nil {
		return model.Message{}, fmt.Errorf("read message %q: %w", messageID, err)
	}
	message.CreatedAt = time.UnixMilli(createdMS).UTC()
	return message, nil
}

// StoreIncomingMessage inserts a message from a peer, or reports that this
// sender already sent it.
//
// The insert is what decides, in one statement. A check-then-insert would let
// two concurrent deliveries of the same id both see nothing and both try to
// write, and the loser would surface as a generic storage error — which the
// sender would read as a refusal and give up on a message that was in fact
// stored.
//
// The duplicate lookup is scoped to the sender. An unscoped one would let a
// peer discover whether an id exists anywhere in this node's inbox, including
// ids from local sends and from other peers, which is an oracle over a
// namespace it has no business reading.
func (r *Registry) StoreIncomingMessage(ctx context.Context, message model.Message) (bool, error) {
	if err := r.checkMessageAcceptable(ctx, message); err != nil {
		return false, err
	}
	if message.CreatedAt.IsZero() {
		message.CreatedAt = time.Now().UTC()
	}

	result, err := r.db.ExecContext(ctx, `
INSERT INTO messages (id, sender_id, recipient_id, destination_node_id, body, created_at_ms)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO NOTHING`,
		message.ID, message.From, message.To, message.DestinationNodeID,
		message.Body, message.CreatedAt.UTC().UnixMilli())
	if err != nil {
		return false, fmt.Errorf("store incoming message: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store incoming message: %w", err)
	}
	if affected == 1 {
		return true, nil
	}

	// The id was taken. Only the same sender writing to the same session counts
	// as a redelivery; anything else is refused with the ordinary refusal, so a
	// peer cannot tell a taken id from a session that declines.
	existing, err := r.MessageByID(ctx, message.ID)
	if err != nil {
		return false, err
	}
	if existing.From == message.From && existing.To == message.To {
		return false, nil
	}
	return false, fmt.Errorf("%w: that message cannot be stored", ErrInvalidSession)
}

// checkMessageAcceptable applies the same rules CreateMessage does, so the
// insert above cannot store something the local path would refuse.
func (r *Registry) checkMessageAcceptable(ctx context.Context, message model.Message) error {
	switch {
	case strings.TrimSpace(message.To) == "":
		return fmt.Errorf("%w: message recipient is required", ErrInvalidSession)
	case strings.TrimSpace(message.Body) == "" || len(message.Body) > 32768:
		return fmt.Errorf("%w: message body must contain 1 to 32768 bytes", ErrInvalidSession)
	case message.DestinationNodeID == "":
		return fmt.Errorf("%w: message destination node is required", ErrInvalidSession)
	case message.ID == "":
		return fmt.Errorf("%w: message id is required", ErrInvalidSession)
	}
	destination, err := r.GetSession(ctx, message.To)
	if err != nil {
		return err
	}
	if !destination.Audience.AcceptMessages {
		return fmt.Errorf("%w: session %q does not accept messages", ErrInvalidSession, message.To)
	}
	return nil
}

// RecordAttempt notes a delivery attempt that did not settle the message.
//
// Without it, a message that has failed a hundred times reads as "pending,
// attempts: 0, last error: none", and the owner has no way to see why it is not
// moving.
func (r *Registry) RecordAttempt(ctx context.Context, messageID, reason string) error {
	if _, err := r.db.ExecContext(ctx, `
UPDATE outbound_messages
SET attempts = attempts + 1, last_error = ?, updated_at_ms = ?
WHERE id = ? AND state = 'pending'`,
		reason, time.Now().UTC().UnixMilli(), messageID); err != nil {
		return fmt.Errorf("record delivery attempt: %w", err)
	}
	return nil
}
