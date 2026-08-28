//go:build !windows

package identity

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"
)

// On Unix the mode is the protection, so the stored form is the bare seed and
// the mode is part of the contract rather than a decoration.
func TestUnixKeyIsARawSeedInAnOwnerOnlyFile(t *testing.T) {
	directory := t.TempDir()
	if _, err := LoadOrCreateKeypair(directory); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(directory, keyFileName))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("node key mode = %o, want 600", mode)
	}
	if info.Size() != ed25519.SeedSize {
		t.Errorf("node key is %d bytes, want the %d-byte seed", info.Size(), ed25519.SeedSize)
	}
}

// A key restored from a backup as 0644 must not be used silently: anything that
// can read it can impersonate this machine.
func TestUnixLoadRefusesAKeyOthersCanRead(t *testing.T) {
	for name, mode := range map[string]os.FileMode{
		"group readable": 0o640,
		"world readable": 0o604,
		"world writable": 0o602,
		"wide open":      0o666,
	} {
		t.Run(name, func(t *testing.T) {
			directory := t.TempDir()
			if _, err := LoadOrCreateKeypair(directory); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(directory, keyFileName), mode); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadOrCreateKeypair(directory); err == nil {
				t.Errorf("a node key at mode %o was accepted", mode)
			}
		})
	}
}

// A directory that cannot be written must fail rather than half-create a key.
func TestUnixCreationInAnUnwritableDirectoryLeavesNothing(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	directory := filepath.Join(t.TempDir(), "keys")
	if err := os.Mkdir(directory, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })

	if _, err := LoadOrCreateKeypair(directory); err == nil {
		t.Fatal("created a key in a directory that cannot be written")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("failed creation left %d entries behind, want none", len(entries))
	}
}
