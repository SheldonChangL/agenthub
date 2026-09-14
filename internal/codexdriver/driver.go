// Package codexdriver starts a turn in a Codex thread when a message arrives
// for it.
//
// It goes through the app-server's own API — thread/resume then turn/start —
// and never writes into Codex's files or its process. That boundary (#16) is
// the reason waking a Codex session is possible at all without the project
// having to become something it refused to be.
package codexdriver

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"agenthub.local/agenthub/internal/codexapp"
	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/wake"
)

// Conversation is the part of a Codex connection this driver uses.
//
// An interface, not the concrete client, because everything this package
// claims lives in which calls it makes and with what — resume before start,
// the body as untrusted context, the trigger recorded. With a concrete
// dependency none of that could be tested without a live app-server, and
// mutation testing found the consequence: deleting the resume, deleting the
// untrusted marking, and flipping the kind to "application" all passed.
type Conversation interface {
	ResumeThread(ctx context.Context, threadID string) (codexapp.ResumeResult, error)
	StartTurn(ctx context.Context, params codexapp.StartTurnParams) (codexapp.TurnResult, error)
	// WaitForTurn blocks until the server says the turn is over. It is what
	// tells this driver a thread is free for the next message; without it
	// "one turn at a time" could only be guessed at from timing.
	WaitForTurn(ctx context.Context, turnID string) error
}

// Connector hands out a connection, opening one if there is not one.
type Connector interface {
	Conversation(ctx context.Context) (Conversation, error)
}

// Driver drives Codex threads.
//
// One turn at a time per thread (#101). Not because concurrent turns race —
// app-server serialises them itself — but because of how it serialises them:
// messages that arrive while a turn is running are appended to that same turn,
// and messages that arrive together are merged into one model call which
// answers only the last of them. Measured against codex-cli 0.153.4, one of
// three near-simultaneous messages got no reply at all, and all three were
// recorded here as three wakes against a single turn on Codex's side.
//
// So the second message waits here instead. Waiting, not refusing: app-server
// has already guaranteed nothing runs in parallel, so there is no correctness
// argument for throwing away a message that can be delivered safely a moment
// later — and a message that waits gets its own turn and its own reply, which
// is what the audit trail has been claiming all along.
type Driver struct {
	connect Connector

	// maxWait bounds how long one message waits for the turn ahead of it.
	maxWait time.Duration
	// maxTurn bounds how long a thread stays held for a turn that never
	// reports completing.
	maxTurn time.Duration
	// queueDepth is how many messages may wait behind the turn in flight
	// before the rest are left in the inbox.
	queueDepth int

	mu    sync.Mutex
	lanes map[string]*lane
}

// MaxWait is how long a message waits for the thread ahead of it.
//
// Under the two minutes the API layer gives a whole wake, so the reason an
// owner reads is this driver's — "waited 90s for the turn in flight" — rather
// than a bare context deadline that says nothing about what was waited for. A
// turn longer than this is a turn whose queue belongs in the inbox: the
// message is still there to read, and a reader who has been busy for two
// minutes is not served by a wake queued behind a wake.
//
// Exported because that "under" is a coupling between two packages and not a
// coincidence: what is left of the wake's budget after this wait is what
// thread/resume and turn/start have to finish in. internal/api pins the
// arithmetic, the way it already pins the channel driver's.
const MaxWait = 90 * time.Second

// defaultMaxTurn is the backstop on holding a thread for a turn that never
// reports completing. A turn really can run for a long time; this is not a
// timeout on the turn, only on this node's belief that it is still running.
const defaultMaxTurn = 30 * time.Minute

// defaultQueueDepth is how many messages may wait behind a running turn.
//
// Two, which with the one in flight makes three — the same number as the
// per-pair rate limit, and for the same reason: past that, what is arriving is
// not a conversation this node should keep feeding into one agent.
const defaultQueueDepth = 2

// New returns a driver over a supervisor.
func New(supervisor *codexapp.Supervisor) *Driver {
	return NewWith(supervised{supervisor})
}

// NewWith returns a driver over any connector, for tests.
func NewWith(connector Connector) *Driver {
	return &Driver{
		connect:    connector,
		maxWait:    MaxWait,
		maxTurn:    defaultMaxTurn,
		queueDepth: defaultQueueDepth,
		lanes:      map[string]*lane{},
	}
}

// lane returns the thread's lane, making one if this is its first message.
//
// Kept once made, rather than reaped when the thread goes quiet. A lane is a
// mutex, a short slice and a turn id; there is one per Codex thread this node
// has ever woken, which is a number bounded by the owner's own sessions. And
// the turn id has to survive the quiet: a turn id that comes back is how a
// merged turn is recognised, and a lane reaped between two messages would
// forget the one thing worth remembering about the first.
func (d *Driver) lane(threadID string) *lane {
	d.mu.Lock()
	defer d.mu.Unlock()
	existing, ok := d.lanes[threadID]
	if !ok {
		existing = &lane{}
		d.lanes[threadID] = existing
	}
	return existing
}

// supervised adapts a Supervisor to Connector.
type supervised struct{ supervisor *codexapp.Supervisor }

func (s supervised) Conversation(ctx context.Context) (Conversation, error) {
	return s.supervisor.Client(ctx)
}

// Provider says which sessions this driver serves.
func (d *Driver) Provider() model.Provider { return model.ProviderCodex }

// Drive resumes the thread and sends it one turn.
//
// It returns once the turn has been started, not once it has finished: a wake
// is reported on how it was handed over, and a model turn can outlast any
// deadline the caller could reasonably give this. The thread stays held after
// the return, by a goroutine waiting for turn/completed, which is what the
// next message queues behind.
func (d *Driver) Drive(ctx context.Context, session model.Session, envelope wake.Envelope) error {
	if session.ProviderSessionID == "" {
		return fmt.Errorf("session %q has no Codex thread id", session.ID)
	}
	// The thread, not the session, is what app-server serialises, and two
	// sessions can name one thread. Keying on the thread makes the two cases
	// the same case.
	threadID := session.ProviderSessionID
	held := d.lane(threadID)
	if err := held.enter(ctx, d.queueDepth, d.maxWait); err != nil {
		return fmt.Errorf("thread %q: %w", threadID, err)
	}

	client, turnID, sent, err := d.start(ctx, threadID, envelope)
	if err != nil {
		d.releaseAfterFailure(threadID, held, sent, err)
		return err
	}
	repeated := held.recordTurn(turnID)
	// Held until the turn ends, whatever else is reported about it: even a
	// merged turn is a turn running in this thread, and the next message must
	// not be pushed into it either.
	// #nosec G118 -- deliberately not the caller's context: the wait outlives
	// the wake that started it by design. A wake is reported on the handover
	// and its context ends there; the turn runs for as long as the model runs,
	// and cancelling this wait with the wake would free the thread while the
	// turn was still in it, which is the whole bug.
	go d.holdUntilOver(threadID, held, client, turnID)
	if repeated {
		return fmt.Errorf("thread %q: %w (turn %s)", threadID, ErrTurnCoalesced, turnID)
	}
	return nil
}

// releaseAfterFailure gives the lane back after a turn that did not start —
// at once when it is known no turn started, at the backstop when it is not.
//
// The two costs are not the same size. Holding a thread that has no turn in it
// costs one message left in the inbox, where it is readable and where an audit
// row says why it stayed: waking was only ever an extra push. Releasing a
// thread that does have a turn in it costs a message app-server merges into
// that turn and may never answer — and the merge is invisible from here,
// because the turn id that would have shown it as repeated is the very thing
// the failure lost. So the ambiguous case holds.
//
// Ambiguous is the default, not the exception. Only two answers rule a turn
// out: the server refusing the call (it answered, so it took nothing) and the
// call never leaving this process. A deadline, a connection that went away
// mid-call, a reply this node could not read — each of those is a turn that
// may be running in a thread nobody here is watching.
func (d *Driver) releaseAfterFailure(threadID string, held *lane, sent bool, err error) {
	if !sent || !answerLost(err) {
		held.leave()
		return
	}
	log.Printf("codexdriver: turn/start into thread %q failed without an answer (%v); "+
		"holding the thread for up to %s in case app-server took the turn anyway",
		threadID, err, d.maxTurn)
	// The same bound as a turn whose completion never comes, for the same
	// reason: this node believes a turn may be running and has nothing to read
	// that would end that belief. time.AfterFunc rather than a goroutine —
	// there is nothing to wait on, only a deadline to reach.
	time.AfterFunc(d.maxTurn, held.leave)
}

// answerLost reports whether a failed turn/start leaves it unknown what
// app-server did with it.
func answerLost(err error) bool {
	var refused codexapp.ServerError
	if errors.As(err, &refused) {
		// The server answered, and its answer was no.
		return false
	}
	// Never written, so there is nothing for the server to have acted on.
	return !errors.Is(err, codexapp.ErrNotSent)
}

// start does the two calls, and reports the turn app-server says they made.
//
// sent says whether turn/start was dispatched at all, which is what separates
// "no turn can exist" from "a turn may exist and its id was lost".
func (d *Driver) start(ctx context.Context, threadID string, envelope wake.Envelope) (client Conversation, turnID string, sent bool, err error) {
	client, err = d.connect.Conversation(ctx)
	if err != nil {
		return nil, "", false, err
	}
	// Resume first, always, even for a thread that looks running. It is how a
	// running thread is rejoined rather than a second one being started beside
	// the conversation the owner is actually watching.
	if _, err := client.ResumeThread(ctx, threadID); err != nil {
		return nil, "", false, fmt.Errorf("resume thread %q: %w", threadID, err)
	}
	// The turn id is kept, where it used to be dropped. It is the only handle
	// on "this turn is over", and the only way to notice app-server answering
	// with a turn that was already running.
	result, err := client.StartTurn(ctx, codexapp.StartTurnParams{
		ThreadID:    threadID,
		Input:       []codexapp.TurnInput{{Type: "text", Text: prompt(envelope)}},
		TurnTrigger: TurnTrigger,
		AdditionalContext: map[string]codexapp.ContextEntry{
			ContextKey: {Kind: codexapp.ContextUntrusted, Value: envelope.Body},
		},
	})
	if err != nil {
		// sent: the request went out, and whether a turn came of it is exactly
		// what this error is unable to say.
		return nil, "", true, fmt.Errorf("start a turn in thread %q: %w", threadID, err)
	}
	// The connection is returned with the turn id, not looked up again later:
	// the wait belongs to the client the turn was started on, and by the time
	// it ends the supervisor may be handing out a different one.
	return client, result.Turn.ID, true, nil
}

// holdUntilOver keeps the thread's lane until its turn reports completing.
//
// Every way out of the wait releases the lane. A connection that died, a turn
// that never reported, a node that took longer than the backstop — none of
// those is a reason for a thread to stay unwakeable for the rest of this
// process's life. The cost of releasing early is the coalescing this whole
// mechanism avoids; the cost of not releasing is a session that is never woken
// again, which is worse and silent.
func (d *Driver) holdUntilOver(threadID string, held *lane, client Conversation, turnID string) {
	defer held.leave()
	if turnID == "" || client == nil {
		// Nothing to wait on. app-server names every turn it starts, so this
		// is a server that answered without one; holding the thread on that
		// basis would be holding it on nothing.
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), d.maxTurn)
	defer cancel()
	if err := client.WaitForTurn(ctx, turnID); err != nil {
		log.Printf("codexdriver: stopped waiting for turn %s in thread %q: %v; "+
			"the thread is free for the next message", turnID, threadID, err)
	}
}

// TurnTrigger marks a turn as one this node started.
//
// Recorded by Codex, so an owner reading their own Codex history can see which
// turns came from here without having to take AgentHub's audit trail on trust.
const TurnTrigger = "agenthub-wake"

// ContextKey names the fragment carrying the peer's words.
const ContextKey = "agenthub:message"

// prompt is what the agent reads as its input.
//
// The body is not in here. It travels as an additionalContext entry marked
// untrusted, which is Codex's own notion and a stronger barrier than any
// sentence this function could write: the two never share a string, so no part
// of a message can be read as part of the instruction around it.
//
// What is here is who sent it and what that is worth. The fingerprint
// especially: it is the one thing about a sender a person can check out of
// band, and this turn has no person in it to ask for one.
func prompt(envelope wake.Envelope) string {
	var builder strings.Builder
	builder.WriteString(wake.Notice)
	builder.WriteString("\n\nThe message is in the untrusted context fragment ")
	builder.WriteString(ContextKey)
	builder.WriteString(".\n")
	if envelope.SenderNodeID != "" {
		fmt.Fprintf(&builder, "Sender node: %s\n", envelope.SenderNodeID)
	}
	if envelope.Fingerprint != "" {
		fmt.Fprintf(&builder, "Sender fingerprint: %s\n", envelope.Fingerprint)
	}
	if envelope.SenderLabel != "" {
		// Quoted: a label is chosen by whoever sent it, and quoting is what
		// keeps a newline in one from forging a line of this prompt.
		fmt.Fprintf(&builder, "Sender's own label for itself: %q\n", envelope.SenderLabel)
	}
	fmt.Fprintf(&builder, "Message id: %s\n", envelope.MessageID)
	if envelope.Hops > 0 {
		fmt.Fprintf(&builder,
			"This is %d automatic wakes into an exchange, so a reply may wake the other side again.\n",
			envelope.Hops)
	}
	return builder.String()
}
