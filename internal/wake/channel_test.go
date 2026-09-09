package wake

import (
	"context"
	"errors"
	"sync"
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
// record a wake that could not have happened.
func TestDrivingASessionNobodyIsListeningForFails(t *testing.T) {
	driver := NewChannelDriver()
	if driver.Waiting() != 0 {
		t.Error("a fresh driver reports a subscriber")
	}
	err := driver.Drive(context.Background(), claudeSession(), Envelope{MessageID: "msg_1"})
	if !errors.Is(err, ErrNoSubscriber) {
		t.Fatalf("Drive() error = %v, want ErrNoSubscriber", err)
	}
}

// A subscriber receives the envelope, and Drive returns only once it has.
//
// The handoff is unbuffered on purpose: a buffered one made Drive returning
// nil mean "queued", so a message could be recorded as a wake and then thrown
// away with the channel it was sitting in.
func TestASubscriberReceivesTheEnvelope(t *testing.T) {
	driver := NewChannelDriver()
	subscription := driver.Subscribe("claude:target")
	defer subscription.Close()
	if driver.Waiting() != 1 {
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
	case got := <-subscription.Messages:
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

// Closing takes the session out of reach again.
func TestClosingEndsTheSubscription(t *testing.T) {
	driver := NewChannelDriver()
	subscription := driver.Subscribe("claude:target")
	subscription.Close()

	if driver.Waiting() != 0 {
		t.Error("the session still has a subscriber after closing")
	}
	if err := driver.Drive(context.Background(), claudeSession(), Envelope{MessageID: "msg_1"}); !errors.Is(err, ErrNoSubscriber) {
		t.Errorf("Drive() error = %v, want ErrNoSubscriber", err)
	}
}

// A second subscriber for one session replaces the first, and the displaced
// one is told rather than left hanging until its deadline.
func TestASecondSubscriberReplacesTheFirst(t *testing.T) {
	driver := NewChannelDriver()
	first := driver.Subscribe("claude:target")
	second := driver.Subscribe("claude:target")
	defer second.Close()

	select {
	case <-first.Done:
	case <-time.After(5 * time.Second):
		t.Fatal("the displaced subscriber was left hanging; its poll would wait out its deadline")
	}

	go func() { _ = driver.Drive(context.Background(), claudeSession(), Envelope{MessageID: "msg_1"}) }()
	select {
	case got := <-second.Messages:
		if got.MessageID != "msg_1" {
			t.Errorf("the live subscriber received %+v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the live subscriber received nothing")
	}
}

// Closing a subscription that has already been replaced must not unsubscribe
// its replacement.
func TestAStaleCloseLeavesTheLiveSubscriberAlone(t *testing.T) {
	driver := NewChannelDriver()
	first := driver.Subscribe("claude:target")
	second := driver.Subscribe("claude:target")
	defer second.Close()

	first.Close()

	if driver.Waiting() != 1 {
		t.Error("a stale close removed the live subscriber; the session became " +
			"unreachable while an agent was still listening")
	}
}

// A registered subscriber that is not collecting does not hold a wake open for
// ever.
func TestASubscriberThatDoesNotCollectTimesOut(t *testing.T) {
	driver := NewChannelDriver()
	driver.handoff = 50 * time.Millisecond
	subscription := driver.Subscribe("claude:target")
	defer subscription.Close()

	started := time.Now()
	err := driver.Drive(context.Background(), claudeSession(), Envelope{MessageID: "msg_1"})
	if err == nil {
		t.Fatal("a subscriber that never collects took the message")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("the handoff waited %s", elapsed)
	}
}

// A subscription that ends mid-handoff fails the drive rather than crashing.
//
// The driver used to close the channel a Drive might be sending on, and a send
// on a closed channel panics — on the bare goroutine the wake runs on, with no
// recover anywhere, so it took the whole node down. A peer sending as a poll
// turned over was enough, and a poll turns over every half minute.
func TestASubscriptionEndingMidHandoffFailsRatherThanPanics(t *testing.T) {
	driver := NewChannelDriver()
	driver.handoff = 2 * time.Second
	subscription := driver.Subscribe("claude:target")

	failed := make(chan error, 1)
	go func() {
		failed <- driver.Drive(context.Background(), claudeSession(), Envelope{MessageID: "msg_1"})
	}()
	// Let the drive commit to the send, then pull the subscription out from
	// under it.
	time.Sleep(100 * time.Millisecond)
	subscription.Close()

	select {
	case err := <-failed:
		if !errors.Is(err, ErrSubscriptionEnded) {
			t.Errorf("Drive() error = %v, want ErrSubscriptionEnded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the drive never noticed the subscription ending")
	}
}

// A subscription displaced mid-handoff fails the drive rather than crashing.
//
// The Close path and the displacement path both end a subscription, and they
// are different lines. A test covering one says nothing about the other:
// reinstating the crash in Subscribe's displacement branch passed the whole
// suite, including the stress test below, whose comment claimed to catch it.
func TestADisplacementMidHandoffFailsRatherThanPanics(t *testing.T) {
	driver := NewChannelDriver()
	driver.handoff = 2 * time.Second
	first := driver.Subscribe("claude:target")

	failed := make(chan error, 1)
	go func() {
		failed <- driver.Drive(context.Background(), claudeSession(), Envelope{MessageID: "msg_1"})
	}()
	// Let the drive commit to the send, then displace the subscriber it is
	// sending to — which is what an agent's MCP server restarting does.
	time.Sleep(100 * time.Millisecond)
	second := driver.Subscribe("claude:target")
	defer second.Close()
	_ = first

	select {
	case err := <-failed:
		if !errors.Is(err, ErrSubscriptionEnded) {
			t.Errorf("Drive() error = %v, want ErrSubscriptionEnded", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the drive never noticed the displacement")
	}
}

// The same, driven hard: closing and displacing while sends are in flight.
//
// Displacement is what the loop below produces, and it did not before: it
// closed each subscription before taking the next, so the map entry was always
// gone and Subscribe never displaced anything. Instrumented, it reached the
// branch zero times in five hundred. Now the close is deferred past the next
// Subscribe, so most iterations displace.
//
// Run under -race, this is what catches a send racing either way of ending a
// subscription.
func TestTheHandoffSurvivesSubscribersComingAndGoing(t *testing.T) {
	driver := NewChannelDriver()
	driver.handoff = 20 * time.Millisecond

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_ = driver.Drive(context.Background(), claudeSession(), Envelope{MessageID: "msg"})
			}
		}()
	}
	var previous *Subscription
	displacements := 0
	for range 500 {
		subscription := driver.Subscribe("claude:target")
		if previous != nil {
			// The old one is still registered when the new one arrives, so
			// this Subscribe displaced it.
			displacements++
			previous.Close()
		}
		select {
		case <-subscription.Messages:
		default:
		}
		held := subscription
		previous = &held
	}
	if previous != nil {
		previous.Close()
	}
	close(stop)
	wg.Wait()

	if displacements < 400 {
		t.Errorf("only %d of 500 iterations displaced a live subscriber; the loop is not "+
			"exercising the branch it exists for", displacements)
	}
}

// Waiting counts what is held, so the endpoint can refuse to hold an unbounded
// number of them.
func TestWaitingCountsLiveSubscriptions(t *testing.T) {
	driver := NewChannelDriver()
	if driver.Waiting() != 0 {
		t.Fatalf("a fresh driver is holding %d", driver.Waiting())
	}
	first := driver.Subscribe("claude:one")
	second := driver.Subscribe("claude:two")
	if driver.Waiting() != 2 {
		t.Errorf("Waiting() = %d, want 2", driver.Waiting())
	}
	// A replacement is not an addition.
	third := driver.Subscribe("claude:two")
	if driver.Waiting() != 2 {
		t.Errorf("Waiting() = %d after a replacement, want 2", driver.Waiting())
	}
	first.Close()
	second.Close()
	third.Close()
	if driver.Waiting() != 0 {
		t.Errorf("Waiting() = %d after everything closed", driver.Waiting())
	}
}
