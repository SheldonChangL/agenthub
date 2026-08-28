package identity

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestKeypairIsStableAcrossRestarts(t *testing.T) {
	directory := t.TempDir()

	first, err := LoadOrCreateKeypair(directory)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreateKeypair(directory)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint() != second.Fingerprint() {
		t.Errorf("fingerprint changed across restarts: %q then %q", first.Fingerprint(), second.Fingerprint())
	}
	if !first.Public.Equal(second.Public) {
		t.Error("public key changed across restarts")
	}
}

func TestDifferentNodesHaveDifferentFingerprints(t *testing.T) {
	a, err := LoadOrCreateKeypair(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreateKeypair(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if a.Fingerprint() == b.Fingerprint() {
		t.Error("two nodes produced the same fingerprint")
	}
}

// The fingerprint is compared by eye across two machines, so its shape matters.
func TestFingerprintIsReadableAloud(t *testing.T) {
	keypair, err := LoadOrCreateKeypair(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^([0-9A-F]{4} ){5}[0-9A-F]{4}$`).MatchString(keypair.Fingerprint()) {
		t.Errorf("fingerprint %q is not six groups of four hex digits", keypair.Fingerprint())
	}
}

// Anything that can read the key can impersonate this machine.
func TestPrivateKeyIsNotWorldReadable(t *testing.T) {
	directory := t.TempDir()
	if _, err := LoadOrCreateKeypair(directory); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(directory, "node.key"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("node key mode = %o, want 600", mode)
	}
}

func TestSignaturesVerifyAgainstThePublishedKey(t *testing.T) {
	keypair, err := LoadOrCreateKeypair(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	message := []byte("node.hello")
	signature := keypair.Sign(message)

	encoded := EncodePublicKey(keypair.Public)
	decoded, err := DecodePublicKey(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !ed25519.Verify(decoded, message, signature) {
		t.Error("a signature did not verify against the published public key")
	}
	if ed25519.Verify(decoded, []byte("node.hell0"), signature) {
		t.Error("a signature verified against a different message")
	}
	if Fingerprint(decoded) != keypair.Fingerprint() {
		t.Error("the fingerprint of a round-tripped key differs")
	}
}

// A peer's key is untrusted input.
func TestDecodePublicKeyRejectsMalformedInput(t *testing.T) {
	for name, encoded := range map[string]string{
		"not base64": "!!!!",
		"empty":      "",
		"too short":  "AAAA",
		"too long":   EncodePublicKey(make([]byte, ed25519.PublicKeySize+1)),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodePublicKey(encoded); err == nil {
				t.Errorf("DecodePublicKey(%q) succeeded", encoded)
			}
		})
	}
}

func TestCorruptKeyFileIsReported(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "node.key"), []byte("too short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKeypair(directory); err == nil {
		t.Error("a truncated key file was accepted; a silently regenerated identity would break every pairing")
	}
}

// A key restored from a backup as 0644 must not be used silently.
func TestLoadRefusesAKeyOthersCanRead(t *testing.T) {
	directory := t.TempDir()
	if _, err := LoadOrCreateKeypair(directory); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "node.key")
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateKeypair(directory); err == nil {
		t.Error("a world-readable node key was accepted")
	}
}

// A crash between creating and writing must not leave a key that every later
// start refuses. The write goes to a temporary file and is renamed into place.
func TestKeyIsNeverLeftEmpty(t *testing.T) {
	directory := t.TempDir()
	if _, err := LoadOrCreateKeypair(directory); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(directory, "node.key"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != ed25519.SeedSize {
		t.Errorf("node key is %d bytes, want %d", info.Size(), ed25519.SeedSize)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "node.key" {
			t.Errorf("left a stray file behind: %s", entry.Name())
		}
	}
}
