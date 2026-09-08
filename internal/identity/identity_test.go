package identity

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

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

func TestLoadOrCreatePersistsStableIdentity(t *testing.T) {
	ctx := context.Background()
	store, err := registry.Open(ctx, filepath.Join(t.TempDir(), "agenthub.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

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

// The name this node announces must be the machine's own, not the one the
// network happens to call it.
//
// On macOS with no HostName set — the default — gethostname() answers from DHCP
// and DNS. Measured on one machine: ComputerName was "sheldon.chang mac" while
// os.Hostname() returned "J-FrankieChang.jet-opto.com.tw", a previous occupant
// of that DNS record, and the node broadcast that name to the whole segment.
func TestTheMachineNameIsNotWhateverTheNetworkCallsIt(t *testing.T) {
	name := MachineName()
	if name == "" {
		t.Fatal("a node with no name would announce nothing identifying at all")
	}
	if len(name) > MaxDisplayName {
		t.Errorf("MachineName() is %d characters; a peer refuses more than %d",
			len(name), MaxDisplayName)
	}
	if !utf8.ValidString(name) {
		t.Errorf("MachineName() = %q, which is not valid UTF-8", name)
	}

	// On a machine whose own name and network name differ, the machine's wins.
	// Where they are the same this proves nothing, so it says so.
	if runtime.GOOS != "darwin" {
		t.Skipf("the fallback-to-DNS behaviour this guards is macOS's; %s answers from the machine",
			runtime.GOOS)
	}
	own := localMachineName()
	if own == "" {
		t.Skip("this machine reports no name of its own, so there is nothing to prefer")
	}
	hostname, _ := os.Hostname()
	if own == hostname {
		t.Skipf("this machine's own name and its network name agree (%q), so the preference "+
			"is not exercised here", own)
	}
	if name != own {
		t.Errorf("MachineName() = %q, want the machine's own name %q rather than the network's %q",
			name, own, hostname)
	}
}

// A name given on the command line replaces a stored one. A node whose name was
// wrong from the day it started needs a way to correct it, and pairing survives
// the change because trust is keyed on the node id.
func TestAChosenNameReplacesTheStoredOne(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)

	first, err := LoadOrCreate(ctx, store, "")
	if err != nil {
		t.Fatal(err)
	}
	if first.DisplayName == "" {
		t.Fatal("a new node was created with no name")
	}

	renamed, err := LoadOrCreate(ctx, store, "the machine on my desk")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.DisplayName != "the machine on my desk" {
		t.Errorf("display name = %q, want the chosen one", renamed.DisplayName)
	}
	if renamed.ID != first.ID {
		t.Errorf("the node id changed from %q to %q; a rename would break every pairing",
			first.ID, renamed.ID)
	}

	// And it sticks, so the flag is not needed on every start.
	reopened, err := LoadOrCreate(ctx, store, "")
	if err != nil {
		t.Fatal(err)
	}
	if reopened.DisplayName != "the machine on my desk" {
		t.Errorf("display name = %q after a restart without the flag", reopened.DisplayName)
	}
}

// A name a peer would refuse is refused here, rather than stored and then
// dropped silently from every announcement.
func TestANameTooLongForAPeerIsRefused(t *testing.T) {
	ctx := context.Background()
	store := openTestRegistry(t)
	if _, err := LoadOrCreate(ctx, store, strings.Repeat("a", MaxDisplayName+1)); err == nil {
		t.Error("a name longer than any peer accepts was stored")
	}
	// The bound itself is usable.
	if _, err := LoadOrCreate(ctx, store, strings.Repeat("a", MaxDisplayName)); err != nil {
		t.Errorf("a name of exactly the maximum was refused: %v", err)
	}
}

// A machine name over the bound is cut on a rune boundary.
//
// Tested directly because no machine here has a name that long, so the branch
// is never reached by TestTheMachineNameIsNotWhateverTheNetworkCallsIt. A cut
// through the middle of a multi-byte rune produces a name that is not valid
// UTF-8, which the announcement would carry to every peer on the segment.
func TestALongMachineNameIsCutOnARuneBoundary(t *testing.T) {
	// 三 is three bytes, so 43 of them is 129: one byte over, and the cut at
	// MaxDisplayName lands inside the last rune rather than between two.
	long := strings.Repeat("三", 43)
	if len(long) <= MaxDisplayName {
		t.Fatalf("the fixture is %d bytes, which does not exceed the %d limit it exists to cross",
			len(long), MaxDisplayName)
	}
	cut := truncateName(long)
	if len(cut) > MaxDisplayName {
		t.Errorf("truncateName() returned %d bytes, over the %d a peer accepts", len(cut), MaxDisplayName)
	}
	if !utf8.ValidString(cut) {
		t.Errorf("truncateName() = %q, which is not valid UTF-8", cut)
	}
	if cut == "" {
		t.Error("truncateName() emptied a name that had 42 whole runes to keep")
	}
	if !strings.HasPrefix(long, cut) {
		t.Errorf("truncateName() = %q, which is not a prefix of the name it shortened", cut)
	}

	// A name already inside the bound is returned untouched, bytes and all.
	if got := truncateName("三四五"); got != "三四五" {
		t.Errorf("truncateName(%q) = %q; a short name must not be altered", "三四五", got)
	}
}
