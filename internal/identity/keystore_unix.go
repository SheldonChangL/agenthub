//go:build !windows

package identity

import (
	"fmt"
	"io/fs"
	"os"
)

// On Unix the file mode is the protection. 0600 is enforced by the kernel on
// every open, so the seed is stored raw and the mode is checked on every load.
const keyFileMode os.FileMode = 0o600

// protectSeed stores the seed as itself. There is no key to encrypt it with
// that would not have to live beside it in the same directory.
func protectSeed(seed []byte) ([]byte, error) {
	stored := make([]byte, len(seed))
	copy(stored, seed)
	return stored, nil
}

// unprotectSeed returns the stored bytes unchanged. Length is validated by the
// caller, which already reports a truncated key by size.
func unprotectSeed(stored []byte, _ string) ([]byte, error) {
	seed := make([]byte, len(stored))
	copy(seed, stored)
	return seed, nil
}

// checkKeyFileAccess refuses a key any other user can read. Anything that can
// read this file can impersonate this machine, so a key restored from a backup
// as 0644 is an error rather than a warning.
func checkKeyFileAccess(info fs.FileInfo, path string) error {
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf(
			"node key %s is mode %o; it must not be readable by anyone else", path, mode)
	}
	return nil
}

// syncDirectory makes a freshly linked name survive a power loss. Without it
// the file's content is durable but the directory entry pointing at it need
// not be, and the node would come back with no key it can find.
func syncDirectory(directory string) error {
	handle, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open key directory: %w", err)
	}
	defer func() { _ = handle.Close() }()
	if err := handle.Sync(); err != nil {
		return fmt.Errorf("flush key directory: %w", err)
	}
	return nil
}
