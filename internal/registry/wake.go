package registry

import (
	"context"
	"database/sql"
	"errors"
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
	ID        string `json:"id"`
	MessageID string `json:"messageId"`
	// SourceNodeID is the node the envelope's signature proves sent this, or
	// "" for a message that never left this machine.
	SourceNodeID string `json:"sourceNodeId,omitempty"`
	// SourceSession is the label the sender chose for itself. Shown to a
	// person, and never counted on — see PairKey.
	SourceSession      string      `json:"sourceSession,omitempty"`
	DestinationSession string      `json:"destinationSession"`
	Hops               int         `json:"hops"`
	Outcome            WakeOutcome `json:"outcome"`
	// PairKey is what the per-pair limit counts by. Stored rather than derived
	// at read time so a later change to the derivation cannot silently
	// re-bucket history that was already counted one way.
	PairKey_ string    `json:"-"`
	Detail   string    `json:"detail,omitempty"`
	At       time.Time `json:"at"`
}

// PairKey is the identity the per-pair limit counts by.
//
// The node id, which the envelope's signature proves, and never the session
// label, which the sender writes. Keying on the label made the limit
// decorative: a peer varying `from` on every message minted a fresh bucket
// each time, and a measured run took the same peer from 3 wakes to 12 by
// doing nothing but changing a string it controls.
//
// A peer's sessions therefore share one bucket. That is the point — the thing
// being limited is "how fast may that machine make this agent move", and the
// machine is the only part of the claim this node can check.
//
// "local" for a message that never left this machine. Not the empty string:
// empty would mean "count every source", which is the session limit wearing
// the pair limit's name, and a local send would then be charged against every
// peer that shares its destination. One bucket for local traffic is a real
// bound on two sessions here answering each other, which is otherwise
// unlimited.
func (e WakeEvent) PairKey() string {
	if e.PairKey_ != "" {
		return e.PairKey_
	}
	if e.SourceNodeID != "" {
		return "node:" + e.SourceNodeID
	}
	return "local"
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
    pair_key TEXT NOT NULL DEFAULT 'local',
    destination_session TEXT NOT NULL,
    hops INTEGER NOT NULL DEFAULT 0 CHECK (hops >= 0),
    outcome TEXT NOT NULL,
    detail TEXT NOT NULL DEFAULT '',
    at_ms INTEGER NOT NULL,
    chain_used INTEGER NOT NULL DEFAULT 0 CHECK (chain_used IN (0, 1))
);
CREATE INDEX IF NOT EXISTS idx_wake_events_pair
    ON wake_events(pair_key, destination_session, at_ms DESC);
CREATE INDEX IF NOT EXISTS idx_wake_events_destination
    ON wake_events(destination_session, at_ms DESC);
CREATE INDEX IF NOT EXISTS idx_wake_events_at
    ON wake_events(at_ms DESC);
`
	if _, err := r.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate wake events: %w", err)
	}
	// CREATE TABLE IF NOT EXISTS does not revisit a table that already exists,
	// and neither does CREATE INDEX — so a database written by an earlier
	// build of this same branch opened without complaint and then failed every
	// wake with "no such column: pair_key", permanently, with only a log line.
	// It fails closed, which is the right direction and not a reason to leave
	// it: every other column added here has this path.
	added := []string{}
	for _, column := range []struct{ name, definition string }{
		{"pair_key", "TEXT NOT NULL DEFAULT 'local'"},
		// Without the CHECK the fresh schema carries: SQLite's ALTER TABLE
		// cannot add one, and rebuilding the table to gain it would cost more
		// than a constraint on a column only this file writes is worth.
		{"chain_used", "INTEGER NOT NULL DEFAULT 0"},
	} {
		has, err := r.hasColumn(ctx, "wake_events", column.name)
		if err != nil {
			return err
		}
		if has {
			continue
		}
		if _, err := r.db.ExecContext(ctx,
			fmt.Sprintf("ALTER TABLE wake_events ADD COLUMN %s %s", column.name, column.definition),
		); err != nil {
			return fmt.Errorf("add wake events column %q: %w", column.name, err)
		}
		added = append(added, column.name)
	}
	if len(added) > 0 {
		// The pair index was created over the old columns and CREATE INDEX IF
		// NOT EXISTS will not replace one that exists under the same name — so
		// after adding pair_key the index still covers source_session, and
		// every reservation on this node scans the table for the rest of its
		// life. Dropped and rebuilt rather than left to rot quietly.
		if _, err := r.db.ExecContext(ctx, `
DROP INDEX IF EXISTS idx_wake_events_pair;
CREATE INDEX IF NOT EXISTS idx_wake_events_pair
    ON wake_events(pair_key, destination_session, at_ms DESC);`); err != nil {
			return fmt.Errorf("rebuild the wake events pair index: %w", err)
		}
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
		pairKey, destination string
		window               time.Duration
		limit                int
		outcome              WakeOutcome
	}{
		{event.PairKey(), event.DestinationSession, limits.PairWindow, limits.Pair, WakeRefusedPair},
		{"", event.DestinationSession, limits.SessionWindow, limits.Session, WakeRefusedSession},
		{"", "", limits.NodeWindow, limits.Node, WakeRefusedNode},
	}
	event.Outcome = WakeWoken
	for _, check := range checks {
		count, err := countWakesTx(ctx, transaction, check.pairKey, check.destination,
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

// queryRower is satisfied by both *sql.DB and *sql.Tx, so the count a
// reservation takes and the one a caller can ask for are the same query.
type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func countWakesTx(ctx context.Context, q queryRower, pairKey, destinationSession string,
	since time.Time) (int, error) {
	query := `SELECT count(*) FROM wake_events WHERE outcome = ? AND at_ms >= ?`
	arguments := []any{string(WakeWoken), since.UTC().UnixMilli()}
	if pairKey != "" {
		query += ` AND pair_key = ?`
		arguments = append(arguments, pairKey)
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
    (id, message_id, source_node_id, source_session, pair_key, destination_session,
     hops, outcome, detail, at_ms)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID, event.MessageID, event.SourceNodeID, event.SourceSession, event.PairKey(),
		event.DestinationSession, event.Hops, string(event.Outcome), event.Detail,
		event.At.UTC().UnixMilli()); err != nil {
		return WakeEvent{}, fmt.Errorf("record wake event: %w", err)
	}
	return event, nil
}

// WakeAttributionWindow bounds how long after a wake an outbound message may
// still be treated as caused by it.
//
// A heuristic, and named as one. Nothing links an agent's decision to send to
// the message that woke it: the agent calls agent_send like any other caller,
// and no provider says why. So the chain is reconstructed by proximity.
//
// The window is an upper bound, not the whole rule — a wake is claimed by at
// most one message, so a slow agent is still attributed while a person typing
// afterwards is not. Without that, one wake tainted every send from that
// session for the whole window, and a human's own message could be refused at
// the far end with a hop-limit refusal they had no way to see.
const WakeAttributionWindow = 15 * time.Minute

// PeekWakeChain reports the hop count a message from this session should
// carry, and the id of the wake it came from, without claiming it.
//
// Two calls rather than one because the claim must not be spent on a message
// that then fails to send. Spent eagerly, a woken agent could zero its own
// chain by addressing one throwaway message at a session that does not exist:
// the send is refused, the claim is gone, and its real reply goes out at zero
// hops.
//
// Zero and "" when there is no unclaimed wake in the window: the answer for a
// person typing into their own agent, for a second message after a reply has
// already been attributed, and whenever this cannot tell.
func (r *Registry) PeekWakeChain(ctx context.Context, sessionID string, now time.Time) (int, string, error) {
	if sessionID == "" {
		return 0, "", nil
	}
	var id string
	var hops int
	// rowid, not id, for the tie-break. Ids are random hex, so ordering by
	// them picks arbitrarily between two wakes in the same millisecond —
	// measured returning 4 from a pair recorded at 0 and 3. rowid is issued in
	// insert order, which is the order that actually happened.
	err := r.db.QueryRowContext(ctx, `
SELECT id, hops FROM wake_events
WHERE destination_session = ? AND outcome = ? AND chain_used = 0 AND at_ms >= ?
ORDER BY at_ms DESC, rowid DESC LIMIT 1`,
		sessionID, string(WakeWoken),
		now.Add(-WakeAttributionWindow).UTC().UnixMilli()).Scan(&id, &hops)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", fmt.Errorf("read the last wake of %q: %w", sessionID, err)
	}
	// One more than the wake that caused it.
	return hops + 1, id, nil
}

// ClaimWakeChain marks a wake as inherited, so a later message does not
// inherit it again.
//
// Called after the message that inherited it is safely stored. "Later", not
// "another": peek and claim are two statements, so sends that overlap between
// them all see the same unclaimed wake and all carry its count. How many
// overlap is a race — four concurrent sends were measured inheriting
// anything between two and four times — so the bound is "no message after
// the claim lands", not a number.
//
// Left that way rather than claimed at peek time, which is what the split
// undid: an eager claim let a woken agent zero its own chain with one
// throwaway message to a session that does not exist. Between over-counting
// concurrent replies and letting an agent erase its own history, over-counting
// is the one that stops an exchange early, and stopping early is the direction
// this whole mechanism fails in.
//
// Claiming an already-claimed wake is not an error.
func (r *Registry) ClaimWakeChain(ctx context.Context, wakeID string) error {
	if wakeID == "" {
		return nil
	}
	if _, err := r.db.ExecContext(ctx,
		`UPDATE wake_events SET chain_used = 1 WHERE id = ?`, wakeID); err != nil {
		return fmt.Errorf("claim the wake %q: %w", wakeID, err)
	}
	return nil
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
SELECT id, message_id, source_node_id, source_session, pair_key, destination_session,
       hops, outcome, detail, at_ms
FROM wake_events`
	arguments := []any{}
	if sessionID != "" {
		query += ` WHERE destination_session = ?`
		arguments = append(arguments, sessionID)
	}
	// rowid, not id: ids are random hex, so two wakes in the same millisecond
	// come back in an arbitrary order — and "newest first" is the whole
	// contract of this listing. rowid is issued in insert order.
	query += ` ORDER BY at_ms DESC, rowid DESC LIMIT ?`
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
			&event.PairKey_, &event.DestinationSession, &event.Hops, &outcome, &event.Detail,
			&atMS); err != nil {
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
