package wake

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
)

type recordingDriver struct {
	mu       sync.Mutex
	driven   []Envelope
	failWith error
}

func (d *recordingDriver) Provider() model.Provider { return model.ProviderCodex }

func (d *recordingDriver) Drive(_ context.Context, _ model.Session, envelope Envelope) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failWith != nil {
		return d.failWith
	}
	d.driven = append(d.driven, envelope)
	return nil
}

func (d *recordingDriver) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.driven)
}

func gateFixture(t *testing.T, autoWake bool) (*registry.Registry, *recordingDriver, *Gate, model.Session) {
	t.Helper()
	ctx := context.Background()
	store, err := registry.Open(ctx, filepath.Join(t.TempDir(), "agenthub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	session, err := store.UpsertSession(ctx, model.Session{
		ID: "codex:target", Provider: model.ProviderCodex, ProviderSessionID: "target",
		Management: model.Unmanaged, Status: model.StatusActive, StatusSource: "test",
		LastSeenAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetAudience(ctx, session.ID, model.Audience{
		Mode: model.AudienceAllPaired, AcceptMessages: true, AutoWake: autoWake,
	}); err != nil {
		t.Fatal(err)
	}
	driver := &recordingDriver{}
	return store, driver, New(store, registry.DefaultWakeLimits(), driver), session
}

// peerNode is what an envelope's signature proved, as the peer surface passes
// it: never parsed back out of the label.
const peerNode = "node_peer0000000000000"

func arrived(session string, hops int) model.Message {
	return model.Message{
		ID: "msg_1", To: session, From: "node_peer0000000000000/codex:theirs",
		Body: "hello", WakeHops: hops, CreatedAt: time.Now().UTC(),
	}
}

// The closed switch stops everything, and leaves no trace.
//
// No trace on purpose: closed is the default and the common case, so a row per
// message to every session would bury the rows that mean something. The
// absence is not silence about a decision — there was no decision to make.
func TestAClosedSwitchDrivesNothingAndRecordsNothing(t *testing.T) {
	ctx := context.Background()
	store, driver, gate, session := gateFixture(t, false)

	gate.Consider(ctx, arrived(session.ID, 0), peerNode, "fingerprint")

	if driver.count() != 0 {
		t.Error("a session with waking closed was driven")
	}
	events, err := store.ListWakes(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Errorf("waking was never on the table and %d events were recorded: %+v", len(events), events)
	}
}

// An open switch drives the agent, and the record says so.
func TestAnOpenSwitchDrivesTheAgent(t *testing.T) {
	ctx := context.Background()
	store, driver, gate, session := gateFixture(t, true)

	gate.Consider(ctx, arrived(session.ID, 0), peerNode, "2DCF 9604 DBA9 778A")

	if driver.count() != 1 {
		t.Fatalf("the driver was called %d times", driver.count())
	}
	driver.mu.Lock()
	envelope := driver.driven[0]
	driver.mu.Unlock()
	if envelope.Fingerprint != "2DCF 9604 DBA9 778A" {
		t.Errorf("the driver got fingerprint %q", envelope.Fingerprint)
	}
	if envelope.SenderNodeID != "node_peer0000000000000" {
		t.Errorf("the driver got sender node %q", envelope.SenderNodeID)
	}
	events, err := store.ListWakes(ctx, session.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Outcome != registry.WakeWoken {
		t.Fatalf("events = %+v", events)
	}
	// The bucket the gate wrote, read back from the store.
	//
	// Asserting a PairKey() on an event built here proves only that the
	// function works; the bug it exists for was the gate handing it the wrong
	// SourceNodeID, and that is invisible unless the recorded row is the thing
	// checked. Blanking the field passed the whole suite: every peer and every
	// local send back in one bucket, with audit rows naming a peer's wake as
	// this machine's own.
	if events[0].SourceNodeID != peerNode {
		t.Errorf("the recorded source node is %q, want the one the envelope proved",
			events[0].SourceNodeID)
	}
	if got := events[0].PairKey(); got != "node:"+peerNode {
		t.Errorf("the wake was bucketed as %q, want the peer's own bucket", got)
	}
}

// A message already at the hop limit is stopped, and the owner is told.
func TestAMessageAtTheHopLimitIsStopped(t *testing.T) {
	ctx := context.Background()
	store, driver, gate, session := gateFixture(t, true)

	gate.Consider(ctx, arrived(session.ID, protocol.MaxWakeHops), peerNode, "fingerprint")

	if driver.count() != 0 {
		t.Error("an exchange past the hop limit kept going")
	}
	events, err := store.ListWakes(ctx, session.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Outcome != registry.WakeRefusedHops {
		t.Fatalf("events = %+v", events)
	}
	if events[0].Detail == "" {
		t.Error("the refusal does not say how far the exchange had gone")
	}
}

// A driver that refuses leaves the record saying so, and the message is not
// lost — nothing here deletes it, which is the point.
func TestADriverThatRefusesIsRecordedAsFailed(t *testing.T) {
	ctx := context.Background()
	store, driver, gate, session := gateFixture(t, true)
	driver.failWith = context.DeadlineExceeded

	gate.Consider(ctx, arrived(session.ID, 0), peerNode, "fingerprint")

	events, err := store.ListWakes(ctx, session.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Outcome != registry.WakeFailed {
		t.Fatalf("events = %+v", events)
	}
	// And it stops counting, so the next message is not blocked by a turn that
	// never ran. Asserted by driving another one rather than by counting: the
	// count was a number nobody acts on, and "not zero" is satisfied by almost
	// any bug.
	driver.failWith = nil
	next := arrived(session.ID, 0)
	next.ID = "msg_after"
	limits := registry.DefaultWakeLimits()
	limits.Pair = 1
	New(store, limits, driver).Consider(ctx, next, peerNode, "fingerprint")
	if driver.count() != 1 {
		t.Errorf("a wake that failed still holds its slot: the retry drove %d times",
			driver.count())
	}
}

// A session whose provider has no driver is recorded as failed, not ignored.
// The owner turned waking on and is entitled to know it did not happen.
func TestAProviderWithNoDriverIsRecordedAsFailed(t *testing.T) {
	ctx := context.Background()
	store, _, _, session := gateFixture(t, true)
	// A gate with no drivers at all.
	gate := New(store, registry.DefaultWakeLimits())

	gate.Consider(ctx, arrived(session.ID, 0), peerNode, "fingerprint")

	events, err := store.ListWakes(ctx, session.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Outcome != registry.WakeFailed {
		t.Fatalf("events = %+v", events)
	}
}

// A refused reservation drives nothing.
//
// The registry decides; this is the line that obeys it, and deleting it made
// every limit decorative while the whole root suite still passed. The rate
// limits are what the ADR calls the sound half of the loop defence, and the
// only thing between a compromised peer and an agent it can run all night.
func TestARefusedReservationDrivesNothing(t *testing.T) {
	ctx := context.Background()
	store, driver, _, session := gateFixture(t, true)
	limits := registry.DefaultWakeLimits()
	limits.Pair = 1
	gate := New(store, limits, driver)

	gate.Consider(ctx, arrived(session.ID, 0), peerNode, "fingerprint")
	if driver.count() != 1 {
		t.Fatalf("the first wake drove %d times", driver.count())
	}

	// The pair's slot is taken, and it is still taken while the turn runs.
	second := arrived(session.ID, 0)
	second.ID = "msg_2"
	gate.Consider(ctx, second, peerNode, "fingerprint")
	if driver.count() != 1 {
		t.Errorf("a refused reservation drove the agent anyway (%d calls); every limit is "+
			"decorative if this line is not here", driver.count())
	}

	events, err := store.ListWakes(ctx, session.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Outcome != registry.WakeRefusedPair {
		t.Fatalf("events = %+v", events)
	}
}

// Every refusal stops the drive, not just the pair one.
func TestEveryRefusalStopsTheDrive(t *testing.T) {
	ctx := context.Background()
	for name, limits := range map[string]registry.WakeLimits{
		"pair":    {Pair: 0, PairWindow: time.Hour, Session: 9, SessionWindow: time.Hour, Node: 9, NodeWindow: time.Hour},
		"session": {Pair: 9, PairWindow: time.Hour, Session: 0, SessionWindow: time.Hour, Node: 9, NodeWindow: time.Hour},
		"node":    {Pair: 9, PairWindow: time.Hour, Session: 9, SessionWindow: time.Hour, Node: 0, NodeWindow: time.Hour},
	} {
		store, driver, _, session := gateFixture(t, true)
		New(store, limits, driver).Consider(ctx, arrived(session.ID, 0), peerNode, "fingerprint")
		if driver.count() != 0 {
			t.Errorf("the %s limit was at zero and the agent was driven anyway", name)
		}
	}
}

// The count the gate records is the one a reply inherits.
//
// This is the origin of the whole chain: PeekWakeChain reads it back and
// returns it plus one, so a gate that records zero makes every leg of an
// exchange go out at 1 and MaxWakeHops never fires at all. Deleting it left
// the whole repository green — the API tests hand-write the value into
// ReserveWake with a comment saying they are standing in for the gate, so the
// producer was simulated and never observed.
func TestTheRecordedHopCountIsWhatAReplyInherits(t *testing.T) {
	ctx := context.Background()
	store, driver, gate, session := gateFixture(t, true)

	gate.Consider(ctx, arrived(session.ID, 3), peerNode, "fingerprint")
	if driver.count() != 1 {
		t.Fatalf("the driver was called %d times", driver.count())
	}

	events, err := store.ListWakes(ctx, session.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Hops != 3 {
		t.Fatalf("the gate recorded %+v, want the 3 hops the message arrived with", events)
	}
	// And that is what the next message out of this session carries.
	hops, chain, err := store.PeekWakeChain(ctx, session.ID, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if hops != 4 {
		t.Errorf("a reply would go out at %d hops, want 4; the limit is %d and would never fire",
			hops, protocol.MaxWakeHops)
	}
	if chain == "" {
		t.Error("the reply has no chain to claim, so it can never be spent")
	}

	// The agent is told how far in it is, too — that is the sentence in its
	// prompt warning that a reply may wake the other side again.
	driver.mu.Lock()
	told := driver.driven[0].Hops
	driver.mu.Unlock()
	if told != 3 {
		t.Errorf("the driver was told %d hops", told)
	}
}
