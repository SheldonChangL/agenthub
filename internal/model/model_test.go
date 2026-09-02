package model

import "testing"

// Node identifiers end up in a trust list a person reads. Two entries that look
// identical but are different strings are how the wrong machine gets trusted.
func TestValidateNodeIDRejectsLookalikes(t *testing.T) {
	valid := "node_0123456789abcdef"
	if err := ValidateNodeID(valid); err != nil {
		t.Fatalf("ValidateNodeID(%q) = %v", valid, err)
	}

	for name, nodeID := range map[string]string{
		"too short":         "node_short",
		"trailing space":    valid + " ",
		"leading space":     " " + valid,
		"address separator": "node_a/node_b0000000",
		"full width":        "node_ｆａｋｅ0123456789",
		"cyrillic":          "nоde_0123456789abcd",
		"control char":      valid + "\x00",
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateNodeID(nodeID); err == nil {
				t.Errorf("ValidateNodeID(%q) accepted a lookalike", nodeID)
			}
		})
	}
}

// A node id must not also be readable as a session id.
//
// Every local session id of sixteen characters or more otherwise satisfies every
// other rule, so a reader holding one bare string cannot tell which namespace it
// belongs to — and a message whose sender named no session is stored as exactly
// that bare string.
func TestANodeIDMayNotReadAsASessionID(t *testing.T) {
	for _, id := range []string{
		"claude:0123456789abcdef",
		"codex:some-local-session",
		"claude:aaaaaaaaaaaaaaaaaaaa",
	} {
		if err := ValidateNodeID(id); err == nil {
			t.Errorf("ValidateNodeID(%q) accepted an id that reads as a session", id)
		}
	}
	// Ids that merely contain a colon are fine; only a provider prefix collides.
	for _, id := range []string{
		"node_0123456789abcdef0123",
		"host:0123456789abcdef",
		"claudex:0123456789abcdef",
	} {
		if err := ValidateNodeID(id); err != nil {
			t.Errorf("ValidateNodeID(%q) = %v, want accepted", id, err)
		}
	}
}
