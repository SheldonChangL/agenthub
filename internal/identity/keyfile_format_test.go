package identity

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"
)

// The envelope is what lets a future scheme change be reported instead of
// mistaken for a damaged key, so its framing is tested on every platform even
// though only Windows writes it.
func TestProtectedEnvelopeRoundTrips(t *testing.T) {
	payload := bytes.Repeat([]byte{0xAB}, 200)

	stored, err := wrapProtected(schemeDPAPIUser, payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != protectedHeaderSize+len(payload) {
		t.Fatalf("envelope is %d bytes, want %d", len(stored), protectedHeaderSize+len(payload))
	}
	got, err := unwrapProtected(stored, schemeDPAPIUser)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Error("payload did not survive the envelope")
	}
}

func TestWrapProtectedRefusesAnEmptyPayload(t *testing.T) {
	if _, err := wrapProtected(schemeDPAPIUser, nil); err == nil {
		t.Error("wrapped an empty payload; an empty key file must never be written")
	}
}

// Every malformed envelope is refused rather than interpreted. Guessing at one
// is how a corrupt file becomes a silently regenerated identity, which would
// invalidate every pairing this node has.
func TestUnwrapProtectedFailsClosed(t *testing.T) {
	valid, err := wrapProtected(schemeDPAPIUser, []byte("ciphertext"))
	if err != nil {
		t.Fatal(err)
	}

	truncatedHeader := valid[:protectedHeaderSize-1]

	wrongMagic := bytes.Clone(valid)
	wrongMagic[0] = 'X'

	unknownScheme := bytes.Clone(valid)
	unknownScheme[len(protectedMagic)] = schemeDPAPIUser + 7

	overstated := bytes.Clone(valid)
	binary.BigEndian.PutUint32(overstated[len(protectedMagic)+1:], 9999)

	understated := bytes.Clone(valid)
	binary.BigEndian.PutUint32(understated[len(protectedMagic)+1:], 1)

	zeroLength := bytes.Clone(valid)
	binary.BigEndian.PutUint32(zeroLength[len(protectedMagic)+1:], 0)

	headerOnly := bytes.Clone(valid[:protectedHeaderSize])
	binary.BigEndian.PutUint32(headerOnly[len(protectedMagic)+1:], 0)

	for name, stored := range map[string][]byte{
		"empty":                    nil,
		"truncated header":         truncatedHeader,
		"wrong magic":              wrongMagic,
		"unknown scheme":           unknownScheme,
		"length longer than file":  overstated,
		"length shorter than file": understated,
		"declared zero length":     zeroLength,
		"header with no payload":   headerOnly,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := unwrapProtected(stored, schemeDPAPIUser); err == nil {
				t.Fatalf("unwrapProtected accepted %s", name)
			} else if !errors.Is(err, ErrKeyFileFormat) {
				t.Errorf("error = %v; want ErrKeyFileFormat so the loader can tell a format from a failed decrypt", err)
			}
		})
	}
}
