package wake

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"agenthub.local/agenthub/internal/model"
)

// ChannelDriver wakes a Claude Code session through a subscriber.
//
// Unlike Codex, this node cannot reach a Claude Code session at all. The MCP
// server that serves it is a stdio child of the agent, and it talks to this
// node over loopback HTTP — outbound only, request and response. Nothing here
// can open a connection to it.
//
// So the direction is inverted: the MCP server subscribes, and this driver
// hands a message to whoever is waiting. That inversion is also what answers
// the question the gate cannot otherwise answer for Claude — whether anybody
// is there. A session with no subscriber has no agent running, and the drive
// fails, which leaves the message in the inbox for whenever one starts.
type ChannelDriver struct {
	mu      sync.Mutex
	waiting map[string]chan Envelope
	// handoff bounds how long a driver waits for a subscriber to take the
	// envelope it is holding out.
	handoff time.Duration
}

// NewChannelDriver returns a driver with no subscribers.
func NewChannelDriver() *ChannelDriver {
	return &ChannelDriver{waiting: map[string]chan Envelope{}, handoff: 5 * time.Second}
}

// Provider says which sessions this driver serves.
func (d *ChannelDriver) Provider() model.Provider { return model.ProviderClaude }

// ErrNoSubscriber means no agent is listening for this session.
var ErrNoSubscriber = errors.New("no agent is subscribed for this session")

// Subscribe registers a waiter and returns it with the function that removes
// it.
//
// One waiter per session, and a second replaces the first: two MCP servers
// started for one session is a mistake, and the newer is the likelier to be
// the live one. The displaced waiter is closed so its own poll ends rather
// than hanging until its deadline.
func (d *ChannelDriver) Subscribe(sessionID string) (<-chan Envelope, func()) {
	waiter := make(chan Envelope, 1)
	d.mu.Lock()
	if existing, ok := d.waiting[sessionID]; ok {
		close(existing)
	}
	d.waiting[sessionID] = waiter
	d.mu.Unlock()

	return waiter, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		// Only if it is still ours: a later Subscribe may already have
		// replaced it, and removing that one would silently unsubscribe a
		// live agent.
		if current, ok := d.waiting[sessionID]; ok && current == waiter {
			delete(d.waiting, sessionID)
			close(waiter)
		}
	}
}

// Subscribed reports whether anybody is listening for this session.
func (d *ChannelDriver) Subscribed(sessionID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.waiting[sessionID]
	return ok
}

// Drive hands the envelope to this session's subscriber.
//
// What it reports is that a subscriber took the message, and no more than
// that. A channel notification is not acknowledged: Claude Code drops one it
// cannot deliver — an unregistered channel, an organisation that has not
// enabled the feature — without telling the server anything. So a wake
// recorded here means "handed to a live agent's MCP server", which is the
// furthest this node can see, and the audit trail says so.
func (d *ChannelDriver) Drive(ctx context.Context, session model.Session, envelope Envelope) error {
	d.mu.Lock()
	waiter, ok := d.waiting[session.ID]
	d.mu.Unlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrNoSubscriber, session.ID)
	}

	handoff, cancel := context.WithTimeout(ctx, d.handoff)
	defer cancel()
	select {
	case waiter <- envelope:
		return nil
	case <-handoff.Done():
		// The subscriber is registered but not collecting. Its poll may have
		// ended between the lookup and now, or the agent may be wedged. Either
		// way the message stays in the inbox.
		return fmt.Errorf("the subscriber for %s did not take the message within %s",
			session.ID, d.handoff)
	}
}
