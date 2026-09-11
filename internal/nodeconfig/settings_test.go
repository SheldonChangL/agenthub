package nodeconfig

import (
	"strings"
	"testing"
)

func stringOf(value string) *string { return &value }
func boolOf(value bool) *bool       { return &value }
func listOf(values ...string) *[]string {
	list := values
	return &list
}

// A flag given now decides this start, and says so, whatever was remembered.
func TestResolvePrefersTheFlagOverTheRememberedValue(t *testing.T) {
	given := Partial{PeerListen: stringOf("192.168.1.10:7463"), AllowLAN: boolOf(true)}
	remembered := Partial{PeerListen: stringOf("127.0.0.1:7463"), AllowLAN: boolOf(false), Discover: boolOf(true)}

	settings, sources := Resolve(given, remembered, DefaultSettings())

	if settings.PeerListen != "192.168.1.10:7463" || !settings.AllowLAN {
		t.Fatalf("settings = %+v", settings)
	}
	if sources[SettingPeerListen] != SourceFlag || sources[SettingAllowLAN] != SourceFlag {
		t.Fatalf("sources = %v", sources)
	}
	// The flag said nothing about discovery, so the remembered answer stands
	// and is labelled as remembered rather than as something just typed.
	if !settings.Discover || sources[SettingDiscover] != SourceRemembered {
		t.Fatalf("discover = %t from %q", settings.Discover, sources[SettingDiscover])
	}
	if sources[SettingAutoWake] != SourceDefault || settings.AutoWake {
		t.Fatalf("auto-wake = %t from %q", settings.AutoWake, sources[SettingAutoWake])
	}
}

// A remembered switch that is off is an answer somebody gave, not an absence.
// If false were read as "nothing stored", -allow-lan could never be turned back
// off except by a flag on every start, which is the failure being removed.
func TestResolveKeepsARememberedFalseApart(t *testing.T) {
	settings, sources := Resolve(Partial{}, Partial{AllowLAN: boolOf(false)}, DefaultSettings())
	if settings.AllowLAN {
		t.Fatalf("allowLan = true from a remembered false")
	}
	if sources[SettingAllowLAN] != SourceRemembered {
		t.Fatalf("a stored false is reported as %q, not %q", sources[SettingAllowLAN], SourceRemembered)
	}
	// And nothing stored at all is the default, which is a different fact.
	_, empty := Resolve(Partial{}, Partial{}, DefaultSettings())
	if empty[SettingAllowLAN] != SourceDefault {
		t.Fatalf("an empty store is reported as %q", empty[SettingAllowLAN])
	}
}

// -treat-as-private replaces the whole declaration. Merging would leave the
// owner believing they had withdrawn a range that is in fact still trusted.
func TestResolveReplacesTheWholePrivateDeclaration(t *testing.T) {
	remembered := Partial{TreatAsPrivate: listOf("10.9.0.0/16", "122.122.0.0/16")}
	settings, sources := Resolve(Partial{TreatAsPrivate: listOf("172.20.0.0/16")}, remembered, DefaultSettings())

	if len(settings.TreatAsPrivate) != 1 || settings.TreatAsPrivate[0] != "172.20.0.0/16" {
		t.Fatalf("treatAsPrivate = %v; the remembered ranges were merged in rather than replaced", settings.TreatAsPrivate)
	}
	if sources[SettingTreatAsPrivate] != SourceFlag {
		t.Fatalf("source = %q", sources[SettingTreatAsPrivate])
	}
	// An empty declaration given on purpose is still a declaration: it clears.
	cleared, clearedSources := Resolve(Partial{TreatAsPrivate: listOf()}, remembered, DefaultSettings())
	if len(cleared.TreatAsPrivate) != 0 {
		t.Fatalf("an empty declaration left %v", cleared.TreatAsPrivate)
	}
	if clearedSources[SettingTreatAsPrivate] != SourceFlag {
		t.Fatalf("clearing is reported as %q", clearedSources[SettingTreatAsPrivate])
	}
}

// Nothing given anywhere is the behaviour every build before these settings
// existed had: loopback, no discovery, no waking, nothing declared private.
func TestResolveFallsBackToTheFlagDefaults(t *testing.T) {
	settings, sources := Resolve(Partial{}, Partial{}, DefaultSettings())
	if settings.PeerListen != "127.0.0.1:7463" || settings.AllowLAN || settings.Discover || settings.AutoWake ||
		len(settings.TreatAsPrivate) != 0 {
		t.Fatalf("settings = %+v", settings)
	}
	for _, field := range SettingNames {
		if sources[field] != SourceDefault {
			t.Fatalf("%s came from %q", field, sources[field])
		}
	}
}

// The API and the node must refuse the same configuration, or a desktop can
// save an address that stops the service from starting.
func TestSettingsValidateAppliesTheNodesOwnRules(t *testing.T) {
	if _, err := (Settings{PeerListen: "192.168.1.10:7463"}).Validate(); err == nil {
		t.Fatal("a LAN peer listener was accepted without allowLan")
	}
	if _, err := (Settings{PeerListen: "192.168.1.10:7463", AllowLAN: true}).Validate(); err != nil {
		t.Fatalf("a private address with allowLan was refused: %v", err)
	}
	ranges, err := (Settings{
		PeerListen: "122.122.0.1:7463", AllowLAN: true, TreatAsPrivate: []string{"122.122.0.0/16"},
	}).Validate()
	if err != nil {
		t.Fatalf("a declared range was not honoured: %v", err)
	}
	if len(ranges) != 1 {
		t.Fatalf("ranges = %v", ranges)
	}
	if _, err := (Settings{PeerListen: "127.0.0.1:7463", TreatAsPrivate: []string{"not-a-cidr"}}).Validate(); err == nil {
		t.Fatal("a declaration that is not a CIDR block was accepted")
	}
}

// The start-up log is the only place a remembered -allow-lan appears: nothing
// on the command line mentions it, and it is the one switch here that lets
// data leave the machine.
func TestDescribeNamesEverySettingAndItsSource(t *testing.T) {
	settings := Settings{PeerListen: "192.168.1.10:7463", AllowLAN: true, TreatAsPrivate: []string{"10.0.0.0/8"}}
	lines := strings.Join(Describe(settings, map[string]string{
		SettingAllowLAN: SourceRemembered, SettingPeerListen: SourceFlag,
	}), "\n")
	for _, want := range []string{
		"peer-listen = 192.168.1.10:7463 (flag)",
		"allow-lan = true (remembered)",
		"discover = false (default)",
		"treat-as-private = 10.0.0.0/8 (default)",
		"auto-wake = false (default)",
	} {
		if !strings.Contains(lines, want) {
			t.Errorf("the start-up description lacks %q:\n%s", want, lines)
		}
	}
}
