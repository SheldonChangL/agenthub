package codexapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
)

// turnTracker remembers which turns the server has said are over.
//
// It exists because a turn is not request/response: turn/start returns a turn
// id straight away and the turn itself ends later, in a notification. Anything
// that needs to know a thread is free again — and #101's serialisation is
// exactly that — has no other signal to read.
//
// Completions are remembered, not just signalled, because the notification can
// arrive before the caller has the turn id to wait on. Both frames come off
// one reader goroutine and nothing orders the response ahead of the
// notification, so a tracker that only closed live waiters would strand every
// caller that lost that race.
type turnTracker struct {
	mu       sync.Mutex
	waiters  map[string]*turnWaiter
	finished map[string]struct{}
	// order is finished turn ids oldest first, so the memory stays bounded on
	// a connection that lives for days.
	order []string
}

// turnWaiter is the channel one turn's waiters are released on, with a count
// of how many are on it.
//
// Counted because one turn id can have several waiters and they share the
// channel: without the count, the first of them to give up would delete the
// entry and the others would never be released by complete() at all.
type turnWaiter struct {
	ready   chan struct{}
	waiting int
}

// turnsRemembered is how many finished turns are kept. Turns are serialised
// per thread and a node has few threads, so this is generous for the only
// thing it has to cover: a completion seen before its own turn/start response.
const turnsRemembered = 64

func (t *turnTracker) complete(turnID string) {
	if turnID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished == nil {
		t.finished = map[string]struct{}{}
	}
	if _, already := t.finished[turnID]; already {
		return
	}
	t.finished[turnID] = struct{}{}
	t.order = append(t.order, turnID)
	for len(t.order) > turnsRemembered {
		delete(t.finished, t.order[0])
		t.order = t.order[1:]
	}
	if waiter, waiting := t.waiters[turnID]; waiting {
		close(waiter.ready)
		delete(t.waiters, turnID)
	}
}

// wait returns the waiter whose channel is closed when the turn is over,
// already closed if it is over already. Every wait() must be matched by an
// abandon() unless the channel closed, or the waiter is never forgotten.
func (t *turnTracker) wait(turnID string) *turnWaiter {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, done := t.finished[turnID]; done {
		ready := make(chan struct{})
		close(ready)
		// Not put in the map: it is over, so nothing will ever come for it.
		return &turnWaiter{ready: ready}
	}
	if t.waiters == nil {
		t.waiters = map[string]*turnWaiter{}
	}
	if waiter, waiting := t.waiters[turnID]; waiting {
		waiter.waiting++
		return waiter
	}
	waiter := &turnWaiter{ready: make(chan struct{}), waiting: 1}
	t.waiters[turnID] = waiter
	return waiter
}

// abandon forgets a waiter that nobody is listening to any more.
//
// complete() is the only other thing that removes a waiter, and the two ways
// out of WaitForTurn that are not a completion — the caller's context ending,
// the connection dying — have as their premise that the completion is never
// coming. Leaving their channels in the map is therefore unbounded growth: one
// entry per abandoned wait for as long as the connection lives, and a node
// under a busy thread abandons one per refused message.
func (t *turnTracker) abandon(turnID string, waiter *turnWaiter) {
	t.mu.Lock()
	defer t.mu.Unlock()
	// Only if it is still the same waiter. complete() may have closed and
	// removed this one in the same instant this caller gave up, and a wait()
	// for the same turn id after that would have put a fresh one here for
	// somebody else — forgetting that one would strand them.
	current, waiting := t.waiters[turnID]
	if !waiting || current != waiter {
		return
	}
	current.waiting--
	// And only when this was the last of them: the channel is shared, so
	// dropping the entry while another caller is still selecting on it would
	// leave that caller for complete() to find nothing to close.
	if current.waiting <= 0 {
		delete(t.waiters, turnID)
	}
}

// TurnCompleted is the notification method that ends a turn.
//
// codex-cli 0.153.4's ServerNotification has no failure counterpart:
// turn/completed is the only terminal frame a turn has, so it is the only one
// worth watching. A turn that ends some other way is covered by the caller's
// own deadline and by the connection dying, not by another method name.
const TurnCompleted = "turn/completed"

// observe feeds the tracker from the notification stream. Called by the reader
// for every notification, before the caller's own OnNotify.
func (c *Client) observe(method string, params json.RawMessage) {
	if method != TurnCompleted {
		return
	}
	var payload struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		// Not worth dropping the connection over, but worth knowing about: a
		// completion this client cannot read is a thread that stays held
		// until a deadline instead of until its turn ended.
		return
	}
	c.turns.complete(payload.Turn.ID)
}

// WaitForTurn blocks until the server says this turn is over.
//
// It returns when the turn completes, when the context ends, or when the
// connection dies — never later than the caller asked, because the caller is
// holding a thread's turn slot while it waits and a wait with no floor is a
// thread nothing can ever wake again.
func (c *Client) WaitForTurn(ctx context.Context, turnID string) error {
	if turnID == "" {
		return errors.New("turn id is required")
	}
	over := c.turns.wait(turnID)
	select {
	case <-over.ready:
		return nil
	case <-c.done:
		c.turns.abandon(turnID, over)
		err := c.Err()
		if err == nil {
			err = errors.New("Codex App Server connection closed")
		}
		return fmt.Errorf("waiting for turn %q: %w", turnID, err)
	case <-ctx.Done():
		c.turns.abandon(turnID, over)
		return fmt.Errorf("waiting for turn %q: %w", turnID, ctx.Err())
	}
}
