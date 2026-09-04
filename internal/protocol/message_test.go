package protocol_test

import (
	"agenthub.local/agenthub/internal/model"
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
	// What the bound is for: the refusal is acked back to the peer and logged.
	// Without it the address parser echoes the value it was given, and a peer
	// chooses the size of both.
	payload.From = strings.Repeat("a", 1<<20) + "/claude:x"
	err := payload.Validate()
	if err == nil {
		t.Fatal("a megabyte sender label was accepted")
	}
	if len(err.Error()) > 512 {
		t.Errorf("the refusal is %d bytes; a peer chose the size of a log line", len(err.Error()))
	}
	payload.From = "node_peer0000000000000/claude:abc"
	if err := payload.Validate(); err != nil {
		t.Errorf("an ordinary sender label was refused: %v", err)
	}
	// An id is printable ASCII, like every other identifier here — it is shown
	// to a person and named in `ah inbox-clear`. The refusal does not echo it.
	for name, id := range map[string]string{
		"a space":         "msg with space",
		"a tab":           "msg_\ttab",
		"non-ASCII":       "msg_é",
		"an RTL override": "msg_\u202Etxt",
	} {
		bad := protocol.MessagePayload{MessageID: id, To: "claude:abc", From: "node_peer0000000000000/claude:x", Body: "hello"}
		if err := bad.Validate(); err == nil {
			t.Errorf("a message id with %s was accepted", name)
		} else if strings.Contains(err.Error(), id) {
			t.Errorf("the refusal for %s echoes the id: %s", name, err.Error())
		}
	}
	payload.MessageID = "msg_1"

	// The bound is derived from the parts' own limits, so a legitimate sender
	// with every part at its limit fits.
	payload.From = "node_" + strings.Repeat("a", model.MaxNodeIDLength-5) + "/claude:" +
		strings.Repeat("b", model.MaxProviderSessionIDLength)
	if err := payload.Validate(); err != nil {
		t.Errorf("a sender at every limit was refused: %v", err)
	}
}
