package transport

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// ErrPeerKeyChanged reports that the machine at an address presented a
// different key than the one this exchange started against.
//
// It is the middle of the exchange going wrong, not a network error, and the
// only correct reaction is to abandon the pairing: the two halves would
// otherwise be negotiated with two different machines.
var ErrPeerKeyChanged = errors.New("the machine at that address presented a different key than before")

// PairDialer talks to a node this one has not paired with.
//
// Every other outbound request in this package is pinned to a key the owner
// recorded when pairing. This one cannot be: the whole point is that no key has
// been recorded yet. So the first connection accepts whatever key the far side
// presents and *records* it, and every later request in the same exchange is
// pinned to that recorded key. The recorded key is then checked against the key
// in the descriptor the far side signed, and only a human comparing
// fingerprints on two screens decides whether that key is the right machine's.
//
// Recording rather than trusting is the distinction that matters. A middle can
// still terminate the first connection — nothing stops it — and it will be
// caught because the key it presented is not the key the real machine's
// descriptor names, and because the fingerprint on the two screens will differ.
type PairDialer struct {
	transport *http.Transport
	policy    AddressPolicy
	timeout   time.Duration
}

// NewPairDialer builds a dialer that will only reach addresses the policy
// allows — the same policy the publisher applies, so an address that cannot be
// delivered to is not one this node will pair over either.
func NewPairDialer(policy AddressPolicy) *PairDialer {
	if policy == nil {
		policy = LoopbackOnly
	}
	return &PairDialer{
		policy:  policy,
		timeout: deliveryTimeout,
		transport: &http.Transport{
			// No proxy, for the reason NewPublisher gives: the boundary should
			// not depend on another package's environment parsing.
			Proxy:               nil,
			TLSHandshakeTimeout: deliveryTimeout,
			DisableKeepAlives:   true,
		},
	}
}

// Reply is what a peer answered, and which key terminated the connection.
type Reply struct {
	Status int
	Body   []byte
	// PresentedKey is the Ed25519 key of the certificate that completed the
	// handshake. This is the key the descriptor in Body must match.
	PresentedKey ed25519.PublicKey
}

// Post sends one request to a node that is not paired yet.
//
// pinned may be nil on the first contact of an exchange, which records the key
// presented; on every later request it must be the recorded key, and a
// different one fails the handshake with ErrPeerKeyChanged.
func (d *PairDialer) Post(ctx context.Context, address, path string, body []byte, pinned ed25519.PublicKey) (Reply, error) {
	return d.do(ctx, http.MethodPost, address, path, body, pinned)
}

// Get reads one resource from a node that is not paired yet, under the same
// rules as Post.
func (d *PairDialer) Get(ctx context.Context, address, path string, pinned ed25519.PublicKey) (Reply, error) {
	return d.do(ctx, http.MethodGet, address, path, nil, pinned)
}

func (d *PairDialer) do(
	ctx context.Context, method, address, path string, body []byte, pinned ed25519.PublicKey,
) (Reply, error) {
	if err := d.policy(address); err != nil {
		return Reply{}, fmt.Errorf("will not pair over %q: %w", address, err)
	}

	// Written by the handshake, read after it. The mutex is not ceremony: the
	// callback runs on the transport's goroutine and the read happens on this
	// one, and a race detector run is the cheapest place to find that out.
	var (
		mu        sync.Mutex
		presented ed25519.PublicKey
	)
	clientTransport := d.transport.Clone()
	clientTransport.TLSClientConfig = &tls.Config{
		// There is no authority to chain to, and on this path there is not even
		// a pinned key on the first call. VerifyConnection below is the whole
		// check: it records what answered, and refuses a key that is not the
		// one this exchange has already been talking to.
		InsecureSkipVerify: true, // #nosec G402 -- replaced by VerifyConnection below, which records and pins the peer key
		MinVersion:         tls.VersionTLS13,
		// VerifyConnection, not VerifyPeerCertificate: a resumed TLS 1.3
		// session performs no full handshake, so a check installed in
		// VerifyPeerCertificate would be skipped on resumption.
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return fmt.Errorf("%s presented no certificate", address)
			}
			key, ok := state.PeerCertificates[0].PublicKey.(ed25519.PublicKey)
			if !ok {
				return fmt.Errorf("%s presented a %T key; this protocol pins Ed25519",
					address, state.PeerCertificates[0].PublicKey)
			}
			if len(pinned) == ed25519.PublicKeySize && !key.Equal(pinned) {
				return fmt.Errorf("%w: %s", ErrPeerKeyChanged, address)
			}
			mu.Lock()
			presented = key
			mu.Unlock()
			return nil
		},
		ClientSessionCache: nil,
	}
	client := &http.Client{
		Timeout:       d.timeout,
		Transport:     clientTransport,
		CheckRedirect: refuseRedirects,
	}
	defer client.CloseIdleConnections()

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, "https://"+address+path, reader)
	if err != nil {
		return Reply{}, fmt.Errorf("create request: %w", err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}

	response, err := client.Do(request)
	if err != nil {
		return Reply{}, fmt.Errorf("%s %s: %w", method, address+path, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBody))
		_ = response.Body.Close()
	}()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if err != nil {
		return Reply{}, fmt.Errorf("read response from %s: %w", address, err)
	}
	mu.Lock()
	key := presented
	mu.Unlock()
	return Reply{Status: response.StatusCode, Body: data, PresentedKey: key}, nil
}
