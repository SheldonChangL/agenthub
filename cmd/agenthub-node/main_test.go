package main

import "testing"

// The port in an announcement is what a peer dials for TLS, not the port the
// multicast packet arrived from. Getting it wrong produces a candidate that
// looks right and connects to nothing, so every spelling the listener itself
// accepts has to survive being read here — and the spellings it cannot carry
// have to be refused at startup rather than announced.
func TestListenPortReadsWhatTheListenerAccepts(t *testing.T) {
	for name, testCase := range map[string]struct {
		address string
		want    int
	}{
		"a number":                  {"127.0.0.1:7463", 7463},
		"a number on every address": {":7463", 7463},
		// net.Listen accepts a service name, so refusing one here would stop a
		// node over an address that works.
		"a service name": {"127.0.0.1:https", 443},
		"an ipv6 host":   {"[::1]:7463", 7463},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := listenPort(testCase.address)
			if err != nil {
				t.Fatalf("listenPort(%q) error = %v", testCase.address, err)
			}
			if got != testCase.want {
				t.Errorf("listenPort(%q) = %d, want %d", testCase.address, got, testCase.want)
			}
		})
	}

	for name, address := range map[string]string{
		// Port zero asks the kernel to choose, so this number is not the one
		// the listener ends up on. Announcing it invites a connection to
		// nothing.
		"port zero":    "127.0.0.1:0",
		"no port":      "127.0.0.1",
		"not a port":   "127.0.0.1:not-a-service",
		"out of range": "127.0.0.1:70000",
		"empty":        "",
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := listenPort(address); err == nil {
				t.Errorf("listenPort(%q) = %d, want an error", address, got)
			}
		})
	}
}
