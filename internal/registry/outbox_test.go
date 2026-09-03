package registry

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
)

const outboxPeer = "node_peer0000000000000"

func trustedPeer(t *testing.T, store *Registry) string {
	t.Helper()
	if err := store.TrustNode(context.Background(), peer(outboxPeer, "key-a")); err != nil {
		t.Fatal(err)
	}
	return outboxPeer
}

// TestQueueingIsNotDelivering pins the contract `ah send` depends on: a queued
// message has been recorded here and nothing else has happened.
func TestQueueingIsNotDelivering(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	node := trustedPeer(t, store)

	queued, err := store.QueueOutbound(ctx, OutboundMessage{
		DestinationNodeID: node, To: "codex:theirs", Body: "hello",
	})
	if err != nil {
		t.Fatalf("QueueOutbound() error = %v", err)
	}
	if queued.State != OutboundPending {
		t.Fatalf("state = %q; a freshly queued message is pending", queued.State)
	}
	if queued.ID == "" {
		t.Fatal("no id was assigned")
	}

	pending, err := store.PendingOutbound(ctx, node, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != queued.ID {
		t.Fatalf("pending = %#v", pending)
	}
}

// TestQueueingForAnUnpairedNodeIsRefused keeps the queue from filling with
// messages that can never be delivered.
func TestQueueingForAnUnpairedNodeIsRefused(t *testing.T) {
	store := openTestRegistry(t)
	_, err := store.QueueOutbound(context.Background(), OutboundMessage{
		DestinationNodeID: "node_neverpaired00000", To: "codex:theirs", Body: "hello",
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v; want ErrNotFound", err)
	}
}

// TestASettledMessageIsNeverRetried is what stops a delivered message becoming
// a second copy in the reader's inbox, and a refused one being re-offered.
func TestASettledMessageIsNeverRetried(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	node := trustedPeer(t, store)

	for _, terminal := range []OutboundState{OutboundDelivered, OutboundRefused} {
		queued, err := store.QueueOutbound(ctx, OutboundMessage{
			DestinationNodeID: node, To: "codex:theirs", Body: "hello",
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := store.MarkOutbound(ctx, queued.ID, terminal, "because"); err != nil {
			t.Fatalf("MarkOutbound(%q) error = %v", terminal, err)
		}

		// It must leave the pending queue...
		pending, err := store.PendingOutbound(ctx, node, 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range pending {
			if message.ID == queued.ID {
				t.Fatalf("a %s message is still pending", terminal)
			}
		}
		// ...and a second attempt to settle it must find nothing to do.
		if err := store.MarkOutbound(ctx, queued.ID, OutboundDelivered, ""); !errors.Is(err, ErrOutboundNotFound) {
			t.Errorf("re-settling a %s message = %v; want ErrOutboundNotFound", terminal, err)
		}

		settled, err := store.OutboundFor(ctx, queued.ID)
		if err != nil {
			t.Fatal(err)
		}
		if settled.State != terminal {
			t.Errorf("state = %q; want %q", settled.State, terminal)
		}
		if settled.Attempts != 1 {
			t.Errorf("attempts = %d; want the refused re-settle not to have counted", settled.Attempts)
		}
	}
}

// TestQueueRefusesUnusableMessages keeps malformed rows out of the queue rather
// than discovering them at delivery time.
func TestQueueRefusesUnusableMessages(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	node := trustedPeer(t, store)

	for name, message := range map[string]OutboundMessage{
		"no destination": {To: "codex:theirs", Body: "hello"},
		"no recipient":   {DestinationNodeID: node, Body: "hello"},
		"no body":        {DestinationNodeID: node, To: "codex:theirs"},
		"oversized body": {DestinationNodeID: node, To: "codex:theirs", Body: string(make([]byte, 32769))},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := store.QueueOutbound(ctx, message); err == nil {
				t.Fatal("an unusable message was queued")
			}
		})
	}
}

// TestOutboundForReportsAnUnknownID keeps a status lookup from inventing one.
func TestOutboundForReportsAnUnknownID(t *testing.T) {
	store := openTestRegistry(t)
	if _, err := store.OutboundFor(context.Background(), "msg_nope"); !errors.Is(err, ErrOutboundNotFound) {
		t.Fatalf("error = %v; want ErrOutboundNotFound", err)
	}
}

// TestMessageByIDFindsAStoredMessage is what makes redelivery detection
// possible on the receiving side.
func TestMessageByIDFindsAStoredMessage(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := acceptingSession(t, store)

	created, err := store.CreateMessage(ctx, model.Message{
		ID: "msg_known", To: session.ID, DestinationNodeID: testNodeID, Body: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	found, err := store.MessageByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("MessageByID() error = %v", err)
	}
	if found.Body != "hello" || found.To != session.ID {
		t.Fatalf("found = %+v", found)
	}
	if _, err := store.MessageByID(ctx, "msg_absent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("error for an absent id = %v; want ErrNotFound", err)
	}
}

// TestATransientStoreFailureIsNotARefusal is the distinction the ack statuses
// were documented as making and did not make.
//
// A recipient whose database is momentarily busy must not answer in a way the
// sender settles permanently. Here the storage layer must report the failure as
// an error rather than as a decision, so the caller can tell them apart.
func TestATransientStoreFailureIsNotARefusal(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := acceptingSession(t, store)

	// Close the database underneath to produce a failure that is nothing to do
	// with the owner's decisions.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := store.StoreIncomingMessage(ctx, model.Message{
		ID: "msg_transient", To: session.ID, From: "node_peer0000000000000",
		DestinationNodeID: testNodeID, Body: "hello",
	})
	if err == nil {
		t.Fatal("a storage failure was reported as success")
	}
	if errors.Is(err, ErrInvalidSession) || errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v; a storage failure must not look like a decision the owner made", err)
	}
}

// TestARedeliveryFromTheSameSenderIsNotStoredTwice pins the case a lost ack
// creates.
func TestARedeliveryFromTheSameSenderIsNotStoredTwice(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := acceptingSession(t, store)
	message := model.Message{
		ID: "msg_repeat", To: session.ID, From: "node_peer0000000000000",
		DestinationNodeID: testNodeID, Body: "only once",
	}

	stored, err := store.StoreIncomingMessage(ctx, message)
	if err != nil || !stored {
		t.Fatalf("first store = %v, %v", stored, err)
	}
	stored, err = store.StoreIncomingMessage(ctx, message)
	if err != nil {
		t.Fatalf("second store = %v", err)
	}
	if stored {
		t.Fatal("a redelivery was stored as a new message")
	}
	inbox, err := store.Inbox(ctx, session.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(inbox) != 1 {
		t.Fatalf("inbox holds %d copies", len(inbox))
	}
}

// TestADifferentSenderCannotTellATakenIDApart closes an oracle: an unscoped
// duplicate lookup would let a peer discover whether an id exists anywhere in
// this node's inbox, including ids from local sends and from other peers.
func TestADifferentSenderCannotTellATakenIDApart(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := acceptingSession(t, store)

	if _, err := store.StoreIncomingMessage(ctx, model.Message{
		ID: "msg_taken", To: session.ID, From: "node_peeraaaaaaaaaaaa",
		DestinationNodeID: testNodeID, Body: "first",
	}); err != nil {
		t.Fatal(err)
	}

	// A different peer reusing that id must be refused with the ordinary
	// refusal, indistinguishable from a session that declines messages.
	_, takenErr := store.StoreIncomingMessage(ctx, model.Message{
		ID: "msg_taken", To: session.ID, From: "node_peerbbbbbbbbbbbb",
		DestinationNodeID: testNodeID, Body: "second",
	})
	if !errors.Is(takenErr, ErrInvalidSession) {
		t.Fatalf("error = %v; want the ordinary refusal", takenErr)
	}

	// Which is the same class of error a declining session produces.
	declining := testSession("claude:declines")
	if _, err := store.UpsertSession(ctx, declining); err != nil {
		t.Fatal(err)
	}
	_, declineErr := store.StoreIncomingMessage(ctx, model.Message{
		ID: "msg_new", To: declining.ID, From: "node_peerbbbbbbbbbbbb",
		DestinationNodeID: testNodeID, Body: "hello",
	})
	if !errors.Is(declineErr, ErrInvalidSession) {
		t.Fatalf("declining error = %v; want ErrInvalidSession", declineErr)
	}
}

// TestAttemptsAreRecordedWhileStillPending keeps a stuck message from reading
// as though nothing had been tried.
func TestAttemptsAreRecordedWhileStillPending(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	node := trustedPeer(t, store)

	queued, err := store.QueueOutbound(ctx, OutboundMessage{
		DestinationNodeID: node, To: "codex:theirs", Body: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if err := store.RecordAttempt(ctx, queued.ID, "connection refused"); err != nil {
			t.Fatal(err)
		}
	}
	after, err := store.OutboundFor(ctx, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.State != OutboundPending {
		t.Fatalf("state = %q; recording an attempt must not settle the message", after.State)
	}
	if after.Attempts != 3 {
		t.Fatalf("attempts = %d; want 3", after.Attempts)
	}
	if after.LastError == "" {
		t.Error("a stuck message does not say why it is stuck")
	}
}

// TestAFullInboxDefersRatherThanRefuses is the decision at the centre of this
// bound.
//
// Refusing would make the sender settle the message permanently — the message
// is destroyed, which is the failure the bound exists to prevent rather than to
// cause. Dropping the oldest would destroy one silently at this end. Neither is
// acceptable for something a person wrote, so a full inbox is a distinct,
// temporary condition that the caller can leave queued.
func TestAFullInboxDefersRatherThanRefuses(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := acceptingSession(t, store)

	for i := range MaxInboxMessages {
		if _, err := store.StoreIncomingMessage(ctx, model.Message{
			ID: fmt.Sprintf("msg_fill_%d", i), To: session.ID, From: outboxPeer,
			DestinationNodeID: testNodeID, Body: "filler",
		}); err != nil {
			t.Fatalf("filling at %d: %v", i, err)
		}
	}

	_, err := store.StoreIncomingMessage(ctx, model.Message{
		ID: "msg_over", To: session.ID, From: outboxPeer,
		DestinationNodeID: testNodeID, Body: "one too many",
	})
	if !errors.Is(err, ErrInboxFull) {
		t.Fatalf("error = %v; want ErrInboxFull", err)
	}
	// It must NOT read as a decision the owner made, or the sender settles it.
	if errors.Is(err, ErrInvalidSession) || errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v; a full inbox must not look like a refusal", err)
	}

	held, err := store.CountInbox(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if held != MaxInboxMessages {
		t.Fatalf("held = %d; want the bound of %d", held, MaxInboxMessages)
	}
}

// TestClearingAnInboxLetsItReceiveAgain is what makes the bound survivable. A
// bound with no release is a session that stops receiving forever.
func TestClearingAnInboxLetsItReceiveAgain(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := acceptingSession(t, store)

	for i := range MaxInboxMessages {
		if _, err := store.StoreIncomingMessage(ctx, model.Message{
			ID: fmt.Sprintf("msg_%d", i), To: session.ID, From: outboxPeer,
			DestinationNodeID: testNodeID, Body: "filler",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.StoreIncomingMessage(ctx, model.Message{
		ID: "msg_blocked", To: session.ID, From: outboxPeer,
		DestinationNodeID: testNodeID, Body: "blocked",
	}); !errors.Is(err, ErrInboxFull) {
		t.Fatalf("the inbox did not fill: %v", err)
	}

	// Deleting one message makes room for exactly one.
	if err := store.DeleteMessage(ctx, session.ID, "msg_0"); err != nil {
		t.Fatalf("DeleteMessage() error = %v", err)
	}
	if _, err := store.StoreIncomingMessage(ctx, model.Message{
		ID: "msg_after_delete", To: session.ID, From: outboxPeer,
		DestinationNodeID: testNodeID, Body: "fits now",
	}); err != nil {
		t.Fatalf("a freed slot was not usable: %v", err)
	}

	removed, err := store.ClearInbox(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if removed != MaxInboxMessages {
		t.Fatalf("removed = %d; want the whole inbox", removed)
	}
	held, err := store.CountInbox(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if held != 0 {
		t.Fatalf("held = %d after clearing", held)
	}
}

// TestDeletingScopesToTheSession keeps one session's clear-out from reaching
// another's messages.
func TestDeletingScopesToTheSession(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	mine := acceptingSession(t, store)

	other := testSession("claude:other-inbox")
	if _, err := store.UpsertSession(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := store.SetAudience(ctx, other.ID, model.Audience{
		Mode: model.AudienceAllPaired, AcceptMessages: true,
	}); err != nil {
		t.Fatal(err)
	}
	for id, to := range map[string]string{"msg_mine": mine.ID, "msg_theirs": other.ID} {
		if _, err := store.StoreIncomingMessage(ctx, model.Message{
			ID: id, To: to, From: outboxPeer, DestinationNodeID: testNodeID, Body: "hello",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Deleting by the wrong session id must not work.
	if err := store.DeleteMessage(ctx, mine.ID, "msg_theirs"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v; a message was deletable through another session", err)
	}
	if _, err := store.ClearInbox(ctx, mine.ID); err != nil {
		t.Fatal(err)
	}
	held, err := store.CountInbox(ctx, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if held != 1 {
		t.Fatalf("the other session holds %d; clearing one inbox emptied another", held)
	}
}

// TestPruningKeepsSettledMessagesQueryableForAWhile balances the two things
// that pull against each other: ah outbound needs the row, and nothing needs it
// forever.
func TestPruningKeepsSettledMessagesQueryableForAWhile(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	node := trustedPeer(t, store)

	queued, err := store.QueueOutbound(ctx, OutboundMessage{
		DestinationNodeID: node, To: "codex:theirs", Body: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkOutbound(ctx, queued.ID, OutboundDelivered, ""); err != nil {
		t.Fatal(err)
	}

	// A long retention keeps it: the owner can still ask what happened.
	removed, err := store.PruneSettledOutbound(ctx, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("removed %d; a just-settled message must stay queryable", removed)
	}
	if _, err := store.OutboundFor(ctx, queued.ID); err != nil {
		t.Fatalf("OutboundFor() = %v", err)
	}

	// A retention shorter than its age removes it. Timestamps are stored in
	// milliseconds, so the message has to be measurably older than the cutoff
	// rather than merely not-newer.
	time.Sleep(5 * time.Millisecond)
	removed, err = store.PruneSettledOutbound(ctx, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d; want the settled message pruned", removed)
	}
	if _, err := store.OutboundFor(ctx, queued.ID); !errors.Is(err, ErrOutboundNotFound) {
		t.Errorf("error = %v; want it gone", err)
	}
}

// TestPruningNeverRemovesAPendingMessage keeps the cleanup from deleting work.
func TestPruningNeverRemovesAPendingMessage(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	node := trustedPeer(t, store)

	queued, err := store.QueueOutbound(ctx, OutboundMessage{
		DestinationNodeID: node, To: "codex:theirs", Body: "still trying",
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	removed, err := store.PruneSettledOutbound(ctx, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("removed = %d; a pending message must never be pruned", removed)
	}
	if _, err := store.OutboundFor(ctx, queued.ID); err != nil {
		t.Fatalf("the pending message is gone: %v", err)
	}
}

// TestAFullInboxStillRecognisesARedelivery is the interaction between two
// features that each looked correct alone.
//
// A sender whose ack was lost redelivers. If a full inbox answered "full" to
// that, the sender's row would stay pending forever, `ah outbound` would report
// a delivered message as undelivered, and — worse — the moment the owner read
// and cleared the inbox the id would come free, so the next round would insert
// it again and hand back a message they had already read. The recommended
// recovery action would deterministically produce a duplicate.
//
// A redelivery adds no row, so admitting it can never violate the bound.
func TestAFullInboxStillRecognisesARedelivery(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := acceptingSession(t, store)

	for i := range MaxInboxMessages {
		if _, err := store.StoreIncomingMessage(ctx, model.Message{
			ID: fmt.Sprintf("msg_%d", i), To: session.ID, From: outboxPeer,
			DestinationNodeID: testNodeID, Body: "filler",
		}); err != nil {
			t.Fatalf("filling at %d: %v", i, err)
		}
	}

	// The same sender redelivers something already held, while full.
	stored, err := store.StoreIncomingMessage(ctx, model.Message{
		ID: "msg_0", To: session.ID, From: outboxPeer,
		DestinationNodeID: testNodeID, Body: "filler",
	})
	if err != nil {
		t.Fatalf("a redelivery into a full inbox = %v; it must be recognised as a duplicate, "+
			"or the sender never settles and the owner gets a second copy after clearing", err)
	}
	if stored {
		t.Fatal("a redelivery was stored as a new message")
	}

	// A genuinely new message is still deferred.
	if _, err := store.StoreIncomingMessage(ctx, model.Message{
		ID: "msg_brand_new", To: session.ID, From: outboxPeer,
		DestinationNodeID: testNodeID, Body: "new",
	}); !errors.Is(err, ErrInboxFull) {
		t.Fatalf("a new message into a full inbox = %v; want ErrInboxFull", err)
	}

	held, err := store.CountInbox(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if held != MaxInboxMessages {
		t.Fatalf("held = %d; the redelivery changed the count", held)
	}
}

// TestTheBoundHoldsUnderConcurrentDeliveries covers the count-then-insert race
// the single statement exists to avoid.
func TestTheBoundHoldsUnderConcurrentDeliveries(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	session := acceptingSession(t, store)

	// Fill to one short of the bound.
	for i := range MaxInboxMessages - 1 {
		if _, err := store.StoreIncomingMessage(ctx, model.Message{
			ID: fmt.Sprintf("msg_%d", i), To: session.ID, From: outboxPeer,
			DestinationNodeID: testNodeID, Body: "filler",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Race several deliveries for the last slot.
	var group sync.WaitGroup
	for i := range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			_, _ = store.StoreIncomingMessage(ctx, model.Message{
				ID: fmt.Sprintf("msg_racer_%d", i), To: session.ID, From: outboxPeer,
				DestinationNodeID: testNodeID, Body: "racing",
			})
		}()
	}
	group.Wait()

	held, err := store.CountInbox(ctx, session.ID)
	if err != nil {
		t.Fatal(err)
	}
	if held > MaxInboxMessages {
		t.Fatalf("held = %d; the bound of %d was exceeded by concurrent inserts", held, MaxInboxMessages)
	}
}
