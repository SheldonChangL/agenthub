package model_test

import (
	"strings"
	"testing"

	"agenthub.local/agenthub/internal/model"
)

// MaxSenderLabelLength is derived from this number, so a provider name longer
// than it would silently under-bound the label and refuse a legitimate sender
// whose parts are each at their limit.
func TestEveryProviderNameFitsTheCeilingTheLabelRestsOn(t *testing.T) {
	for _, provider := range []model.Provider{model.ProviderClaude, model.ProviderCodex} {
		if !model.KnownProvider(string(provider)) {
			t.Errorf("%q is not a known provider; this test is out of date", provider)
		}
		if len(provider) > model.MaxProviderNameLength {
			t.Errorf("provider %q is %d bytes, over the %d MaxSenderLabelLength assumes",
				provider, len(provider), model.MaxProviderNameLength)
		}
	}
}

// Real ones are UUIDs. Unbounded, this was the widest free-text field in the
// system: stored, carried in every heartbeat and on every message as the
// sender's label, and the first thing a reader sees in a list.
func TestAProviderSessionIDIsBounded(t *testing.T) {
	if err := model.ValidateProviderSessionID(strings.Repeat("a", model.MaxProviderSessionIDLength)); err != nil {
		t.Errorf("an id at the limit was refused: %v", err)
	}
	err := model.ValidateProviderSessionID(strings.Repeat("a", model.MaxProviderSessionIDLength+1))
	if err == nil {
		t.Fatal("an id over the limit was accepted")
	}
	if len(err.Error()) > 200 {
		t.Errorf("the refusal echoes the value: %d bytes", len(err.Error()))
	}
}
