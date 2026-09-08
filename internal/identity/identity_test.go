package identity

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/registry"
)

func openTestRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	store, err := registry.Open(context.Background(), filepath.Join(t.TempDir(), "agenthub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

// withMachineName makes this machine answer a chosen name, so the preference
// between the machine's own name and the network's can be exercised on any
// platform. Without it these tests assert nothing on a Linux runner, which is
// where CI runs and where the real lookup returns "".
func withMachineName(t *testing.T, name string) {
	t.Helper()
	previous := machineNameLookup
	machineNameLookup = func() string { return name }
	t.Cleanup(func() { machineNameLookup = previous })
}

func TestLoadOrCreatePersistsStableIdentity(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)

	first, err := LoadOrCreate(ctx, store, "")
	if err != nil {
		t.Fatalf("first LoadOrCreate() error = %v", err)
	}
	second, err := LoadOrCreate(ctx, store, "")
	if err != nil {
		t.Fatalf("second LoadOrCreate() error = %v", err)
	}
	if first.ID == "" || first.ID != second.ID {
		t.Fatalf("identity IDs = %q, %q; want same non-empty ID", first.ID, second.ID)
	}
}

// The name this node announces is the machine's own, not the one the network
// happens to call it.
//
// On macOS with no HostName set — the default — gethostname() answers from DHCP
// and DNS. Measured on one machine: ComputerName was "sheldon.chang mac" while
// os.Hostname() returned "J-FrankieChang.jet-opto.com.tw", a previous occupant
// of that DNS record, and the node broadcast that name to the whole segment.
func TestTheMachineNameIsNotWhateverTheNetworkCallsIt(t *testing.T) {
	withMachineName(t, "sheldon.chang mac")
	hostname, _ := os.Hostname()
	if got := MachineName(); got != "sheldon.chang mac" {
		t.Errorf("MachineName() = %q, want the machine's own name rather than the network's %q",
			got, hostname)
	}

	// With nothing of its own to say, the network's name is better than none.
	withMachineName(t, "")
	if hostname != "" {
		if got := MachineName(); got != model.PrintableLabel(hostname) {
			t.Errorf("MachineName() = %q, want the hostname %q when the machine has no name of its own",
				got, hostname)
		}
	}
}

// Whatever MachineName returns must be announceable, because it is announced.
// A name the announcement drops leaves the node with no name on the wire — a
// failure with no error anywhere, visible only on somebody else's screen.
func TestTheMachineNameIsAlwaysAnnounceable(t *testing.T) {
	for _, machine := range map[string]string{
		"this machine's real name": "",
		"far over the bound":       strings.Repeat("三", 60),
		"one byte over":            strings.Repeat("a", model.MaxLabelLength+1),
		"nothing that renders":     "​​​",
		"a control character":      "lab\x07top",
		"invalid UTF-8":            "\xff\xfe",
		"needs normalising":        "my  mac",
		"an emoji with a selector": "☕️ mac",
		"nothing at all":           "   ",
	} {
		withMachineName(t, machine)
		name := MachineName()
		if name == "" {
			t.Errorf("MachineName() is empty for %q; the node would announce nothing identifying", machine)
			continue
		}
		if model.PrintableLabel(name) != name {
			t.Errorf("MachineName() = %q for machine name %q, which the announcement would %s",
				name, machine, describeRefusal(name))
		}
		if len(name) > MaxDisplayName || !utf8.ValidString(name) {
			t.Errorf("MachineName() = %q (%d bytes) for machine name %q", name, len(name), machine)
		}
	}
}

func describeRefusal(name string) string {
	if clean := model.PrintableLabel(name); clean != "" {
		return "rewrite to " + clean
	}
	return "drop"
}

// A long name is shortened rather than discarded: a truncated name still says
// which machine this is, and "agenthub-node" does not.
func TestALongMachineNameIsShortenedNotDiscarded(t *testing.T) {
	// 60 CJK characters is 180 bytes, well over the bound, and every cut inside
	// the limit lands in the middle of a rune.
	withMachineName(t, strings.Repeat("三", 60))
	name := MachineName()
	if name == "agenthub-node" {
		t.Fatal("a long name was discarded; the node lost the one thing identifying it")
	}
	if !strings.HasPrefix(strings.Repeat("三", 60), name) {
		t.Errorf("MachineName() = %q, which is not a prefix of the name it shortened", name)
	}
	if !utf8.ValidString(name) {
		t.Errorf("MachineName() = %q, which is not valid UTF-8: the cut went through a rune", name)
	}
	if len(name) > MaxDisplayName {
		t.Errorf("MachineName() = %d bytes, over the %d an announcement carries", len(name), MaxDisplayName)
	}
}

// The bug this change exists for: a node created before the fix already has the
// wrong name stored, and correcting only newly created nodes leaves it
// announcing a stranger's DNS name forever.
func TestAnExistingNodeThatNobodyNamedIsCorrected(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)

	withMachineName(t, "")
	// Stand in for a node created by the earlier build, whose only source was
	// os.Hostname().
	started, err := LoadOrCreate(ctx, store, "")
	if err != nil {
		t.Fatal(err)
	}
	if started.NameIsChosen {
		t.Fatal("a name read off the machine was recorded as one somebody picked")
	}

	// The machine now reports its own name, as it would after this change ships.
	withMachineName(t, "sheldon.chang mac")
	corrected, err := LoadOrCreate(ctx, store, "")
	if err != nil {
		t.Fatal(err)
	}
	if corrected.DisplayName != "sheldon.chang mac" {
		t.Errorf("display name = %q; an upgraded node still announces the name it was created with",
			corrected.DisplayName)
	}
	if corrected.ID != started.ID {
		t.Errorf("node id changed from %q to %q; the correction broke every pairing",
			started.ID, corrected.ID)
	}
	if corrected.NameIsChosen {
		t.Error("a corrected name was recorded as chosen, so it would stop following the machine")
	}
}

// A name a person picked is theirs. It survives the machine being renamed, and
// every restart after it.
func TestAChosenNameIsNotRevised(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)

	withMachineName(t, "sheldon.chang mac")
	if _, err := LoadOrCreate(ctx, store, ""); err != nil {
		t.Fatal(err)
	}
	picked, err := LoadOrCreate(ctx, store, "the machine on my desk")
	if err != nil {
		t.Fatal(err)
	}
	if picked.DisplayName != "the machine on my desk" {
		t.Fatalf("display name = %q, want the chosen one", picked.DisplayName)
	}
	if !picked.NameIsChosen {
		t.Fatal("a name given on the command line was not recorded as chosen")
	}

	// The machine is renamed. The owner's choice stands.
	withMachineName(t, "a completely different name")
	kept, err := LoadOrCreate(ctx, store, "")
	if err != nil {
		t.Fatal(err)
	}
	if kept.DisplayName != "the machine on my desk" {
		t.Errorf("display name = %q; the machine's name overwrote the owner's", kept.DisplayName)
	}
	if kept.ID != picked.ID {
		t.Errorf("node id changed from %q to %q", picked.ID, kept.ID)
	}
}

// A chosen name the announcement would not carry is refused while the person
// who typed it is still there to be told — not stored, printed as announced,
// and then dropped from the TXT record with nowhere to report it.
func TestAChosenNameThatCouldNotBeAnnouncedIsRefused(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	withMachineName(t, "sheldon.chang mac")

	// 22 CJK characters is 66 bytes: under the trust store's 128, over the 64
	// an announcement carries, and an unremarkable name in this product's own
	// UI language.
	tooLong := strings.Repeat("三", 22)
	if len(tooLong) <= MaxDisplayName {
		t.Fatalf("the fixture is %d bytes and does not cross the %d bound it exists to cross",
			len(tooLong), MaxDisplayName)
	}
	if _, err := LoadOrCreate(ctx, store, tooLong); err == nil {
		t.Error("a name the announcement would silently drop was accepted")
	}
	if _, err := LoadOrCreate(ctx, store, "my  mac"); err == nil {
		t.Error("a name that would be announced in a different shape was accepted as typed")
	}
	if _, err := LoadOrCreate(ctx, store, strings.Repeat("a", MaxDisplayName)); err != nil {
		t.Errorf("a name of exactly the maximum was refused: %v", err)
	}

	// Nothing was half-applied on the way to those refusals.
	if got := storedName(t, store); got != strings.Repeat("a", MaxDisplayName) {
		t.Errorf("display name = %q after the refusals", got)
	}

	// And the same on the rename path. The check above ran while the store was
	// empty, so it only proves the create path refuses; renaming an existing
	// node is the other half, and the one an owner actually reaches.
	if _, err := LoadOrCreate(ctx, store, tooLong); err == nil {
		t.Error("an existing node was renamed to something no announcement would carry")
	}
	if got := storedName(t, store); got != strings.Repeat("a", MaxDisplayName) {
		t.Errorf("display name = %q; the refused rename was applied anyway", got)
	}
}

func storedName(t *testing.T, store *registry.Registry) string {
	t.Helper()
	identity, err := store.GetNodeIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return identity.DisplayName
}
