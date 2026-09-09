package main

import (
	"agenthub.local/agenthub/internal/api"
	"agenthub.local/agenthub/internal/model"
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/discovery"
)

// A full candidate list is a condition an attacker can hold this node in, by
// continuing to send. One log line per packet would turn a bounded list — which
// is what the candidate layer exists to keep bounded — into unbounded log
// output, which is the same flood arriving by another route.
func TestARecurringCandidateConditionIsLoggedOnceNotPerPacket(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags)
	})

	// A candidate list that is already full, so every packet hits the condition.
	candidates := discovery.NewCandidates("node_local0000000000", func(context.Context, string) (bool, error) {
		return false, nil
	}, func(string) error { return nil })
	source := netip.MustParseAddr("192.168.9.9")
	handle := candidateHandler(candidates)
	fill := make([]discovery.Announcement, 0, discovery.MaxCandidates+1)
	for i := 0; i <= discovery.MaxCandidates; i++ {
		fill = append(fill, discovery.Announcement{
			NodeID:  fmt.Sprintf("node_filler%09d", i),
			Address: source.String() + ":7463",
			// A fingerprint is what makes an announcement an offer to pair.
			Fingerprint: "1223 03EA 5E96 543A 2DD8 BFEA",
		})
	}
	for packet := 0; packet < 50; packet++ {
		handle(context.Background(), source, fill)
	}

	full := strings.Count(logged.String(), "candidate list is full")
	if full == 0 {
		t.Fatalf("a full list was never reported: %s", logged.String())
	}
	if full > 1 {
		t.Errorf("a full list was logged %d times for 50 packets; one condition, one line", full)
	}

	// And the throttle is per condition, not one gate over everything: a
	// different failure arriving while a full list is being reported has to be
	// reported too, or whoever is holding the list full silences the log.
	handle(context.Background(), netip.Addr{}, fill)
	if !strings.Contains(logged.String(), "could not read pairing offers") {
		t.Errorf("a second, different failure was swallowed by the first: %s", logged.String())
	}
}

// A flag passed with its default value is still a flag that was passed.
//
// -display-name "" means "hand the name back to the machine", and reading the
// flag's value cannot say whether an empty string was typed or the flag was
// left out. Getting this wrong makes a pinned name permanent, which is not a
// failure anything else here would catch: the node starts, keeps the old name,
// and looks like the rename simply did not take.
func TestWasSetDistinguishesAPassedFlagFromAnAbsentOne(t *testing.T) {
	for name, args := range map[string][]string{
		"passed empty":       {"-display-name", ""},
		"passed a value":     {"-display-name", "the machine on my desk"},
		"passed with equals": {"-display-name="},
	} {
		flags := flag.NewFlagSet("test", flag.ContinueOnError)
		flags.String("display-name", "", "")
		if err := flags.Parse(args); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !wasSet(flags, "display-name") {
			t.Errorf("%s (%q) reads as absent, so an empty value could never release a pinned name",
				name, args)
		}
	}

	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	flags.String("display-name", "", "")
	flags.String("other", "", "")
	if err := flags.Parse([]string{"-other", "x"}); err != nil {
		t.Fatal(err)
	}
	if wasSet(flags, "display-name") {
		t.Error("an absent flag reads as passed, so every start would release the name")
	}
}

// The deadline the owner listener sets is the one the API is told about.
//
// They were two literals in two places, and changing either alone passed every
// test — while putting the wake stream back to holding a poll past the
// deadline that cuts it, which is the bug that made the whole feature not work
// in steady state. The API caps a poll below whatever it is told; if it is
// told the wrong number, the cap is against the wrong deadline.
func TestTheOwnerListenerAndTheAPIAgreeOnTheWriteDeadline(t *testing.T) {
	server := ownerServer("127.0.0.1:0", http.NotFoundHandler())
	if server.WriteTimeout != ownerWriteTimeout {
		t.Errorf("the listener writes for %s and the API is told %s",
			server.WriteTimeout, ownerWriteTimeout)
	}
	// And a poll capped against it leaves room to answer.
	if api.NewServer(nil, nil, nil, model.NodeIdentity{},
		api.WithWriteTimeout(ownerWriteTimeout)).WakeStreamWait(0) >= server.WriteTimeout {
		t.Error("a poll may run to the connection deadline, so it can never answer")
	}
}

// Shutdown ends held requests before waiting for handlers.
//
// http.Server.Shutdown waits for handlers to return and does not cancel their
// contexts, so a held wake stream kept the node alive to its own deadline: the
// shutdown reported a timeout and the peer listener was never closed. Nothing
// pinned the Drain call, and deleting it passed the whole suite while taking
// shutdown from 0.10s to 5.08s and exit 1.
func TestShutdownDrainsBeforeWaiting(t *testing.T) {
	drained := make(chan struct{})
	held := make(chan struct{})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(held)
		<-drained
	})
	owner := httptest.NewServer(handler)
	defer owner.Close()
	peers := httptest.NewServer(http.NotFoundHandler())
	defer peers.Close()

	go func() { _, _ = http.Get(owner.URL) }()
	<-held

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	started := time.Now()
	err := shutDown(ctx, drainer(func() { close(drained) }), owner.Config, peers.Config)
	if err != nil {
		t.Fatalf("shutDown() error = %v; a held request outlasted the budget", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("shutdown took %s with one held request", elapsed)
	}
}

// Both listeners are closed even when the first shutdown fails.
//
// A peer listener left open is a socket still accepting deliveries from the
// network after this process has decided to stop, and it was skipped by an
// early return whenever the owner listener timed out.
func TestShutdownClosesThePeerListenerEvenWhenTheOwnerOneFails(t *testing.T) {
	stuck := make(chan struct{})
	owner := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		<-stuck
	}))
	// Released before Close, not after: httptest.Server.Close waits for
	// handlers, and a deferred close runs last.
	defer func() { close(stuck); owner.Close() }()
	peers := httptest.NewServer(http.NotFoundHandler())
	peerURL := peers.URL
	defer peers.Close()

	reached := make(chan struct{})
	go func() { close(reached); _, _ = http.Get(owner.URL) }()
	<-reached
	time.Sleep(100 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	// A drainer that does nothing, so the owner shutdown really does time out.
	if err := shutDown(ctx, drainer(func() {}), owner.Config, peers.Config); err == nil {
		t.Fatal("a shutdown that could not finish reported success")
	}
	if _, err := http.Get(peerURL); err == nil {
		t.Error("the peer listener is still accepting after shutdown; an early return " +
			"on the owner listener's failure used to skip it entirely")
	}
}

type drainer func()

func (d drainer) Drain() { d() }
