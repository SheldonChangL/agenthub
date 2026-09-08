package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"net/netip"
	"os"
	"strings"
	"testing"

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
