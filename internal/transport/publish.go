// Package transport delivers this node's presence to the peers it has paired
// with.
//
// It is the sending half of the exchange whose receiving half lives in
// internal/api. Nothing here decides what a peer may see: that is
// HeartbeatBuilder.BuildFor, which applies the owner's per-peer grants. This
// package decides only where a built envelope goes and what happens when it
// does not arrive.
package transport

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	"agenthub.local/agenthub/internal/identity"
	"agenthub.local/agenthub/internal/nodeconfig"
	"agenthub.local/agenthub/internal/protocol"
	"agenthub.local/agenthub/internal/registry"
)

// deliveryTimeout bounds one delivery. A peer that has gone away must not hold
// a publisher's turn open long enough to starve the peers after it.
const deliveryTimeout = 10 * time.Second

// maxResponseBody bounds what a peer can make this node read in reply. A
// successful delivery answers 204 with nothing; an error answers with a small
// JSON object. Neither needs more than this, and a peer is not trusted to
// bound its own response.
const maxResponseBody = 64 * 1024

// AddressPolicy decides whether an address may be delivered to.
//
// It exists so the boundary in docs/multinode-plan.md — no session data leaves
// the host before the pairing gate is complete — is enforced by something a
// reviewer can point at, rather than by nobody having written the code yet.
type AddressPolicy func(address string) error

// LoopbackOnly refuses every address that is not loopback.
//
// This is the policy the current build ships with. Two nodes on one machine can
// therefore exchange real, signed, per-peer heartbeats — which is what makes
// the per-peer export view testable — while no session metadata reaches the
// network.
//
// It is stricter than nodeconfig.ValidateLoopback, which accepts the name
// "localhost" on trust without resolving it. That is a reasonable rule for a
// listen address — binding to a name that does not resolve to loopback simply
// fails — but here the name decides where bytes are sent, so every address a
// name resolves to must be loopback. A name that resolves to even one routable
// address is refused, because the resolver picks which one is used.
func LoopbackOnly(address string) error {
	if err := nodeconfig.ValidateLoopback(address); err != nil {
		return err
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid peer address %q: %w", address, err)
	}
	if net.ParseIP(host) != nil {
		// ValidateLoopback already confirmed the literal is loopback.
		return nil
	}
	resolved, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("peer address %q does not resolve: %w", address, err)
	}
	if len(resolved) == 0 {
		return fmt.Errorf("peer address %q resolves to nothing", address)
	}
	for _, ip := range resolved {
		if !ip.IsLoopback() {
			return fmt.Errorf("peer address %q resolves to %s, which is not loopback", address, ip)
		}
	}
	return nil
}

// PrivateNetworks allows delivery to loopback and to private network addresses.
//
// This is the policy a node uses once its owner has opted into serving peers on
// a local network. It is deliberately not "anything routable": session metadata
// goes to the machine on the next desk, not onto the internet, and an address
// that left the private ranges is either a mistake or a destination the owner
// did not intend.
//
// Names are refused outright rather than resolved, which is the same rule the
// listen side applies and for the same reason.
//
// Resolving here would check one answer and dial another: the policy resolves
// the name, then net/http resolves it again when the connection is made. A DNS
// answer that is private at check time and public at dial time sends the TCP
// connection and the TLS ClientHello — carrying the name in SNI — to a host the
// owner never chose, every publishing round. The pinned key means no session
// metadata follows, because the handshake cannot complete, but a check that can
// be satisfied by one answer and acted on with another is not a check.
func PrivateNetworks(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid peer address %q: %w", address, err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf(
			"peer address %q must be an IP address, not a name; a name can resolve somewhere else between this check and the connection",
			address)
	}
	if !privateOrLoopback(ip) {
		return fmt.Errorf("peer address %q is outside the private network ranges", address)
	}
	return nil
}

// refuseRedirects is the redirect policy for every peer request.
//
// The address policy is checked against the address the owner configured, and a
// redirect is the peer choosing a different destination after that check has
// passed. This is not theoretical: the request body is a bytes.Reader, so
// net/http populates GetBody and replays the body on a 307 or 308. A peer at a
// loopback address answering "307 Location: http://192.0.2.10:7462/v1/heartbeat"
// would have the entire heartbeat re-sent to a routable host while the delivery
// still counted as a success.
func refuseRedirects(request *http.Request, _ []*http.Request) error {
	return fmt.Errorf("peer redirected the delivery to %s; refusing to follow", request.URL.Host)
}

// privateOrLoopback mirrors the listen-side rule so a node cannot be configured
// to serve on an address it would refuse to deliver to, or the reverse.
func privateOrLoopback(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

// Publisher sends this node's heartbeat to each paired peer that has an address.
type Publisher struct {
	store       *registry.Registry
	builder     *protocol.HeartbeatBuilder
	localNodeID string
	// transport is the template every per-peer client is cloned from, so the
	// proxy and timeout decisions are made once.
	transport *http.Transport
	policy    AddressPolicy
	interval  time.Duration
	now       func() time.Time
}

func NewPublisher(store *registry.Registry, builder *protocol.HeartbeatBuilder, localNodeID string, policy AddressPolicy, interval time.Duration) *Publisher {
	return &Publisher{
		store:       store,
		builder:     builder,
		localNodeID: localNodeID,
		policy:      policy,
		interval:    interval,
		now:         func() time.Time { return time.Now().UTC() },
		transport: &http.Transport{
			// No proxy. The default transport reads HTTP_PROXY; Go already
			// declines to proxy loopback, but the boundary should not depend
			// on another package's env parsing.
			Proxy:               nil,
			TLSHandshakeTimeout: deliveryTimeout,
		},
	}
}

// Result describes one publishing round. It is returned so a caller — a test,
// or an operator-facing command — can see what happened without reading logs.
type Result struct {
	Delivered int
	Skipped   int
	Failed    int
}

// Run publishes on every tick until ctx is cancelled.
func (p *Publisher) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := p.PublishOnce(ctx); err != nil {
				log.Printf("heartbeat publishing failed: %v", err)
			}
		}
	}
}

// PublishOnce delivers one heartbeat to every eligible peer.
//
// One peer's failure never stops the others. A machine that is asleep is the
// normal case, not an error worth abandoning the round for — and letting it
// abort the loop would hand any peer the ability to stop this node publishing
// to every other peer by refusing connections.
//
// The envelope is built per peer, immediately before it is sent. Building once
// and fanning out would defeat the recipient binding: an envelope names exactly
// one recipient, and it carries only the sessions that recipient was granted.
func (p *Publisher) PublishOnce(ctx context.Context) (Result, error) {
	peers, err := p.store.TrustedNodes(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("list trusted nodes: %w", err)
	}

	var result Result
	for _, peer := range peers {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if peer.Address == "" {
			// Paired but not located. Nothing to do, and nothing wrong.
			result.Skipped++
			continue
		}
		if err := p.policy(peer.Address); err != nil {
			// The address exists but this build refuses to reach it. This is a
			// boundary refusal, not a delivery failure, so it is counted apart
			// and said out loud: an owner who set a LAN address needs to know
			// why nothing is arriving.
			log.Printf("not delivering to %q at %q: %v", peer.NodeID, peer.Address, err)
			result.Skipped++
			continue
		}
		if err := p.deliver(ctx, peer); err != nil {
			log.Printf("heartbeat to %q failed: %v", peer.NodeID, err)
			result.Failed++
			continue
		}
		result.Delivered++
	}
	return result, nil
}

// challenge asks whatever answers at this address to prove it holds the key the
// owner recorded for this peer, before any session metadata is sent.
//
// WHAT THIS PROVES, AND WHAT IT DOES NOT.
//
// It proves the peer's key-holder is reachable and answered this exact
// challenge. It catches an address pointing at the wrong node, a node that has
// lost or rotated its key, and an off-path spoofer that cannot reach the real
// peer. Comparing public keys would catch none of that, because a public key is
// public and an impostor can present the real one.
//
// It does NOT prove that the entity at this address is that peer. An active
// relay defeats it: forward the challenge to the genuine peer, return the
// genuine answer, and collect the heartbeat. The answer binds the responder id,
// the challenger id and the nonce, and a relay forwards all three unchanged, so
// binding them cannot help. TestARelayDefeatsTheChallenge reproduces this and
// exists so the limit cannot be forgotten.
//
// The consequence for the roadmap is concrete: this is not enough to make
// discovered addresses safe, because an attacker who can spoof mDNS on a LAN
// can generally also reach the peer being impersonated. Widening LoopbackOnly
// requires the channel itself to be bound to the peer's identity — TLS pinned
// to the key in the trust store — so the metadata is unreadable to whatever
// sits in the middle. Until then delivery stays on loopback, where there is no
// middle to sit in.
//
// This runs before every delivery rather than once per peer. A cached result
// would mean the address was verified at some point in the past, which is not
// the same as "the thing about to receive this heartbeat answered just now".
func (p *Publisher) challenge(ctx context.Context, peer registry.TrustedNode, localNodeID string) error {
	publicKey, err := identity.DecodePublicKey(peer.PublicKey)
	if err != nil {
		return fmt.Errorf("stored key for %q is unusable: %w", peer.NodeID, err)
	}
	nonce, err := protocol.NewChallengeNonce()
	if err != nil {
		return err
	}

	request := struct {
		Nonce            string `json:"nonce"`
		ChallengerNodeID string `json:"challengerNodeId"`
	}{Nonce: base64.StdEncoding.EncodeToString(nonce), ChallengerNodeID: localNodeID}
	body, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode challenge: %w", err)
	}

	answerBody, err := p.post(ctx, peer, "/v1/challenge", body)
	if err != nil {
		return fmt.Errorf("challenge %q: %w", peer.NodeID, err)
	}
	var answer struct {
		NodeID    string `json:"nodeId"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(answerBody, &answer); err != nil {
		return fmt.Errorf("%w: %q answered with something unreadable", protocol.ErrChallengeUnanswered, peer.NodeID)
	}
	if answer.NodeID != peer.NodeID {
		// Whatever is at this address is not claiming to be the peer. Say so
		// distinctly: an owner whose discovery pointed at the wrong machine
		// needs a different message than one whose peer is misbehaving.
		return fmt.Errorf("%w: %q answered as %q", protocol.ErrChallengeUnanswered, peer.Address, answer.NodeID)
	}
	return protocol.VerifyChallengeAnswer(publicKey, peer.NodeID, localNodeID, nonce, answer.Signature)
}

// clientFor builds a client whose TLS connection will only complete against
// this peer's key.
//
// The client is per peer and per delivery rather than shared: a pooled
// connection carries the identity it was established with, and reusing one
// across peers would mean the pin that mattered was whichever peer opened it
// first.
func (p *Publisher) clientFor(peer registry.TrustedNode) (*http.Client, error) {
	publicKey, err := identity.DecodePublicKey(peer.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("stored key for %q is unusable: %w", peer.NodeID, err)
	}
	transport := p.transport.Clone()
	// Each delivery builds its own client, so a pooled connection has nobody to
	// serve after this request: it would sit idle, holding a socket and a
	// readLoop goroutine, until the peer closed it. Against a peer that never
	// closes, that grows without bound. Keep-alive buys nothing here because
	// the challenge and the heartbeat already use separate clients.
	transport.DisableKeepAlives = true
	transport.TLSClientConfig = &tls.Config{
		// There is no CA to chain to. VerifyPeerCertificate below is the
		// check, and it is stricter than a chain: it accepts one key, not
		// anything an authority vouched for.
		InsecureSkipVerify: true, // #nosec G402 -- replaced by the pin below, not omitted
		MinVersion:         tls.VersionTLS13,
		// VerifyConnection, not VerifyPeerCertificate: a resumed TLS 1.3
		// session performs no full handshake, so a pin installed in
		// VerifyPeerCertificate would be skipped on resumption.
		VerifyConnection: identity.PinnedConnectionVerifier(publicKey, fmt.Sprintf("peer %q", peer.NodeID)),
		// And no session cache, so resumption is not attempted in the first
		// place. Belt and braces: the pin above is correct on its own, but a
		// connection that is never resumed cannot be resumed past a future
		// mistake either.
		ClientSessionCache: nil,
	}
	return &http.Client{
		Timeout:       deliveryTimeout,
		Transport:     transport,
		CheckRedirect: refuseRedirects,
	}, nil
}

func (p *Publisher) deliver(ctx context.Context, peer registry.TrustedNode) error {
	if err := p.challenge(ctx, peer, p.localNodeID); err != nil {
		return err
	}
	envelope, err := p.builder.BuildFor(ctx, p.now(), peer.NodeID)
	if err != nil {
		if errors.Is(err, protocol.ErrPeerNotTrusted) {
			// Trust was withdrawn between listing the peers and building for
			// this one. Not delivering is the correct outcome.
			return fmt.Errorf("peer is no longer trusted: %w", err)
		}
		return fmt.Errorf("build heartbeat: %w", err)
	}

	body, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("encode envelope: %w", err)
	}

	if _, err := p.post(ctx, peer, "/v1/heartbeat", body); err != nil {
		return err
	}
	return nil
}

// post sends one request to a peer and returns its body.
//
// Every outbound request goes through here so the transport's guarantees are
// stated once: the address policy has already been applied by the caller, the
// connection is TLS pinned to that peer's key, redirects and proxies are
// refused, and the response is bounded.
func (p *Publisher) post(ctx context.Context, peer registry.TrustedNode, path string, body []byte) ([]byte, error) {
	client, err := p.clientFor(peer)
	if err != nil {
		return nil, err
	}
	// Keep-alives are already off, so this is the second of two belts: a
	// transport that outlived its request must not outlive this function.
	defer client.CloseIdleConnections()
	endpoint := "https://" + peer.Address + path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("post to %s: %w", endpoint, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBody))
		_ = response.Body.Close()
	}()

	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBody))
	if err != nil {
		return nil, fmt.Errorf("read response from %s: %w", endpoint, err)
	}
	if response.StatusCode == http.StatusNoContent || response.StatusCode == http.StatusOK {
		return data, nil
	}
	return nil, fmt.Errorf("peer answered HTTP %d: %s", response.StatusCode, bytes.TrimSpace(data))
}
