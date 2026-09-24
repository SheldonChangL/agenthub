package nodeconfig

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// The safety core of ADR-005: a reader that knows only the scalar — an older
// window, an older `ah` — writes peerListen alone, and that write has to close
// every address the list held, not just replace its first entry. A node that
// kept serving the Wi-Fi address after the window said "this machine only"
// would make the window lie in the one sentence that matters.
func TestWritingTheScalarReplacesTheWholeList(t *testing.T) {
	stored := Partial{
		PeerListen:  stringOf("192.168.1.10:7463"),
		PeerListens: listOf("192.168.1.10:7463", "10.0.0.5:7463"),
		AllowLAN:    boolOf(true),
	}
	written, err := NormalizePeerListen(Partial{PeerListen: stringOf("127.0.0.1:7463")})
	if err != nil {
		t.Fatal(err)
	}
	if written.PeerListens == nil || !slices.Equal(*written.PeerListens, []string{"127.0.0.1:7463"}) {
		t.Fatalf("a scalar write normalised to list %v; it has to be the whole list", written.PeerListens)
	}
	next := stored.Overlay(written)
	if !slices.Equal(*next.PeerListens, []string{"127.0.0.1:7463"}) || *next.PeerListen != "127.0.0.1:7463" {
		t.Fatalf("after a scalar write the stored list is %v / %q", *next.PeerListens, *next.PeerListen)
	}
	// And without normalising first: Overlay and Apply move the two together
	// on their own, so no caller can forget the step and leave the list open.
	raw := stored.Overlay(Partial{PeerListen: stringOf("127.0.0.1:7463")})
	if !slices.Equal(*raw.PeerListens, []string{"127.0.0.1:7463"}) {
		t.Fatalf("Overlay of a bare scalar left the list %v", *raw.PeerListens)
	}
	applied := Partial{PeerListen: stringOf("127.0.0.1:7463")}.Apply(Settings{
		PeerListen: "192.168.1.10:7463", PeerListens: []string{"192.168.1.10:7463", "10.0.0.5:7463"},
	})
	if !slices.Equal(applied.PeerListens, []string{"127.0.0.1:7463"}) || applied.PeerListen != "127.0.0.1:7463" {
		t.Fatalf("Apply of a bare scalar = %q / %v", applied.PeerListen, applied.PeerListens)
	}
}

func TestNormalizePeerListenFillsTheOtherSpelling(t *testing.T) {
	onlyList, err := NormalizePeerListen(Partial{PeerListens: listOf("192.168.1.10:7463", "10.0.0.5:7463")})
	if err != nil {
		t.Fatal(err)
	}
	if onlyList.PeerListen == nil || *onlyList.PeerListen != "192.168.1.10:7463" {
		t.Fatalf("a list alone left the scalar %v; it has to be the first entry", onlyList.PeerListen)
	}
	empty, err := NormalizePeerListen(Partial{PeerListens: listOf()})
	if err != nil {
		t.Fatal(err)
	}
	if *empty.PeerListen != DefaultPeerListen || !slices.Equal(*empty.PeerListens, []string{DefaultPeerListen}) {
		t.Fatalf("an empty list normalised to %q / %v, want the default", *empty.PeerListen, *empty.PeerListens)
	}
	agreeing, err := NormalizePeerListen(Partial{
		PeerListen: stringOf("192.168.1.10:7463"), PeerListens: listOf("192.168.1.10:7463", "10.0.0.5:7463"),
	})
	if err != nil || len(*agreeing.PeerListens) != 2 {
		t.Fatalf("agreeing spellings: %v, %v", agreeing.PeerListens, err)
	}
	untouched, err := NormalizePeerListen(Partial{AllowLAN: boolOf(true)})
	if err != nil || untouched.PeerListen != nil || untouched.PeerListens != nil {
		t.Fatalf("a request that said nothing about the listener gained one: %+v, %v", untouched, err)
	}
}

// Two spellings that disagree are two instructions, and picking one is a guess
// about which the caller meant.
func TestNormalizePeerListenRefusesSpellingsThatDisagree(t *testing.T) {
	_, err := NormalizePeerListen(Partial{
		PeerListen: stringOf("127.0.0.1:7463"), PeerListens: listOf("192.168.1.10:7463", "10.0.0.5:7463"),
	})
	if !errors.Is(err, ErrPeerListenMismatch) {
		t.Fatalf("disagreeing spellings: err = %v", err)
	}
	if _, err := (Settings{
		PeerListen: "127.0.0.1:7463", PeerListens: []string{"127.0.0.2:7463"},
	}).Validate(); err == nil {
		t.Fatal("a configuration whose scalar is not its first entry validated")
	}
}

// Every rule that keeps one address safe keeps each address in a list safe,
// and the list as a whole has rules of its own.
func TestValidatePeerListensRefusesWhatCannotBeServedTogether(t *testing.T) {
	declared, err := ParsePrivateRanges([]string{"122.122.0.0/16"})
	if err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name     string
		list     []string
		allowLAN bool
	}{
		{"unspecified v4", []string{"192.168.1.10:7463", "0.0.0.0:7463"}, true},
		{"unspecified v6", []string{"192.168.1.10:7463", "[::]:7463"}, true},
		{"unspecified v6 spelled 0::0", []string{"192.168.1.10:7463", "[0::0]:7463"}, true},
		{"unspecified v4-mapped", []string{"192.168.1.10:7463", "[::ffff:0.0.0.0]:7463"}, true},
		{"no host is every interface", []string{"192.168.1.10:7463", ":7463"}, true},
		{"zone", []string{"192.168.1.10:7463", "[fe80::1%en0]:7463"}, true},
		{"name", []string{"192.168.1.10:7463", "myhost.local:7463"}, true},
		{"public", []string{"192.168.1.10:7463", "8.8.8.8:7463"}, true},
		{"five entries", []string{
			"192.168.1.10:7463", "192.168.1.11:7463", "10.0.0.5:7463", "172.16.0.1:7463", "122.122.122.1:7463",
		}, true},
		{"duplicate", []string{"192.168.1.10:7463", "192.168.1.10:7463"}, true},
		{"duplicate after unmapping", []string{"192.168.1.10:7463", "[::ffff:192.168.1.10]:7463"}, true},
		{"different ports", []string{"192.168.1.10:7463", "10.0.0.5:7464"}, true},
		{"LAN with allowLan off", []string{"127.0.0.1:7463", "192.168.1.10:7463"}, false},
		{"only LAN with allowLan off", []string{"192.168.1.10:7463"}, false},
		{"loopback beside LAN", []string{"127.0.0.1:7463", "192.168.1.10:7463"}, true},
		{"LAN beside loopback", []string{"192.168.1.10:7463", "[::1]:7463"}, true},
		{"kernel-chosen port in a list", []string{"127.0.0.1:0", "[::1]:0"}, false},
		{"localhost beside its own address", []string{"localhost:7463", "127.0.0.1:7463"}, false},
		{"localhost after an address", []string{"[::1]:7463", "LocalHost:7463"}, false},
		{"empty", []string{}, true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := ValidatePeerListens(testCase.list, testCase.allowLAN, declared); err == nil {
				t.Fatalf("ValidatePeerListens(%q, allowLan=%t) accepted it", testCase.list, testCase.allowLAN)
			}
			// And through Settings.Validate, which is what the API and the node
			// both call: a rule only one of them applies is a saved value that
			// stops the service from starting.
			if len(testCase.list) == 0 {
				return
			}
			settings := Settings{
				PeerListen: testCase.list[0], PeerListens: testCase.list,
				AllowLAN: testCase.allowLAN, TreatAsPrivate: []string{"122.122.0.0/16"},
			}
			if _, err := settings.Validate(); err == nil {
				t.Fatalf("Settings.Validate accepted %q", testCase.list)
			}
		})
	}
	for _, testCase := range []struct {
		name     string
		list     []string
		allowLAN bool
	}{
		{"cable and Wi-Fi", []string{"192.168.1.10:7463", "122.122.122.1:7463"}, true},
		{"four", []string{"192.168.1.10:7463", "192.168.1.11:7463", "10.0.0.5:7463", "172.16.0.1:7463"}, true},
		{"two loopbacks", []string{"127.0.0.1:7463", "[::1]:7463"}, false},
		{"one loopback on a kernel port", []string{"127.0.0.1:0"}, false},
		{"localhost alone, as a single address always was", []string{"localhost:7463"}, false},
	} {
		t.Run("accepts "+testCase.name, func(t *testing.T) {
			if err := ValidatePeerListens(testCase.list, testCase.allowLAN, declared); err != nil {
				t.Fatalf("ValidatePeerListens(%q) = %v", testCase.list, err)
			}
		})
	}
}

// Closing allowLan takes every network address off, keeps loopback ones where
// they are, and lands on the default when nothing is left.
func TestWithdrawPeerListensDropsEveryNetworkAddress(t *testing.T) {
	kept, withdrawn, dropped := WithdrawPeerListens(false, false, []string{"192.168.1.10:7463", "10.0.0.5:7463"})
	if !withdrawn || !slices.Equal(kept, []string{DefaultPeerListen}) {
		t.Fatalf("kept %v (withdrawn=%t), want the default", kept, withdrawn)
	}
	if !slices.Equal(dropped, []string{"192.168.1.10:7463", "10.0.0.5:7463"}) {
		t.Fatalf("dropped = %v", dropped)
	}
	// A hand-edited mix keeps its loopback half.
	kept, withdrawn, _ = WithdrawPeerListens(false, false, []string{"192.168.1.10:7463", "127.0.0.1:9000"})
	if !withdrawn || !slices.Equal(kept, []string{"127.0.0.1:9000"}) {
		t.Fatalf("kept %v (withdrawn=%t)", kept, withdrawn)
	}
	if _, withdrawn, _ := WithdrawPeerListens(false, false, []string{"127.0.0.1:9000"}); withdrawn {
		t.Fatal("a loopback-only list was withdrawn")
	}
	if _, withdrawn, _ := WithdrawPeerListens(true, false, []string{"192.168.1.10:7463"}); withdrawn {
		t.Fatal("withdrawn with allowLan on")
	}
	if _, withdrawn, _ := WithdrawPeerListens(false, true, []string{"192.168.1.10:7463"}); withdrawn {
		t.Fatal("withdrawn although the caller named the listener; that contradiction is refused, not guessed at")
	}
	if reason := WithdrawalReason("allowLan", "a:1", "b:2"); !strings.Contains(reason, `"a:1"`) ||
		!strings.Contains(reason, `"b:2"`) {
		t.Fatalf("the reason does not name every withdrawn address: %q", reason)
	}
}

// Given, remembered or default is asked of the setting, whichever spelling
// carried it.
func TestResolveReportsTheListUnderTheOneSetting(t *testing.T) {
	settings, sources := Resolve(Partial{}, Partial{
		PeerListen: stringOf("192.168.1.10:7463"), PeerListens: listOf("192.168.1.10:7463", "10.0.0.5:7463"),
	}, DefaultSettings())
	if sources[SettingPeerListen] != SourceRemembered {
		t.Fatalf("source = %q", sources[SettingPeerListen])
	}
	if settings.PeerListen != "192.168.1.10:7463" || len(settings.PeerListens) != 2 {
		t.Fatalf("settings = %+v", settings)
	}
	// A flag given once pins a list of one over a remembered list of two.
	pinned, sources := Resolve(Partial{PeerListens: listOf("10.0.0.5:7463")}, Partial{
		PeerListen: stringOf("192.168.1.10:7463"), PeerListens: listOf("192.168.1.10:7463", "10.0.0.5:7463"),
	}, DefaultSettings())
	if !slices.Equal(pinned.PeerListens, []string{"10.0.0.5:7463"}) || pinned.PeerListen != "10.0.0.5:7463" ||
		sources[SettingPeerListen] != SourceFlag {
		t.Fatalf("pinned = %+v from %q", pinned, sources[SettingPeerListen])
	}
	defaults, _ := Resolve(Partial{}, Partial{}, DefaultSettings())
	if !slices.Equal(defaults.PeerListens, []string{DefaultPeerListen}) {
		t.Fatalf("default list = %v", defaults.PeerListens)
	}
}

func TestDescribeNamesEveryPeerAddress(t *testing.T) {
	lines := strings.Join(Describe(Settings{
		PeerListen: "192.168.1.10:7463", PeerListens: []string{"192.168.1.10:7463", "10.0.0.5:7463"},
	}, nil), "\n")
	if !strings.Contains(lines, "peer-listen = 192.168.1.10:7463, 10.0.0.5:7463 (default)") {
		t.Fatalf("start-up description:\n%s", lines)
	}
}
