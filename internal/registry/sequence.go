package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
)

// ErrSequenceExhausted marks a counter that has reached the end of the range
// SQLite can store. A caller that gets it must publish nothing: there is no
// number left that a receiver has not already seen.
var ErrSequenceExhausted = errors.New("outbound heartbeat sequence is exhausted")

// maxSequence is the largest value SQLite stores in an INTEGER column. The
// column is signed, so the counter stops here rather than at MaxUint64.
const maxSequence = uint64(math.MaxInt64)

func (r *Registry) migrateSequence(ctx context.Context) error {
	// One counter for everything this node sends. A per-peer counter would let
	// the same node ID publish the same sequence twice under one signature, and
	// a receiver holding the highest sequence it has seen cannot order those.
	//
	// A database written by an earlier build starts from zero, so the first
	// heartbeat after an upgrade carries sequence 1. That is safe because no
	// build before this one persisted anything: nothing was published under a
	// number this counter could repeat.
	const schema = `
CREATE TABLE IF NOT EXISTS heartbeat_sequence (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    last_value INTEGER NOT NULL DEFAULT 0 CHECK (last_value >= 0)
);
INSERT OR IGNORE INTO heartbeat_sequence (singleton, last_value) VALUES (1, 0);
`
	if _, err := r.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate heartbeat sequence: %w", err)
	}
	return nil
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
			// grows. Reaching it means the stored value was tampered with, and a
			// heartbeat must not be built on a number a receiver may hold.
			return 0, fmt.Errorf("%w: stored counter is %d", ErrSequenceExhausted, next)
		}
		return uint64(next), nil
	case errors.Is(err, sql.ErrNoRows):
		// The guard refused the update, or the row is missing. Both fail closed;
		// the message has to say which, because they need opposite fixes.
		return 0, r.explainMissingSequence(ctx)
	default:
		return 0, fmt.Errorf("allocate heartbeat sequence: %w", err)
	}
}

func (r *Registry) explainMissingSequence(ctx context.Context) error {
	var current int64
	err := r.db.QueryRowContext(ctx,
		`SELECT last_value FROM heartbeat_sequence WHERE singleton = 1`).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: the counter row is missing", ErrSequenceExhausted)
	}
	if err != nil {
		return fmt.Errorf("read heartbeat sequence: %w", err)
	}
	return fmt.Errorf("%w: the counter has reached %d", ErrSequenceExhausted, current)
}
