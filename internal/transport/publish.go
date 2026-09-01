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

// Publisher sends this node's heartbeat to each paired peer that has an address.
type Publisher struct {
	store       *registry.Registry
	builder     *protocol.HeartbeatBuilder
	localNodeID string
	client      *http.Client
	policy      AddressPolicy
	interval    time.Duration
	now         func() time.Time
}

func NewPublisher(store *registry.Registry, builder *protocol.HeartbeatBuilder, localNodeID string, policy AddressPolicy, interval time.Duration) *Publisher {
	return &Publisher{
		store:       store,
		builder:     builder,
		localNodeID: localNodeID,
		client: &http.Client{
			Timeout: deliveryTimeout,
			// An explicit transport with no proxy. The default transport reads
			// HTTP_PROXY from the environment; Go already declines to proxy
			// loopback destinations, but relying on that leaves the boundary
			// depending on a detail of another package's env parsing. Saying
			// "no proxy" is the same reasoning as refusing redirects below:
			// the bytes go to the address the owner configured, or nowhere.
			Transport: &http.Transport{
				Proxy:               nil,
				TLSHandshakeTimeout: deliveryTimeout,
			},
			// Never follow a redirect. The address policy is checked against
			// the address the owner configured, and a redirect is the peer
			// choosing a different destination after that check has passed.
			//
			// This is not theoretical. The request body is a bytes.Reader, so
			// http.NewRequestWithContext populates GetBody, and Go replays the
			// body on a 307 or 308. A peer at a loopback address answering
			// "307 Location: http://192.0.2.10:7462/v1/heartbeat" would
			// therefore have the entire signed heartbeat re-POSTed to a
			// routable host — session ids, statuses and any granted cwd —
			// while the delivery still counted as a success. The recipient
			// binding does not help: the bytes are readable by whatever host
			// receives them.
			CheckRedirect: func(request *http.Request, _ []*http.Request) error {
				return fmt.Errorf("peer redirected the delivery to %s; refusing to follow", request.URL.Host)
			},
		},
		policy:   policy,
		interval: interval,
		now:      func() time.Time { return time.Now().UTC() },
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

	answerBody, err := p.post(ctx, peer.Address, "/v1/challenge", body)
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

	if _, err := p.post(ctx, peer.Address, "/v1/heartbeat", body); err != nil {
		return err
	}
	return nil
}

// post sends one request to a peer and returns its body.
//
// Every outbound request goes through here so the transport's guarantees are
// stated once: the address policy has already been applied by the caller, the
// client refuses redirects and proxies, and the response is bounded.
func (p *Publisher) post(ctx context.Context, address, path string, body []byte) ([]byte, error) {
	endpoint := "http://" + address + path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := p.client.Do(request)
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
