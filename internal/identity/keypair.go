package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
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

// keyFileMode keeps the private key readable only by its owner. The key is the
// node's identity: anything that can read it can impersonate this machine.
const keyFileMode os.FileMode = 0o600

// LoadOrCreateKeypair reads the node's signing key, generating one on first use.
//
// The key lives beside the database rather than inside it: a database is copied,
// backed up and inspected far more casually than a file named private key, and
// a copied database that carried the key would clone the node's identity.
func LoadOrCreateKeypair(directory string) (Keypair, error) {
	if directory == "" {
		return Keypair{}, errors.New("a directory is required for the node key")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return Keypair{}, fmt.Errorf("create key directory: %w", err)
	}
	path := filepath.Join(directory, "node.key")

	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		// Permissions are checked on every load, not only at creation: a key
		// restored from a backup as 0644 would otherwise be used silently.
		info, statErr := os.Stat(path)
		if statErr != nil {
			return Keypair{}, fmt.Errorf("stat node key: %w", statErr)
		}
		if mode := info.Mode().Perm(); mode&0o077 != 0 {
			return Keypair{}, fmt.Errorf(
				"node key %s is mode %o; it must not be readable by anyone else", path, mode)
		}
		return keypairFromSeed(raw, path)
	case errors.Is(err, os.ErrNotExist):
	default:
		return Keypair{}, fmt.Errorf("read node key: %w", err)
	}

	seed := make([]byte, ed25519.SeedSize)
	if _, err := rand.Read(seed); err != nil {
		return Keypair{}, fmt.Errorf("generate node key: %w", err)
	}
	// Written to a temporary file and renamed into place: a crash between
	// creating and writing would otherwise leave an empty node.key that every
	// later start refuses, and rename is atomic. O_EXCL on the final name means
	// two nodes starting at once cannot each believe they created the identity.
	if err := writeKeyAtomically(directory, path, seed); err != nil {
		if errors.Is(err, os.ErrExist) {
			existing, readErr := os.ReadFile(path)
			if readErr != nil {
				return Keypair{}, fmt.Errorf("read node key: %w", readErr)
			}
			return keypairFromSeed(existing, path)
		}
		return Keypair{}, err
	}
	return keypairFromSeed(seed, path)
}

func writeKeyAtomically(directory, path string, seed []byte) error {
	// Claim the final name first so a concurrent start loses the race here
	// rather than after both have written a key.
	claim, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, keyFileMode)
	if err != nil {
		return err
	}
	_ = claim.Close()

	temporary, err := os.CreateTemp(directory, ".node.key-*")
	if err != nil {
		return fmt.Errorf("create temporary node key: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()

	if err := temporary.Chmod(keyFileMode); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set node key mode: %w", err)
	}
	if _, err := temporary.Write(seed); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write node key: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("flush node key: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close node key: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("install node key: %w", err)
	}
	return nil
}

func keypairFromSeed(seed []byte, path string) (Keypair, error) {
	if len(seed) != ed25519.SeedSize {
		return Keypair{}, fmt.Errorf("node key at %s is %d bytes, want %d", path, len(seed), ed25519.SeedSize)
	}
	private := ed25519.NewKeyFromSeed(seed)
	return Keypair{Public: private.Public().(ed25519.PublicKey), private: private}, nil
}
