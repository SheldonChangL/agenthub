package registry

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func snapshotAt(nodeID string, sequence uint64, expires time.Time, body string) PeerSnapshot {
	return PeerSnapshot{
		NodeID:    nodeID,
		Sequence:  sequence,
		ExpiresAt: expires,
		Payload:   json.RawMessage(body),
	}
}

const peerA = "node_aaaaaaaaaaaaaaaaaaaa"

// TestSecondSnapshotReplacesRatherThanMergesTheFirst is issue #17's central
// claim, held at the storage layer where it cannot be undone by a caller: a
// session that disappears from a peer's array is a revocation, and a consumer
// that merged would keep showing it.
func TestSecondSnapshotReplacesRatherThanMergesTheFirst(t *testing.T) {
	store := openTestRegistry(t)
	ctx := context.Background()
	now := time.Now().UTC()
	future := now.Add(time.Minute)

	first := `{"sequence":1,"sessions":[{"id":"a"},{"id":"b"}]}`
	if err := store.StorePeerSnapshot(ctx, snapshotAt(peerA, 1, future, first), now); err != nil {
		t.Fatalf("StorePeerSnapshot() error = %v", err)
	}
	// "b" is gone: the owner unpublished it, or withdrew this peer's grant.
	second := `{"sequence":2,"sessions":[{"id":"a"}]}`
	if err := store.StorePeerSnapshot(ctx, snapshotAt(peerA, 2, future, second), now); err != nil {
		t.Fatalf("StorePeerSnapshot() error = %v", err)
	}

	held, found, err := store.PeerSnapshotFor(ctx, peerA)
	if err != nil || !found {
		t.Fatalf("PeerSnapshotFor() = %v, %v, %v", held, found, err)
	}
	if string(held.Payload) != second {
		t.Fatalf("payload = %s; want the second snapshot verbatim", held.Payload)
	}
	var decoded struct {
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(held.Payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Sessions) != 1 || decoded.Sessions[0].ID != "a" {
		t.Fatalf("sessions = %#v; the unpublished session survived the replacement", decoded.Sessions)
	}
	if count := countRows(t, store, peerA); count != 1 {
		t.Fatalf("rows for the peer = %d; one peer must hold exactly one snapshot", count)
	}
}

// TestSequenceMustStrictlyAdvance covers the replay half of issue #11.
func TestSequenceMustStrictlyAdvance(t *testing.T) {
	store := openTestRegistry(t)
	ctx := context.Background()
	now := time.Now().UTC()
	future := now.Add(time.Minute)

	live := `{"sequence":5,"sessions":[{"id":"live"}]}`
	if err := store.StorePeerSnapshot(ctx, snapshotAt(peerA, 5, future, live), now); err != nil {
		t.Fatal(err)
	}

	for name, sequence := range map[string]uint64{
		"a replay of the same sequence": 5,
		"an older sequence":             4,
		"the very first sequence":       1,
	} {
		t.Run(name, func(t *testing.T) {
			stale := `{"sequence":0,"sessions":[{"id":"stale"},{"id":"attacker"}]}`
			err := store.StorePeerSnapshot(ctx, snapshotAt(peerA, sequence, future, stale), now)
			if !errors.Is(err, ErrStaleSnapshot) {
				t.Fatalf("error = %v; want ErrStaleSnapshot", err)
			}
			held, _, err := store.PeerSnapshotFor(ctx, peerA)
			if err != nil {
				t.Fatal(err)
			}
			if string(held.Payload) != live {
				t.Fatalf("payload = %s; a refused heartbeat overwrote the live snapshot", held.Payload)
			}
			if held.Sequence != 5 {
				t.Fatalf("sequence = %d; want the stored 5 to be untouched", held.Sequence)
			}
		})
	}
}

// TestAnAlreadyExpiredSnapshotIsRefused covers the expiry half of issue #11.
func TestAnAlreadyExpiredSnapshotIsRefused(t *testing.T) {
	store := openTestRegistry(t)
	ctx := context.Background()
	now := time.Now().UTC()

	err := store.StorePeerSnapshot(ctx, snapshotAt(peerA, 1, now.Add(-time.Second), `{"sequence":1}`), now)
	if !errors.Is(err, ErrSnapshotExpired) {
		t.Fatalf("error = %v; want ErrSnapshotExpired", err)
	}
	if _, found, err := store.PeerSnapshotFor(ctx, peerA); err != nil || found {
		t.Fatalf("PeerSnapshotFor() found = %v, err = %v; an expired heartbeat must store nothing", found, err)
	}

	// The boundary itself: expiring exactly now is expired, not live.
	err = store.StorePeerSnapshot(ctx, snapshotAt(peerA, 1, now, `{"sequence":1}`), now)
	if !errors.Is(err, ErrSnapshotExpired) {
		t.Fatalf("error = %v; a heartbeat expiring exactly now must be refused", err)
	}
}

// TestExpiredSnapshotReadsAsOfflineNotAbsent pins the difference an owner needs:
// a peer that went quiet is not a peer that was never there.
func TestExpiredSnapshotReadsAsOfflineNotAbsent(t *testing.T) {
	store := openTestRegistry(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := store.StorePeerSnapshot(ctx, snapshotAt(peerA, 1, now.Add(time.Second), `{"sequence":1}`), now); err != nil {
		t.Fatal(err)
	}
	held, found, err := store.PeerSnapshotFor(ctx, peerA)
	if err != nil || !found {
		t.Fatalf("PeerSnapshotFor() = %v, %v", found, err)
	}
	if held.Expired(now) {
		t.Fatal("a snapshot one second from expiry reads as expired")
	}
	if !held.Expired(now.Add(2 * time.Second)) {
		t.Fatal("a snapshot two seconds past expiry still reads as live")
	}
}

// TestRevokingANodeDiscardsItsSnapshot pins that withdrawing trust withdraws
// the view that trust admitted.
func TestRevokingANodeDiscardsItsSnapshot(t *testing.T) {
	store := openTestRegistry(t)
	ctx := context.Background()
	now := time.Now().UTC()

	if err := store.TrustNode(ctx, peer(peerA, "key-a")); err != nil {
		t.Fatalf("TrustNode() error = %v", err)
	}
	if err := store.StorePeerSnapshot(ctx,
		snapshotAt(peerA, 1, now.Add(time.Minute), `{"sequence":1,"sessions":[{"id":"a"}]}`), now); err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeNode(ctx, peerA); err != nil {
		t.Fatalf("RevokeNode() error = %v", err)
	}
	if _, found, err := store.PeerSnapshotFor(ctx, peerA); err != nil || found {
		t.Fatalf("PeerSnapshotFor() found = %v, err = %v; a revoked node's view must not survive", found, err)
	}
}

func countRows(t *testing.T, store *Registry, nodeID string) int {
	t.Helper()
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM peer_snapshots WHERE node_id = ?`, nodeID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
