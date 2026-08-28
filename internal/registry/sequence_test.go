package registry

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"sync"
	"testing"
)

func openAt(t *testing.T, path string) *Registry {
	t.Helper()
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// The sequence is what lets a receiver tell a fresh snapshot from a replayed
// one. An in-memory counter returns to one after a restart, so a correct
// receiver would have to reject the restarted sender forever or stop checking.
func TestOutboundSequenceSurvivesReopening(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agenthub.db")

	store := openAt(t, path)
	var last uint64
	for range 3 {
		next, err := store.NextHeartbeatSequence(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if next <= last {
			t.Fatalf("sequence went from %d to %d", last, next)
		}
		last = next
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openAt(t, path)
	next, err := reopened.NextHeartbeatSequence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if next <= last {
		t.Errorf("after reopening, sequence = %d; the previous process had already used %d", next, last)
	}
}

// Concurrent allocation must never hand the same number to two callers: two
// snapshots sharing a sequence are indistinguishable to a receiver that keeps
// the highest one it has seen.
func TestOutboundSequenceNeverRepeatsUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	store := openAt(t, filepath.Join(t.TempDir(), "agenthub.db"))

	const workers, perWorker = 8, 16
	var mutex sync.Mutex
	seen := map[uint64]bool{}
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			for range perWorker {
				next, err := store.NextHeartbeatSequence(ctx)
				if err != nil {
					t.Errorf("allocate sequence: %v", err)
					return
				}
				mutex.Lock()
				if seen[next] {
					t.Errorf("sequence %d was handed out twice", next)
				}
				seen[next] = true
				mutex.Unlock()
			}
		}()
	}
	group.Wait()
	if len(seen) != workers*perWorker {
		t.Errorf("got %d distinct sequences, want %d", len(seen), workers*perWorker)
	}
	if seen[0] {
		t.Error("zero was handed out; the first published sequence must be 1")
	}
}

// A database written by an earlier build has no counter. It must start from
// zero and publish nothing on the way there.
func TestOutboundSequenceMigratesFromZero(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agenthub.db")

	store := openAt(t, path)
	if _, err := store.db.ExecContext(ctx, `DROP TABLE heartbeat_sequence`); err != nil {
		t.Fatalf("simulate a pre-sequence database: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded := openAt(t, path)
	next, err := upgraded.NextHeartbeatSequence(ctx)
	if err != nil {
		t.Fatalf("upgraded database refused to allocate a sequence: %v", err)
	}
	if next != 1 {
		t.Errorf("first sequence on an upgraded database = %d, want 1", next)
	}
	sessions, err := upgraded.ListSessions(ctx, ListOptions{PublicOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Errorf("the migration published %d sessions", len(sessions))
	}
}

// SQLite stores a signed 64-bit integer. At the top of that range the only safe
// answer is a refusal: wrapping, returning zero, or reusing the last value all
// hand a receiver a sequence it has already seen.
func TestOutboundSequenceFailsClosedWhenExhausted(t *testing.T) {
	ctx := context.Background()
	store := openAt(t, filepath.Join(t.TempDir(), "agenthub.db"))

	if _, err := store.db.ExecContext(ctx,
		`UPDATE heartbeat_sequence SET last_value = ? WHERE singleton = 1`, int64(math.MaxInt64)-1); err != nil {
		t.Fatal(err)
	}

	last, err := store.NextHeartbeatSequence(ctx)
	if err != nil {
		t.Fatalf("the last usable sequence was refused: %v", err)
	}
	if last != uint64(math.MaxInt64) {
		t.Fatalf("last sequence = %d, want %d", last, uint64(math.MaxInt64))
	}

	for range 2 {
		next, err := store.NextHeartbeatSequence(ctx)
		if !errors.Is(err, ErrSequenceExhausted) {
			t.Fatalf("error = %v; want ErrSequenceExhausted", err)
		}
		if next != 0 {
			t.Errorf("a refused allocation returned %d; it must return nothing usable", next)
		}
	}

	var stored int64
	if err := store.db.QueryRowContext(ctx,
		`SELECT last_value FROM heartbeat_sequence WHERE singleton = 1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != math.MaxInt64 {
		t.Errorf("stored counter = %d; a refusal must not move it", stored)
	}
}
