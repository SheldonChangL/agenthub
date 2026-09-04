package nodeconfig

import (
	"strings"
	"testing"
)

func TestValidateLoopbackAcceptsLocalAddresses(t *testing.T) {
	for _, address := range []string{"127.0.0.1:7462", "localhost:7462", "[::1]:7462"} {
		if err := ValidateLoopback(address); err != nil {
			t.Errorf("ValidateLoopback(%q) error = %v", address, err)
		}
	}
}

// The owner's API stays on loopback permanently, not until something is built.
// It has no authentication of its own; sharing with another machine is the peer
// listener's job, and a heartbeat or message there must be signed by a node in
// the trust store.
//
// The refusal has to say which of those two the reader wants, because the name
// of the flag they reached for is the wrong one either way.
func TestValidateLoopbackRefusesToServeTheOwnerAPIToTheNetwork(t *testing.T) {
	for _, address := range []string{"0.0.0.0:7462", "192.168.1.10:7462", ":7462"} {
		err := ValidateLoopback(address)
		if err == nil {
			t.Errorf("ValidateLoopback(%q) succeeded; want rejection", address)
			continue
		}
		for _, want := range []string{"no authentication", "-allow-lan", "-peer-listen", "trust store"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("ValidateLoopback(%q) does not mention %q: %v", address, want, err)
			}
		}
		// The message this replaced said LAN mode was not implemented, and a
		// reader would have waited for a feature that was never coming, having
		// already been given the one they wanted. Naming a gap is the specific
		// thing this message must not do again.
		for _, never := range []string{"not implemented", "not supported", "does not exist", "yet"} {
			if strings.Contains(err.Error(), never) {
				t.Errorf("ValidateLoopback(%q) describes a gap (%q), but the rule is permanent: %v",
					address, never, err)
			}
		}
	}
}
