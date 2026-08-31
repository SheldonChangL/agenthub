package registry

import (
	"context"
	"database/sql"
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

// A database written by an earlier build has neither the counter nor the marker
// that records the counter was ever created. It must start from zero and publish
// nothing on the way there.
func TestOutboundSequenceMigratesFromZero(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agenthub.db")

	store := openAt(t, path)
	if _, err := store.db.ExecContext(ctx, `
DROP TABLE heartbeat_sequence;
DROP TABLE schema_markers;`); err != nil {
		t.Fatalf("simulate a pre-sequence database: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	upgraded := openAt(t, path)
	// The upgrade records that it happened. Without that record, a later start
	// cannot tell this database apart from one whose counter was deleted.
	if markers := countMarkerRows(t, path); markers != 1 {
		t.Errorf("the upgrade left %d marker rows, want 1", markers)
	}
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
		if errors.Is(err, ErrSequenceUnavailable) {
			t.Errorf("error = %v; a counter that ran out is not a counter that is missing", err)
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

// A missing counter row is not exhaustion. Both fail closed, but they need
// opposite responses from an operator: exhaustion means this node has published
// every sequence a signed 64-bit integer can hold and needs a new identity,
// while a missing row means the database is damaged or was written by something
// other than this program, and the counter's real position is unknown.
func TestMissingSequenceRowIsNotExhaustion(t *testing.T) {
	ctx := context.Background()
	store := openAt(t, filepath.Join(t.TempDir(), "agenthub.db"))

	if _, err := store.db.ExecContext(ctx, `DELETE FROM heartbeat_sequence`); err != nil {
		t.Fatal(err)
	}

	next, err := store.NextHeartbeatSequence(ctx)
	if !errors.Is(err, ErrSequenceUnavailable) {
		t.Fatalf("error = %v; want ErrSequenceUnavailable", err)
	}
	if errors.Is(err, ErrSequenceExhausted) {
		t.Errorf("error = %v; a damaged counter was reported as a lifetime that ran out", err)
	}
	if next != 0 {
		t.Errorf("a refused allocation returned %d; it must return nothing usable", next)
	}
}

// Reopening must never invent a counter.
//
// A row that is gone while the process is stopped is the dangerous case: the
// database still names this node, so a counter re-created at zero would hand a
// receiver sequence 1 again — the exact replay the persisted counter exists to
// prevent, and one no receiver can distinguish from a genuine first heartbeat.
// The high-water mark is unknown at that point, so the only safe answer is to
// refuse to open.
func TestReopeningRefusesToRecreateADeletedSequenceRow(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agenthub.db")

	store := openAt(t, path)
	for range 3 {
		if _, err := store.NextHeartbeatSequence(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `DELETE FROM heartbeat_sequence`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if reopened != nil {
		_ = reopened.Close()
	}
	if !errors.Is(err, ErrSequenceUnavailable) {
		t.Fatalf("Open error = %v; want ErrSequenceUnavailable", err)
	}

	if rows := countSequenceRows(t, path); rows != 0 {
		t.Errorf("opening re-created %d counter row(s); the previous high-water mark is unknown", rows)
	}
}

// An existing counter is left exactly as it was found. Opening is not a repair.
func TestOpeningPreservesAnExistingSequenceRow(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agenthub.db")

	store := openAt(t, path)
	if _, err := store.db.ExecContext(ctx,
		`UPDATE heartbeat_sequence SET last_value = 41 WHERE singleton = 1`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := openAt(t, path)
	next, err := reopened.NextHeartbeatSequence(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if next != 42 {
		t.Errorf("first sequence after reopening = %d, want 42", next)
	}
}

// A counter table this program did not write says nothing reliable about what
// this node already published, so it is refused rather than adopted.
func TestOpeningRefusesAnUnusableSequenceTable(t *testing.T) {
	ctx := context.Background()

	for name, setup := range map[string]string{
		"two counter rows": `
DROP TABLE heartbeat_sequence;
CREATE TABLE heartbeat_sequence (singleton INTEGER, last_value INTEGER);
INSERT INTO heartbeat_sequence (singleton, last_value) VALUES (1, 7), (2, 9);`,
		"counter is not a number": `
DROP TABLE heartbeat_sequence;
CREATE TABLE heartbeat_sequence (singleton INTEGER PRIMARY KEY, last_value TEXT);
INSERT INTO heartbeat_sequence (singleton, last_value) VALUES (1, 'not a sequence');`,
		"negative counter": `
UPDATE heartbeat_sequence SET last_value = -1 WHERE singleton = 1;`,
		"no columns this store wrote": `
DROP TABLE heartbeat_sequence;
CREATE TABLE heartbeat_sequence (something_else INTEGER);`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agenthub.db")
			store := openAt(t, path)
			// The CHECK constraint on a table this store created rejects a
			// negative value, so it is written the only way it could arrive:
			// with constraint enforcement off, as a damaged file would.
			if _, err := store.db.ExecContext(ctx, `PRAGMA ignore_check_constraints = ON`); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, setup); err != nil {
				t.Fatalf("build the unusable database: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			reopened, err := Open(ctx, path)
			if reopened != nil {
				_ = reopened.Close()
			}
			if !errors.Is(err, ErrSequenceUnavailable) {
				t.Fatalf("Open error = %v; want ErrSequenceUnavailable", err)
			}
		})
	}
}

func countSequenceRows(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open the database directly: %v", err)
	}
	defer db.Close()
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM heartbeat_sequence`).Scan(&rows); err != nil {
		t.Fatalf("count counter rows: %v", err)
	}
	return rows
}

// The dangerous rollback: the whole counter table is gone while the process is
// stopped, not just its row.
//
// Table existence cannot be the evidence that the counter was ever created,
// because deleting the table would then make this database look like one written
// before the counter existed — and the upgrade path for those starts at zero and
// republishes sequence 1 under this node's own identity and signature. The
// marker written alongside the table is what tells the two apart, so this must
// refuse to open and must recreate nothing.
func TestReopeningRefusesToRecreateADroppedSequenceTable(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agenthub.db")

	store := openAt(t, path)
	for range 3 {
		if _, err := store.NextHeartbeatSequence(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `DROP TABLE heartbeat_sequence`); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(ctx, path)
	if reopened != nil {
		_ = reopened.Close()
	}
	if !errors.Is(err, ErrSequenceUnavailable) {
		t.Fatalf("Open error = %v; want ErrSequenceUnavailable", err)
	}
	if tableExistsInFile(t, path, "heartbeat_sequence") {
		t.Error("opening recreated the counter table; the previous high-water mark is unknown")
	}
	if markers := countMarkerRows(t, path); markers != 1 {
		t.Errorf("marker rows after the refusal = %d, want the original 1", markers)
	}
}

// Every state where the marker and the counter disagree is refused. None of
// them can be resolved by writing: whichever half is missing, the last published
// sequence is not knowable from what is left, and inventing one republishes
// numbers a receiver may already hold.
func TestOpeningRefusesAnInconsistentMigrationMarker(t *testing.T) {
	ctx := context.Background()

	for name, damage := range map[string]string{
		"counter table but no marker table": `DROP TABLE schema_markers;`,
		"marker table without the counter's marker": `
DELETE FROM schema_markers WHERE name = 'heartbeat_sequence';`,
		"marker value this build did not write": `
UPDATE schema_markers SET value = 'v0' WHERE name = 'heartbeat_sequence';`,
		"marker table with columns this store did not write": `
DROP TABLE schema_markers;
CREATE TABLE schema_markers (something_else TEXT);`,
		"marker present, counter table gone": `DROP TABLE heartbeat_sequence;`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agenthub.db")
			store := openAt(t, path)
			if _, err := store.NextHeartbeatSequence(ctx); err != nil {
				t.Fatal(err)
			}
			if _, err := store.db.ExecContext(ctx, damage); err != nil {
				t.Fatalf("build the inconsistent database: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}

			reopened, err := Open(ctx, path)
			if reopened != nil {
				_ = reopened.Close()
			}
			if !errors.Is(err, ErrSequenceUnavailable) {
				t.Fatalf("Open error = %v; want ErrSequenceUnavailable", err)
			}
		})
	}
}

// The marker and the counter are created together or not at all. A start that
// fails partway must not leave a marker claiming a counter that is not there,
// because that state is refused forever after.
func TestTheMarkerAndTheCounterAppearTogether(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agenthub.db")
	openAt(t, path)

	if !tableExistsInFile(t, path, "heartbeat_sequence") || !tableExistsInFile(t, path, "schema_markers") {
		t.Fatal("opening a new database did not create both the counter and its marker")
	}
	if markers := countMarkerRows(t, path); markers != 1 {
		t.Errorf("marker rows = %d, want exactly 1", markers)
	}
}

func directDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open the database directly: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func tableExistsInFile(t *testing.T, path, name string) bool {
	t.Helper()
	var count int
	if err := directDB(t, path).QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&count); err != nil {
		t.Fatalf("look for table %q: %v", name, err)
	}
	return count > 0
}

func countMarkerRows(t *testing.T, path string) int {
	t.Helper()
	var rows int
	if err := directDB(t, path).QueryRow(
		`SELECT count(*) FROM schema_markers WHERE name = 'heartbeat_sequence'`).Scan(&rows); err != nil {
		t.Fatalf("count marker rows: %v", err)
	}
	return rows
}
