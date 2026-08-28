package protocol_test

import (
	"errors"
	"testing"

	"agenthub.local/agenthub/internal/protocol"
)

const localNode = "node_0123456789abcdef0123"

func TestParseAddressAcceptsBothForms(t *testing.T) {
	cases := map[string]struct {
		raw       string
		wantNode  string
		wantLocal string
	}{
		"bare local":         {"claude:abc", "", "claude:abc"},
		"qualified local":    {localNode + "/claude:abc", "", "claude:abc"},
		"qualified remote":   {"node_peer000000000000/codex:xyz", "node_peer000000000000", "codex:xyz"},
		"surrounding spaces": {"  claude:abc  ", "", "claude:abc"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			address, err := protocol.ParseAddress(testCase.raw, localNode)
			if err != nil {
				t.Fatalf("ParseAddress(%q) = %v", testCase.raw, err)
			}
			if address.NodeID != testCase.wantNode || address.SessionID != testCase.wantLocal {
				t.Errorf("ParseAddress(%q) = %+v", testCase.raw, address)
			}
			if address.Local() != (testCase.wantNode == "") {
				t.Errorf("Local() = %v for %+v", address.Local(), address)
			}
		})
	}
}

// A well-formed address for a machine we have not paired with is a routing
// answer, not a syntax error: the user needs to hear "unknown node".
func TestParseAddressSeparatesRoutingFromSyntax(t *testing.T) {
	address, err := protocol.ParseAddress("node_peer000000000000/claude:abc", localNode)
	if err != nil {
		t.Fatalf("a qualified remote address must parse: %v", err)
	}
	if address.Local() {
		t.Error("a remote address resolved as local")
	}

	malformed := map[string]string{
		"empty":              "",
		"no provider":        "abc",
		"unknown provider":   "gemini:abc",
		"empty session":      "claude:",
		"empty node":         "/claude:abc",
		"nested separator":   "node_a/node_b/claude:abc",
		"short node id":      "node_a/claude:abc",
		"node id with space": "node_peer 000000000000/claude:abc",
		"non ascii node id":  "node_peer_ｆａｋｅ0000/claude:abc",
	}
	for name, raw := range malformed {
		t.Run(name, func(t *testing.T) {
			if _, err := protocol.ParseAddress(raw, localNode); err == nil {
				t.Errorf("ParseAddress(%q) succeeded", raw)
			} else if errors.Is(err, protocol.ErrUnknownNode) {
				t.Errorf("ParseAddress(%q) reported a routing failure for malformed input", raw)
			}
		})
	}
}
