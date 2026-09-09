package wake

import (
	"context"
	"errors"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
)

func claudeSession() model.Session {
	return model.Session{
		ID: "claude:target", Provider: model.ProviderClaude, ProviderSessionID: "target",
	}
}

// A session nobody is listening for is a session with no agent running.
//
// That is the whole reason the direction is inverted here: this node cannot
// reach a Claude Code session, so the presence of a subscriber is the only
// evidence it has that one exists. Reporting success with nobody there would
// record a wake that could not have happened and lose the message from the
// owner's attention.
func TestDrivingASessionNobodyIsListeningForFails(t *testing.T) {
	driver := NewChannelDriver()
	if driver.Subscribed("claude:target") {
		t.Error("a fresh driver reports a subscriber")
	}
	err := driver.Drive(context.Background(), claudeSession(), Envelope{MessageID: "msg_1"})
	if !errors.Is(err, ErrNoSubscriber) {
		t.Fatalf("Drive() error = %v, want ErrNoSubscriber", err)
	}
}

// A subscriber receives the envelope, with everything a reader needs to judge
// it.
func TestASubscriberReceivesTheEnvelope(t *testing.T) {
	driver := NewChannelDriver()
	waiter, unsubscribe := driver.Subscribe("claude:target")
	defer unsubscribe()
	if !driver.Subscribed("claude:target") {
		t.Fatal("Subscribe did not register")
	}

	sent := Envelope{
		MessageID: "msg_1", Body: "hello", SenderNodeID: "node_peer0000000000000",
		SenderLabel: "node_peer0000000000000/claude:theirs",
		Fingerprint: "2DCF 9604 DBA9 778A", Hops: 2,
	}
	done := make(chan error, 1)
	go func() { done <- driver.Drive(context.Background(), claudeSession(), sent) }()

	select {
	case got := <-waiter:
		if got != sent {
			t.Errorf("the subscriber received %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the subscriber never received the envelope")
	}
	if err := <-done; err != nil {
		t.Errorf("Drive() error = %v", err)
	}
}

// Unsubscribing takes the session out of reach again, so a wake after an agent
// exits fails rather than being handed to a channel nobody reads.
func TestUnsubscribingEndsTheSubscription(t *testing.T) {
	driver := NewChannelDriver()
	_, unsubscribe := driver.Subscribe("claude:target")
	unsubscribe()

	if driver.Subscribed("claude:target") {
		t.Error("the session still has a subscriber after unsubscribing")
	}
	if err := driver.Drive(context.Background(), claudeSession(), Envelope{MessageID: "msg_1"}); !errors.Is(err, ErrNoSubscriber) {
		t.Errorf("Drive() error = %v, want ErrNoSubscriber", err)
	}
}

// A second subscriber for one session replaces the first, and the displaced
// one is told rather than left hanging until its deadline.
func TestASecondSubscriberReplacesTheFirst(t *testing.T) {
	driver := NewChannelDriver()
	first, _ := driver.Subscribe("claude:target")
	second, unsubscribe := driver.Subscribe("claude:target")
	defer unsubscribe()

	select {
	case _, open := <-first:
		if open {
			t.Error("the displaced subscriber received an envelope")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the displaced subscriber was left hanging; its poll would wait out its deadline")
	}

	go func() {
		_ = driver.Drive(context.Background(), claudeSession(), Envelope{MessageID: "msg_1"})
	}()
	select {
	case got := <-second:
		if got.MessageID != "msg_1" {
			t.Errorf("the live subscriber received %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the live subscriber received nothing")
	}
}

// Unsubscribing a waiter that has already been replaced must not unsubscribe
// its replacement.
func TestAStaleUnsubscribeLeavesTheLiveSubscriberAlone(t *testing.T) {
	driver := NewChannelDriver()
	_, unsubscribeFirst := driver.Subscribe("claude:target")
	_, unsubscribeSecond := driver.Subscribe("claude:target")
	defer unsubscribeSecond()

	// The first poll ends and cleans up after being displaced.
	unsubscribeFirst()

	if !driver.Subscribed("claude:target") {
		t.Error("a stale unsubscribe removed the live subscriber; the session became " +
			"unreachable while an agent was still listening")
	}
}

// A registered subscriber that is not collecting does not hold a wake open for
// ever.
func TestASubscriberThatDoesNotCollectTimesOut(t *testing.T) {
	driver := NewChannelDriver()
	driver.handoff = 50 * time.Millisecond
	// Registered, and nothing reads from it. The buffer of one is filled by
	// the first message so the second has nowhere to go.
	_, unsubscribe := driver.Subscribe("claude:target")
	defer unsubscribe()
	if err := driver.Drive(context.Background(), claudeSession(), Envelope{MessageID: "msg_1"}); err != nil {
		t.Fatalf("the first message was not buffered: %v", err)
	}

	started := time.Now()
	err := driver.Drive(context.Background(), claudeSession(), Envelope{MessageID: "msg_2"})
	if err == nil {
		t.Fatal("a subscriber that never collects took a second message")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("the handoff waited %s", elapsed)
	}
}
