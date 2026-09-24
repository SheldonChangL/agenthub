package nodeconfig

import (
	"context"
	"errors"
	"net"
	"slices"
	"sync"
	"testing"
	"time"
)

// fakeNetwork decides which addresses bind, and records every address the set
// asked for. The sockets it hands out are real loopback ones on a free port,
// so a listener that "bound 192.168.1.10:7463" can still be dialled.
type fakeNetwork struct {
	mu        sync.Mutex
	available map[string]bool
	asked     []string
}

func newFakeNetwork(available ...string) *fakeNetwork {
	network := &fakeNetwork{available: map[string]bool{}}
	for _, address := range available {
		network.available[address] = true
	}
	return network
}

func (n *fakeNetwork) listen(_, address string) (net.Listener, error) {
	n.mu.Lock()
	n.asked = append(n.asked, address)
	ok := n.available[address] || address == anyLoopbackPort
	n.mu.Unlock()
	if !ok {
		return nil, errors.New("listen tcp " + address + ": bind: can't assign requested address")
	}
	return net.Listen("tcp", "127.0.0.1:0")
}

func (n *fakeNetwork) appear(address string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.available[address] = true
}

func (n *fakeNetwork) askedFor() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.asked...)
}

func (n *fakeNetwork) options(fallback string) ListenerSetOptions {
	return ListenerSetOptions{
		Fallback: fallback,
		Listen:   n.listen,
		// The machine holds the cable address only; the Wi-Fi one is "gone".
		Interfaces: func() ([]string, error) { return []string{"127.0.0.1/8", "192.168.1.10/24"}, nil },
		Probe:      func(string, string) error { return nil },
	}
}

const (
	cable    = "192.168.1.10:7463"
	wifi     = "10.0.0.5:7463"
	fallback = "127.0.0.1:7463"
)

// One address binding is enough: the other is reported as failed and the node
// does not fall back to loopback. The fallback exists for a node that would
// otherwise have no peer listener at all, and a node reachable on its cable is
// not that node.
func TestAListenerSetWithOneAddressBoundDoesNotFallBack(t *testing.T) {
	network := newFakeNetwork(cable)
	set := NewListenerSet([]string{cable, wifi}, network.options(fallback))
	if err := set.Bind(); err != nil {
		t.Fatal(err)
	}
	defer set.Close()

	if problem := set.Problem(); problem != nil {
		t.Fatalf("one address bound and the set reports a degradation: %+v", problem)
	}
	if asked := network.askedFor(); !slices.Equal(asked, []string{cable, wifi}) {
		t.Fatalf("the set asked for %v; the fallback must not be tried while an address is bound", asked)
	}
	if running := set.Running(); running != cable {
		t.Errorf("running = %q, want the bound address %q", running, cable)
	}
	states := set.States()
	if len(states) != 2 || states[0].State != ListenerBound || states[1].State != ListenerFailed {
		t.Fatalf("states = %+v", states)
	}
	if states[1].Reason != ListenAddressGone || states[1].Detail == "" || states[1].Message == "" {
		t.Errorf("the failed address is not explained: %+v", states[1])
	}
}

// Every address failing is the one case the old single-address fallback
// covered, and it still runs in the same order.
func TestAListenerSetWithNothingBoundFallsBackAsBefore(t *testing.T) {
	network := newFakeNetwork(fallback)
	set := NewListenerSet([]string{wifi, "10.0.0.6:7463"}, network.options(fallback))
	if err := set.Bind(); err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	problem := set.Problem()
	if problem == nil {
		t.Fatal("nothing configured bound and no problem was reported")
	}
	if problem.Address != wifi || problem.Reason != ListenAddressGone || problem.Message == "" {
		t.Errorf("problem = %+v; it names the first configured address, which an older reader shows", problem)
	}
	if problem.RunningOn != set.Running() {
		t.Errorf("problem says %q, set runs on %q", problem.RunningOn, set.Running())
	}
	if asked := network.askedFor(); !slices.Equal(asked, []string{wifi, "10.0.0.6:7463", fallback}) {
		t.Errorf("asked for %v", asked)
	}

	// The fallback being one of the configured addresses: it just failed, so
	// the last resort is next, never the same address again.
	busy := newFakeNetwork()
	again := NewListenerSet([]string{fallback}, busy.options(fallback))
	if err := again.Bind(); err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if asked := busy.askedFor(); !slices.Equal(asked, []string{fallback, anyLoopbackPort}) {
		t.Errorf("asked for %v; the failed address was retried as its own remedy", asked)
	}
	if again.Problem() == nil || again.Problem().RunningOn == fallback {
		t.Errorf("problem = %+v", again.Problem())
	}
}

// The case the retry exists for: launchd starts the node before Wi-Fi has an
// address. The tick is delivered by the test, not waited for.
func TestAListenerSetBindsAnAddressThatAppearsLater(t *testing.T) {
	network := newFakeNetwork(cable)
	set := NewListenerSet([]string{cable, wifi}, network.options(fallback))
	if err := set.Bind(); err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	served := make(chan net.Listener, 4)
	set.Serve(func(listener net.Listener) { served <- listener })
	<-served // the cable, served at once

	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go set.Run(ctx, ticks)

	ticks <- time.Now() // still gone: nothing new is served
	select {
	case listener := <-served:
		t.Fatalf("a retry served %v while the address was still gone", listener.Addr())
	case <-time.After(50 * time.Millisecond):
	}
	network.appear(wifi)
	ticks <- time.Now()
	select {
	case <-served:
	case <-time.After(2 * time.Second):
		t.Fatal("the address appeared and a retry did not bind it")
	}
	if bound := set.Bound(); !slices.Equal(bound, []string{cable, wifi}) {
		t.Errorf("bound = %v", bound)
	}
	// Nothing but configured addresses was ever asked for.
	for _, address := range network.askedFor() {
		if address != cable && address != wifi {
			t.Errorf("the set tried to bind %q, which is not configured", address)
		}
	}
}

// A degraded node whose address comes back is not degraded any more: the
// fallback is closed, on purpose, and the problem goes with it.
func TestARetryThatBindsRetiresTheFallback(t *testing.T) {
	network := newFakeNetwork(fallback)
	set := NewListenerSet([]string{wifi}, network.options(fallback))
	if err := set.Bind(); err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	var served []net.Listener
	set.Serve(func(listener net.Listener) { served = append(served, listener) })
	if len(served) != 1 || set.Problem() == nil {
		t.Fatalf("served %d, problem %v", len(served), set.Problem())
	}
	degraded := served[0]

	network.appear(wifi)
	set.Retry()
	if set.Problem() != nil {
		t.Fatalf("an address is bound and the problem stands: %+v", set.Problem())
	}
	if !set.Retired(degraded) {
		t.Error("the fallback was closed but not marked retired; the serving loop would read it as a failure")
	}
	if _, err := degraded.Accept(); err == nil {
		t.Error("the retired fallback still accepts")
	}
	if len(served) != 2 || set.Running() != wifi {
		t.Errorf("served %d, running %q", len(served), set.Running())
	}
	// And the retry never reached for the fallback again.
	if asked := network.askedFor(); !slices.Equal(asked, []string{wifi, fallback, wifi}) {
		t.Errorf("asked for %v", asked)
	}
}

// Nothing unconfigured, ever: a set whose addresses are all bound asks for
// nothing on a retry, and one that is waiting asks only for what it waits for.
func TestAListenerSetNeverBindsAnAddressItWasNotGiven(t *testing.T) {
	network := newFakeNetwork(cable, wifi)
	set := NewListenerSet([]string{cable, wifi}, network.options(fallback))
	if err := set.Bind(); err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	for range 3 {
		set.Retry()
	}
	if asked := network.askedFor(); !slices.Equal(asked, []string{cable, wifi}) {
		t.Errorf("asked for %v after retries with everything bound", asked)
	}
}

// The cap is the process's, not each address's: a second address must not
// double how many connections an attacker can hold open.
func TestTheConnectionCapIsSharedAcrossListeners(t *testing.T) {
	network := newFakeNetwork(cable, wifi)
	options := network.options(fallback)
	options.Limit = 2
	set := NewListenerSet([]string{cable, wifi}, options)
	if err := set.Bind(); err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	var listeners []net.Listener
	var held sync.WaitGroup
	var mu sync.Mutex
	var accepted []net.Conn
	set.Serve(func(listener net.Listener) {
		listeners = append(listeners, listener)
		held.Add(1)
		go func() {
			defer held.Done()
			for {
				connection, err := listener.Accept()
				if err != nil {
					return
				}
				mu.Lock()
				accepted = append(accepted, connection)
				mu.Unlock()
			}
		}()
	})
	if len(listeners) != 2 {
		t.Fatalf("served %d listeners", len(listeners))
	}
	dial := func(listener net.Listener) net.Conn {
		t.Helper()
		connection, err := net.Dial("tcp", listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = connection.Close() })
		return connection
	}
	dial(listeners[0])
	dial(listeners[0])
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(accepted) == 2 })
	// The first listener holds both slots, so the second one's connection is
	// turned away although that listener itself holds nothing.
	dial(listeners[1])
	waitFor(t, func() bool { return set.Limit().Refused() == 1 })
	mu.Lock()
	if len(accepted) != 2 {
		t.Errorf("accepted %d connections under a cap of 2", len(accepted))
	}
	for _, connection := range accepted {
		_ = connection.Close()
	}
	mu.Unlock()
	_ = set.Close()
	held.Wait()
}

func TestAListenerSetIsPendingUntilBound(t *testing.T) {
	set := NewListenerSet([]string{cable}, newFakeNetwork(cable).options(fallback))
	if states := set.States(); len(states) != 1 || states[0].State != ListenerPending {
		t.Fatalf("states before Bind = %+v", states)
	}
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached within 2s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
