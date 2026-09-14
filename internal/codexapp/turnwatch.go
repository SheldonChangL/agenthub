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
	waiters  map[string]chan struct{}
	finished map[string]struct{}
	// order is finished turn ids oldest first, so the memory stays bounded on
	// a connection that lives for days.
	order []string
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
		close(waiter)
		delete(t.waiters, turnID)
	}
}

// wait returns a channel closed when the turn is over, already closed if it is
// over already.
func (t *turnTracker) wait(turnID string) <-chan struct{} {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, done := t.finished[turnID]; done {
		ready := make(chan struct{})
		close(ready)
		return ready
	}
	if t.waiters == nil {
		t.waiters = map[string]chan struct{}{}
	}
	if waiter, waiting := t.waiters[turnID]; waiting {
		return waiter
	}
	waiter := make(chan struct{})
	t.waiters[turnID] = waiter
	return waiter
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
	case <-over:
		return nil
	case <-c.done:
		err := c.Err()
		if err == nil {
			err = errors.New("Codex App Server connection closed")
		}
		return fmt.Errorf("waiting for turn %q: %w", turnID, err)
	case <-ctx.Done():
		return fmt.Errorf("waiting for turn %q: %w", turnID, ctx.Err())
	}
}
