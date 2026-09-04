package registry_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/registry"
)

const cursorTestNode = "node_local000000000000"

func openWithAcceptingSession(t *testing.T) (*registry.Registry, string) {
	t.Helper()
	ctx := context.Background()
	store, err := registry.Open(ctx, filepath.Join(t.TempDir(), "inbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Now().UTC()
	const id = "claude:reader"
	if _, err := store.UpsertSession(ctx, model.Session{
		ID: id, Provider: model.ProviderClaude, ProviderSessionID: "reader",
		Management: model.Unmanaged, Status: model.StatusIdle, StatusSource: "test",
		LastSeenAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAudience(ctx, id, model.Audience{Mode: model.AudienceNone, AcceptMessages: true}); err != nil {
		t.Fatal(err)
	}
	return store, id
}

func messageIDs(messages []model.Message) string {
	ids := make([]string, len(messages))
	for i, m := range messages {
		ids[i] = m.ID
	}
	return strings.Join(ids, ",")
}

// A page is defined by what sorts after the last message read, not by how many
// rows precede it. An offset skips a message when one before it is deleted
// between pages, and repeats one when one arrives — and the reader is told
// "here is your inbox" with a message silently missing.
func TestInboxPagesByCursorAndADeletionBetweenPagesSkipsNothing(t *testing.T) {
	ctx := context.Background()
	store, id := openWithAcceptingSession(t)
	for i := 0; i < 25; i++ {
		if _, err := store.CreateMessage(ctx, model.Message{
			To: id, From: "codex:sender", DestinationNodeID: cursorTestNode, Body: fmt.Sprintf("m%02d", i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := store.Inbox(ctx, id, 200, registry.InboxStart)
	if err != nil || len(all) != 25 {
		t.Fatalf("all = %d, %v", len(all), err)
	}

	page1, err := store.Inbox(ctx, id, 10, registry.InboxStart)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := messageIDs(page1), messageIDs(all[:10]); got != want {
		t.Fatalf("page 1 = %s, want %s", got, want)
	}

	// The owner drops the second message while the reader is between pages.
	if err := store.DeleteMessage(ctx, id, all[1].ID); err != nil {
		t.Fatal(err)
	}

	page2, err := store.Inbox(ctx, id, 10, registry.CursorAfter(page1[len(page1)-1]))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := messageIDs(page2), messageIDs(all[10:20]); got != want {
		t.Fatalf("page 2 after a deletion = %s, want %s", got, want)
	}
	page3, err := store.Inbox(ctx, id, 10, registry.CursorAfter(page2[len(page2)-1]))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := messageIDs(page3), messageIDs(all[20:]); got != want {
		t.Fatalf("page 3 = %s, want %s", got, want)
	}
}

// The cursor a page hands out is what the next page takes; anything else is
// refused rather than guessed at.
func TestAnInboxCursorRoundTripsAndGarbageIsRefused(t *testing.T) {
	cursor := registry.CursorAfter(model.Message{ID: "msg_abc", CreatedAt: time.UnixMilli(1725000000123).UTC()})
	parsed, err := registry.ParseInboxCursor(cursor.String())
	if err != nil || parsed != cursor {
		t.Fatalf("round trip = %+v, %v", parsed, err)
	}
	if start, err := registry.ParseInboxCursor(""); err != nil || start != registry.InboxStart {
		t.Errorf("empty = %+v, %v; want the start", start, err)
	}
	for _, bad := range []string{
		"abc", "12", ".bXNnX2E", "12.", "-1.bXNnX2E", "12.not base64!",
		strings.Repeat("1", 30) + ".bXNnX2E", "12." + strings.Repeat("YQ", 200),
	} {
		if _, err := registry.ParseInboxCursor(bad); err == nil {
			t.Errorf("%q was accepted as a cursor", bad)
		}
	}
}

// A peer chooses the id of a message it sends, and the store admits any
// non-blank one within its bound. A cursor that could not name one of those was
// a cursor this node would emit at a page boundary and then refuse on the next
// request — the failure this whole change is about, reachable with eleven small
// messages instead of fifty large ones.
//
// The protocol layer refuses such an id on the wire now, but a row already in a
// database does not go back through it, so the cursor has to be total anyway.
func TestACursorCanNameAnyIDTheStoreHolds(t *testing.T) {
	ctx := context.Background()
	store, id := openWithAcceptingSession(t)
	awkward := []string{"msg with space", "msg_é", "msg_\ttab", "msg_\"quote\"", "msg_/slash"}
	for i, messageID := range awkward {
		if _, err := store.CreateMessage(ctx, model.Message{
			ID: messageID, To: id, From: "codex:sender", DestinationNodeID: cursorTestNode,
			Body: fmt.Sprintf("m%d", i),
		}); err != nil {
			t.Fatalf("seed %q: %v", messageID, err)
		}
	}
	seen := map[string]bool{}
	after := registry.InboxStart
	for page := 0; page < len(awkward)+1; page++ {
		messages, err := store.Inbox(ctx, id, 2, after)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(messages) == 0 {
			break
		}
		for _, m := range messages {
			if seen[m.ID] {
				t.Fatalf("message %q came back twice", m.ID)
			}
			seen[m.ID] = true
		}
		// Round-tripped through the wire form, which is what the API does.
		next := registry.CursorAfter(messages[len(messages)-1])
		parsed, err := registry.ParseInboxCursor(next.String())
		if err != nil {
			t.Fatalf("the node refused a cursor it issued for %q: %v", next.ID, err)
		}
		if parsed != next {
			t.Fatalf("cursor round trip = %+v, want %+v", parsed, next)
		}
		after = parsed
	}
	if len(seen) != len(awkward) {
		t.Errorf("saw %d of %d messages: %v", len(seen), len(awkward), seen)
	}
}

// The sender label is bounded on every write path, not only where it arrives
// from a peer: the owner's API takes a `from` too, and a bound with a way round
// it is not one.
func TestASenderLabelIsBoundedOnEveryWritePath(t *testing.T) {
	ctx := context.Background()
	store, id := openWithAcceptingSession(t)
	long := "node_peer0000000000000/claude:" + strings.Repeat("a", model.MaxSenderLabelLength)
	if _, err := store.CreateMessage(ctx, model.Message{
		To: id, From: long, DestinationNodeID: cursorTestNode, Body: "hi",
	}); err == nil {
		t.Error("CreateMessage stored an oversized sender label")
	}
	if _, err := store.StoreIncomingMessage(ctx, model.Message{
		ID: "msg_in", To: id, From: long, DestinationNodeID: cursorTestNode, Body: "hi",
	}); err == nil {
		t.Error("StoreIncomingMessage stored an oversized sender label")
	}
	// An id longer than the bound is refused on both write paths. Both, because
	// an id the store admits and the cursor cannot name is a page boundary the
	// node cannot get past — and the owner's path does not go through the
	// peer path's checks.
	longID := strings.Repeat("i", model.MaxMessageIDLength+1)
	if _, err := store.StoreIncomingMessage(ctx, model.Message{
		ID: longID, To: id, From: "codex:sender", DestinationNodeID: cursorTestNode, Body: "hi",
	}); err == nil {
		t.Error("StoreIncomingMessage stored an oversized message id")
	}
	if _, err := store.CreateMessage(ctx, model.Message{
		ID: longID, To: id, From: "codex:sender", DestinationNodeID: cursorTestNode, Body: "hi",
	}); err == nil {
		t.Error("CreateMessage stored an oversized message id")
	}
	// A sender with every part at its limit is legitimate and fits.
	atLimit := strings.Repeat("n", model.MaxNodeIDLength) + "/claude:" + strings.Repeat("s", model.MaxProviderSessionIDLength)
	if _, err := store.CreateMessage(ctx, model.Message{
		To: id, From: atLimit, DestinationNodeID: cursorTestNode, Body: "hi",
	}); err != nil {
		t.Errorf("a sender at every limit was refused: %v", err)
	}
}
