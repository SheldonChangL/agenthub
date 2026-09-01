package registry

import (
	"context"
	"errors"
	"testing"

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
