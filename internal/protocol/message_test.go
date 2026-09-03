package protocol_test

import (
	"strings"
	"testing"

	"agenthub.local/agenthub/internal/protocol"
)

// A sender's label is stored and shown beside the body, without the body's
// framing. Unbounded, a peer could attach a megabyte to every message — storage
// the recipient did not agree to, and an accelerant for filling the response a
// reader has to decode.
func TestASenderLabelIsBounded(t *testing.T) {
	payload := protocol.MessagePayload{
		MessageID: "msg_1",
		To:        "claude:abc",
		From:      "node_peer0000000000000/claude:" + strings.Repeat("a", 600),
		Body:      "hello",
	}
	if err := payload.Validate(); err == nil {
		t.Fatal("an oversized sender label was accepted")
	}
	payload.From = "node_peer0000000000000/claude:abc"
	if err := payload.Validate(); err != nil {
		t.Errorf("an ordinary sender label was refused: %v", err)
	}
}
