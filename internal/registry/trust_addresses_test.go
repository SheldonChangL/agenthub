package registry

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const addressedNode = "node_peer0000000000000"

func trustedWithAddresses(t *testing.T) *Registry {
	t.Helper()
	store := openTestRegistry(t)
	if err := store.TrustNode(context.Background(), peer(addressedNode, "key-a")); err != nil {
		t.Fatal(err)
	}
	return store
}

func addressesOf(t *testing.T, store *Registry) (string, []string) {
	t.Helper()
	node, err := store.TrustedNode(context.Background(), addressedNode)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := store.TrustedNodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Both readers decode the column; a test that read only one would let the
	// other drift.
	if len(listed) != 1 || listed[0].Address != node.Address ||
		!reflect.DeepEqual(listed[0].Alternates, node.Alternates) {
		t.Fatalf("TrustedNodes = %+v, TrustedNode = %+v; the two readers disagree", listed, node)
	}
	return node.Address, node.Alternates
}

func wantAddresses(t *testing.T, store *Registry, preferred string, alternates ...string) {
	t.Helper()
	gotPreferred, gotAlternates := addressesOf(t, store)
	if len(alternates) == 0 {
		alternates = nil
	}
	if gotPreferred != preferred || !reflect.DeepEqual(gotAlternates, alternates) {
		t.Fatalf("addresses = %q %q; want %q %q", gotPreferred, gotAlternates, preferred, alternates)
	}
}

func allowAll(string) error { return nil }

// A database paired by a build that predates alternates gains the column, and
// the address it held stays the preferred one with nothing invented beside it.
func TestAnOlderTrustStoreGainsTheAlternatesColumn(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.ExecContext(ctx, `
CREATE TABLE trusted_nodes (
    node_id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    platform TEXT NOT NULL,
    public_key TEXT NOT NULL,
    fingerprint TEXT NOT NULL,
    paired_at_ms INTEGER NOT NULL,
    last_seen_at_ms INTEGER NOT NULL DEFAULT 0,
    address TEXT NOT NULL DEFAULT ''
);
INSERT INTO trusted_nodes VALUES
    ('`+addressedNode+`', 'peer', 'linux/amd64', 'key-a', 'FP', 1, 0, '192.168.1.20:7463');
`); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() on a database from before alternates: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	wantAddresses(t, store, "192.168.1.20:7463")

	if err := store.SetNodeAddress(ctx, addressedNode, "10.0.0.5:7463"); err != nil {
		t.Fatalf("SetNodeAddress on the migrated row: %v", err)
	}
	wantAddresses(t, store, "10.0.0.5:7463", "192.168.1.20:7463")
}

// Opening twice must not try to add the column a second time.
func TestTheAlternatesMigrationRunsOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agenthub.db")
	for range 2 {
		store, err := Open(ctx, path)
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		_ = store.Close()
	}
}

// ADR-005 §4: the new address is preferred, the old preferred becomes the
// first alternate, and an address already known moves rather than repeats.
func TestSetNodeAddressKeepsTheOldPreferredAsAnAlternate(t *testing.T) {
	ctx := context.Background()
	store := trustedWithAddresses(t)

	steps := []struct {
		set        string
		preferred  string
		alternates []string
	}{
		{"192.168.1.20:7463", "192.168.1.20:7463", nil},
		{"10.0.0.5:7463", "10.0.0.5:7463", []string{"192.168.1.20:7463"}},
		// Setting the preferred again changes nothing.
		{"10.0.0.5:7463", "10.0.0.5:7463", []string{"192.168.1.20:7463"}},
		// An alternate set as preferred leaves the list, it does not repeat.
		{"192.168.1.20:7463", "192.168.1.20:7463", []string{"10.0.0.5:7463"}},
		{"10.0.0.6:7463", "10.0.0.6:7463", []string{"192.168.1.20:7463", "10.0.0.5:7463"}},
		{"10.0.0.7:7463", "10.0.0.7:7463", []string{"10.0.0.6:7463", "192.168.1.20:7463", "10.0.0.5:7463"}},
		// A fifth address pushes out the oldest: four in all.
		{"10.0.0.8:7463", "10.0.0.8:7463", []string{"10.0.0.7:7463", "10.0.0.6:7463", "192.168.1.20:7463"}},
	}
	for index, step := range steps {
		if err := store.SetNodeAddress(ctx, addressedNode, step.set); err != nil {
			t.Fatalf("step %d: SetNodeAddress(%q): %v", index, step.set, err)
		}
		preferred, alternates := addressesOf(t, store)
		if preferred != step.preferred || !reflect.DeepEqual(alternates, step.alternates) {
			t.Fatalf("step %d: after SetNodeAddress(%q) addresses = %q %q; want %q %q",
				index, step.set, preferred, alternates, step.preferred, step.alternates)
		}
	}
}

// "" means "I no longer know where this peer is", which has to include the
// alternates: a cleared node that still dialled its old second address would
// not be cleared.
func TestSetNodeAddressEmptyClearsTheAlternatesToo(t *testing.T) {
	ctx := context.Background()
	store := trustedWithAddresses(t)
	if err := store.SetNodeAddresses(ctx, addressedNode,
		[]string{"10.0.0.5:7463", "192.168.1.20:7463"}, allowAll); err != nil {
		t.Fatal(err)
	}
	if err := store.SetNodeAddress(ctx, addressedNode, ""); err != nil {
		t.Fatal(err)
	}
	wantAddresses(t, store, "")
	node, err := store.TrustedNode(ctx, addressedNode)
	if err != nil {
		t.Fatal(err)
	}
	if node.PublicKey != "key-a" {
		t.Fatalf("clearing the address touched trust: %+v", node)
	}
}

func TestSetNodeAddressesReplacesTheWholeSet(t *testing.T) {
	ctx := context.Background()
	store := trustedWithAddresses(t)
	if err := store.SetNodeAddress(ctx, addressedNode, "10.9.9.9:7463"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetNodeAddresses(ctx, addressedNode,
		[]string{" 10.0.0.5:7463", "192.168.1.20:7463", "10.0.0.5:7463"}, allowAll); err != nil {
		t.Fatal(err)
	}
	// Replaced, not merged: the earlier preferred is gone, and the repeat is
	// dropped rather than counted.
	wantAddresses(t, store, "10.0.0.5:7463", "192.168.1.20:7463")

	if err := store.SetNodeAddresses(ctx, addressedNode, nil, allowAll); err != nil {
		t.Fatal(err)
	}
	wantAddresses(t, store, "")
}

func TestSetNodeAddressesRefusesMoreThanFour(t *testing.T) {
	ctx := context.Background()
	store := trustedWithAddresses(t)
	four := []string{"10.0.0.1:7463", "10.0.0.2:7463", "10.0.0.3:7463", "10.0.0.4:7463"}
	if err := store.SetNodeAddresses(ctx, addressedNode, four, allowAll); err != nil {
		t.Fatalf("four addresses refused: %v", err)
	}
	wantAddresses(t, store, four[0], four[1:]...)

	err := store.SetNodeAddresses(ctx, addressedNode, append(four, "10.0.0.5:7463"), allowAll)
	if !errors.Is(err, ErrInvalidSession) {
		t.Fatalf("five addresses: error = %v; want ErrInvalidSession", err)
	}
	// Refused whole: the four already stored are untouched.
	wantAddresses(t, store, four[0], four[1:]...)
}

func TestSetNodeAddressesAppliesThePolicyToEveryAddress(t *testing.T) {
	ctx := context.Background()
	store := trustedWithAddresses(t)
	if err := store.SetNodeAddress(ctx, addressedNode, "10.0.0.1:7463"); err != nil {
		t.Fatal(err)
	}
	policy := func(address string) error {
		if strings.HasPrefix(address, "203.0.113.") {
			return fmt.Errorf("peer address %q is outside the private network ranges", address)
		}
		return nil
	}
	cases := map[string][]string{
		"a public alternate":       {"10.0.0.5:7463", "203.0.113.9:7463"},
		"a public preferred":       {"203.0.113.9:7463", "10.0.0.5:7463"},
		"not host:port":            {"10.0.0.5:7463", "10.0.0.6"},
		"an empty entry":           {"10.0.0.5:7463", " "},
		"no policy to check with":  nil,
		"nil policy with an entry": {"10.0.0.5:7463"},
	}
	for name, addresses := range cases {
		t.Run(name, func(t *testing.T) {
			check := policy
			if strings.Contains(name, "policy") {
				check = nil
			}
			err := store.SetNodeAddresses(ctx, addressedNode, addresses, check)
			if !errors.Is(err, ErrInvalidSession) {
				t.Fatalf("error = %v; want ErrInvalidSession", err)
			}
			wantAddresses(t, store, "10.0.0.1:7463")
		})
	}
}

func TestSetNodeAddressesNeverCreatesARow(t *testing.T) {
	store := openTestRegistry(t)
	err := store.SetNodeAddresses(context.Background(), "node_stranger00000000",
		[]string{"10.0.0.5:7463"}, allowAll)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("error = %v; want ErrNotFound", err)
	}
}

func TestPromoteNodeAddressSwapsTheWorkingAlternateIn(t *testing.T) {
	ctx := context.Background()
	store := trustedWithAddresses(t)
	if err := store.SetNodeAddresses(ctx, addressedNode,
		[]string{"10.0.0.1:7463", "10.0.0.2:7463", "10.0.0.3:7463"}, allowAll); err != nil {
		t.Fatal(err)
	}
	if err := store.PromoteNodeAddress(ctx, addressedNode, "10.0.0.3:7463"); err != nil {
		t.Fatal(err)
	}
	wantAddresses(t, store, "10.0.0.3:7463", "10.0.0.1:7463", "10.0.0.2:7463")

	// Already preferred: nothing moves.
	if err := store.PromoteNodeAddress(ctx, addressedNode, "10.0.0.3:7463"); err != nil {
		t.Fatal(err)
	}
	wantAddresses(t, store, "10.0.0.3:7463", "10.0.0.1:7463", "10.0.0.2:7463")

	// No longer known — the owner replaced the set while the dial ran — so it
	// is not put back.
	if err := store.PromoteNodeAddress(ctx, addressedNode, "10.0.0.9:7463"); err != nil {
		t.Fatal(err)
	}
	wantAddresses(t, store, "10.0.0.3:7463", "10.0.0.1:7463", "10.0.0.2:7463")

	if err := store.PromoteNodeAddress(ctx, "node_stranger00000000", "10.0.0.1:7463"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("promote on an unknown node: error = %v; want ErrNotFound", err)
	}
}

// An older build writes address alone. If it wrote an address the alternates
// already held, the new build must not read that address twice.
func TestAnAlternateThatRepeatsThePreferredIsNotReadTwice(t *testing.T) {
	ctx := context.Background()
	store := trustedWithAddresses(t)
	if err := store.SetNodeAddresses(ctx, addressedNode,
		[]string{"10.0.0.1:7463", "10.0.0.2:7463"}, allowAll); err != nil {
		t.Fatal(err)
	}
	// What an older build's SetNodeAddress does.
	if _, err := store.db.ExecContext(ctx,
		`UPDATE trusted_nodes SET address = '10.0.0.2:7463' WHERE node_id = ?`, addressedNode); err != nil {
		t.Fatal(err)
	}
	wantAddresses(t, store, "10.0.0.2:7463")

	// A column that does not decode reads as no alternates, not as an error.
	if _, err := store.db.ExecContext(ctx,
		`UPDATE trusted_nodes SET alternate_addresses = 'not json' WHERE node_id = ?`, addressedNode); err != nil {
		t.Fatal(err)
	}
	wantAddresses(t, store, "10.0.0.2:7463")
}

// An older build clears a node by writing address alone. The alternates it
// leaves behind are what the owner cleared, so they are not read, and not
// revived when an address is set again.
func TestAnOlderBuildsClearedAddressClearsTheAlternates(t *testing.T) {
	ctx := context.Background()
	store := trustedWithAddresses(t)
	if err := store.SetNodeAddresses(ctx, addressedNode,
		[]string{"10.0.0.1:7463", "10.0.0.2:7463"}, allowAll); err != nil {
		t.Fatal(err)
	}
	// What an older build's SetNodeAddress(ctx, id, "") does.
	if _, err := store.db.ExecContext(ctx,
		`UPDATE trusted_nodes SET address = '' WHERE node_id = ?`, addressedNode); err != nil {
		t.Fatal(err)
	}
	wantAddresses(t, store, "")

	if err := store.SetNodeAddress(ctx, addressedNode, "192.168.1.9:7463"); err != nil {
		t.Fatal(err)
	}
	wantAddresses(t, store, "192.168.1.9:7463")
}
