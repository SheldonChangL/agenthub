package identity

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// The protected key file is a versioned envelope rather than a bare ciphertext.
//
// Without a header, a file written by a future scheme would be handed to
// today's decryption routine and fail as "corrupt" — indistinguishable from a
// damaged key, and the wrong thing to tell an owner whose identity is fine. The
// header makes a scheme change a fact the loader can read.
//
// Layout: magic (4 bytes) | scheme (1 byte) | payload length (4 bytes, big
// endian) | payload.
//
// The framing lives here, outside any build tag, so it is compiled and tested
// on every platform even though only Windows writes it today.
const (
	protectedMagic = "AHNK"

	// schemeDPAPIUser is a DPAPI blob bound to the current Windows user.
	schemeDPAPIUser byte = 1

	protectedHeaderSize = len(protectedMagic) + 1 + 4
)

// ErrKeyFileFormat marks a key file this build cannot interpret, as opposed to
// one it can read but cannot decrypt.
var ErrKeyFileFormat = errors.New("unrecognised node key format")

func wrapProtected(scheme byte, payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		return nil, errors.New("refusing to store an empty protected node key")
	}
	if uint64(len(payload)) > uint64(^uint32(0)) {
		return nil, errors.New("protected node key is too large to frame")
	}
	out := make([]byte, 0, protectedHeaderSize+len(payload))
	out = append(out, protectedMagic...)
	out = append(out, scheme)
	// #nosec G115 -- the length was just bounded by MaxUint32 above, so the
	// conversion cannot truncate.
	out = binary.BigEndian.AppendUint32(out, uint32(len(payload)))
	return append(out, payload...), nil
}

// unwrapProtected returns the payload for a well-formed envelope of an expected
// scheme. Anything else is refused: a wrong magic, an unknown scheme, or a
// declared length that disagrees with the bytes present all mean the file is
// not a key this build wrote, and guessing at it is how a corrupt file turns
// into a silently regenerated identity.
func unwrapProtected(stored []byte, expected byte) ([]byte, error) {
	if len(stored) < protectedHeaderSize {
		return nil, fmt.Errorf("%w: file is %d bytes, shorter than a header", ErrKeyFileFormat, len(stored))
	}
	if string(stored[:len(protectedMagic)]) != protectedMagic {
		return nil, fmt.Errorf("%w: wrong magic", ErrKeyFileFormat)
	}
	scheme := stored[len(protectedMagic)]
	if scheme != expected {
		return nil, fmt.Errorf("%w: scheme %d, this build stores scheme %d", ErrKeyFileFormat, scheme, expected)
	}
	declared := binary.BigEndian.Uint32(stored[len(protectedMagic)+1 : protectedHeaderSize])
	payload := stored[protectedHeaderSize:]
	if declared == 0 {
		return nil, fmt.Errorf("%w: header declares an empty payload", ErrKeyFileFormat)
	}
	if uint64(declared) != uint64(len(payload)) {
		return nil, fmt.Errorf(
			"%w: header declares %d payload bytes, file carries %d", ErrKeyFileFormat, declared, len(payload))
	}
	return payload, nil
}
