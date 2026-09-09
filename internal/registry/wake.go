package registry

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"agenthub.local/agenthub/internal/id"
)

// WakeOutcome is what happened when a message could have started a turn.
type WakeOutcome string

const (
	// WakeWoken means an agent was actually driven.
	WakeWoken WakeOutcome = "woken"
	// WakeRefusedHops means the automatic exchange had already travelled far
	// enough that this node stopped relaying it.
	WakeRefusedHops WakeOutcome = "refused_hops"
	// WakeRefusedPair, WakeRefusedSession and WakeRefusedNode are the three
	// rate limits, from narrowest to widest.
	WakeRefusedPair    WakeOutcome = "refused_pair_rate"
	WakeRefusedSession WakeOutcome = "refused_session_rate"
	WakeRefusedNode    WakeOutcome = "refused_node_rate"
	// WakeFailed means this node tried and the provider did not take it. The
	// message is still in the inbox; nothing was lost.
	WakeFailed WakeOutcome = "failed"
)

// WakeEvent is one record of an agent being woken, or of a wake being refused.
//
// Refusals are recorded, not dropped. A limit that silently swallows the thing
// it stopped leaves an owner with two indistinguishable worlds: one where
// nothing arrived, and one where something arrived and was held back. The
// second is the one they need to know about.
//
// Nothing is recorded for a session whose autoWake is closed. That is not a
// refusal, it is waking never having been on the table, and a row for every
// message to every session would bury the rows that mean something.
type WakeEvent struct {
	ID                 string      `json:"id"`
	MessageID          string      `json:"messageId"`
	SourceNodeID       string      `json:"sourceNodeId,omitempty"`
	SourceSession      string      `json:"sourceSession,omitempty"`
	DestinationSession string      `json:"destinationSession"`
	Hops               int         `json:"hops"`
	Outcome            WakeOutcome `json:"outcome"`
	Detail             string      `json:"detail,omitempty"`
	At                 time.Time   `json:"at"`
}

// The three windows, narrowest first.
//
// They are not alternatives; each answers a question the others cannot. The
// pair limit is the one that stops two machines answering each other, and it is
// the only one of the three that cannot be evaded by involving a third node.
// The session limit bounds what any one agent can be made to do. The node limit
// bounds what the machine as a whole can be made to do, so one peer cannot
// spend the owner's budget by spreading itself across many sessions.
const (
	MaxWakesPerPair    = 3
	WakePairWindow     = 10 * time.Minute
	MaxWakesPerSession = 12
	WakeSessionWindow  = time.Hour
	MaxWakesPerNode    = 60
	WakeNodeWindow     = time.Hour
)

func (r *Registry) migrateWakeEvents(ctx context.Context) error {
	// at_ms is indexed with each of the three keys the limits count by, because
	// every one of those counts runs on the delivery path: a message waits on
	// them before it can wake anything.
	const schema = `
CREATE TABLE IF NOT EXISTS wake_events (
    id TEXT PRIMARY KEY,
    message_id TEXT NOT NULL,
    source_node_id TEXT NOT NULL DEFAULT '',
    source_session TEXT NOT NULL DEFAULT '',
    destination_session TEXT NOT NULL,
    hops INTEGER NOT NULL DEFAULT 0 CHECK (hops >= 0),
    outcome TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    at_ms INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_wake_events_pair
    ON wake_events(source_session, destination_session, at_ms DESC);
CREATE INDEX IF NOT EXISTS idx_wake_events_destination
    ON wake_events(destination_session, at_ms DESC);
CREATE INDEX IF NOT EXISTS idx_wake_events_at
    ON wake_events(at_ms DESC);
`
	if _, err := r.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate wake events: %w", err)
	}
	return nil
}

// RecordWake writes one wake event and returns it.
//
// For an outcome decided without a reservation — a hop count already over the
// limit, say, which needs no counting because it does not depend on what else
// has happened recently.
func (r *Registry) RecordWake(ctx context.Context, event WakeEvent) (WakeEvent, error) {
	if event.DestinationSession == "" {
		return WakeEvent{}, fmt.Errorf("%w: wake event needs a destination session", ErrInvalidSession)
	}
	return insertWakeTx(ctx, r.db, event)
}

// WakeLimits is what the caller will allow, passed in rather than read here.
//
// The policy belongs to the caller; only the counting has to happen here,
// because a count that is not in the same transaction as the insert it guards
// is not a limit. Two messages arriving together would both count two, both
// see room, and both wake.
type WakeLimits struct {
	Pair, Session, Node                   int
	PairWindow, SessionWindow, NodeWindow time.Duration
}

// DefaultWakeLimits are the three windows this node applies unless told
// otherwise.
func DefaultWakeLimits() WakeLimits {
	return WakeLimits{
		Pair: MaxWakesPerPair, PairWindow: WakePairWindow,
		Session: MaxWakesPerSession, SessionWindow: WakeSessionWindow,
		Node: MaxWakesPerNode, NodeWindow: WakeNodeWindow,
	}
}

// ReserveWake decides whether one more wake fits, and writes the record either
// way, in a single transaction.
//
// The row is the reservation. Counting and inserting apart would let two
// concurrent deliveries both find room; here the count that permits a row is
// taken in the same transaction that writes it, so the second delivery counts
// the first.
//
// A reserved wake is written as WakeWoken before the provider is called,
// because the count has to include it while the turn is running — that is
// exactly when a second message must not start another. SettleWake flips it
// afterwards if the provider refused, and CountWakes stops counting it then,
// which is right: nothing ran, so nothing was spent.
func (r *Registry) ReserveWake(ctx context.Context, event WakeEvent, limits WakeLimits) (WakeEvent, error) {
	if event.DestinationSession == "" {
		return WakeEvent{}, fmt.Errorf("%w: wake event needs a destination session", ErrInvalidSession)
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	transaction, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return WakeEvent{}, fmt.Errorf("begin wake reservation: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	// Narrowest first, so the reason an owner is told is the most specific one
	// that applies. "This pair has been talking too fast" points at something
	// they can act on; "this node is busy" does not.
	checks := []struct {
		source, destination string
		window              time.Duration
		limit               int
		outcome             WakeOutcome
	}{
		{event.SourceSession, event.DestinationSession, limits.PairWindow, limits.Pair, WakeRefusedPair},
		{"", event.DestinationSession, limits.SessionWindow, limits.Session, WakeRefusedSession},
		{"", "", limits.NodeWindow, limits.Node, WakeRefusedNode},
	}
	event.Outcome = WakeWoken
	for _, check := range checks {
		// A pair check with no source session would count every source, which
		// is the session limit, not the pair one. Skipped rather than widened:
		// a local send with no named sender must not be counted against every
		// peer that shares its destination.
		if check.outcome == WakeRefusedPair && check.source == "" {
			continue
		}
		count, err := countWakesTx(ctx, transaction, check.source, check.destination,
			event.At.Add(-check.window))
		if err != nil {
			return WakeEvent{}, err
		}
		if count >= check.limit {
			event.Outcome = check.outcome
			event.Detail = fmt.Sprintf("%d in the last %s, at the limit of %d",
				count, check.window, check.limit)
			break
		}
	}

	stored, err := insertWakeTx(ctx, transaction, event)
	if err != nil {
		return WakeEvent{}, err
	}
	if err := transaction.Commit(); err != nil {
		return WakeEvent{}, fmt.Errorf("commit wake reservation: %w", err)
	}
	return stored, nil
}

// SettleWake records what became of a reserved wake.
//
// Only a reservation moves: an outcome already refused stays refused, so a
// late settle cannot turn a wake that never happened into one that did.
func (r *Registry) SettleWake(ctx context.Context, wakeID string, outcome WakeOutcome, detail string) error {
	result, err := r.db.ExecContext(ctx,
		`UPDATE wake_events SET outcome = ?, detail = ? WHERE id = ? AND outcome = ?`,
		string(outcome), detail, wakeID, string(WakeWoken))
	if err != nil {
		return fmt.Errorf("settle wake %q: %w", wakeID, err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("settle wake %q: %w", wakeID, err)
	}
	if changed == 0 {
		return fmt.Errorf("wake %q: %w", wakeID, ErrNotFound)
	}
	return nil
}

// CountWakes counts the wakes that actually happened in a window.
//
// Only WakeWoken is counted. A refusal cost nothing — no turn ran, no tokens
// were spent — so counting it would let a burst of refusals hold down a limit
// that exists to bound work actually done, and an exchange stopped by the pair
// limit would then also be counted against the node.
//
// sourceSession is optional: empty counts every source, which is how the
// per-session and node-wide limits are asked for. destinationSession empty
// counts every destination, which is the node-wide one.
func (r *Registry) CountWakes(ctx context.Context, sourceSession, destinationSession string,
	since time.Time) (int, error) {
	query := `SELECT count(*) FROM wake_events WHERE outcome = ? AND at_ms >= ?`
	arguments := []any{string(WakeWoken), since.UTC().UnixMilli()}
	if sourceSession != "" {
		query += ` AND source_session = ?`
		arguments = append(arguments, sourceSession)
	}
	if destinationSession != "" {
		query += ` AND destination_session = ?`
		arguments = append(arguments, destinationSession)
	}
	var count int
	if err := r.db.QueryRowContext(ctx, query, arguments...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count wake events: %w", err)
	}
	return count, nil
}

// queryRower is satisfied by both *sql.DB and *sql.Tx, so the count a
// reservation takes and the one a caller can ask for are the same query.
type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func countWakesTx(ctx context.Context, q queryRower, sourceSession, destinationSession string,
	since time.Time) (int, error) {
	query := `SELECT count(*) FROM wake_events WHERE outcome = ? AND at_ms >= ?`
	arguments := []any{string(WakeWoken), since.UTC().UnixMilli()}
	if sourceSession != "" {
		query += ` AND source_session = ?`
		arguments = append(arguments, sourceSession)
	}
	if destinationSession != "" {
		query += ` AND destination_session = ?`
		arguments = append(arguments, destinationSession)
	}
	var count int
	if err := q.QueryRowContext(ctx, query, arguments...).Scan(&count); err != nil {
		return 0, fmt.Errorf("count wake events: %w", err)
	}
	return count, nil
}

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func insertWakeTx(ctx context.Context, e execer, event WakeEvent) (WakeEvent, error) {
	if event.MessageID == "" {
		return WakeEvent{}, fmt.Errorf("%w: wake event needs a message id", ErrInvalidSession)
	}
	if event.Outcome == "" {
		return WakeEvent{}, fmt.Errorf("%w: wake event needs an outcome", ErrInvalidSession)
	}
	if event.ID == "" {
		generated, err := id.New("wake_")
		if err != nil {
			return WakeEvent{}, err
		}
		event.ID = generated
	}
	if event.At.IsZero() {
		event.At = time.Now().UTC()
	}
	if _, err := e.ExecContext(ctx, `
INSERT INTO wake_events
    (id, message_id, source_node_id, source_session, destination_session, hops, outcome, detail, at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID, event.MessageID, event.SourceNodeID, event.SourceSession,
		event.DestinationSession, event.Hops, string(event.Outcome), event.Detail,
		event.At.UTC().UnixMilli()); err != nil {
		return WakeEvent{}, fmt.Errorf("record wake event: %w", err)
	}
	return event, nil
}

// ListWakes returns the most recent wake events, newest first.
//
// Newest first because the question an owner arrives with is "what just
// happened", not "what happened when this node was first set up".
func (r *Registry) ListWakes(ctx context.Context, sessionID string, limit int) ([]WakeEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	query := `
SELECT id, message_id, source_node_id, source_session, destination_session,
       hops, outcome, detail, at_ms
FROM wake_events`
	arguments := []any{}
	if sessionID != "" {
		query += ` WHERE destination_session = ?`
		arguments = append(arguments, sessionID)
	}
	query += ` ORDER BY at_ms DESC, id DESC LIMIT ?`
	arguments = append(arguments, limit)

	rows, err := r.db.QueryContext(ctx, query, arguments...)
	if err != nil {
		return nil, fmt.Errorf("list wake events: %w", err)
	}
	defer rows.Close()
	events := make([]WakeEvent, 0)
	for rows.Next() {
		var event WakeEvent
		var outcome string
		var atMS int64
		if err := rows.Scan(&event.ID, &event.MessageID, &event.SourceNodeID, &event.SourceSession,
			&event.DestinationSession, &event.Hops, &outcome, &event.Detail, &atMS); err != nil {
			return nil, fmt.Errorf("scan wake event: %w", err)
		}
		event.Outcome = WakeOutcome(outcome)
		event.At = time.UnixMilli(atMS).UTC()
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read wake events: %w", err)
	}
	return events, nil
}
