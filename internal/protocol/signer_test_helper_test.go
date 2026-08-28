package protocol_test

import (
	"crypto/ed25519"
	"testing"
)

// testKeypair signs with a real key so tests exercise the same path production
// does; a stub returning fixed bytes would let a broken signature pass.
type testKeypair struct {
	public  ed25519.PublicKey
	private ed25519.PrivateKey
}

func newTestKeypair(t *testing.T) testKeypair {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	return testKeypair{public: public, private: private}
}

func (k testKeypair) Sign(message []byte) []byte { return ed25519.Sign(k.private, message) }
