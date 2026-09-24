package transport

import (
	"context"
	"crypto/ed25519"
	"net"
	"net/http"
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"agenthub.local/agenthub/internal/model"
	"agenthub.local/agenthub/internal/registry"
)

const multiPeerID = "node_peeraaaaaaaaaaaa"

// deadAddress is a loopback address nothing listens on: the connection is
// refused, which is what an address whose interface has gone looks like to
// the dialer when the kernel answers at all.
func deadAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}

// silentAddress accepts connections and never says anything, which is the
// shape of an address that answers but never completes a handshake — the
// case a per-attempt timeout exists for. It counts what connected.
func silentAddress(t *testing.T, accepted *atomic.Int32) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var held sync.WaitGroup
	var mu sync.Mutex
	var conns []net.Conn
	held.Add(1)
	go func() {
		defer held.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		held.Wait()
		mu.Lock()
		defer mu.Unlock()
		for _, conn := range conns {
			_ = conn.Close()
		}
	})
	return listener.Addr().String()
}

// settled reads a count once the accept goroutines have caught up with the
// connections the kernel already completed.
func settled(count *atomic.Int32) int32 {
	for range 20 {
		before := count.Load()
		time.Sleep(10 * time.Millisecond)
		if count.Load() == before {
			return before
		}
	}
	return count.Load()
}

// trustAt pairs the peer and gives it these addresses, the first preferred.
func trustAt(t *testing.T, store *registry.Registry, public ed25519.PublicKey, addresses ...string) {
	t.Helper()
	trust(t, store, multiPeerID, "", public)
	if err := store.SetNodeAddresses(context.Background(), multiPeerID, addresses, LoopbackOnly); err != nil {
		t.Fatal(err)
	}
}

func preferredOf(t *testing.T, store *registry.Registry) (string, []string) {
	t.Helper()
	node, err := store.TrustedNode(context.Background(), multiPeerID)
	if err != nil {
		t.Fatal(err)
	}
	return node.Address, node.Alternates
}

// twoAddressesOfOnePeer is one peer answering at two addresses with one key.
func twoAddressesOfOnePeer(t *testing.T) (first, second *capture) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return newCaptureKeyed(t, multiPeerID, public, private), newCaptureKeyed(t, multiPeerID, public, private)
}

// ADR-005 §4: the preferred address is gone, the alternate answers, the
// heartbeat arrives there, and the alternate becomes the preferred address so
// the next round starts where this one succeeded.
func TestADeadPreferredAddressFallsBackToTheAlternate(t *testing.T) {
	store := openStore(t)
	alternate := newCapture(t, multiPeerID)
	dead := deadAddress(t)
	trustAt(t, store, alternate.public, dead, alternate.address(t))
	publish(t, store, "claude:shared", model.Audience{Mode: model.AudienceAllPaired})

	result, err := publisherFor(t, store).PublishOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Delivered != 1 || len(alternate.envelopes) != 1 {
		t.Fatalf("result = %+v, alternate received %d; want the heartbeat delivered at the alternate",
			result, len(alternate.envelopes))
	}
	preferred, alternates := preferredOf(t, store)
	if preferred != alternate.address(t) || !reflect.DeepEqual(alternates, []string{dead}) {
		t.Fatalf("addresses = %q %q; want the working alternate preferred and the dead one kept behind it",
			preferred, alternates)
	}
}

// A key that is not the pinned one is not the peer, whatever it answers.
func TestAPinMismatchMovesOnToTheNextAddress(t *testing.T) {
	store := openStore(t)
	impostor := newCapture(t, multiPeerID) // a different key under the same id
	real := newCapture(t, multiPeerID)
	trustAt(t, store, real.public, impostor.address(t), real.address(t))
	publish(t, store, "claude:shared", model.Audience{Mode: model.AudienceAllPaired})

	result, err := publisherFor(t, store).PublishOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(impostor.envelopes) != 0 || impostor.challenges.Load() != 0 {
		t.Fatalf("the impostor received %d envelopes and %d challenges; the handshake should have stopped both",
			len(impostor.envelopes), impostor.challenges.Load())
	}
	if result.Delivered != 1 || len(real.envelopes) != 1 {
		t.Fatalf("result = %+v; want the heartbeat delivered at the second address", result)
	}
	if preferred, _ := preferredOf(t, store); preferred != real.address(t) {
		t.Fatalf("preferred = %q; want %q", preferred, real.address(t))
	}
}

// The right key answering as a different node is not a proof either, and the
// heartbeat must go where the proof was given — not back to the preferred
// address the search started from.
func TestAChallengeAnsweredByTheWrongNodeMovesOnAndTheRoundStaysThere(t *testing.T) {
	store := openStore(t)
	wrong, right := twoAddressesOfOnePeer(t)
	wrong.answerAs = "node_somebodyelse0000"
	trustAt(t, store, right.public, wrong.address(t), right.address(t))
	publish(t, store, "claude:shared", model.Audience{Mode: model.AudienceAllPaired})

	result, err := publisherFor(t, store).PublishOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if wrong.challenges.Load() != 1 {
		t.Fatalf("the preferred address saw %d challenges; want it tried first", wrong.challenges.Load())
	}
	if len(wrong.envelopes) != 0 {
		t.Fatal("the heartbeat went to the address that failed the challenge")
	}
	if result.Delivered != 1 || len(right.envelopes) != 1 {
		t.Fatalf("result = %+v; want the heartbeat at the address that passed", result)
	}
}

// Messages in a round follow the same proof: every one goes to the address
// that answered the challenge.
func TestEveryMessageInARoundGoesWhereTheChallengePassed(t *testing.T) {
	store := openStore(t)
	wrong, right := twoAddressesOfOnePeer(t)
	wrong.answerAs = "node_somebodyelse0000"
	trustAt(t, store, right.public, wrong.address(t), right.address(t))
	ctx := context.Background()
	for _, body := range []string{"one", "two"} {
		if _, err := store.QueueOutbound(ctx, registry.OutboundMessage{
			DestinationNodeID: multiPeerID, To: "codex:theirs", Body: body,
		}); err != nil {
			t.Fatal(err)
		}
	}

	result, err := publisherFor(t, store).DeliverMessages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if result.Delivered != 2 || len(right.messages) != 2 || len(wrong.messages) != 0 {
		t.Fatalf("result = %+v, right got %d, wrong got %d; want both messages where the proof was given",
			result, len(right.messages), len(wrong.messages))
	}
	if right.challenges.Load() != 1 {
		t.Fatalf("the working address was challenged %d times; the proof is once per round", right.challenges.Load())
	}
}

// Once the peer has proven itself, an HTTP error is the peer's answer. Trying
// its other address would ask the same machine again.
func TestAnHTTPErrorAfterTheChallengeDoesNotMoveOn(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			store := openStore(t)
			preferred, alternate := twoAddressesOfOnePeer(t)
			preferred.status = status
			trustAt(t, store, preferred.public, preferred.address(t), alternate.address(t))
			publish(t, store, "claude:shared", model.Audience{Mode: model.AudienceAllPaired})

			result, err := publisherFor(t, store).PublishOnce(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if result.Failed != 1 {
				t.Fatalf("result = %+v; want the delivery failed", result)
			}
			if alternate.challenges.Load() != 0 || len(alternate.envelopes) != 0 {
				t.Fatalf("the alternate was tried after the peer answered HTTP %d", status)
			}
			if got, _ := preferredOf(t, store); got != preferred.address(t) {
				t.Fatalf("preferred = %q; an HTTP error must not change it", got)
			}
		})
	}
}

// The same holds for an HTTP error to the challenge itself: the TLS pin has
// passed, so whatever wrote the status holds the peer's key.
func TestAnHTTPErrorToTheChallengeDoesNotMoveOn(t *testing.T) {
	store := openStore(t)
	preferred, alternate := twoAddressesOfOnePeer(t)
	preferred.challengeStatus = http.StatusTooManyRequests
	trustAt(t, store, preferred.public, preferred.address(t), alternate.address(t))
	publish(t, store, "claude:shared", model.Audience{Mode: model.AudienceAllPaired})

	result, err := publisherFor(t, store).PublishOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 1 || alternate.challenges.Load() != 0 {
		t.Fatalf("result = %+v, alternate challenged %d times; want no move after the peer's own answer",
			result, alternate.challenges.Load())
	}
}

// Addresses are tried in order, one at a time, and a silent one costs one
// attempt — not the round.
func TestASilentAddressCostsOneAttemptNotTheRound(t *testing.T) {
	store := openStore(t)
	var accepted atomic.Int32
	silent := silentAddress(t, &accepted)
	alternate := newCapture(t, multiPeerID)
	trustAt(t, store, alternate.public, silent, alternate.address(t))
	publish(t, store, "claude:shared", model.Audience{Mode: model.AudienceAllPaired})

	publisher := publisherFor(t, store)
	publisher.attemptTimeout = 200 * time.Millisecond
	publisher.searchBudget = 5 * time.Second
	started := time.Now()
	result, err := publisher.PublishOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Delivered != 1 || settled(&accepted) != 1 {
		t.Fatalf("result = %+v, silent address accepted %d; want it tried once, then the alternate",
			result, accepted.Load())
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("the round took %s; a silent address should cost one %s attempt",
			elapsed, publisher.attemptTimeout)
	}
}

// Four addresses that all say nothing share one budget: the round ends when it
// runs out, not after four full attempts.
func TestEveryAddressFailingStaysInsideTheBudget(t *testing.T) {
	store := openStore(t)
	var accepted atomic.Int32
	addresses := []string{
		silentAddress(t, &accepted), silentAddress(t, &accepted),
		silentAddress(t, &accepted), silentAddress(t, &accepted),
	}
	public, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	trustAt(t, store, public, addresses...)
	publish(t, store, "claude:shared", model.Audience{Mode: model.AudienceAllPaired})

	publisher := publisherFor(t, store)
	publisher.attemptTimeout = 300 * time.Millisecond
	publisher.searchBudget = 700 * time.Millisecond
	started := time.Now()
	result, err := publisher.PublishOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	if result.Failed != 1 {
		t.Fatalf("result = %+v; want the delivery failed", result)
	}
	// 300 + 300 + what is left of 700: three addresses tried, the fourth never.
	if got := settled(&accepted); got != 3 {
		t.Fatalf("%d addresses were tried; want three inside a 700ms budget of 300ms attempts", got)
	}
	if elapsed > publisher.searchBudget+500*time.Millisecond {
		t.Fatalf("every address failing took %s; the budget is %s", elapsed, publisher.searchBudget)
	}
	if preferred, _ := preferredOf(t, store); preferred != addresses[0] {
		t.Fatalf("preferred = %q; nothing worked, so nothing is promoted", preferred)
	}
}

// slowConnect makes every TCP connection to these addresses take delay before
// it is made, the way a far or congested link does. The time is spent inside
// the dial, where a timeout on the dialer itself would count it.
func slowConnect(publisher *Publisher, delay time.Duration, slow ...string) {
	publisher.dialer.ControlContext = func(ctx context.Context, _, address string, _ syscall.RawConn) error {
		if !slices.Contains(slow, address) {
			return nil
		}
		select {
		case <-time.After(delay):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// slowLink is how long the slow link in the two tests below takes to connect:
// longer than one attempt, well inside the whole budget.
const slowLink = 4 * time.Second

// A peer with one address has nothing to fall back to, so its one connection
// keeps the whole budget, as it did before there were alternates: a link that
// takes four seconds to connect still gets the heartbeat through. The same
// holds for the heartbeat's own connection after the challenge.
func TestAPeerWithOneSlowAddressKeepsTheWholeBudget(t *testing.T) {
	t.Parallel()
	store := openStore(t)
	peer := newCapture(t, multiPeerID)
	trustAt(t, store, peer.public, peer.address(t))
	publish(t, store, "claude:shared", model.Audience{Mode: model.AudienceAllPaired})

	publisher := publisherFor(t, store)
	slowConnect(publisher, slowLink, peer.address(t))
	started := time.Now()
	result, err := publisher.PublishOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Delivered != 1 || len(peer.envelopes) != 1 {
		t.Fatalf("result = %+v, peer received %d after %s; a %s connection to a peer's only address should get through",
			result, len(peer.envelopes), time.Since(started), slowLink)
	}
}

// With an alternate behind it, the preferred address still gives up after one
// attempt, not after the slow link finally connects.
func TestASlowPreferredAddressGivesUpAfterOneAttempt(t *testing.T) {
	t.Parallel()
	store := openStore(t)
	slow, fast := twoAddressesOfOnePeer(t)
	trustAt(t, store, slow.public, slow.address(t), fast.address(t))
	publish(t, store, "claude:shared", model.Audience{Mode: model.AudienceAllPaired})

	publisher := publisherFor(t, store)
	slowConnect(publisher, slowLink, slow.address(t))
	started := time.Now()
	result, err := publisher.PublishOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(started)
	if result.Delivered != 1 || len(fast.envelopes) != 1 || slow.challenges.Load() != 0 {
		t.Fatalf("result = %+v, alternate received %d, slow address challenged %d times; want the alternate used",
			result, len(fast.envelopes), slow.challenges.Load())
	}
	if elapsed < publisher.attemptTimeout || elapsed >= slowLink {
		t.Fatalf("the round took %s; want the slow address abandoned at %s, before its %s connection completed",
			elapsed, publisher.attemptTimeout, slowLink)
	}
}

// The production numbers are the ADR's: 3 s a connection, 10 s in all.
func TestTheDialBudgetIsTheADRs(t *testing.T) {
	publisher := publisherFor(t, openStore(t))
	if publisher.attemptTimeout != 3*time.Second || publisher.searchBudget != 10*time.Second {
		t.Fatalf("attempt %s, budget %s; ADR-005 §4 says 3s inside the existing 10s",
			publisher.attemptTimeout, publisher.searchBudget)
	}
	// The attempt's context is what cuts one address short; a timeout on the
	// dialer would cut short a peer's only address too.
	if publisher.dialer.Timeout != 0 {
		t.Fatalf("the dialer times out after %s; the attempt context bounds a connection, not the dialer",
			publisher.dialer.Timeout)
	}
}

// An address the policy refuses is left out and the next one is used; with
// every address refused the peer is skipped, not failed, as it always was.
func TestARefusedAlternateIsSkippedAndTheRestAreTried(t *testing.T) {
	store := openStore(t)
	peer := newCapture(t, multiPeerID)
	trust(t, store, multiPeerID, "", peer.public)
	// Written past the policy, as a row from a node that allowed LAN would be.
	allow := func(string) error { return nil }
	if err := store.SetNodeAddresses(context.Background(), multiPeerID,
		[]string{"192.168.1.20:7463", peer.address(t)}, allow); err != nil {
		t.Fatal(err)
	}
	publish(t, store, "claude:shared", model.Audience{Mode: model.AudienceAllPaired})

	result, err := publisherFor(t, store).PublishOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Delivered != 1 || len(peer.envelopes) != 1 {
		t.Fatalf("result = %+v; want the loopback alternate used", result)
	}

	if err := store.SetNodeAddresses(context.Background(), multiPeerID,
		[]string{"192.168.1.20:7463", "10.0.0.5:7463"}, allow); err != nil {
		t.Fatal(err)
	}
	result, err = publisherFor(t, store).PublishOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped != 1 || result.Failed != 0 {
		t.Fatalf("result = %+v; every address refused is a skip", result)
	}
}
