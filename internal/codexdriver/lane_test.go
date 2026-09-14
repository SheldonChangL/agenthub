package codexdriver

import (
	"context"
	"errors"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/wake"
)

// settle is how long these tests give the driver to do a thing it should not
// do. Generous, because a false green here is a test that would pass with the
// serialisation deleted.
const settle = 250 * time.Millisecond

// eventually waits for a condition, and says so if it never holds.
func eventually(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The second message's turn/start is sent after the first message's turn
// completes, not beside it.
//
// This is the whole of #101. Measured against codex-cli 0.153.4: a turn/start
// into a thread that is already running does not open a second turn — the
// input is appended to the running one, and inputs that land together are
// merged into a single model call that answers only the last of them. One of
// three probe messages got no reply at all. Holding the second message here
// until turn/completed is what gives every message its own turn and its own
// answer.
func TestASecondMessageWaitsForTheTurnInFlight(t *testing.T) {
	conversation := &recordingConversation{turnIDs: []string{"turn-1", "turn-2"}}
	driver := NewWith(conversation)

	// The first message returns as soon as its turn is started — the turn
	// itself is still running, and the thread is still held.
	if err := driver.Drive(context.Background(), codexSession(), envelope("msg_1")); err != nil {
		t.Fatalf("the first message: %v", err)
	}
	if got := conversation.started(); got != 1 {
		t.Fatalf("the first message started %d turns", got)
	}

	second := make(chan error, 1)
	go func() {
		second <- driver.Drive(context.Background(), codexSession(), envelope("msg_2"))
	}()

	// Nothing the second message does may reach the thread while the first
	// turn is running.
	time.Sleep(settle)
	if got := conversation.started(); got != 1 {
		t.Fatalf("a second turn/start was sent into a thread whose turn was still "+
			"running: %d turns started, and the calls were %s", got, conversation.order())
	}
	select {
	case err := <-second:
		t.Fatalf("the second message was answered while the first turn was running: %v", err)
	default:
	}

	// The turn ends; now the second message goes.
	conversation.finish("turn-1")
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("the second message: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the turn completed and the message waiting behind it was never let through")
	}
	if got := conversation.started(); got != 2 {
		t.Fatalf("after the first turn completed, %d turns had been started", got)
	}
	if got := conversation.order(); got != "resume,start,resume,start" {
		t.Errorf("calls = %q, want each message resumed and started in turn", got)
	}
}

// Waiters are served in the order they arrived.
//
// Two messages sent one after the other should reach the agent in that order.
// Handing the thread to whichever goroutine the runtime happens to pick would
// reorder a conversation for no reason anyone could see afterwards.
func TestWaitingMessagesAreServedInArrivalOrder(t *testing.T) {
	conversation := &recordingConversation{turnIDs: []string{"turn-1", "turn-2", "turn-3"}}
	driver := NewWith(conversation)

	if err := driver.Drive(context.Background(), codexSession(), envelope("msg_1")); err != nil {
		t.Fatal(err)
	}
	for waiting, id := range []string{"msg_2", "msg_3"} {
		go func() { _ = driver.Drive(context.Background(), codexSession(), envelope(id)) }()
		// Queued one at a time, so that "arrival order" is a fact of this test
		// rather than of the scheduler.
		want := waiting + 1
		eventually(t, "message "+id+" to queue", func() bool { return driver.queued() >= want })
	}

	conversation.finish("turn-1")
	eventually(t, "the second message to start", func() bool { return conversation.started() == 2 })
	conversation.finish("turn-2")
	eventually(t, "the third message to start", func() bool { return conversation.started() == 3 })

	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	for index, want := range []string{"msg_1", "msg_2", "msg_3"} {
		if got := conversation.turns[index].AdditionalContext[ContextKey].Value; got != want {
			t.Errorf("turn %d carried %q, want %q: the queue reordered the conversation",
				index+1, got, want)
		}
	}
}

// The queue behind a running turn has a floor of a depth.
//
// A turn can run for as long as the model runs. Without a cap, every message
// arriving behind a slow one would park a goroutine for the length of that
// turn, and a node under a burst would hold the burst in memory instead of in
// the inbox — which is where a message that cannot be delivered belongs, since
// waking has never been what makes a message readable.
func TestTheQueueBehindARunningTurnIsBounded(t *testing.T) {
	conversation := &recordingConversation{turnIDs: []string{"turn-1", "turn-2", "turn-3"}}
	driver := NewWith(conversation)
	driver.queueDepth = 1

	if err := driver.Drive(context.Background(), codexSession(), envelope("msg_1")); err != nil {
		t.Fatal(err)
	}
	go func() { _ = driver.Drive(context.Background(), codexSession(), envelope("msg_2")) }()
	eventually(t, "the second message to queue", func() bool { return driver.queued() == 1 })

	// In a goroutine with a deadline, because the failure this is looking for
	// is a message that queues instead of being refused — and that message
	// would otherwise sit here for the whole of maxWait.
	third := make(chan error, 1)
	go func() { third <- driver.Drive(context.Background(), codexSession(), envelope("msg_3")) }()
	select {
	case err := <-third:
		if !errors.Is(err, ErrThreadBusy) {
			t.Fatalf("a third message past the queue depth returned %v, want ErrThreadBusy", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a third message past the queue depth queued up instead of being refused")
	}
	if got := conversation.started(); got != 1 {
		t.Errorf("%d turns were started; the refused message went to the thread anyway", got)
	}
	conversation.finish("turn-1")
}

// A message does not wait for a slow turn for ever.
//
// The bound is this driver's own, not only the caller's context, so the reason
// recorded names what was waited for. Past it the message stays in the inbox,
// which costs the reader nothing: a wake was only ever an extra push.
func TestWaitingForABusyThreadIsBounded(t *testing.T) {
	conversation := &recordingConversation{turnIDs: []string{"turn-1", "turn-2"}}
	driver := NewWith(conversation)
	driver.maxWait = 20 * time.Millisecond

	if err := driver.Drive(context.Background(), codexSession(), envelope("msg_1")); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	second := make(chan error, 1)
	go func() { second <- driver.Drive(context.Background(), codexSession(), envelope("msg_2")) }()
	select {
	case err := <-second:
		if !errors.Is(err, ErrThreadBusy) {
			t.Fatalf("the second message returned %v, want ErrThreadBusy after the wait ran out", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("the second message was still waiting for a turn that has not ended, "+
			"well past the %s bound", driver.maxWait)
	}
	if waited := time.Since(start); waited > settle {
		t.Errorf("it waited %s, well past the %s bound", waited, driver.maxWait)
	}
	if got := conversation.started(); got != 1 {
		t.Errorf("%d turns started; the message that gave up was sent anyway", got)
	}
	// And the thread is still held by the turn that is still running, not
	// released by the message that gave up waiting for it.
	if got := driver.queued(); got != 0 {
		t.Errorf("%d messages are still queued after one gave up", got)
	}
	conversation.finish("turn-1")
}

// A message that gives up in the instant the thread is handed to it hands it
// on rather than keeping it.
//
// The race is real and it is not rare on a busy node: leave() pops the next
// ticket and closes it while that waiter may, in the same instant, be
// returning on its own deadline. Whoever was handed the lane owns it whether
// they wanted it or not. A waiter that walks away from it leaves the thread
// held by nobody — and nothing would ever wake that session again, silently,
// for the life of the process.
//
// Written as the state rather than as a stress loop: two hundred attempts at
// hitting the window by timing never entered it once, and a race test that
// does not reach the race is a test that passes with the handling deleted.
func TestGivingUpInTheHandoverDoesNotStrandTheThread(t *testing.T) {
	held := &lane{}
	if err := held.enter(context.Background(), 1, time.Second); err != nil {
		t.Fatal(err)
	}
	// A waiter queued behind the turn in flight.
	ticket := make(chan struct{})
	held.mu.Lock()
	held.queue = append(held.queue, ticket)
	held.mu.Unlock()

	// The turn ends and the lane is handed to that waiter...
	held.leave()
	select {
	case <-ticket:
	default:
		t.Fatal("leave() did not hand the lane to the waiting message")
	}
	// ...which, in the same instant, gave up on its own deadline.
	held.abandon(ticket)

	if !held.idle() {
		t.Fatal("a message that gave up in the handover kept the thread; it is now held " +
			"by nobody and no later message can ever take it")
	}
}

// A turn that never reports completing does not hold the thread for ever.
//
// The connection under it can die, and when it does every waiter on it is
// released with an error. Keeping the thread held on that basis would make one
// dead app-server into a session that is never woken again — silently, which
// is worse than a message left in the inbox.
func TestAFailedWaitReleasesTheThread(t *testing.T) {
	conversation := &recordingConversation{
		turnIDs: []string{"turn-1", "turn-2"},
		waitErr: errors.New("Codex App Server closed the connection"),
	}
	driver := NewWith(conversation)

	if err := driver.Drive(context.Background(), codexSession(), envelope("msg_1")); err != nil {
		t.Fatal(err)
	}
	// Bounded, because the failure this is looking for is a thread that stays
	// held: without a deadline that failure looks like a test that never ends.
	second := make(chan error, 1)
	go func() { second <- driver.Drive(context.Background(), codexSession(), envelope("msg_2")) }()
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("the next message never got the thread back: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the connection died under the turn and the thread stayed held; " +
			"this session would never be woken again")
	}
	if got := conversation.started(); got != 2 {
		t.Errorf("%d turns started", got)
	}
}

// A turn whose completion never comes does not hold the thread for ever.
//
// The connection dying is the failure with a signal; this is the one without.
// app-server stays up, the turn is never reported as over, and nothing else in
// this driver has an opinion about it — so the backstop is the only thing
// standing between that and a session no message can ever wake again. Deleting
// it (context.WithCancel in place of the timeout) passed both packages.
func TestATurnThatNeverCompletesDoesNotHoldTheThreadForEver(t *testing.T) {
	conversation := &recordingConversation{turnIDs: []string{"turn-1", "turn-2"}}
	driver := NewWith(conversation)
	driver.maxTurn = 20 * time.Millisecond

	if err := driver.Drive(context.Background(), codexSession(), envelope("msg_1")); err != nil {
		t.Fatal(err)
	}
	// turn-1 is never finished here. The fake honours the wait's context, as
	// the real client does, so the backstop expiring is the only thing that can
	// end this wait — and maxWait is untouched, so a message that gets through
	// got through because the thread was released, not because it gave up.
	second := make(chan error, 1)
	go func() { second <- driver.Drive(context.Background(), codexSession(), envelope("msg_2")) }()
	select {
	case err := <-second:
		if err != nil {
			t.Fatalf("the next message after the backstop: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("a turn that never reported completing held the thread well past the %s "+
			"backstop; this session would never be woken again", driver.maxTurn)
	}
	if got := conversation.started(); got != 2 {
		t.Errorf("%d turns started; the message let through never reached the thread", got)
	}
}

// app-server answering with a turn that was already running is reported, not
// reported as a success.
//
// It means the message was appended to somebody else's turn — the owner's own
// Codex window driving the same thread is the way this happens — and a message
// appended to a running turn may get no answer of its own at all. Recording
// that as a delivered wake is recording something this node cannot see.
func TestATurnIdThatRepeatsIsReportedAsCoalesced(t *testing.T) {
	conversation := &recordingConversation{turnIDs: []string{"turn-1", "turn-1"}}
	driver := NewWith(conversation)

	if err := driver.Drive(context.Background(), codexSession(), envelope("msg_1")); err != nil {
		t.Fatal(err)
	}
	conversation.finish("turn-1")
	eventually(t, "the thread to be released", func() bool { return driver.idleLanes() })

	err := driver.Drive(context.Background(), codexSession(), envelope("msg_2"))
	if !errors.Is(err, ErrTurnCoalesced) {
		t.Fatalf("a repeated turn id was reported as %v, want ErrTurnCoalesced", err)
	}
}

// One thread's turn does not hold up another thread's.
//
// app-server serialises per thread, so serialising more than that here would
// be this node inventing a limit Codex does not have — and a node with several
// woken sessions would deliver them one at a time for no reason.
func TestThreadsDoNotWaitForEachOther(t *testing.T) {
	conversation := &recordingConversation{turnIDs: []string{"turn-1", "turn-2"}}
	driver := NewWith(conversation)

	first := codexSession()
	second := model.Session{
		ID: "codex:thread-2", Provider: model.ProviderCodex, ProviderSessionID: "thread-2",
	}
	if err := driver.Drive(context.Background(), first, envelope("msg_1")); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- driver.Drive(context.Background(), second, envelope("msg_2")) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("the other thread's message: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a message for another thread waited behind an unrelated thread's turn")
	}
	conversation.finish("turn-1")
	conversation.finish("turn-2")
}

func envelope(messageID string) wake.Envelope {
	return wake.Envelope{
		MessageID:    messageID,
		Body:         messageID,
		SenderNodeID: "node_peer0000000000000",
	}
}

// queued reports how many messages are waiting across every thread.
func (d *Driver) queued() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	total := 0
	for _, held := range d.lanes {
		held.mu.Lock()
		total += len(held.queue)
		held.mu.Unlock()
	}
	return total
}

// idleLanes reports whether no thread is held.
func (d *Driver) idleLanes() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, held := range d.lanes {
		if !held.idle() {
			return false
		}
	}
	return true
}
