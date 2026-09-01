package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"sync"
	"time"
)

// tlsCertificateLifetime is how long a generated certificate is valid.
//
// The certificate is not the identity — the key is, and the key is durable.
// Nothing consults the expiry to decide whether a peer is trusted, because
// trust comes from the pinned key. A bounded lifetime is kept anyway so a
// certificate scraped off the wire is not indefinitely reusable in some other
// context that does check dates.
const tlsCertificateLifetime = 90 * 24 * time.Hour

// TLSCertificate builds a self-signed certificate whose key is this node's
// identity key.
//
// This is what makes peer verification a property of the connection rather than
// a separate exchange. A challenge-response proves the key holder answered
// somewhere; it is forwardable, so a relay passes it by asking the real peer.
// A TLS handshake against a pinned key cannot be forwarded: whatever terminates
// the connection has to hold the private key to complete it, so a relay that
// does not hold it never sees the plaintext at all.
//
// There is no CA and no chain. A certificate here is a container for a public
// key, and the only question ever asked of it is "is this the key the owner
// recorded when pairing" — which is exactly the question pairing answered, and
// the reason a CA would add nothing.
func (k Keypair) TLSCertificate(nodeID string) (tls.Certificate, error) {
	return k.tlsCertificateAt(nodeID, time.Now())
}

// tlsCertificateAt is TLSCertificate with the moment made explicit.
//
// The validity window and the decision to renew must come from one clock. When
// they did not, a renewal could produce a certificate that was already outside
// the window the caller was measuring against — which is not merely a testing
// artifact: a process whose clock is stepped between the two reads would hit
// the same thing.
func (k Keypair) tlsCertificateAt(nodeID string, now time.Time) (tls.Certificate, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate certificate serial: %w", err)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: nodeID},
		NotBefore:    now.Add(-time.Hour).UTC(),
		NotAfter:     now.Add(tlsCertificateLifetime).UTC(),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
			x509.ExtKeyUsageClientAuth,
		},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, k.Public, k.private)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create certificate: %w", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k.private}, nil
}

// PinnedConnectionVerifier returns a TLS verification callback that accepts
// exactly one public key.
//
// It replaces chain verification rather than supplementing it: there is no
// authority to chain to, so a caller sets InsecureSkipVerify and supplies this.
// That combination reads alarmingly and is correct here — the name means "skip
// the CA chain", and this callback is a stricter check than any chain would be,
// because it accepts one key rather than anything a CA vouched for.
//
// It is deliberately VerifyConnection and not VerifyPeerCertificate. The two
// look interchangeable and are not: VerifyPeerCertificate runs during a full
// handshake, and a resumed TLS 1.3 session does not perform one, so a pin
// installed there is silently skipped on resumption. VerifyConnection runs for
// resumed sessions too. A pin that applies to the first connection and not the
// second is not a pin.
func PinnedConnectionVerifier(expected ed25519.PublicKey, describe string) func(tls.ConnectionState) error {
	return func(state tls.ConnectionState) error {
		if len(expected) != ed25519.PublicKeySize {
			return fmt.Errorf("no usable key is pinned for %s", describe)
		}
		if len(state.PeerCertificates) == 0 {
			return fmt.Errorf("%s presented no certificate", describe)
		}
		presented, ok := state.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
		if !ok {
			return fmt.Errorf("%s presented a %T key; this protocol pins Ed25519",
				describe, state.PeerCertificates[0].PublicKey)
		}
		if !presented.Equal(expected) {
			return fmt.Errorf("%s presented a key that is not the one recorded when pairing", describe)
		}
		return nil
	}
}

// renewBefore is how long before expiry a certificate is replaced.
//
// Generously early. Renewal costs one signature and the peers verifying it look
// only at the key, so there is nothing to gain by cutting it fine and a real
// cost to being late.
const renewBefore = 7 * 24 * time.Hour

// RotatingCertificate hands out a certificate that is always inside its
// validity window.
//
// A node generated its certificate once at startup, which is correct until the
// process outlives the certificate. Ninety days is short enough that a
// long-running node reaches it, and at that point it would serve an expired
// certificate to every peer. Clients here pin the key and ignore dates, so
// nothing in this project would notice — which is precisely why it needs
// handling now rather than when something does.
//
// The certificate is not the identity. The key is, and it does not change, so a
// renewal is invisible to a peer: it pins the key, and the key is the same one.
type RotatingCertificate struct {
	keypair Keypair
	nodeID  string
	now     func() time.Time

	mutex   sync.Mutex
	current *tls.Certificate
	expires time.Time
}

func NewRotatingCertificate(keypair Keypair, nodeID string) *RotatingCertificate {
	return &RotatingCertificate{keypair: keypair, nodeID: nodeID, now: time.Now}
}

// GetCertificate satisfies tls.Config.GetCertificate.
//
// It is called per handshake, which is the only moment that can be sure the
// certificate is still valid — a timer would have to guess when the process
// might be suspended, and a laptop that slept through the renewal window would
// wake up serving an expired certificate anyway.
func (r *RotatingCertificate) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	now := r.now()
	if r.current != nil && now.Before(r.expires.Add(-renewBefore)) {
		return r.current, nil
	}
	certificate, err := r.keypair.tlsCertificateAt(r.nodeID, now)
	if err != nil {
		if r.current != nil {
			// Serving a certificate that is valid for a while yet beats
			// refusing every connection because one signature failed.
			return r.current, nil
		}
		return nil, err
	}
	parsed, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse freshly built certificate: %w", err)
	}
	r.current = &certificate
	r.expires = parsed.NotAfter
	return r.current, nil
}
