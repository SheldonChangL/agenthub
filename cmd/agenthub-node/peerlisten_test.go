package main

import (
	"fmt"
	"net"
	"strings"
	"testing"

	"agenthub.local/agenthub/internal/nodeconfig"
)

// freeLoopback is an address this machine will serve right now, port included.
// Taken and given straight back, because a test that degrades onto a fixed port
// fails on the developer's own machine, where a node is usually already running
// on it.
func freeLoopback(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

// The ordinary start: the address binds and nothing is said about it.
func TestBindPeerListenerBindsWhatItWasAsked(t *testing.T) {
	address := freeLoopback(t)
	listener, problem, err := bindPeerListener(address, freeLoopback(t), func(string, ...any) {})
	if err != nil {
		t.Fatalf("bindPeerListener: %v", err)
	}
	defer listener.Close()
	if problem != nil {
		t.Fatalf("a listener that bound reported a problem: %+v", problem)
	}
	if got := listener.Addr().String(); got != address {
		t.Errorf("bound %s, want %s", got, address)
	}
}

// The failure this exists for. Before it, the same situation ended run(), which
// took the owner's API down with it and left the address unreachable from the
// only surface that can change it.
func TestBindPeerListenerDegradesInsteadOfFailing(t *testing.T) {
	// TEST-NET-3. Reserved for documentation, so no machine running this test
	// holds it, which is exactly the state a pulled cable leaves behind.
	gone := "203.0.113.1:7463"
	fallback := freeLoopback(t)
	var logged []string
	listener, problem, err := bindPeerListener(gone, fallback, func(format string, args ...any) {
		logged = append(logged, fmt.Sprintf(format, args...))
	})
	if err != nil {
		t.Fatalf("an unbindable address ended the start: %v", err)
	}
	defer listener.Close()
	if problem == nil {
		t.Fatal("degraded onto loopback without saying so")
	}
	if problem.Address != gone {
		t.Errorf("problem names %q, want the address that failed, %q", problem.Address, gone)
	}
	if problem.Reason != nodeconfig.ListenAddressGone {
		t.Errorf("reason is %q, want %q", problem.Reason, nodeconfig.ListenAddressGone)
	}
	// The address being served, not the one that was asked for: a desktop shows
	// this to say where the node actually is.
	if problem.RunningOn != listener.Addr().String() {
		t.Errorf("problem says it is running on %q, listener is on %q",
			problem.RunningOn, listener.Addr().String())
	}
	if problem.Detail == "" {
		t.Error("the system's own words were dropped; they are what an owner searches for")
	}
	if problem.Message == "" {
		t.Error("no sentence for a reader that does not know the reason codes")
	}
	// Said out loud as well. A degraded node that looks healthy in its log is
	// one nobody investigates until a peer complains.
	if len(logged) == 0 || !strings.Contains(strings.Join(logged, "\n"), "could not be bound") {
		t.Errorf("the degradation was not logged: %q", logged)
	}
}

// The loopback fallback's own port being taken must not end the start either.
//
// Found by running it: a second node on this machine holds 127.0.0.1:7463, so
// the degrade path failed and the node died anyway — on exactly the machine
// where the owner most needs the settings page, one already running a node they
// care about. The last resort is loopback on any free port: it serves nobody,
// which is what a degraded node already is, and the process lives.
func TestBindPeerListenerFallsBackAgainWhenTheDefaultPortIsTaken(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	listener, problem, err := bindPeerListener("203.0.113.1:7463", occupied.Addr().String(),
		func(string, ...any) {})
	if err != nil {
		t.Fatalf("a busy fallback port ended the start: %v", err)
	}
	defer listener.Close()
	if problem == nil {
		t.Fatal("degraded twice over without saying so")
	}
	// The address it actually landed on, which is the one on screen and the one
	// that tells an owner nobody is reaching this node.
	if problem.RunningOn != listener.Addr().String() {
		t.Errorf("problem says %q, listener is on %q", problem.RunningOn, listener.Addr().String())
	}
	if problem.RunningOn == occupied.Addr().String() {
		t.Error("the node reported the port it could not have as the one it is serving")
	}
	if !strings.Contains(problem.Message, "203.0.113.1:7463") {
		t.Errorf("the address that failed is not named: %q", problem.Message)
	}
}

// Asked for loopback, failed on loopback: there is nothing to fall back to, and
// a degradation that reported the same address as its own remedy would be a
// loop with a reassuring message on it.
func TestBindPeerListenerDoesNotDegradeOntoTheAddressThatFailed(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	address := occupied.Addr().String()
	listener, problem, err := bindPeerListener(address, address, func(string, ...any) {})
	if err == nil {
		listener.Close()
		t.Fatal("the fallback was the address that just failed, and it was reported as recovery")
	}
	if problem != nil {
		t.Errorf("a failed start reported a problem to publish: %+v", problem)
	}
}

// Asking for loopback and failing is the one case with no way out: the fallback
// would be the address that just failed, and the last resort is the same
// interface. The start ends and says all of it.
