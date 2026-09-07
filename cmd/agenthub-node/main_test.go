package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/netip"
	"os"
	"strings"
	"testing"

	"agenthub.local/agenthub/internal/discovery"
)

// The port in an announcement is what a peer dials for TLS, not the port the
// multicast packet arrived from. Getting it wrong produces a candidate that
// looks right and connects to nothing, so every spelling the listener itself
// accepts has to survive being read here — and the spellings it cannot carry
// have to be refused at startup rather than announced.
func TestListenPortReadsWhatTheListenerAccepts(t *testing.T) {
	for name, testCase := range map[string]struct {
		address string
		want    int
	}{
		"a number":                  {"127.0.0.1:7463", 7463},
		"a number on every address": {":7463", 7463},
		// net.Listen accepts a service name, so refusing one here would stop a
		// node over an address that works.
		"a service name": {"127.0.0.1:https", 443},
		"an ipv6 host":   {"[::1]:7463", 7463},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := listenPort(testCase.address)
			if err != nil {
				t.Fatalf("listenPort(%q) error = %v", testCase.address, err)
			}
			if got != testCase.want {
				t.Errorf("listenPort(%q) = %d, want %d", testCase.address, got, testCase.want)
			}
		})
	}

	for name, address := range map[string]string{
		// Port zero asks the kernel to choose, so this number is not the one
		// the listener ends up on. Announcing it invites a connection to
		// nothing.
		"port zero":    "127.0.0.1:0",
		"no port":      "127.0.0.1",
		"not a port":   "127.0.0.1:not-a-service",
		"out of range": "127.0.0.1:70000",
		"empty":        "",
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := listenPort(address); err == nil {
				t.Errorf("listenPort(%q) = %d, want an error", address, got)
			}
		})
	}
}

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
