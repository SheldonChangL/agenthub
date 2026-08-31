package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// ErrSequenceExhausted marks a counter that has reached the end of the range
// SQLite can store. A caller that gets it must publish nothing: there is no
// number left that a receiver has not already seen.
var ErrSequenceExhausted = errors.New("outbound heartbeat sequence is exhausted")

// ErrSequenceUnavailable marks a counter this store cannot read: the row is
// missing, or holds a value it could not have produced.
//
// It is separate from ErrSequenceExhausted because the two describe different
// damage. Exhaustion is this node reaching the end of what a signed 64-bit
// counter can hold, with every number in that range already published. An
// unavailable counter is a database whose position is unknown, which is not the
// same situation and does not have the same cause.
//
// Both fail closed, and neither is fixed by writing a new counter. Re-creating
// the row restarts at zero under the same node identity and republishes
// sequences a receiver may already hold, authenticated by this node's own
// signature — the replay the persisted counter exists to prevent. The safe
// recoveries are a backup whose high-water mark cannot roll back, or rotating
// to a new node identity with explicit re-pairing.
var ErrSequenceUnavailable = errors.New("outbound heartbeat sequence is unavailable")

// maxSequence is the largest value SQLite stores in an INTEGER column. The
// column is signed, so the counter stops here rather than at MaxUint64.
const maxSequence = uint64(math.MaxInt64)

// The names and value of the migration marker. The marker is what tells a
// database that never had a counter apart from one whose counter was deleted:
// table existence alone cannot, because dropping the table would make a node
// that has been publishing for months look like a fresh upgrade.
//
// The value is versioned so a marker written by a future migration is not read
// as this one. Nothing else writes schema_markers today; a marker table without
// this row therefore means the row was lost, not that another migration ran.
const (
	markerTableName     = "schema_markers"
	sequenceTableName   = "heartbeat_sequence"
	sequenceMarkerName  = "heartbeat_sequence"
	sequenceMarkerValue = "v1"
)

// migrateSequence establishes the outbound counter, or refuses to open.
//
// The only state it will write into is a database that has neither the counter
// nor its marker: that is a database from before this build, which published
// nothing under a number the new counter could repeat, so starting at zero is
// safe. Every other combination is read and preserved, or refused.
//
// What this can and cannot detect is worth stating plainly. It detects
// inconsistent loss — the counter without its marker, the marker without its
// counter, a marker this build did not write — because each leaves evidence that
// contradicts the rest. It cannot detect the loss of all the evidence at once: a
// database rolled back to a backup taken before any heartbeat, or deleted and
// recreated along with node.key, is indistinguishable from a first start.
// Ruling that out needs a monotonic store outside this file, or a node identity
// that rotates when the database does, and this build claims neither.
func (r *Registry) migrateSequence(ctx context.Context) error {
	// One counter for everything this node sends. A per-peer counter would let
	// the same node ID publish the same sequence twice under one signature, and
	// a receiver holding the highest sequence it has seen cannot order those.
	transaction, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin heartbeat sequence migration: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()

	markerPresent, err := tableExists(ctx, transaction, markerTableName)
	if err != nil {
		return err
	}
	sequencePresent, err := tableExists(ctx, transaction, sequenceTableName)
	if err != nil {
		return err
	}

	if !markerPresent {
		if sequencePresent {
			// A counter with no record that the migration creating it ever ran.
			// This build writes the two together, so one without the other means
			// part of the evidence is gone, and what this node already published
			// cannot be read from what is left.
			return fmt.Errorf(
				"%w: the counter exists with no migration marker, so what this node has already published is unknown",
				ErrSequenceUnavailable)
		}
		return createSequence(ctx, transaction)
	}

	if err := checkSequenceMarker(ctx, transaction); err != nil {
		return err
	}
	if !sequencePresent {
		// The marker says this database has had a counter, and it is gone. That
		// is the case table existence alone would have read as "never migrated",
		// and re-creating the table would restart at zero and republish sequences
		// under this node's own signature.
		return fmt.Errorf(
			"%w: the migration marker records a counter this database no longer has",
			ErrSequenceUnavailable)
	}
	// The counter is here and this node has already run a build that persists it.
	// Whatever state the row is in is the only evidence of what was published, so
	// check it and refuse if it is not usable; repairing it here would replace
	// that evidence with a guess.
	return checkSequenceRow(ctx, transaction)
}

// createSequence writes the counter and its marker in one transaction.
//
// Together or not at all: a marker without a counter is refused by every later
// start, so a partial create would leave a database that can never be opened
// again. Plain CREATE rather than CREATE IF NOT EXISTS, because reaching here
// means neither object exists and a surprise is worth a failure.
func createSequence(ctx context.Context, transaction *sql.Tx) error {
	const schema = `
CREATE TABLE schema_markers (
    name TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    created_at_ms INTEGER NOT NULL CHECK (created_at_ms > 0)
);
CREATE TABLE heartbeat_sequence (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    last_value INTEGER NOT NULL DEFAULT 0 CHECK (last_value >= 0)
);
INSERT INTO heartbeat_sequence (singleton, last_value) VALUES (1, 0);
`
	if _, err := transaction.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate heartbeat sequence: %w", err)
	}
	if _, err := transaction.ExecContext(ctx,
		`INSERT INTO schema_markers (name, value, created_at_ms) VALUES (?, ?, ?)`,
		sequenceMarkerName, sequenceMarkerValue, time.Now().UTC().UnixMilli()); err != nil {
		return fmt.Errorf("record the heartbeat sequence migration: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit heartbeat sequence migration: %w", err)
	}
	return nil
}

// checkSequenceMarker reads the marker and refuses anything it did not write.
//
// The columns are named in the query, so a marker table with a different shape
// fails here rather than being adopted. The value is compared exactly: a marker
// from another version records a migration whose counter semantics this build
// does not know.
func checkSequenceMarker(ctx context.Context, transaction *sql.Tx) error {
	rows, err := transaction.QueryContext(ctx,
		`SELECT value, created_at_ms FROM schema_markers WHERE name = ?`, sequenceMarkerName)
	if err != nil {
		return fmt.Errorf("%w: read the migration marker: %w", ErrSequenceUnavailable, err)
	}
	defer rows.Close()

	found := 0
	for rows.Next() {
		var value string
		var createdMS int64
		if err := rows.Scan(&value, &createdMS); err != nil {
			return fmt.Errorf("%w: the migration marker is not readable: %w", ErrSequenceUnavailable, err)
		}
		if value != sequenceMarkerValue || createdMS <= 0 {
			return fmt.Errorf("%w: the migration marker reads %q, recorded at %d",
				ErrSequenceUnavailable, value, createdMS)
		}
		found++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: read the migration marker: %w", ErrSequenceUnavailable, err)
	}
	switch found {
	case 1:
		return nil
	case 0:
		// Nothing else writes this table, so an absent row is a lost row, not a
		// database that predates the counter.
		return fmt.Errorf("%w: the migration marker is missing from a marker table this database already has",
			ErrSequenceUnavailable)
	default:
		return fmt.Errorf("%w: the marker table holds %d rows for %q",
			ErrSequenceUnavailable, found, sequenceMarkerName)
	}
}

func tableExists(ctx context.Context, transaction *sql.Tx, name string) (bool, error) {
	var count int
	err := transaction.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("look for table %q: %w", name, err)
	}
	return count > 0, nil
}

// checkSequenceRow refuses anything it cannot read as this store's own counter.
//
// It never writes. A missing row, several rows, or a value this store could not
// have written all mean the same thing: the position this node published up to
// is unknown. Re-creating the row would restart at zero and republish sequences
// a receiver may already hold — a replay this node's own signature would
// authenticate. The recoverable answers are a backup whose high-water mark
// cannot roll back, or a new node identity with explicit re-pairing; neither is
// something opening a database may decide.
func checkSequenceRow(ctx context.Context, transaction *sql.Tx) error {
	rows, err := transaction.QueryContext(ctx,
		`SELECT singleton, last_value FROM heartbeat_sequence`)
	if err != nil {
		return fmt.Errorf("%w: read the counter: %w", ErrSequenceUnavailable, err)
	}
	defer rows.Close()

	found := 0
	for rows.Next() {
		var singleton, lastValue int64
		if err := rows.Scan(&singleton, &lastValue); err != nil {
			return fmt.Errorf("%w: the counter does not hold a number: %w", ErrSequenceUnavailable, err)
		}
		if singleton != 1 || lastValue < 0 {
			return fmt.Errorf("%w: the counter row reads singleton %d, value %d",
				ErrSequenceUnavailable, singleton, lastValue)
		}
		found++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: read the counter: %w", ErrSequenceUnavailable, err)
	}
	switch found {
	case 1:
		return nil
	case 0:
		return fmt.Errorf(
			"%w: the counter row is missing, so the last published sequence is unknown", ErrSequenceUnavailable)
	default:
		return fmt.Errorf("%w: the counter holds %d rows", ErrSequenceUnavailable, found)
	}
}

// NextHeartbeatSequence reserves the next outbound heartbeat sequence.
//
// The reservation is the increment: the value is never read and written as two
// steps, so two concurrent builders — or two builders created from the same
// store — cannot both be handed the same number. Values may have gaps, because a
// build that fails after reserving one does not give it back; a gap costs a
// receiver nothing, while a repeat makes a fresh snapshot indistinguishable from
// a replayed one.
//
// It returns ErrSequenceExhausted rather than wrapping, reusing, or returning
// zero at the top of the range. Every one of those hands a receiver a number it
// may already have seen, which is precisely what the counter exists to prevent.
func (r *Registry) NextHeartbeatSequence(ctx context.Context) (uint64, error) {
	// One statement, so the read and the write cannot be separated by another
	// writer. The guard keeps the addition inside the range the column holds:
	// SQLite has no wrapping behavior to rely on here, and a value it could not
	// store is not a value to publish.
	var next int64
	err := r.db.QueryRowContext(ctx, `
UPDATE heartbeat_sequence SET last_value = last_value + 1
WHERE singleton = 1 AND last_value < ?
RETURNING last_value`, int64(maxSequence)).Scan(&next)
	switch {
	case err == nil:
		if next <= 0 {
			// Unreachable through this API: the row starts at zero and only ever
			// grows. Reaching it means the stored value came from somewhere else,
			// so the counter's real position is unknown and a heartbeat must not
			// be built on a number a receiver may already hold.
			return 0, fmt.Errorf("%w: stored counter is %d", ErrSequenceUnavailable, next)
		}
		return uint64(next), nil
	case errors.Is(err, sql.ErrNoRows):
		// The guard refused the update, or the row is missing. Both fail closed,
		// and they are classified apart, because they need opposite fixes.
		return 0, r.classifyRefusedSequence(ctx)
	default:
		return 0, fmt.Errorf("allocate heartbeat sequence: %w", err)
	}
}

// classifyRefusedSequence says why the reservation above matched no row. The
// answer decides what an operator does next, so it is read rather than assumed.
func (r *Registry) classifyRefusedSequence(ctx context.Context) error {
	var current int64
	err := r.db.QueryRowContext(ctx,
		`SELECT last_value FROM heartbeat_sequence WHERE singleton = 1`).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: the counter row is missing", ErrSequenceUnavailable)
	}
	if err != nil {
		return fmt.Errorf("%w: read heartbeat sequence: %w", ErrSequenceUnavailable, err)
	}
	if current >= int64(maxSequence) {
		return fmt.Errorf("%w: the counter has reached %d", ErrSequenceExhausted, current)
	}
	// The row exists and is below the guard, so the reservation should have
	// matched it. Something changed the row between the two statements, or the
	// value is one this store could not have written; either way the counter's
	// position is not known well enough to publish from.
	return fmt.Errorf("%w: the counter reads %d but no reservation could be made",
		ErrSequenceUnavailable, current)
}
