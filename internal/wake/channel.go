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
	waiting map[string]*subscriber
	// handoff bounds how long a driver waits for a subscriber to take the
	// envelope it is holding out.
	handoff time.Duration
	// ackWait bounds how long it then waits to be told whether the taker got
	// it out. Longer than handoff, and for a different reason: taking the
	// envelope is instant, and writing it is not.
	ackWait time.Duration
}

// subscriber is one poll waiting for its session's next message.
//
// Two channels rather than one, and the envelope channel is never closed.
//
// A buffered channel that ended a subscription by closing it had two faults
// that a review found and a test would not have: a Drive already committed to
// sending panicked on the closed channel and took the whole node with it, and
// a message sitting in the buffer when the subscription ended was recorded as
// a wake nobody could ever read. Unbuffered means Drive returning nil is a
// reader having taken it, which is what the audit trail claims; done is what
// ends the wait, and closing it cannot race a send.
type subscriber struct {
	envelopes chan Envelope
	// acks carries what the taker made of the envelope. Buffered by one so a
	// taker reporting an outcome never blocks on a Drive that has already
	// given up, and so the report cannot be lost to the close that follows it.
	acks chan error
	done chan struct{}
	once sync.Once
}

func (s *subscriber) end() { s.once.Do(func() { close(s.done) }) }

// Handoff is how long Drive waits for a subscriber to take the envelope.
//
// Short on purpose: a registered poll that is not collecting is an agent that
// has stopped reading, and the message is better left in the inbox than held
// against a subscriber that will not answer.
const Handoff = 5 * time.Second

// AckWait is how long Drive waits to be told the taker got the message out.
//
// It has to exceed the write deadline of the listener the taker answers on,
// because that deadline is the longest a write can legitimately block: a
// stalled reader holds the flush until the server gives up on it. Under that,
// a message still on its way is settled as one the agent never got — the
// opposite falsehood to the one the acknowledgement was added to stop, in the
// same row.
//
// Exported so the relationship can be asserted where both numbers are
// visible, which is the node's own package. As two literals in two packages
// they drift, and the drift is silent in both directions.
const AckWait = 90 * time.Second

// NewChannelDriver returns a driver with no subscribers.
func NewChannelDriver() *ChannelDriver {
	return &ChannelDriver{
		waiting: map[string]*subscriber{},
		handoff: Handoff,
		ackWait: AckWait,
	}
}

// Provider says which sessions this driver serves.
func (d *ChannelDriver) Provider() model.Provider { return model.ProviderClaude }

// ErrNoSubscriber means no agent is listening for this session.
var ErrNoSubscriber = errors.New("no agent is subscribed for this session")

// ErrSubscriptionEnded means the subscriber went away mid-handoff.
var ErrSubscriptionEnded = errors.New("the subscriber went away before taking the message")

// Subscription is one registered poll.
type Subscription struct {
	// Messages yields at most one envelope, when a Drive hands one over.
	Messages <-chan Envelope
	// Done closes when this subscription ends, whether because the poll
	// finished or because a later one displaced it.
	Done <-chan struct{}
	// Close ends the subscription. Safe to call more than once.
	Close func()
	// Ack reports what became of the envelope taken from Messages: nil if it
	// reached the agent's MCP server, an error if it did not. Drive does not
	// return until this is called or AckWait runs out, so a taker that
	// received an envelope owes exactly one call. Later calls are ignored.
	Ack func(error)
}

// Subscribe registers a waiter for a session.
//
// One waiter per session, and a second replaces the first: two MCP servers
// started for one session is a mistake, and the newer is the likelier to be
// the live one. The displaced one is ended so its poll returns at once rather
// than waiting out its deadline.
func (d *ChannelDriver) Subscribe(sessionID string) Subscription {
	fresh := &subscriber{
		envelopes: make(chan Envelope),
		acks:      make(chan error, 1),
		done:      make(chan struct{}),
	}
	d.mu.Lock()
	if existing, ok := d.waiting[sessionID]; ok {
		existing.end()
	}
	d.waiting[sessionID] = fresh
	d.mu.Unlock()

	return Subscription{
		Messages: fresh.envelopes,
		Done:     fresh.done,
		Close: func() {
			d.mu.Lock()
			// Only if it is still ours: a later Subscribe may already have
			// replaced it, and removing that one would silently unsubscribe a
			// live agent.
			if current, ok := d.waiting[sessionID]; ok && current == fresh {
				delete(d.waiting, sessionID)
			}
			d.mu.Unlock()
			fresh.end()
		},
		Ack: func(err error) {
			// Non-blocking: the buffer holds the first report and a taker
			// that calls twice, or calls after Drive gave up, is not stuck.
			select {
			case fresh.acks <- err:
			default:
			}
		},
	}
}

// Waiting reports how many subscriptions are live, for a bound on them.
func (d *ChannelDriver) Waiting() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.waiting)
}

// Drive hands the envelope to this session's subscriber.
//
// What it reports is that a poll took the message, and no more than that. A
// channel notification is not acknowledged: Claude Code drops one it cannot
// deliver — an unregistered channel, an organisation that has not enabled the
// feature — without telling the server anything. So a wake recorded here means
// "taken by a live agent's MCP server", which is the furthest this node can
// see, and the audit trail says so.
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
	case waiter.envelopes <- envelope:
	case <-waiter.done:
		// The poll ended between the lookup and now. The message stays in the
		// inbox for the next one.
		return fmt.Errorf("%w: %s", ErrSubscriptionEnded, session.ID)
	case <-handoff.Done():
		// Registered but not collecting: the agent may be wedged, or its poll
		// may be between reading and writing. Either way the message stays.
		return fmt.Errorf("the subscriber for %s did not take the message within %s",
			session.ID, d.handoff)
	}

	// Taken off the channel is not the same as sent. The taker is this node's
	// own HTTP handler, and its write can fail after the receive: the client
	// disconnects, or the connection deadline fires. Returning nil there
	// recorded a wake as woken that no MCP server ever received — permanently,
	// because only a settled failure stops counting against the limits, and a
	// wake nobody settles counts against all three windows for its whole life.
	//
	// So the outcome is what the taker reports, not what the receive implies.
	// Not selected against done: the taker acks before it closes, the ack is
	// buffered, and selecting on both would pick the close half the time on a
	// delivery that worked. What that costs: a taker that dies between the
	// receive and the ack — a panic in the handler, which net/http recovers —
	// holds this goroutine and leaves the row unsettled for the whole of
	// AckWait, ninety seconds, rather than the five it used to be.
	//
	// On its own timer, not the handoff's. The taker's write can block on a
	// reader that has stopped reading until the server's own write deadline
	// — 60s on the owner listener — and ending the wait at the handoff's five
	// seconds would settle a message as failed while it was still on its way,
	// writing the opposite falsehood into the same row.
	acknowledged, stopWaiting := context.WithTimeout(ctx, d.ackWait)
	defer stopWaiting()
	select {
	case err := <-waiter.acks:
		return err
	case <-acknowledged.Done():
		return fmt.Errorf("the subscriber for %s took the message but did not say "+
			"whether it was sent within %s", session.ID, d.ackWait)
	}
}
