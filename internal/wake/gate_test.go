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

	gate.Consider(ctx, arrived(session.ID, 0), "fingerprint")

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

	gate.Consider(ctx, arrived(session.ID, 0), "2DCF 9604 DBA9 778A")

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
}

// A message already at the hop limit is stopped, and the owner is told.
func TestAMessageAtTheHopLimitIsStopped(t *testing.T) {
	ctx := context.Background()
	store, driver, gate, session := gateFixture(t, true)

	gate.Consider(ctx, arrived(session.ID, protocol.MaxWakeHops), "fingerprint")

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

	gate.Consider(ctx, arrived(session.ID, 0), "fingerprint")

	events, err := store.ListWakes(ctx, session.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Outcome != registry.WakeFailed {
		t.Fatalf("events = %+v", events)
	}
	// And it stops counting, so the next message is not blocked by a turn that
	// never ran.
	count, err := store.CountWakes(ctx, "", session.ID, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("a failed wake counts %d against the limit", count)
	}
}

// A session whose provider has no driver is recorded as failed, not ignored.
// The owner turned waking on and is entitled to know it did not happen.
func TestAProviderWithNoDriverIsRecordedAsFailed(t *testing.T) {
	ctx := context.Background()
	store, _, _, session := gateFixture(t, true)
	// A gate with no drivers at all.
	gate := New(store, registry.DefaultWakeLimits())

	gate.Consider(ctx, arrived(session.ID, 0), "fingerprint")

	events, err := store.ListWakes(ctx, session.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Outcome != registry.WakeFailed {
		t.Fatalf("events = %+v", events)
	}
}
