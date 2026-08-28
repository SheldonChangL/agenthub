//go:build windows

package identity

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// On Windows the file mode is not the protection.
//
// Go documents that Chmod on Windows only uses the 0200 bit, for the read-only
// attribute; it does not produce an owner-only ACL. A node.key at "0600" there
// would be readable by anything that can reach the path, so the seed is
// encrypted to the current user with DPAPI instead and the mode is a courtesy.
//
// DPAPI is used through golang.org/x/sys/windows, already in this module's
// dependency graph for other platform calls. That keeps the guarantee at the
// OS level with no new module: the key is bound to the Windows user account, so
// copying node.key to another machine or another user's profile yields a file
// that cannot be decrypted.
const keyFileMode os.FileMode = 0o600

// keyEntropy is mixed into DPAPI protection so a blob taken from this file
// cannot be decrypted by a call that does not know it came from AgentHub. It is
// not a secret and carries no key material.
//
// Changing it makes every existing key file undecryptable, so a change must
// come with a new scheme byte: the loader can then say "written by another
// version" instead of "corrupt".
var keyEntropy = []byte("agenthub.node.key.v1")

func protectSeed(seed []byte) ([]byte, error) {
	blob, err := dpapi(windows.CryptProtectData, seed)
	if err != nil {
		return nil, fmt.Errorf("protect node key for this Windows user: %w", err)
	}
	defer zero(blob)
	return wrapProtected(schemeDPAPIUser, blob)
}

func unprotectSeed(stored []byte, path string) ([]byte, error) {
	payload, err := unwrapProtected(stored, schemeDPAPIUser)
	if err != nil {
		return nil, fmt.Errorf("node key at %s: %w", path, err)
	}
	seed, err := dpapi(unprotectData, payload)
	if err != nil {
		// Fail closed rather than regenerate. Either the file was written by
		// another Windows user or on another machine, or it is damaged; both are
		// the owner's to resolve, because a new key invalidates every pairing.
		return nil, fmt.Errorf(
			"node key at %s could not be decrypted for the current Windows user; "+
				"it belongs to another user or machine, or it is damaged: %w", path, err)
	}
	if len(seed) != ed25519.SeedSize {
		zero(seed)
		return nil, fmt.Errorf(
			"node key at %s decrypted to %d bytes, want %d", path, len(seed), ed25519.SeedSize)
	}
	return seed, nil
}

// unprotectData adapts CryptUnprotectData to the shape protect and unprotect
// share. The discarded second parameter is the display name DPAPI can return
// with a blob; this package never sets one.
func unprotectData(
	in *windows.DataBlob,
	_ *uint16,
	entropy *windows.DataBlob,
	reserved uintptr,
	prompt *windows.CryptProtectPromptStruct,
	flags uint32,
	out *windows.DataBlob,
) error {
	return windows.CryptUnprotectData(in, nil, entropy, reserved, prompt, flags, out)
}

type dpapiCall func(
	in *windows.DataBlob,
	name *uint16,
	entropy *windows.DataBlob,
	reserved uintptr,
	prompt *windows.CryptProtectPromptStruct,
	flags uint32,
	out *windows.DataBlob,
) error

// dpapi runs one DPAPI transform and copies the result out of the buffer
// Windows allocated, which must be released with LocalFree.
//
// CRYPTPROTECT_UI_FORBIDDEN is set because a node starts as a service or from a
// terminal: a prompt nobody can answer would hang the node instead of failing.
func dpapi(call dpapiCall, in []byte) ([]byte, error) {
	if len(in) == 0 {
		return nil, errors.New("refusing to run DPAPI over no data")
	}
	input := windows.DataBlob{Size: uint32(len(in)), Data: &in[0]}
	entropy := windows.DataBlob{Size: uint32(len(keyEntropy)), Data: &keyEntropy[0]}
	var out windows.DataBlob

	if err := call(&input, nil, &entropy, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	if out.Data == nil {
		return nil, errors.New("DPAPI returned no data")
	}
	defer func() { _, _ = windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data))) }()

	result := make([]byte, out.Size)
	copy(result, unsafe.Slice(out.Data, out.Size))
	// Clear the OS buffer before releasing it so a freed heap block does not
	// keep a copy of the seed.
	zero(unsafe.Slice(out.Data, out.Size))
	return result, nil
}

// checkKeyFileAccess makes no mode claim on Windows.
//
// Refusing a mode here would be theatre: Go reports a synthesised mode for
// Windows files, so the check would either always pass or reject valid keys
// for a reason that has nothing to do with who can read the file. DPAPI is the
// access control, and it is enforced when the blob is decrypted.
func checkKeyFileAccess(_ fs.FileInfo, _ string) error { return nil }

// syncDirectory is a no-op: Windows has no directory-flush call, and
// CreateHardLinkW already updates the directory through the filesystem.
func syncDirectory(string) error { return nil }
