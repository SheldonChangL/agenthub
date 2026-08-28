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
	// Lstat before opening so a link is refused rather than followed, then check
	// through the handle that the bytes actually read came from the file that was
	// inspected. Two observations that are each "a regular file" are not
	// necessarily the same regular file.
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
	if err := sameFileOrRefuse(linkInfo, info, path); err != nil {
		return nil, err
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

// sameFileOrRefuse checks that an Lstat observation and an opened handle
// describe one file, comparing filesystem identity rather than shape.
//
// Without this the shape checks could both pass while the path was replaced in
// between — Lstat sees the real key, the name is swapped for another regular
// file, and the open reads that one. In practice the window is small and reaching
// it needs write access to the data directory, which is mode 0700 and owned by
// the same user the key protects; someone with that access has easier routes.
// The check is here because the cost is one comparison and the alternative is a
// comment claiming a guarantee the code did not make.
func sameFileOrRefuse(linkInfo, handleInfo fs.FileInfo, path string) error {
	if !os.SameFile(linkInfo, handleInfo) {
		return fmt.Errorf(
			"node key %s changed between being inspected and being opened; refusing to read it", path)
	}
	return nil
}

// ErrKeyStorageUnsupported marks a data directory whose filesystem cannot
// install the key safely. It is a configuration problem with one fix — put the
// data directory somewhere else — so it is a distinct condition rather than a
// generic I/O error.
var ErrKeyStorageUnsupported = errors.New("filesystem cannot store the node key safely")

// installKeyFile writes protected to path, creating it only if absent.
//
// The content is written and flushed to a temporary file and only then linked to
// the final name. Hard linking is the create-if-absent primitive here: it is
// atomic and it fails if the name already exists. Together those give the
// invariant the loader depends on — node.key is either absent or a complete key,
// never the empty or truncated file that every later start would refuse.
//
// There is deliberately no second route. Writing the final name directly, even
// with O_EXCL, publishes the name before the content and so reopens exactly the
// window this ordering exists to close; a crash inside that window strands a key
// only a human can clear. When linking is unavailable the install fails closed
// with ErrKeyStorageUnsupported instead, because an operator can move the data
// directory but cannot recover an identity that was never written whole.
func installKeyFile(directory, path string, protected []byte) error {
	return installKeyFileLinkedBy(directory, path, protected, os.Link)
}

// installKeyFileLinkedBy is installKeyFile with the link step passed in, so a
// test can observe what a failing filesystem leaves behind. Production always
// passes os.Link.
func installKeyFileLinkedBy(
	directory, path string, protected []byte, link func(oldname, newname string) error,
) error {
	temporary, err := os.CreateTemp(directory, ".node.key-*")
	if err != nil {
		return fmt.Errorf("create temporary node key: %w", err)
	}
	temporaryPath := temporary.Name()
	// Removed on every path, including success: after linking, the final name is
	// a second name for the same content and the temporary one is litter. On a
	// failed install this is also what leaves the directory as it was found.
	defer func() { _ = os.Remove(temporaryPath) }()

	if err := writeAndSync(temporary, protected); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary node key: %w", err)
	}

	switch linkErr := link(temporaryPath, path); {
	case linkErr == nil:
	case errors.Is(linkErr, fs.ErrExist):
		// Report the bare sentinel: the caller reads the winner's key, and a
		// wrapped *LinkError here would make that check depend on formatting.
		return fs.ErrExist
	default:
		// The final name was never created, so there is nothing to clean up and
		// nothing for a later start to trip over.
		return fmt.Errorf(
			"%w: installing %s needs a filesystem that can create a file atomically only if"+
				" it is absent, which is done here with a hard link; that failed. Move the data"+
				" directory to a local disk (APFS, ext4, NTFS) rather than a FAT/exFAT volume or a"+
				" network share: %w",
			ErrKeyStorageUnsupported, path, linkErr)
	}

	if err := syncDirectory(directory); err != nil {
		return err
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
