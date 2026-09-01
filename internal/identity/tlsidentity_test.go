package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testKeypair(t *testing.T) Keypair {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return Keypair{Public: public, private: private}
}

func TestTheCertificateCarriesTheIdentityKey(t *testing.T) {
	keypair := testKeypair(t)
	certificate, err := keypair.TLSCertificate("node_0123456789abcdef")
	if err != nil {
		t.Fatalf("TLSCertificate() error = %v", err)
	}
	parsed, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	public, ok := parsed.PublicKey.(ed25519.PublicKey)
	if !ok {
		t.Fatalf("certificate key is %T; want ed25519.PublicKey", parsed.PublicKey)
	}
	if !public.Equal(keypair.Public) {
		t.Fatal("the certificate does not carry the node's identity key")
	}
	if parsed.Subject.CommonName != "node_0123456789abcdef" {
		t.Errorf("common name = %q", parsed.Subject.CommonName)
	}
}

// TestThePinDecidesTheHandshake drives a real TLS connection rather than
// calling the verifier directly, because what matters is whether the handshake
// completes.
func TestThePinDecidesTheHandshake(t *testing.T) {
	server := testKeypair(t)
	certificate, err := server.TLSCertificate("node_server000000000")
	if err != nil {
		t.Fatal(err)
	}
	listener := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "delivered")
	}))
	listener.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13}
	listener.StartTLS()
	defer listener.Close()

	dial := func(pinned ed25519.PublicKey) error {
		client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // #nosec G402 -- the pin is the check
			MinVersion:         tls.VersionTLS13,
			VerifyConnection:   PinnedConnectionVerifier(pinned, "the test peer"),
		}}}
		response, err := client.Get(listener.URL)
		if err != nil {
			return err
		}
		defer func() { _ = response.Body.Close() }()
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}

	if err := dial(server.Public); err != nil {
		t.Fatalf("pinning the server's own key failed the handshake: %v", err)
	}

	other := testKeypair(t)
	err = dial(other.Public)
	if err == nil {
		t.Fatal("a connection completed against a key that was not pinned")
	}
	if !strings.Contains(err.Error(), "not the one recorded when pairing") {
		t.Errorf("error = %v; want the pin to be the stated reason", err)
	}

	if err := dial(nil); err == nil {
		t.Fatal("a connection completed with no key pinned at all")
	}
}

// TestThePinRunsOnResumedSessions is the reason PinnedConnectionVerifier is a
// VerifyConnection callback and not a VerifyPeerCertificate one.
//
// A resumed TLS 1.3 session performs no full handshake, and Go calls
// VerifyPeerCertificate only for full handshakes. Measured against a real
// server, three requests over a session cache produce one call to
// VerifyPeerCertificate and three to VerifyConnection — so a pin installed in
// the former stops being applied from the second connection onward.
//
// The test counts both callbacks rather than asserting on a handshake outcome,
// because an incorrect pin fails the first connection and therefore never
// populates the cache: a test written that way passes whichever callback is
// used, and proves nothing. That mistake was made here first.
func TestThePinRunsOnResumedSessions(t *testing.T) {
	server := testKeypair(t)
	certificate, err := server.TLSCertificate("node_server000000000")
	if err != nil {
		t.Fatal(err)
	}
	listener := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "delivered")
	}))
	listener.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13}
	listener.StartTLS()
	defer listener.Close()

	var fullHandshakeChecks, everyConnectionChecks atomic.Int64
	var resumed atomic.Int64
	pin := PinnedConnectionVerifier(server.Public, "the test peer")
	client := &http.Client{Transport: &http.Transport{
		// Without this the requests share one TCP connection and never
		// handshake again, so nothing resumes and the counts prove nothing.
		DisableKeepAlives: true,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // #nosec G402 -- the pin is the check
			MinVersion:         tls.VersionTLS13,
			ClientSessionCache: tls.NewLRUClientSessionCache(8),
			VerifyPeerCertificate: func([][]byte, [][]*x509.Certificate) error {
				fullHandshakeChecks.Add(1)
				return nil
			},
			VerifyConnection: func(state tls.ConnectionState) error {
				everyConnectionChecks.Add(1)
				if state.DidResume {
					resumed.Add(1)
				}
				return pin(state)
			},
		}}}

	const requests = 3
	for attempt := range requests {
		response, err := client.Get(listener.URL)
		if err != nil {
			t.Fatalf("request %d failed: %v", attempt+1, err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		time.Sleep(120 * time.Millisecond)
	}

	if resumed.Load() == 0 {
		t.Skip("no session was resumed in this environment; the comparison below would be vacuous")
	}
	if got := everyConnectionChecks.Load(); got != requests {
		t.Errorf("VerifyConnection ran %d times for %d connections; the pin must run on every one", got, requests)
	}
	if got := fullHandshakeChecks.Load(); got >= requests {
		t.Fatalf("VerifyPeerCertificate ran %d times for %d connections; "+
			"this test can no longer tell the two callbacks apart", got, requests)
	}
	t.Logf("%d connections, %d resumed: VerifyConnection ran %d times, VerifyPeerCertificate %d times",
		requests, resumed.Load(), everyConnectionChecks.Load(), fullHandshakeChecks.Load())
}

// TestThePinRefusesAWrongKeyOnEveryConnection complements the counting test
// above with the outcome that matters.
func TestThePinRefusesAWrongKeyOnEveryConnection(t *testing.T) {
	server := testKeypair(t)
	certificate, err := server.TLSCertificate("node_server000000000")
	if err != nil {
		t.Fatal(err)
	}
	listener := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "delivered")
	}))
	listener.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13}
	listener.StartTLS()
	defer listener.Close()

	wrong := testKeypair(t)
	client := &http.Client{Transport: &http.Transport{
		DisableKeepAlives: true,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // #nosec G402 -- the pin is the check
			MinVersion:         tls.VersionTLS13,
			ClientSessionCache: tls.NewLRUClientSessionCache(8),
			VerifyConnection:   PinnedConnectionVerifier(wrong.Public, "the test peer"),
		}}}

	for attempt := range 3 {
		response, err := client.Get(listener.URL)
		if err == nil {
			_ = response.Body.Close()
			t.Fatalf("connection %d completed against a key that was not pinned", attempt+1)
		}
	}
}
