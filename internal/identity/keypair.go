package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Keypair is this node's signing identity.
//
// The node ID is a random label and proves nothing: anyone can claim one. A
// peer that is about to trust this node needs something only this node can
// produce, so pairing and every signed envelope rest on this key.
type Keypair struct {
	Public  ed25519.PublicKey
	private ed25519.PrivateKey
}

// Sign returns a detached signature over message.
func (k Keypair) Sign(message []byte) []byte {
	return ed25519.Sign(k.private, message)
}

// Fingerprint renders the public key as six groups of four hex digits.
//
// The grouping exists to be read aloud and compared by eye across two machines
// during pairing; a wall of hex invites people to check the first characters
// and skip the rest.
func (k Keypair) Fingerprint() string {
	return Fingerprint(k.Public)
}

// Fingerprint derives the displayed fingerprint of any public key.
func Fingerprint(public ed25519.PublicKey) string {
	digest := sha256.Sum256(public)
	groups := make([]string, 0, 6)
	for i := 0; i < 6; i++ {
		groups = append(groups, fmt.Sprintf("%02X%02X", digest[i*2], digest[i*2+1]))
	}
	return strings.Join(groups, " ")
}

// EncodePublicKey renders a public key for transport and storage.
func EncodePublicKey(public ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(public)
}

// DecodePublicKey parses a public key received from a peer. Peer input is
// untrusted, so a wrong length is refused rather than padded or truncated.
func DecodePublicKey(encoded string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode public key: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public key is %d bytes, want %d", len(raw), ed25519.PublicKeySize)
	}
	return ed25519.PublicKey(raw), nil
}

// keyFileName is the only name this package reads or writes. Both the raw-seed
// and the protected form live under it, so a platform change is not a rename.
const keyFileName = "node.key"

// LoadOrCreateKeypair reads the node's signing key, generating one on first use.
//
// The key lives beside the database rather than inside it: a database is copied,
// backed up and inspected far more casually than a file named private key, and
// a copied database that carried the key would clone the node's identity.
//
// How the file protects the seed is the platform's business, not this
// function's. Unix stores the raw seed and relies on 0600; Windows encrypts it
// to the current user, because Go documents that Chmod there does not produce
// an owner-only ACL and a 0600 that means nothing is worse than no claim.
func LoadOrCreateKeypair(directory string) (Keypair, error) {
	if directory == "" {
		return Keypair{}, errors.New("a directory is required for the node key")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return Keypair{}, fmt.Errorf("create key directory: %w", err)
	}
	path := filepath.Join(directory, keyFileName)

	stored, err := readKeyFile(path)
	switch {
	case err == nil:
		return keypairFromStored(stored, path)
	case errors.Is(err, fs.ErrNotExist):
	default:
		return Keypair{}, err
	}

	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return Keypair{}, fmt.Errorf("generate node key: %w", err)
	}
	defer zero(seed)

	// The bytes that reach disk are formed in full before the file exists, so
	// there is no window in which node.key holds half an identity.
	protected, err := protectSeed(seed)
	if err != nil {
		return Keypair{}, err
	}
	defer zero(protected)

	if err := installKeyFile(directory, path, protected); err != nil {
		if errors.Is(err, fs.ErrExist) {
			// A concurrent start won the race. Its key is the node's identity;
			// read that one rather than reporting an error the owner cannot act
			// on, and never overwrite it.
			existing, readErr := readKeyFile(path)
			if readErr != nil {
				return Keypair{}, readErr
			}
			return keypairFromStored(existing, path)
		}
		return Keypair{}, err
	}
	return keypairFromSeed(seed, path)
}

// readKeyFile returns the stored bytes only for a plain, owner-only regular
// file. Every other shape fails closed: a symlink or a Windows reparse point
// under node.key means something else chose where the identity is read from,
// and a directory or device there means the path is not what this node wrote.
func readKeyFile(path string) ([]byte, error) {
	// Lstat before opening so a link is refused rather than followed. The open
	// below re-checks through the handle, which is what closes the gap between
	// the two calls.
	linkInfo, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("inspect node key: %w", err)
	}
	if !linkInfo.Mode().IsRegular() {
		return nil, fmt.Errorf(
			"node key %s is not a regular file (mode %s); refusing to read it", path, linkInfo.Mode())
	}

	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("open node key: %w", err)
	}
	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat node key: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf(
			"node key %s is not a regular file (mode %s); refusing to read it", path, info.Mode())
	}
	// Checked on every load, not only at creation: a key restored from a backup
	// as 0644 would otherwise be used silently.
	if err := checkKeyFileAccess(info, path); err != nil {
		return nil, err
	}
	// A key file is 32 bytes on Unix and a few hundred on Windows. A larger one
	// is not a key, and reading it whole is how a huge file becomes a hang.
	if info.Size() > maxKeyFileSize {
		return nil, fmt.Errorf(
			"node key %s is %d bytes, far larger than any key file; refusing to read it",
			path, info.Size())
	}

	stored, err := io.ReadAll(io.LimitReader(file, maxKeyFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("read node key: %w", err)
	}
	return stored, nil
}

// maxKeyFileSize bounds what this package will read into memory. A DPAPI blob
// over a 32-byte seed is well under a kilobyte.
const maxKeyFileSize = 64 * 1024

// installKeyFile writes protected to path, creating it only if absent.
//
// The content is written and synced to a temporary file first and only then
// linked to the final name, because linking is atomic and fails if the name
// exists. That ordering is what makes a failed install harmless: an ordinary
// failure — full disk, write error, lost race — leaves the final name either
// absent or holding a complete key, never a truncated or empty one that every
// later start would refuse.
func installKeyFile(directory, path string, protected []byte) error {
	temporary, err := os.CreateTemp(directory, ".node.key-*")
	if err != nil {
		return fmt.Errorf("create temporary node key: %w", err)
	}
	temporaryPath := temporary.Name()
	// Removed on every path, including success: after linking, the final name
	// is a second name for the same content and the temporary one is litter.
	defer func() { _ = os.Remove(temporaryPath) }()

	if err := writeAndSync(temporary, protected); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary node key: %w", err)
	}

	switch linkErr := os.Link(temporaryPath, path); {
	case linkErr == nil:
	case errors.Is(linkErr, fs.ErrExist):
		// Report the bare sentinel: the caller reads the winner's key, and a
		// wrapped *LinkError here would make that check depend on formatting.
		return fs.ErrExist
	default:
		// Not every filesystem supports hard links, and the errno for that
		// varies. Rather than enumerate them, fall back whenever linking failed
		// for a reason other than the name already existing: creating the final
		// name directly still writes complete content before the name appears
		// and still refuses to overwrite an existing identity. What it gives up
		// is only crash atomicity — a power loss inside one write can leave a
		// short file, which the load path reports instead of replacing.
		return createKeyFileExclusively(path, protected, linkErr)
	}

	if err := syncDirectory(directory); err != nil {
		return err
	}
	return nil
}

func createKeyFileExclusively(path string, protected []byte, linkErr error) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, keyFileMode)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fs.ErrExist
		}
		// Both routes failed; report both, because the link error is usually the
		// one that says what is wrong with the directory.
		return fmt.Errorf("install node key: %w (linking first failed: %v)", err, linkErr)
	}
	if err := writeAndSync(file, protected); err != nil {
		_ = file.Close()
		// This process created the name a moment ago, so removing it cannot
		// destroy anyone else's key — and leaving it would strand a partial one.
		_ = os.Remove(path)
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("close node key: %w", err)
	}
	return nil
}

func writeAndSync(file *os.File, protected []byte) error {
	if err := file.Chmod(keyFileMode); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		return fmt.Errorf("set node key mode: %w", err)
	}
	if _, err := file.Write(protected); err != nil {
		return fmt.Errorf("write node key: %w", err)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("flush node key: %w", err)
	}
	return nil
}

func keypairFromStored(stored []byte, path string) (Keypair, error) {
	seed, err := unprotectSeed(stored, path)
	if err != nil {
		return Keypair{}, err
	}
	defer zero(seed)
	return keypairFromSeed(seed, path)
}

func keypairFromSeed(seed []byte, path string) (Keypair, error) {
	if len(seed) != ed25519.SeedSize {
		return Keypair{}, fmt.Errorf("node key at %s is %d bytes, want %d", path, len(seed), ed25519.SeedSize)
	}
	private := ed25519.NewKeyFromSeed(seed)
	return Keypair{Public: private.Public().(ed25519.PublicKey), private: private}, nil
}

// zero clears a buffer that held key material. Go can copy a slice's backing
// array before this runs, so it shortens the window rather than closing it;
// that is still worth doing for the seed, which is the whole identity.
func zero(secret []byte) {
	for i := range secret {
		secret[i] = 0
	}
}
