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
	"fmt"
	"strings"

	"agenthub.local/agenthub/internal/codexapp"
	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/wake"
)

// Driver drives Codex threads.
type Driver struct {
	supervisor *codexapp.Supervisor
}

// New returns a driver over a supervisor.
func New(supervisor *codexapp.Supervisor) *Driver {
	return &Driver{supervisor: supervisor}
}

// Provider says which sessions this driver serves.
func (d *Driver) Provider() model.Provider { return model.ProviderCodex }

// Drive resumes the thread and sends it one turn.
func (d *Driver) Drive(ctx context.Context, session model.Session, envelope wake.Envelope) error {
	if session.ProviderSessionID == "" {
		return fmt.Errorf("session %q has no Codex thread id", session.ID)
	}
	client, err := d.supervisor.Client(ctx)
	if err != nil {
		return err
	}
	// Resume first, always, even for a thread that looks running. It is how a
	// running thread is rejoined rather than a second one being started beside
	// the conversation the owner is actually watching.
	if _, err := client.ResumeThread(ctx, session.ProviderSessionID); err != nil {
		return fmt.Errorf("resume thread %q: %w", session.ProviderSessionID, err)
	}
	if _, err := client.StartTurn(ctx, codexapp.StartTurnParams{
		ThreadID:    session.ProviderSessionID,
		Input:       []codexapp.TurnInput{{Type: "text", Text: prompt(envelope)}},
		TurnTrigger: TurnTrigger,
		AdditionalContext: map[string]codexapp.ContextEntry{
			ContextKey: {Kind: codexapp.ContextUntrusted, Value: envelope.Body},
		},
	}); err != nil {
		return fmt.Errorf("start a turn in thread %q: %w", session.ProviderSessionID, err)
	}
	return nil
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
