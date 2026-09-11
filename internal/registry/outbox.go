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

// ErrInboxFull marks a session whose inbox has reached its limit.
//
// It is deliberately not a refusal. A refusal is a decision the owner made and
// the sender settles it permanently; a full inbox is a temporary condition that
// clears when somebody reads and deletes. Reporting it as a refusal would
// destroy the message, which is the failure this whole bound exists to make
// impossible rather than to cause.
var ErrInboxFull = errors.New("the addressed session's inbox is full")

// MaxInboxMessages is how many messages one session holds.
//
// Bodies are capped at 32KB, so a full inbox is about 16MB for one session.
// That is far more than anyone reads and far less than a compromised peer needs
// to fill a disk. The bound is per session rather than global because sessions
// are created by providers on this machine, not by peers: a peer cannot invent
// sessions to multiply its allowance.
const MaxInboxMessages = 500

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
	// WakeHops travels with the message so the receiving node can tell how far
	// an automatic exchange has already gone. Set by whatever queued it.
	WakeHops int `json:"wakeHops,omitempty"`
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
    last_error TEXT NOT NULL DEFAULT '',
    wake_hops INTEGER NOT NULL DEFAULT 0 CHECK (wake_hops >= 0)
);
CREATE INDEX IF NOT EXISTS idx_outbound_pending
    ON outbound_messages(state, created_at_ms ASC, id ASC);
CREATE INDEX IF NOT EXISTS idx_outbound_recent
    ON outbound_messages(created_at_ms DESC, id DESC);
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
    (id, destination_node_id, recipient_session, sender_label, body, state, attempts, created_at_ms, updated_at_ms, wake_hops)
VALUES (?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`,
		message.ID, message.DestinationNodeID, message.To, message.From, message.Body,
		string(OutboundPending), now.UnixMilli(), now.UnixMilli(), message.WakeHops); err != nil {
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
       created_at_ms, updated_at_ms, last_error, wake_hops
FROM outbound_messages
WHERE destination_node_id = ? AND state = 'pending'
ORDER BY created_at_ms ASC, id ASC LIMIT ?`, nodeID, limit)
	if err != nil {
		return nil, fmt.Errorf("read outbound queue: %w", err)
	}
	defer rows.Close()
	return scanOutbound(rows)
}

// OutboundCursor marks where a page of the outbound list ended.
//
// The same shape and the same wire form as an inbox cursor — a time and an id,
// which is all either needs — so one encoding is parsed and rendered in one
// place. What differs is the direction it is walked: this list is newest first,
// so the page after a cursor is what sorts *before* it.
type OutboundCursor = InboxCursor

// OutboundStart is the cursor before the newest message.
var OutboundStart = InboxStart

// OutboundCursorAfter is the cursor that continues past message.
func OutboundCursorAfter(message OutboundMessage) OutboundCursor {
	return OutboundCursor{CreatedAtMS: message.CreatedAt.UTC().UnixMilli(), ID: message.ID}
}

// ParseOutboundCursor reads back what OutboundCursorAfter rendered; "" is the
// start.
func ParseOutboundCursor(value string) (OutboundCursor, error) {
	return ParseInboxCursor(value)
}

// ListOutbound returns the messages this node has queued for peers, newest
// first, in pages.
//
// Newest first because the question an owner arrives with is "what happened to
// what I just sent", the same reason the wake trail is ordered that way. Paged
// by cursor rather than offset: rows are pruned once they have been settled a
// while, so an offset would skip or repeat rows between two pages.
//
// Bodies are not read here. The list answers "where did my messages go", and a
// page of fifty 32KB bodies is both far more than that question needs and
// enough to pass a reader's response limit — which is how the answer to
// "what is jamming my outbox" becomes unreadable exactly when it is wanted.
//
// A non-empty session narrows the list to what that local session sent. The
// filter runs in SQL rather than over a fetched page, because a page filtered
// after the fact is a page of the wrong size: the caller would be handed two
// rows where it asked for fifty and no way to tell that from the end of the
// list.
func (r *Registry) ListOutbound(ctx context.Context, session string, limit int, after OutboundCursor) ([]OutboundMessage, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `
SELECT id, destination_node_id, recipient_session, sender_label, '', state, attempts,
       created_at_ms, updated_at_ms, last_error, wake_hops
FROM outbound_messages`
	// The two conditions are spelled out as constants and combined by a
	// switch rather than joined from a slice: every fragment of this query is
	// then a literal in the source, and the only way a value reaches the
	// database is as a bound parameter.
	//
	// bySender matches on the session half of the label, not on the whole
	// thing. A label is <node-id>/<session-id> — this node's id, since only a
	// local session may queue — and comparing whole labels would make the
	// answer depend on the node id still being what it was when the row was
	// written. The session is what the caller asked about.
	const bySender = `(CASE WHEN instr(sender_label, ?) > 0
       THEN substr(sender_label, instr(sender_label, ?) + 1) ELSE sender_label END) = ?`
	// beforeCursor is strictly before the cursor in the listing's own order,
	// which is descending. The start cursor is zero and names no row, so it is
	// not a bound at all — applied as one it would exclude everything.
	const beforeCursor = `(created_at_ms < ? OR (created_at_ms = ? AND id < ?))`

	arguments := []any{}
	if session != "" {
		separator := model.SessionIDSeparator
		arguments = append(arguments, separator, separator, session)
	}
	if after != OutboundStart {
		arguments = append(arguments, after.CreatedAtMS, after.CreatedAtMS, after.ID)
	}
	switch {
	case session != "" && after != OutboundStart:
		query += ` WHERE ` + bySender + ` AND ` + beforeCursor
	case session != "":
		query += ` WHERE ` + bySender
	case after != OutboundStart:
		query += ` WHERE ` + beforeCursor
	}
	query += ` ORDER BY created_at_ms DESC, id DESC LIMIT ?`
	arguments = append(arguments, limit)

	rows, err := r.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list outbound messages: %w", err)
	}
	defer rows.Close()
	return scanOutbound(rows)
}

// OutboundFor returns one queued message by id.
func (r *Registry) OutboundFor(ctx context.Context, messageID string) (OutboundMessage, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, destination_node_id, recipient_session, sender_label, body, state, attempts,
       created_at_ms, updated_at_ms, last_error, wake_hops
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
			&message.Body, &state, &message.Attempts, &createdMS, &updatedMS, &message.LastError,
			&message.WakeHops); err != nil {
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
SELECT id, sender_id, recipient_id, destination_node_id, body, created_at_ms, wake_hops
FROM messages WHERE id = ?`, messageID).
		Scan(&message.ID, &message.From, &message.To, &message.DestinationNodeID, &message.Body,
			&createdMS, &message.WakeHops)
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

	// One statement decides everything: whether this id is already held, and
	// whether there is room for a new one. Two statements would let two
	// concurrent deliveries both see 499 and both insert, and would let a
	// separate count drift from the insert it was guarding.
	result, err := r.db.ExecContext(ctx, `
INSERT INTO messages (id, sender_id, recipient_id, destination_node_id, body, created_at_ms, wake_hops)
SELECT ?, ?, ?, ?, ?, ?, ?
WHERE (SELECT count(*) FROM messages WHERE recipient_id = ?) < ?
ON CONFLICT(id) DO NOTHING`,
		message.ID, message.From, message.To, message.DestinationNodeID,
		message.Body, message.CreatedAt.UTC().UnixMilli(), message.WakeHops,
		message.To, MaxInboxMessages)
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

	// Nothing was written, for one of two reasons. Which one matters: a
	// redelivery must be recognised as one even when the inbox is full.
	//
	// Answering "full" to a message already held would be worse than merely
	// unhelpful. The sender's row would stay pending forever, ah outbound would
	// report a delivered message as undelivered, and the moment the owner read
	// and cleared the inbox the id would come free — so the next round would
	// insert it again and hand back a message they had already read. Losing an
	// ack must not cost the reader two of the same message, full or not.
	existing, err := r.MessageByID(ctx, message.ID)
	switch {
	case err == nil:
		if existing.From == message.From && existing.To == message.To {
			return false, nil
		}
		// The id belongs to a different message. Refused with the ordinary
		// refusal, so a peer cannot tell a taken id from a declining session.
		return false, fmt.Errorf("%w: that message cannot be stored", ErrInvalidSession)
	case errors.Is(err, ErrNotFound):
		// The id is free, so the room ran out. Deferred, not refused: the
		// sender keeps it queued and it arrives once somebody reads.
		held, countErr := r.CountInbox(ctx, message.To)
		if countErr != nil {
			return false, countErr
		}
		return false, fmt.Errorf("%w: %q holds %d of %d", ErrInboxFull, message.To, held, MaxInboxMessages)
	default:
		return false, err
	}
}

// checkMessageAcceptable applies the rules that do not depend on how full the
// inbox is. The bound is enforced by the insert itself, so that a redelivery of
// something already held is never turned away for lack of room it does not need.
func (r *Registry) checkMessageAcceptable(ctx context.Context, message model.Message) error {
	switch {
	case strings.TrimSpace(message.To) == "":
		return fmt.Errorf("%w: message recipient is required", ErrInvalidSession)
	case strings.TrimSpace(message.Body) == "" || len(message.Body) > 32768:
		return fmt.Errorf("%w: message body must contain 1 to 32768 bytes", ErrInvalidSession)
	case len(message.From) > model.MaxSenderLabelLength:
		return fmt.Errorf("%w: message sender label is %d bytes, over the %d limit",
			ErrInvalidSession, len(message.From), model.MaxSenderLabelLength)
	case message.DestinationNodeID == "":
		return fmt.Errorf("%w: message destination node is required", ErrInvalidSession)
	case message.ID == "":
		return fmt.Errorf("%w: message id is required", ErrInvalidSession)
	case len(message.ID) > model.MaxMessageIDLength:
		return fmt.Errorf("%w: message id is %d bytes, over the %d limit",
			ErrInvalidSession, len(message.ID), model.MaxMessageIDLength)
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

// CountInbox reports how many messages one session is holding.
func (r *Registry) CountInbox(ctx context.Context, sessionID string) (int, error) {
	var count int
	if err := r.db.QueryRowContext(ctx,
		`SELECT count(*) FROM messages WHERE recipient_id = ?`, sessionID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count inbox for %q: %w", sessionID, err)
	}
	return count, nil
}

// DeleteMessage removes one message from a session's inbox.
//
// This exists because the inbox is bounded, and a bound with no way to clear it
// is a session that stops receiving forever the first time it fills. Reading is
// not enough: nothing here tracks what has been read, and inferring it would
// mean deleting things the owner had not finished with.
func (r *Registry) DeleteMessage(ctx context.Context, sessionID, messageID string) error {
	result, err := r.db.ExecContext(ctx,
		`DELETE FROM messages WHERE id = ? AND recipient_id = ?`, messageID, sessionID)
	if err != nil {
		return fmt.Errorf("delete message: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read delete result: %w", err)
	}
	if count == 0 {
		return fmt.Errorf("message %q in %q: %w", messageID, sessionID, ErrNotFound)
	}
	return nil
}

// ClearInbox empties one session's inbox and reports how many it removed.
func (r *Registry) ClearInbox(ctx context.Context, sessionID string) (int, error) {
	result, err := r.db.ExecContext(ctx, `DELETE FROM messages WHERE recipient_id = ?`, sessionID)
	if err != nil {
		return 0, fmt.Errorf("clear inbox: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read clear result: %w", err)
	}
	return int(count), nil
}

// PruneSettledOutbound removes delivered and refused rows older than the given
// age, and reports how many went.
//
// Settled rows are kept for a while rather than deleted on settlement, because
// `ah outbound <id>` is the only place an owner can find out what happened to a
// message and deleting the answer immediately would make the command useless.
// They are not kept forever, because nothing reads them after that.
func (r *Registry) PruneSettledOutbound(ctx context.Context, olderThan time.Duration) (int, error) {
	if olderThan <= 0 {
		return 0, fmt.Errorf("%w: a retention period must be positive", ErrInvalidSession)
	}
	cutoff := time.Now().UTC().Add(-olderThan).UnixMilli()
	result, err := r.db.ExecContext(ctx, `
DELETE FROM outbound_messages
WHERE state IN ('delivered', 'refused') AND updated_at_ms < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("prune settled outbound messages: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read prune result: %w", err)
	}
	return int(count), nil
}
