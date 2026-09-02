package mcpserver_test

import (
	"strings"
	"testing"

	"agenthub.local/agenthub/internal/mcpserver"
)

// This process writes message bodies authored on other machines into an agent's
// reasoning context. A node URL someone else controls would let that party
// choose what the agent reads.
func TestOnlyALoopbackNodeIsAccepted(t *testing.T) {
	for _, raw := range []string{
		"http://192.168.1.10:7462",
		"http://example.com:7462",
		"https://node.internal:7462",
		"http://0.0.0.0:7462",
	} {
		if _, err := mcpserver.NewClient(raw); err == nil {
			t.Errorf("NewClient(%q) succeeded; only loopback may be used", raw)
		}
	}

	// A zoned ::1 IS loopback. It is refused only because net.ParseIP cannot
	// read a zone, so this is a conservative rejection, not a security one.
	// Recorded separately so that hardening the check with netip.ParseAddr —
	// which does read zones and would call this loopback — reads as the
	// deliberate change it would be, rather than as a test regression.
	if _, err := mcpserver.NewClient("http://[::1%25eth0]:7462"); err == nil {
		t.Error("NewClient with a zoned ::1 succeeded; today it is conservatively refused")
	}
	for _, raw := range []string{
		"http://127.0.0.1:7462",
		"http://localhost:7462",
		"http://[::1]:7462",
	} {
		if _, err := mcpserver.NewClient(raw); err != nil {
			t.Errorf("NewClient(%q) = %v, want success", raw, err)
		}
	}
}

// A query or fragment survives url.String() and would land in the middle of
// every path this client builds.
func TestANodeURLCarriesNothingButSchemeHostPort(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1:7462?x=1",
		"http://127.0.0.1:7462#frag",
		"http://user:pass@127.0.0.1:7462",
	} {
		_, err := mcpserver.NewClient(raw)
		if err == nil {
			t.Errorf("NewClient(%q) succeeded", raw)
			continue
		}
		if !strings.Contains(err.Error(), "bare scheme") {
			t.Errorf("NewClient(%q) = %v; the error should say what shape is wanted", raw, err)
		}
	}
}
