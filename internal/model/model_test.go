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
