package nodeconfig

import (
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLoopbackIsAlwaysAllowed pins the default: a node that is not told to
// serve a network behaves exactly as every earlier build did.
func TestLoopbackIsAlwaysAllowed(t *testing.T) {
	for _, address := range []string{"127.0.0.1:7463", "[::1]:7463", "localhost:7463"} {
		if err := ValidatePeerListen(address, false, nil); err != nil {
			t.Errorf("ValidatePeerListen(%q, false, nil) = %v; loopback must never need a flag", address, err)
		}
		if err := ValidatePeerListen(address, true, nil); err != nil {
			t.Errorf("ValidatePeerListen(%q, true, nil) = %v", address, err)
		}
	}
}

// TestServingANetworkNeedsTheFlag keeps the widening deliberate. Nothing about
// a default configuration may put session metadata on a network.
func TestServingANetworkNeedsTheFlag(t *testing.T) {
	const address = "192.168.1.10:7463"
	err := ValidatePeerListen(address, false, nil)
	if err == nil {
		t.Fatal("a private address was served without the flag")
	}
	if !strings.Contains(err.Error(), "-allow-lan") {
		t.Errorf("error = %v; it should say which flag turns this on", err)
	}
	if err := ValidatePeerListen(address, true, nil); err != nil {
		t.Errorf("ValidatePeerListen(%q, true, nil) = %v; a private address is the case this is for", address, err)
	}
}

// TestAPublicAddressIsRefusedEvenWithTheFlag is the line the flag does not
// cross. Sharing with the machine on the next desk is a different decision from
// publishing to the internet, and one flag must not do both.
func TestAPublicAddressIsRefusedEvenWithTheFlag(t *testing.T) {
	for name, address := range map[string]string{
		"a public IPv4":                        "203.0.113.10:7463",
		"a public IPv6":                        "[2001:db8::1]:7463",
		"carrier-grade NAT":                    "100.64.0.1:7463",
		"a public range someone uses as a LAN": "203.0.113.1:7463",
	} {
		t.Run(name, func(t *testing.T) {
			err := ValidatePeerListen(address, true, nil)
			if err == nil {
				t.Fatalf("ValidatePeerListen(%q, true, nil) = nil; a public address must be refused", address)
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
		err := ValidatePeerListen(address, true, nil)
		if err == nil {
			t.Fatalf("ValidatePeerListen(%q, true, nil) = nil; the unspecified address must be refused", address)
		}
		if !errors.Is(err, ErrNotPrivate) {
			t.Errorf("%q: error = %v; want ErrNotPrivate", address, err)
		}
	}
}

// TestANameIsRefused keeps the decision pinned to an address. A name can
// resolve somewhere else after the check has passed.
func TestANameIsRefused(t *testing.T) {
	if err := ValidatePeerListen("my-laptop.local:7463", true, nil); err == nil {
		t.Fatal("a name was accepted as a peer listen address")
	}
}

// TestPrivateRangesAreRecognised covers the ranges a real local network uses.
func TestPrivateRangesAreRecognised(t *testing.T) {
	for name, address := range map[string]string{
		"RFC 1918 ten":      "10.1.2.3:7463",
		"RFC 1918 172.16":   "172.16.5.4:7463",
		"RFC 1918 192.168":  "192.168.0.43:7463",
		"IPv6 unique local": "[fd00::1]:7463",
		"IPv4 link-local":   "169.254.10.20:7463",
		"IPv6 link-local":   "[fe80::1]:7463",
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidatePeerListen(address, true, nil); err != nil {
				t.Errorf("ValidatePeerListen(%q, true, nil) = %v", address, err)
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

// TestADoubleCloseCannotMintCapacity pins the guard in limitedConn.Close.
//
// An earlier version of this test asserted len(slots) > cap(slots), which a
// channel can never satisfy — it passed with the guard deleted entirely. A
// double release does not grow the channel; it drains a slot belonging to a
// connection that is still open, so the listener admits one more connection
// than its cap. That is what this measures: hold the cap, double-close one
// held connection from both ends, and require the next connection to still be
// refused.
func TestADoubleCloseCannotMintCapacity(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	const limit = 3
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

	dial := func() (net.Conn, bool) {
		client, err := net.DialTimeout("tcp", capped.Addr().String(), 2*time.Second)
		if err != nil {
			return nil, false
		}
		select {
		case server := <-accepted:
			return server, true
		case <-time.After(500 * time.Millisecond):
			_ = client.Close()
			return nil, false
		}
	}

	// Fill to capacity and keep every connection open.
	held := make([]net.Conn, 0, limit)
	for range limit {
		server, ok := dial()
		if !ok {
			t.Fatalf("only filled %d of %d slots", len(held), limit)
		}
		held = append(held, server)
	}
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()

	// Close one of them twice. The second close must release nothing: the other
	// connections are still open and still own their slots.
	_ = held[0].Close()
	_ = held[0].Close()
	held = held[1:]

	// The double close freed exactly one slot, so exactly one more connection
	// fits — and the one after it must be refused.
	replacement, ok := dial()
	if !ok {
		t.Fatal("the slot freed by the close was not reusable")
	}
	held = append(held, replacement)

	if extra, ok := dial(); ok {
		_ = extra.Close()
		t.Fatalf("a connection beyond the cap of %d was admitted; the double close minted a slot", limit)
	}
	if capped.Refused() == 0 {
		t.Error("a connection was refused but not counted")
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

// TestADeclaredRangeMakesAnAddressServable is the case this exists for: two
// machines on a direct cable, using a block that looks public because IANA
// assigned it to somebody else. Nothing about the address says it is private,
// so the owner says.
func TestADeclaredRangeMakesAnAddressServable(t *testing.T) {
	const address = "203.0.113.2:7463"

	// Without a declaration it is refused, as any public address is.
	if err := ValidatePeerListen(address, true, nil); !errors.Is(err, ErrNotPrivate) {
		t.Fatalf("error = %v; want ErrNotPrivate before anything is declared", err)
	}

	declared, err := ParsePrivateRanges([]string{"203.0.113.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePeerListen(address, true, declared); err != nil {
		t.Fatalf("ValidatePeerListen() with the range declared = %v", err)
	}

	// A declaration covers what it names and nothing else.
	if err := ValidatePeerListen("198.51.100.2:7463", true, declared); !errors.Is(err, ErrNotPrivate) {
		t.Errorf("error = %v; a declaration must not cover an unrelated block", err)
	}
}

// TestADeclarationStillNeedsTheFlag keeps the two controls independent: saying
// a network is private does not by itself start serving it.
func TestADeclarationStillNeedsTheFlag(t *testing.T) {
	declared, err := ParsePrivateRanges([]string{"203.0.113.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	err = ValidatePeerListen("203.0.113.2:7463", false, declared)
	if err == nil {
		t.Fatal("a declared range was served without -allow-lan")
	}
	if !strings.Contains(err.Error(), "-allow-lan") {
		t.Errorf("error = %v; it should still name the flag", err)
	}
}

// TestADefaultRouteCannotBeDeclared refuses the declaration that is not one.
//
// "Everything is private" is the absence of a belief about the network, not a
// statement of one, and it is what somebody reaches for at 2am to make an error
// go away. Naming a block is the whole point.
func TestADefaultRouteCannotBeDeclared(t *testing.T) {
	for _, value := range []string{"0.0.0.0/0", "::/0"} {
		if _, err := ParsePrivateRanges([]string{value}); err == nil {
			t.Errorf("ParsePrivateRanges(%q) succeeded; it covers every address", value)
		}
	}
	// A merely large block is allowed: the owner said which one.
	if _, err := ParsePrivateRanges([]string{"122.122.0.0/16"}); err != nil {
		t.Errorf("a /16 was refused: %v", err)
	}
}

// TestAMalformedDeclarationIsRefusedAtStartup keeps a typo from silently
// declaring nothing and leaving the owner wondering why delivery fails.
func TestAMalformedDeclarationIsRefusedAtStartup(t *testing.T) {
	for _, value := range []string{"122.122.122.2", "not-a-cidr", "192.168.0.0/33", "192.168.0.0/-1"} {
		if _, err := ParsePrivateRanges([]string{value}); err == nil {
			t.Errorf("ParsePrivateRanges(%q) succeeded", value)
		}
	}
}

// TestTheDeclarationIsVisible keeps the claim auditable: whatever it later
// allows, the belief behind it is in the startup log.
func TestTheDeclarationIsVisible(t *testing.T) {
	declared, err := ParsePrivateRanges([]string{"122.122.0.0/16", "203.0.113.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	rendered := declared.String()
	for _, want := range []string{"122.122.0.0/16", "203.0.113.0/24"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("String() = %q; missing %q", rendered, want)
		}
	}
	if PrivateRanges(nil).String() != "none" {
		t.Errorf("an empty declaration renders as %q", PrivateRanges(nil).String())
	}
}

// TestDeclaringDoesNotWeakenTheRestOfTheRule pins that the escape hatch is
// exactly as wide as what was declared.
func TestDeclaringDoesNotWeakenTheRestOfTheRule(t *testing.T) {
	declared, err := ParsePrivateRanges([]string{"122.122.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	for name, address := range map[string]string{
		"the unspecified address": "0.0.0.0:7463",
		"a name":                  "peer.local:7463",
		"another public block":    "203.0.113.1:7463",
		"carrier-grade NAT":       "100.64.0.1:7463",
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidatePeerListen(address, true, declared); err == nil {
				t.Fatalf("ValidatePeerListen(%q) = nil despite an unrelated declaration", address)
			}
		})
	}
}

// TestADeclarationHasEdges is the test the earlier ones were not.
//
// Every case in TestDeclaringDoesNotWeakenTheRestOfTheRule differed from the
// declared block in its first octet, so a Contains that compared one octet
// would have passed. These sit on the boundary.
func TestADeclarationHasEdges(t *testing.T) {
	declared, err := ParsePrivateRanges([]string{"122.122.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	for address, want := range map[string]bool{
		"122.122.0.0:7463":     true,  // first address in the block
		"122.122.255.255:7463": true,  // last address in the block
		"122.122.122.2:7463":   true,  // the machine this exists for
		"122.121.255.255:7463": false, // one below
		"122.123.0.0:7463":     false, // one above
		"121.122.122.2:7463":   false, // neighbouring first octet
		"123.122.122.2:7463":   false,
	} {
		t.Run(address, func(t *testing.T) {
			err := ValidatePeerListen(address, true, declared)
			if want && err != nil {
				t.Errorf("ValidatePeerListen(%q) = %v; it is inside the declared block", address, err)
			}
			if !want && err == nil {
				t.Errorf("ValidatePeerListen(%q) = nil; it is outside the declared block", address)
			}
		})
	}
}

// TestTheUnspecifiedAddressIsRefusedInEverySpelling covers the hole a string
// comparison left.
//
// "0.0.0.0" is one spelling. "0::0", "::0", "0:0:0:0:0:0:0:0" and
// "::ffff:0.0.0.0" all bind every interface too, and a string check lets them
// through — which mattered the moment a declaration could make them look
// private.
func TestTheUnspecifiedAddressIsRefusedInEverySpelling(t *testing.T) {
	// A declaration wide enough to cover the unspecified address is refused
	// outright, so build the test on one that is not.
	declared, err := ParsePrivateRanges([]string{"122.122.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{
		"0.0.0.0:7463",
		"[::]:7463",
		"[0::0]:7463",
		"[::0]:7463",
		"[0:0:0:0:0:0:0:0]:7463",
		"[::ffff:0.0.0.0]:7463",
		":7463",
	} {
		if err := ValidatePeerListen(address, true, declared); err == nil {
			t.Errorf("ValidatePeerListen(%q) = nil; it binds every interface", address)
		}
	}
}

// TestADeclarationCannotCoverSpecialPurposeSpace is the principled version of
// refusing a default route: the line is what a block contains, not how many
// bits it has.
func TestADeclarationCannotCoverSpecialPurposeSpace(t *testing.T) {
	for name, value := range map[string]string{
		"everything, v4":       "0.0.0.0/0",
		"everything, v6":       "::/0",
		"half the internet":    "0.0.0.0/1",
		"the unspecified /8":   "0.0.0.0/8",
		"half of v6":           "::/1",
		"multicast":            "224.0.0.0/4",
		"multicast and above":  "128.0.0.0/1",
		"v6 multicast":         "ff00::/8",
		"an IPv4-mapped block": "::ffff:122.122.0.0/112",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePrivateRanges([]string{value}); err == nil {
				t.Errorf("ParsePrivateRanges(%q) succeeded", value)
			}
		})
	}

	// Genuine unicast blocks of any size are the owner's call.
	for _, value := range []string{"122.122.0.0/16", "203.0.113.0/24", "100.64.0.0/10", "2001:db8::/32"} {
		if _, err := ParsePrivateRanges([]string{value}); err != nil {
			t.Errorf("ParsePrivateRanges(%q) = %v; a unicast block is a real declaration", value, err)
		}
	}
}

// TestAZonedLiteralIsRefused keeps a peer from being configured into silence: a
// zoned address parses, passes the private check, and then breaks when a URL is
// built from it.
func TestAZonedLiteralIsRefused(t *testing.T) {
	if err := ValidatePeerListen("[fe80::1%en0]:7463", true, nil); err == nil {
		t.Fatal("a zoned literal was accepted; the URL built from it would be invalid")
	}
}

// TestTheListenCheckStandsWithoutTheParseCheck isolates one of two defences.
//
// Refusing the unspecified address is guarded twice: ParsePrivateRanges will
// not accept a declaration that covers it, and ValidatePeerListen refuses it
// whatever is declared. On the normal path the first defence means the second
// is never reached, so a test that goes through parsing cannot tell which one
// is working — replacing the second with the string comparison it used to be
// left the suite green.
//
// This builds the declaration directly, which is the only way to ask the second
// defence a question the first has not already answered. The redundancy is
// deliberate: the parse check is about what an owner may say, and this is about
// what the listener will bind, and neither should depend on the other.
func TestTheListenCheckStandsWithoutTheParseCheck(t *testing.T) {
	// A declaration ParsePrivateRanges would refuse, constructed anyway.
	everything := PrivateRanges{netip.MustParsePrefix("0.0.0.0/1"), netip.MustParsePrefix("::/1")}

	for _, address := range []string{
		"0.0.0.0:7463",
		"[::]:7463",
		"[0::0]:7463",
		"[::0]:7463",
		"[0:0:0:0:0:0:0:0]:7463",
		"[::ffff:0.0.0.0]:7463",
	} {
		if err := ValidatePeerListen(address, true, everything); err == nil {
			t.Errorf("ValidatePeerListen(%q) = nil even with the unspecified address declared private; "+
				"it binds every interface, including any public one", address)
		}
	}

	// The same declaration does let an ordinary address through, so the test
	// above is not passing because everything is refused.
	if err := ValidatePeerListen("122.122.122.2:7463", true, everything); err != nil {
		t.Fatalf("the constructed declaration refuses everything: %v", err)
	}
}
