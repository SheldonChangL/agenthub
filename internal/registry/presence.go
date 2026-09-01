package registry

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrStaleSnapshot marks a heartbeat that does not advance the sender's
// sequence. It is not a failure of the sender's identity — the envelope was
// authentic — so a caller must not treat it as an attack on its own. It is what
// a replayed, reordered or duplicated delivery looks like.
var ErrStaleSnapshot = errors.New("heartbeat does not advance the sender's sequence")

// ErrSnapshotExpired marks a heartbeat whose own expiry has already passed.
// Storing it would let a sender's stale view outlive the sender's intent.
var ErrSnapshotExpired = errors.New("heartbeat has already expired")

// PeerSnapshot is one peer's complete presence view as this node last received
// it.
//
// One row per peer, holding the whole payload, is the storage shape the
// replacement contract demands: there is nowhere to merge into. A consumer that
// wanted to combine two snapshots would have to defeat the schema to do it,
// which is the point — a session that disappears from a peer's array is a
// revocation, and a merge would keep showing it.
type PeerSnapshot struct {
	NodeID     string          `json:"nodeId"`
	Sequence   uint64          `json:"sequence"`
	ReceivedAt time.Time       `json:"receivedAt"`
	ExpiresAt  time.Time       `json:"expiresAt"`
	Payload    json.RawMessage `json:"payload"`
}

// Expired reports whether this snapshot may still be shown at the given time.
func (p PeerSnapshot) Expired(now time.Time) bool { return !now.Before(p.ExpiresAt) }

func (r *Registry) migratePresence(ctx context.Context) error {
	// The payload is stored as the bytes that were verified, not as parsed
	// columns. Re-encoding a decoded payload would not reproduce the bytes the
	// signature covers, and this table is the only record of what a peer
	// actually said.
	//
	// last_sequence is kept in the same row as the payload it came with, so the
	// monotonicity check and the data it guards cannot disagree.
	const schema = `
CREATE TABLE IF NOT EXISTS peer_snapshots (
    node_id TEXT PRIMARY KEY CHECK (length(node_id) BETWEEN 16 AND 128 AND node_id NOT GLOB '*[^!-~]*'),
    last_sequence INTEGER NOT NULL CHECK (last_sequence > 0),
    received_at_ms INTEGER NOT NULL CHECK (received_at_ms > 0),
    expires_at_ms INTEGER NOT NULL CHECK (expires_at_ms > 0),
    payload BLOB NOT NULL
);
`
	if _, err := r.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate peer snapshots: %w", err)
	}
	return nil
}

// StorePeerSnapshot replaces everything this node holds for one peer.
//
// The sequence must strictly advance. Equal is refused along with lower: a
// repeat of the number a peer last sent is a replay, and accepting it would let
// anyone who captured one delivery re-assert a view the sender has since
// changed. The check and the write share one statement so two concurrent
// deliveries cannot both pass it.
//
// A snapshot that has already expired is refused outright rather than stored
// and hidden. Keeping it would mean the newest thing this node holds for a peer
// is something it may never show, and a later, older-but-live delivery could
// not replace it — the sequence check would reject that too.
func (r *Registry) StorePeerSnapshot(ctx context.Context, snapshot PeerSnapshot, now time.Time) error {
	switch {
	case snapshot.NodeID == "":
		return fmt.Errorf("%w: peer node id is required", ErrInvalidSession)
	case snapshot.Sequence == 0:
		return fmt.Errorf("%w: a heartbeat sequence starts at one", ErrInvalidSession)
	case len(snapshot.Payload) == 0:
		return fmt.Errorf("%w: peer snapshot payload is required", ErrInvalidSession)
	case snapshot.Sequence > uint64(maxSequence):
		return fmt.Errorf("%w: sequence %d is outside the storable range", ErrInvalidSession, snapshot.Sequence)
	}
	if snapshot.Expired(now) {
		return fmt.Errorf("%w: it expired at %s", ErrSnapshotExpired, snapshot.ExpiresAt.UTC().Format(time.RFC3339))
	}

	result, err := r.db.ExecContext(ctx, `
INSERT INTO peer_snapshots (node_id, last_sequence, received_at_ms, expires_at_ms, payload)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(node_id) DO UPDATE SET
    last_sequence  = excluded.last_sequence,
    received_at_ms = excluded.received_at_ms,
    expires_at_ms  = excluded.expires_at_ms,
    payload        = excluded.payload
WHERE excluded.last_sequence > peer_snapshots.last_sequence`,
		snapshot.NodeID, int64(snapshot.Sequence), now.UTC().UnixMilli(),
		snapshot.ExpiresAt.UTC().UnixMilli(), []byte(snapshot.Payload))
	if err != nil {
		return fmt.Errorf("store peer snapshot: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("store peer snapshot: %w", err)
	}
	if affected == 0 {
		// The WHERE clause on the upsert refused it, which can only mean the
		// stored sequence is at least as high as this one.
		return fmt.Errorf("%w: sequence %d", ErrStaleSnapshot, snapshot.Sequence)
	}
	return nil
}

// PeerSnapshotFor returns what this node holds for one peer, expired or not.
// The caller decides what an expired snapshot means; this is storage.
func (r *Registry) PeerSnapshotFor(ctx context.Context, nodeID string) (PeerSnapshot, bool, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT node_id, last_sequence, received_at_ms, expires_at_ms, payload
FROM peer_snapshots WHERE node_id = ?`, nodeID)
	snapshot, err := scanPeerSnapshot(row)
	if errors.Is(err, sql.ErrNoRows) {
		return PeerSnapshot{}, false, nil
	}
	if err != nil {
		return PeerSnapshot{}, false, fmt.Errorf("read peer snapshot: %w", err)
	}
	return snapshot, true, nil
}

// ListPeerSnapshots returns every peer snapshot, ordered by node id.
func (r *Registry) ListPeerSnapshots(ctx context.Context) ([]PeerSnapshot, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT node_id, last_sequence, received_at_ms, expires_at_ms, payload
FROM peer_snapshots ORDER BY node_id`)
	if err != nil {
		return nil, fmt.Errorf("list peer snapshots: %w", err)
	}
	defer rows.Close()

	snapshots := []PeerSnapshot{}
	for rows.Next() {
		snapshot, err := scanPeerSnapshot(rows)
		if err != nil {
			return nil, fmt.Errorf("read peer snapshot: %w", err)
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list peer snapshots: %w", err)
	}
	return snapshots, nil
}

// DropPeerSnapshot removes everything held for one peer. Revoking a node calls
// it: trust is what made the peer's view admissible, and withdrawing trust must
// not leave the last thing it said still on screen.
func (r *Registry) DropPeerSnapshot(ctx context.Context, nodeID string) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM peer_snapshots WHERE node_id = ?`, nodeID); err != nil {
		return fmt.Errorf("drop peer snapshot: %w", err)
	}
	return nil
}

func scanPeerSnapshot(row rowScanner) (PeerSnapshot, error) {
	var (
		snapshot   PeerSnapshot
		sequence   int64
		receivedMS int64
		expiresMS  int64
		payload    []byte
	)
	if err := row.Scan(&snapshot.NodeID, &sequence, &receivedMS, &expiresMS, &payload); err != nil {
		return PeerSnapshot{}, err
	}
	if sequence < 0 {
		return PeerSnapshot{}, fmt.Errorf("stored sequence %d is negative", sequence)
	}
	snapshot.Sequence = uint64(sequence)
	snapshot.ReceivedAt = time.UnixMilli(receivedMS).UTC()
	snapshot.ExpiresAt = time.UnixMilli(expiresMS).UTC()
	snapshot.Payload = json.RawMessage(payload)
	return snapshot, nil
}
