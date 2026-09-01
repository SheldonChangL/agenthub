package nodeconfig

import (
	"errors"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLoopbackIsAlwaysAllowed pins the default: a node that is not told to
// serve a network behaves exactly as every earlier build did.
func TestLoopbackIsAlwaysAllowed(t *testing.T) {
	for _, address := range []string{"127.0.0.1:7463", "[::1]:7463", "localhost:7463"} {
		if err := ValidatePeerListen(address, false); err != nil {
			t.Errorf("ValidatePeerListen(%q, false) = %v; loopback must never need a flag", address, err)
		}
		if err := ValidatePeerListen(address, true); err != nil {
			t.Errorf("ValidatePeerListen(%q, true) = %v", address, err)
		}
	}
}

// TestServingANetworkNeedsTheFlag keeps the widening deliberate. Nothing about
// a default configuration may put session metadata on a network.
func TestServingANetworkNeedsTheFlag(t *testing.T) {
	const address = "192.168.1.10:7463"
	err := ValidatePeerListen(address, false)
	if err == nil {
		t.Fatal("a private address was served without the flag")
	}
	if !strings.Contains(err.Error(), "-allow-lan") {
		t.Errorf("error = %v; it should say which flag turns this on", err)
	}
	if err := ValidatePeerListen(address, true); err != nil {
		t.Errorf("ValidatePeerListen(%q, true) = %v; a private address is the case this is for", address, err)
	}
}

// TestAPublicAddressIsRefusedEvenWithTheFlag is the line the flag does not
// cross. Sharing with the machine on the next desk is a different decision from
// publishing to the internet, and one flag must not do both.
func TestAPublicAddressIsRefusedEvenWithTheFlag(t *testing.T) {
	for name, address := range map[string]string{
		"a public IPv4":                "203.0.113.10:7463",
		"a public IPv6":                "[2001:db8::1]:7463",
		"carrier-grade NAT":            "100.64.0.1:7463",
		"a public range used as a LAN": "122.122.122.1:7463",
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidatePeerListen(address, true)
			if err == nil {
				t.Fatalf("ValidatePeerListen(%q, true) = nil; a public address must be refused", address)
			}
			if !errors.Is(err, ErrNotPrivate) {
				t.Errorf("error = %v; want ErrNotPrivate", err)
			}
		})
	}
}

// TestBindingEveryInterfaceIsRefused covers the shortcut that would undo the
// check above: 0.0.0.0 includes whatever public address the host has or gains.
func TestBindingEveryInterfaceIsRefused(t *testing.T) {
	for _, address := range []string{"0.0.0.0:7463", "[::]:7463", ":7463"} {
		err := ValidatePeerListen(address, true)
		if err == nil {
			t.Fatalf("ValidatePeerListen(%q, true) = nil; the unspecified address must be refused", address)
		}
		if !errors.Is(err, ErrNotPrivate) {
			t.Errorf("%q: error = %v; want ErrNotPrivate", address, err)
		}
	}
}

// TestANameIsRefused keeps the decision pinned to an address. A name can
// resolve somewhere else after the check has passed.
func TestANameIsRefused(t *testing.T) {
	if err := ValidatePeerListen("my-laptop.local:7463", true); err == nil {
		t.Fatal("a name was accepted as a peer listen address")
	}
}

// TestPrivateRangesAreRecognised covers the ranges a real local network uses.
func TestPrivateRangesAreRecognised(t *testing.T) {
	for name, address := range map[string]string{
		"RFC 1918 ten":      "10.1.2.3:7463",
		"RFC 1918 172.16":   "172.16.5.4:7463",
		"RFC 1918 192.168":  "192.168.161.43:7463",
		"IPv6 unique local": "[fd00::1]:7463",
		"IPv4 link-local":   "169.254.10.20:7463",
		"IPv6 link-local":   "[fe80::1]:7463",
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidatePeerListen(address, true); err != nil {
				t.Errorf("ValidatePeerListen(%q, true) = %v", address, err)
			}
		})
	}
}

// TestConnectionsAreCapped covers what the request rate limiter cannot: a
// caller that opens connections and never sends a request pays no request cost
// but still holds a descriptor and a goroutine.
func TestConnectionsAreCapped(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	const limit = 4
	capped := LimitConnections(inner, limit)
	defer func() { _ = capped.Close() }()

	accepted := make(chan net.Conn, limit*4)
	go func() {
		for {
			connection, err := capped.Accept()
			if err != nil {
				return
			}
			accepted <- connection
		}
	}()

	// Open more than the cap and keep them open.
	var held []net.Conn
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()
	for range limit * 3 {
		client, err := net.DialTimeout("tcp", capped.Addr().String(), 2*time.Second)
		if err != nil {
			continue
		}
		held = append(held, client)
	}

	// Exactly `limit` connections should have been handed to the server.
	deadline := time.After(2 * time.Second)
	server := make([]net.Conn, 0, limit)
collect:
	for len(server) < limit {
		select {
		case connection := <-accepted:
			server = append(server, connection)
		case <-deadline:
			break collect
		}
	}
	if len(server) != limit {
		t.Fatalf("server accepted %d connections; want the cap of %d", len(server), limit)
	}
	select {
	case extra := <-accepted:
		_ = extra.Close()
		t.Fatal("a connection beyond the cap was handed to the server")
	case <-time.After(300 * time.Millisecond):
	}

	// Closing one must free exactly one slot, or the cap becomes a one-way
	// ratchet that ends in refusing everything.
	if err := server[0].Close(); err != nil {
		t.Fatal(err)
	}
	client, err := net.DialTimeout("tcp", capped.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial after releasing a slot: %v", err)
	}
	held = append(held, client)
	select {
	case connection := <-accepted:
		_ = connection.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("closing a connection did not free a slot")
	}
}

// TestClosingTwiceReleasesOneSlot pins that a double close cannot mint capacity.
func TestClosingTwiceReleasesOneSlot(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	capped := LimitConnections(inner, 1)
	defer func() { _ = capped.Close() }()

	go func() {
		for {
			connection, err := capped.Accept()
			if err != nil {
				return
			}
			// Close twice on purpose.
			_ = connection.Close()
			_ = connection.Close()
		}
	}()

	for range 5 {
		client, err := net.DialTimeout("tcp", capped.Addr().String(), 2*time.Second)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		_ = client.Close()
		time.Sleep(50 * time.Millisecond)
	}

	capped.mutexProbe(t, 1)
}

// mutexProbe asserts the number of slots currently held.
func (l *LimitedListener) mutexProbe(t *testing.T, capacity int) {
	t.Helper()
	if got := cap(l.slots); got != capacity {
		t.Fatalf("slot capacity = %d; want %d", got, capacity)
	}
	if held := len(l.slots); held > capacity {
		t.Fatalf("slots held = %d, above the capacity of %d: a double close minted capacity", held, capacity)
	}
}

// TestTheListenerIsSafeUnderConcurrentDials matches how it is used.
func TestTheListenerIsSafeUnderConcurrentDials(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	capped := LimitConnections(inner, 8)
	defer func() { _ = capped.Close() }()

	go func() {
		for {
			connection, err := capped.Accept()
			if err != nil {
				return
			}
			go func() {
				time.Sleep(10 * time.Millisecond)
				_ = connection.Close()
			}()
		}
	}()

	var group sync.WaitGroup
	for range 32 {
		group.Add(1)
		go func() {
			defer group.Done()
			client, err := net.DialTimeout("tcp", capped.Addr().String(), 2*time.Second)
			if err != nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
			_ = client.Close()
		}()
	}
	group.Wait()
}
