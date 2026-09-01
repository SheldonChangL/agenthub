package protocol_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/protocol"
)

const (
	responderID  = "node_responder0000000"
	challengerID = "node_challenger000000"
)

func nonce(t *testing.T) []byte {
	t.Helper()
	value, err := protocol.NewChallengeNonce()
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestAnAnswerProvesTheResponderHoldsItsKey(t *testing.T) {
	key := newTestKeypair(t)
	challenge := nonce(t)

	answer, err := protocol.AnswerChallenge(responderID, challengerID, challenge, key)
	if err != nil {
		t.Fatalf("AnswerChallenge() error = %v", err)
	}
	if err := protocol.VerifyChallengeAnswer(key.public, responderID, challengerID, challenge, answer); err != nil {
		t.Fatalf("VerifyChallengeAnswer() = %v", err)
	}
}

// TestAnAnswerIsUselessOutsideItsOwnChallenge is the whole point of the
// exchange. Each case is a way an attacker would try to reuse a signature they
// observed rather than one they could produce.
func TestAnAnswerIsUselessOutsideItsOwnChallenge(t *testing.T) {
	key := newTestKeypair(t)
	original := nonce(t)
	answer, err := protocol.AnswerChallenge(responderID, challengerID, original, key)
	if err != nil {
		t.Fatal(err)
	}

	other := newTestKeypair(t)
	for name, check := range map[string]func() error{
		"replayed against a different nonce": func() error {
			return protocol.VerifyChallengeAnswer(key.public, responderID, challengerID, nonce(t), answer)
		},
		"offered as another node's answer": func() error {
			return protocol.VerifyChallengeAnswer(key.public, "node_someoneelse00000", challengerID, original, answer)
		},
		"presented to a different challenger": func() error {
			return protocol.VerifyChallengeAnswer(key.public, responderID, "node_othersider000000", original, answer)
		},
		"verified against a key the responder does not hold": func() error {
			return protocol.VerifyChallengeAnswer(other.public, responderID, challengerID, original, answer)
		},
		"an empty key": func() error {
			return protocol.VerifyChallengeAnswer(nil, responderID, challengerID, original, answer)
		},
		"an answer that is not base64": func() error {
			return protocol.VerifyChallengeAnswer(key.public, responderID, challengerID, original, "not base64!!")
		},
		"an answer of the wrong length": func() error {
			return protocol.VerifyChallengeAnswer(key.public, responderID, challengerID, original,
				base64.StdEncoding.EncodeToString([]byte("short")))
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := check(); err == nil {
				t.Fatal("a reused answer verified")
			} else if !errors.Is(err, protocol.ErrChallengeUnanswered) {
				t.Errorf("error = %v; want ErrChallengeUnanswered", err)
			}
		})
	}
}

// TestAShortNonceIsRefused keeps a caller from asking for a signature over
// bytes it can fully predict.
func TestAShortNonceIsRefused(t *testing.T) {
	key := newTestKeypair(t)
	if _, err := protocol.AnswerChallenge(responderID, challengerID, []byte("tiny"), key); !errors.Is(err, protocol.ErrChallengeRefused) {
		t.Fatalf("error = %v; want ErrChallengeRefused", err)
	}
	// The boundary: one byte under is refused, exactly the size is answered.
	short := make([]byte, protocol.ChallengeNonceSize-1)
	if _, err := protocol.AnswerChallenge(responderID, challengerID, short, key); !errors.Is(err, protocol.ErrChallengeRefused) {
		t.Fatalf("a nonce one byte under the minimum was answered: %v", err)
	}
	exact := make([]byte, protocol.ChallengeNonceSize)
	if _, err := protocol.AnswerChallenge(responderID, challengerID, exact, key); err != nil {
		t.Fatalf("a nonce of exactly the minimum was refused: %v", err)
	}
}

// TestTheChallengeOracleCannotForgeAnEnvelope is the check that makes this
// endpoint safe to expose at all.
//
// Answering a challenge means signing bytes a stranger chose. If those bytes
// could ever be the signable form of an envelope, the endpoint would sign
// arbitrary heartbeats on request. The domain prefixes make that structurally
// impossible, and this test holds that: no nonce, however chosen, produces
// bytes that start like an envelope.
func TestTheChallengeOracleCannotForgeAnEnvelope(t *testing.T) {
	envelopeDomain := []byte("agenthub.broker/v1alpha1/envelope\n")

	// A caller who wants an envelope signature would supply a nonce designed to
	// make the signed bytes look like one. Try exactly that.
	hostile := [][]byte{
		envelopeDomain,
		append([]byte{}, append(envelopeDomain, []byte("24:agenthub.broker/v1alpha1")...)...),
		bytes.Repeat([]byte{0}, protocol.ChallengeNonceSize),
	}
	for _, attempt := range hostile {
		if len(attempt) < protocol.ChallengeNonceSize {
			attempt = append(attempt, bytes.Repeat([]byte{0}, protocol.ChallengeNonceSize-len(attempt))...)
		}
		signed := protocol.ChallengeBytes(responderID, challengerID, attempt)
		if bytes.HasPrefix(signed, envelopeDomain) {
			t.Fatalf("a challenge signed bytes that begin like an envelope: %q", signed[:64])
		}
		if !bytes.HasPrefix(signed, []byte("agenthub.broker/v1alpha1/challenge\n")) {
			t.Fatalf("challenge bytes lost their domain prefix: %q", signed[:64])
		}
	}

	// And the reverse direction: a real envelope's signable bytes are never a
	// valid challenge for any nonce, because the prefix differs.
	key := newTestKeypair(t)
	envelope, err := protocol.NewEnvelope("node_0123456789abcdef0123", protocol.TypePairRequest,
		protocol.At(time.Now()), map[string]string{"a": "b"}, key)
	if err != nil {
		t.Fatal(err)
	}
	signable := protocol.SignableBytes(envelope)
	if bytes.HasPrefix(signable, []byte("agenthub.broker/v1alpha1/challenge\n")) {
		t.Fatal("an envelope's signable bytes begin like a challenge")
	}

	// An envelope signature must not verify as a challenge answer, whatever
	// nonce is claimed.
	if err := protocol.VerifyChallengeAnswer(key.public, envelope.NodeID, challengerID,
		bytes.Repeat([]byte{7}, protocol.ChallengeNonceSize), envelope.Signature); err == nil {
		t.Fatal("an envelope signature verified as a challenge answer")
	}
}

// TestNoncesDoNotRepeat is a smoke check on the source of unrepeatability.
func TestNoncesDoNotRepeat(t *testing.T) {
	seen := map[string]bool{}
	for range 256 {
		value := nonce(t)
		if len(value) != protocol.ChallengeNonceSize {
			t.Fatalf("nonce length = %d", len(value))
		}
		encoded := string(value)
		if seen[encoded] {
			t.Fatal("NewChallengeNonce repeated a value")
		}
		seen[encoded] = true
	}
}
