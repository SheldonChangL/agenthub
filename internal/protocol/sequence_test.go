package protocol_test

import (
	"context"
	"database/sql"
	"math"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
)

func sequenceOf(t *testing.T, envelope protocol.Envelope) uint64 {
	t.Helper()
	payload, err := protocol.DecodePayload[protocol.HeartbeatPayload](envelope)
	if err != nil {
		t.Fatalf("decode heartbeat: %v", err)
	}
	return payload.Sequence
}

// The counter belongs to the node, not to a builder. Recreating the builder or
// restarting the process must not rewind it: a receiver that rejects a sequence
// it has already seen would have to refuse the restarted sender forever.
func TestHeartbeatSequenceSurvivesBuildersAndRestarts(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agenthub.db")
	store, err := registry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	trustPeers(t, store, peerA)
	key := newTestKeypair(t)

	first, err := heartbeatBuilder(t, store, key).Build(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	// A second builder over the same store is the same node, not a new one.
	second, err := heartbeatBuilder(t, store, key).BuildFor(ctx, time.Now(), peerA)
	if err != nil {
		t.Fatal(err)
	}
	if sequenceOf(t, second) <= sequenceOf(t, first) {
		t.Fatalf("a new builder rewound the sequence: %d then %d",
			sequenceOf(t, first), sequenceOf(t, second))
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := registry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	third, err := heartbeatBuilder(t, restarted, key).Build(ctx, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if sequenceOf(t, third) <= sequenceOf(t, second) {
		t.Errorf("restarting rewound the sequence: %d then %d",
			sequenceOf(t, second), sequenceOf(t, third))
	}
}

// One counter covers every envelope this node sends, whichever build produced
// it. Two counters would let a per-peer heartbeat repeat a number the owner
// preview already published under the same node ID and signature.
func TestHeartbeatSequenceIsSharedByEveryBuild(t *testing.T) {
	ctx := context.Background()
	store := newStore(t)
	trustPeers(t, store, peerA, peerB)
	builder := heartbeatBuilder(t, store, newTestKeypair(t))

	const rounds = 6
	var mutex sync.Mutex
	seen := map[uint64]bool{}
	record := func(envelope protocol.Envelope) {
		mutex.Lock()
		defer mutex.Unlock()
		sequence := sequenceOf(t, envelope)
		if seen[sequence] {
			t.Errorf("sequence %d was published twice", sequence)
		}
		seen[sequence] = true
	}

	var group sync.WaitGroup
	for round := range rounds {
		group.Add(1)
		go func() {
			defer group.Done()
			var envelope protocol.Envelope
			var err error
			switch round % 3 {
			case 0:
				envelope, err = builder.Build(ctx, time.Now())
			case 1:
				envelope, err = builder.BuildFor(ctx, time.Now(), peerA)
			default:
				envelope, err = builder.BuildFor(ctx, time.Now(), peerB)
			}
			if err != nil {
				t.Errorf("build: %v", err)
				return
			}
			record(envelope)
		}()
	}
	group.Wait()
	if len(seen) != rounds {
		t.Errorf("got %d distinct sequences from %d builds", len(seen), rounds)
	}
}

// A sequence that cannot be allocated must stop the heartbeat. Publishing one
// without a fresh number would hand a receiver a snapshot it cannot order
// against the ones it already holds.
func TestHeartbeatIsNotBuiltWithoutASequence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agenthub.db")
	store, err := registry.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	trustPeers(t, store, peerA)

	// Exhaust the counter the way only time could: through a second connection
	// to the same file, because the store deliberately exposes no way to move it.
	exhaust(t, path)

	builder := heartbeatBuilder(t, store, newTestKeypair(t))
	for name, build := range map[string]func() (protocol.Envelope, error){
		"owner preview": func() (protocol.Envelope, error) { return builder.Build(ctx, time.Now()) },
		"per-peer":      func() (protocol.Envelope, error) { return builder.BuildFor(ctx, time.Now(), peerA) },
	} {
		t.Run(name, func(t *testing.T) {
			envelope, err := build()
			if err == nil {
				t.Fatal("a heartbeat was built without a usable sequence")
			}
			if envelope.Signature != "" || envelope.Payload != nil {
				t.Errorf("a failed build produced an envelope: %+v", envelope)
			}
		})
	}
}

func exhaust(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open the database directly: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(
		`UPDATE heartbeat_sequence SET last_value = ? WHERE singleton = 1`, int64(math.MaxInt64)); err != nil {
		t.Fatalf("exhaust the counter: %v", err)
	}
}
