package protocol

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
)

// challengeDomain keeps a challenge signature from being valid as anything
// else. This matters more here than anywhere else in the protocol: answering a
// challenge means signing bytes a stranger chose, so this endpoint is a signing
// oracle by construction. The domain prefix is what stops it from being a
// useful one — an envelope signature begins with signatureDomain, and no
// challenge answer can ever begin with those bytes, so a challenge signature
// can never be presented as an envelope's.
const challengeDomain = "agenthub.broker/v1alpha1/challenge\n"

// ChallengeNonceSize is the smallest nonce a responder will sign.
//
// The nonce is what makes an answer unrepeatable, so a caller that supplies too
// little entropy is refused rather than quietly given a signature that a
// listener could collect and replay later.
const ChallengeNonceSize = 32

// ErrChallengeRefused marks a challenge this node will not answer.
var ErrChallengeRefused = errors.New("challenge refused")

// ErrChallengeUnanswered marks a peer that did not prove it holds the key this
// node has recorded for it.
var ErrChallengeUnanswered = errors.New("peer did not prove it holds its key")

// NewChallengeNonce returns a fresh nonce.
func NewChallengeNonce() ([]byte, error) {
	nonce := make([]byte, ChallengeNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate challenge nonce: %w", err)
	}
	return nonce, nil
}

// ChallengeBytes are what a responder signs.
//
// Both node ids are covered, so an answer proves "I am this node, answering
// this challenger" rather than merely "somebody signed something". Without the
// challenger's id, an answer collected from one node could be presented to
// another as though it had been produced for them; without the responder's id,
// an answer from one peer could be offered as another peer's.
//
// The fields are length-prefixed for the same reason the envelope's are: no
// value may be shifted into its neighbour.
func ChallengeBytes(responderNodeID, challengerNodeID string, nonce []byte) []byte {
	var buffer bytes.Buffer
	buffer.WriteString(challengeDomain)
	for _, field := range [][]byte{
		[]byte(responderNodeID),
		[]byte(challengerNodeID),
		nonce,
	} {
		buffer.WriteString(strconv.Itoa(len(field)))
		buffer.WriteByte(':')
		buffer.Write(field)
	}
	return buffer.Bytes()
}

// AnswerChallenge signs a challenge as responderNodeID.
//
// It refuses a nonce shorter than ChallengeNonceSize. A caller controls the
// nonce, and a short one is either a mistake or an attempt to get a signature
// over bytes the caller can fully predict.
func AnswerChallenge(responderNodeID, challengerNodeID string, nonce []byte, signer Signer) (string, error) {
	if len(nonce) < ChallengeNonceSize {
		return "", fmt.Errorf("%w: a nonce must be at least %d bytes, got %d",
			ErrChallengeRefused, ChallengeNonceSize, len(nonce))
	}
	if responderNodeID == "" {
		return "", fmt.Errorf("%w: this node has no id to answer with", ErrChallengeRefused)
	}
	signature := signer.Sign(ChallengeBytes(responderNodeID, challengerNodeID, nonce))
	return base64.StdEncoding.EncodeToString(signature), nil
}

// VerifyChallengeAnswer checks that the holder of publicKey produced this
// answer for this exact challenge.
//
// The key is the one the caller already trusts for that node. Reading a key out
// of the answer would make the whole exchange decorative: anyone could generate
// a keypair, sign the nonce with it, and present the result.
func VerifyChallengeAnswer(publicKey ed25519.PublicKey, responderNodeID, challengerNodeID string, nonce []byte, answer string) error {
	if len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: no usable key is recorded for %q", ErrChallengeUnanswered, responderNodeID)
	}
	if len(nonce) < ChallengeNonceSize {
		return fmt.Errorf("%w: the nonce was too short to prove anything", ErrChallengeUnanswered)
	}
	signature, err := base64.StdEncoding.DecodeString(answer)
	if err != nil {
		return fmt.Errorf("%w: the answer is not base64", ErrChallengeUnanswered)
	}
	if len(signature) != ed25519.SignatureSize {
		return fmt.Errorf("%w: the answer is not an Ed25519 signature", ErrChallengeUnanswered)
	}
	if !ed25519.Verify(publicKey, ChallengeBytes(responderNodeID, challengerNodeID, nonce), signature) {
		return fmt.Errorf("%w: the answer was not signed by %q's key", ErrChallengeUnanswered, responderNodeID)
	}
	return nil
}
