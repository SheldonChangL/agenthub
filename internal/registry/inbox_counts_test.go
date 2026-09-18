package registry

import (
	"context"
	"fmt"
	"testing"

	"agenthub.local/agenthub/internal/model"
)

// countingSession makes one session that accepts messages, under a chosen id.
//
// acceptingSession exists already and makes exactly one, always the same id;
// the batch count is only interesting with several, so this is the plural form.
func countingSession(t *testing.T, store *Registry, id string) model.Session {
	t.Helper()
	ctx := context.Background()
	stored, err := store.UpsertSession(ctx, testSession(id))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAudience(ctx, stored.ID, model.Audience{
		Mode: model.AudienceAllPaired, AcceptMessages: true,
	}); err != nil {
		t.Fatal(err)
	}
	return stored
}

func fillInbox(t *testing.T, store *Registry, sessionID string, n int) {
	t.Helper()
	ctx := context.Background()
	for i := range n {
		if _, err := store.StoreIncomingMessage(ctx, model.Message{
			ID:                fmt.Sprintf("msg_%s_%d", sessionID, i),
			To:                sessionID,
			From:              outboxPeer,
			DestinationNodeID: testNodeID,
			Body:              "held",
		}); err != nil {
			t.Fatalf("storing message %d for %s: %v", i, sessionID, err)
		}
	}
}

// TestCountInboxesAnswersEverySessionAtOnce is the read the badge is built on.
//
// The desktop draws one per row of a list that has held a thousand sessions, so
// the shape that matters is "all of them in one answer" — a loop of CountInbox
// would satisfy every assertion here and be the thing issue #146 ruled out. The
// query is a single GROUP BY; what this pins is the answer it produces.
func TestCountInboxesAnswersEverySessionAtOnce(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)

	// Nothing stored yet. An empty map, not an error and not a nil map: the
	// caller ranges over it.
	counts, err := store.CountInboxes(ctx)
	if err != nil {
		t.Fatalf("CountInboxes on an empty node: %v", err)
	}
	if counts == nil {
		t.Fatal("CountInboxes returned a nil map; the caller ranges over it")
	}
	if len(counts) != 0 {
		t.Fatalf("CountInboxes on an empty node returned %d entries: %v", len(counts), counts)
	}

	one := countingSession(t, store, "claude:one")
	two := countingSession(t, store, "codex:session-2")
	empty := countingSession(t, store, "claude:quiet")

	fillInbox(t, store, one.ID, 1)
	fillInbox(t, store, two.ID, 7)

	counts, err = store.CountInboxes(ctx)
	if err != nil {
		t.Fatalf("CountInboxes: %v", err)
	}
	if got := counts[one.ID].Held; got != 1 {
		t.Errorf("held for %s = %d; want 1", one.ID, got)
	}
	if got := counts[two.ID].Held; got != 7 {
		t.Errorf("held for %s = %d; want 7", two.ID, got)
	}
	// Absent, not zero. The absence is the documented encoding of "empty", and
	// a row per idle session is what makes this endpoint unaffordable at a
	// thousand sessions.
	if _, present := counts[empty.ID]; present {
		t.Errorf("%s holds nothing and still appears in the counts: %v", empty.ID, counts[empty.ID])
	}
	if len(counts) != 2 {
		t.Errorf("CountInboxes returned %d entries; want only the two that hold something: %v", len(counts), counts)
	}
	// Capacity travels with every entry, so a reader never has to know the
	// bound to draw "7 of 500".
	if got := counts[one.ID].Capacity; got != MaxInboxMessages {
		t.Errorf("capacity = %d; want %d", got, MaxInboxMessages)
	}
	if counts[one.ID].Full || counts[two.ID].Full {
		t.Error("a session well under the bound is reported as full")
	}
}

// TestCountInboxesReportsAFullInboxAsFull is the half a number alone does not
// carry: an inbox at the bound is refusing mail right now, and that is a
// different thing from one that merely holds a lot.
func TestCountInboxesReportsAFullInboxAsFull(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := countingSession(t, store, "claude:full")
	fillInbox(t, store, session.ID, MaxInboxMessages)

	counts, err := store.CountInboxes(ctx)
	if err != nil {
		t.Fatalf("CountInboxes: %v", err)
	}
	count, ok := counts[session.ID]
	if !ok {
		t.Fatalf("a full inbox is missing from the counts: %v", counts)
	}
	if count.Held != MaxInboxMessages {
		t.Errorf("held = %d; want the bound of %d", count.Held, MaxInboxMessages)
	}
	if !count.Full {
		t.Error("an inbox at the bound is not reported full; new messages are being deferred and nothing says so")
	}
}

// TestCountInboxesAgreesWithCountInbox keeps the two readings of the same fact
// from drifting. The drawer reads one session, the badge reads all of them, and
// they sit on the same screen.
func TestCountInboxesAgreesWithCountInbox(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := countingSession(t, store, "claude:agree")
	fillInbox(t, store, session.ID, 4)

	single, err := store.CountInbox(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := store.CountInboxes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if batch[session.ID].Held != single {
		t.Errorf("the batch says %d and the single read says %d for the same inbox",
			batch[session.ID].Held, single)
	}

	// And a deletion moves both. Nothing here tracks reading, so the only
	// things that change a count are a take and a delete.
	if err := store.DeleteMessage(ctx, session.ID, fmt.Sprintf("msg_%s_0", session.ID)); err != nil {
		t.Fatal(err)
	}
	batch, err = store.CountInboxes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if batch[session.ID].Held != 3 {
		t.Errorf("after deleting one of four the batch says %d; want 3", batch[session.ID].Held)
	}

	// Emptied, and the session drops out of the map rather than appearing as a
	// zero.
	if _, err := store.ClearInbox(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	batch, err = store.CountInboxes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := batch[session.ID]; present {
		t.Errorf("an emptied inbox is still in the counts as %v", batch[session.ID])
	}
}
