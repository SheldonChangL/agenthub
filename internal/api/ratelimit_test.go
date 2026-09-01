package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func fixedLimiter(clock *time.Time) *rateLimiter {
	limiter := newRateLimiter()
	limiter.now = func() time.Time { return *clock }
	return limiter
}

// TestABurstIsAllowedThenRefused pins the shape of the budget: a peer that
// publishes normally is never touched, and a caller that keeps going is stopped.
func TestABurstIsAllowedThenRefused(t *testing.T) {
	clock := time.Now()
	limiter := fixedLimiter(&clock)

	for attempt := range peerBurst {
		if !limiter.allow("192.0.2.10") {
			t.Fatalf("request %d of the burst was refused", attempt+1)
		}
	}
	if limiter.allow("192.0.2.10") {
		t.Fatal("the request after the burst was allowed")
	}
}

// TestTheBudgetRefillsOverTime keeps the limiter from being a permanent ban.
func TestTheBudgetRefillsOverTime(t *testing.T) {
	clock := time.Now()
	limiter := fixedLimiter(&clock)
	for range peerBurst {
		limiter.allow("192.0.2.10")
	}
	if limiter.allow("192.0.2.10") {
		t.Fatal("the burst was not exhausted")
	}

	// Five seconds at 120/min is exactly ten tokens. Advancing a full minute
	// would prove nothing about the rate: anything at or above the burst
	// saturates the cap, so a refill 60x too fast or 60x too slow would both
	// pass. The clock is fixed, so this is deterministic.
	clock = clock.Add(5 * time.Second)
	allowed := 0
	for range peerBurst * 2 {
		if limiter.allow("192.0.2.10") {
			allowed++
		}
	}
	if want := peerRequestsPerMinute * 5 / 60; allowed != want {
		t.Fatalf("allowed %d after five seconds; want exactly %d at %d/min", allowed, want, peerRequestsPerMinute)
	}

	// And the cap still holds over a long gap.
	clock = clock.Add(time.Hour)
	allowed = 0
	for range peerBurst * 3 {
		if limiter.allow("192.0.2.10") {
			allowed++
		}
	}
	if allowed != peerBurst {
		t.Fatalf("allowed %d after an hour; the refill must be capped at the burst of %d", allowed, peerBurst)
	}
}

// TestOneSourceCannotStarveAnother is why the limit is per source. A global
// limit would let one host turn a throttle into an outage for every peer.
func TestOneSourceCannotStarveAnother(t *testing.T) {
	clock := time.Now()
	limiter := fixedLimiter(&clock)

	for range peerBurst * 3 {
		limiter.allow("192.0.2.10")
	}
	if limiter.allow("192.0.2.10") {
		t.Fatal("the noisy source still has budget")
	}
	if !limiter.allow("192.0.2.11") {
		t.Fatal("a quiet source was refused because another source was noisy")
	}
}

// TestTheTableDoesNotGrowWithoutBound covers the limiter becoming the attack:
// one request from each of many addresses would otherwise be a memory leak the
// attacker controls.
func TestTheTableDoesNotGrowWithoutBound(t *testing.T) {
	clock := time.Now()
	limiter := fixedLimiter(&clock)

	for i := range maxTrackedSources * 2 {
		limiter.allow(fmt.Sprintf("198.51.100.%d:%d", i%256, i))
	}
	limiter.mutex.Lock()
	tracked := len(limiter.buckets)
	limiter.mutex.Unlock()
	if tracked > maxTrackedSources {
		t.Fatalf("tracked %d sources; the cap is %d", tracked, maxTrackedSources)
	}

	// Once the sources go quiet they must be forgotten, or the cap becomes a
	// permanent refusal for everyone who arrives later.
	clock = clock.Add(limiterIdleTTL + time.Minute)
	if !limiter.allow("203.0.113.1") {
		t.Fatal("a new source was refused after the old ones went idle")
	}
}

// TestAFullTableStillAdmitsANewPeer is the case a full table must not break,
// and the one this test file previously had no coverage for.
//
// Refusing when the table is full would be worse than not limiting: an attacker
// with 4096 addresses fills it once and then every peer not already tracked is
// refused permanently. Keeping the entries warm costs about seven requests a
// second — cheaper than the flood the limiter exists to stop.
func TestAFullTableStillAdmitsANewPeer(t *testing.T) {
	clock := time.Now()
	limiter := fixedLimiter(&clock)

	// An attacker fills the table and keeps every entry warm.
	fill := func() {
		for i := range maxTrackedSources {
			limiter.allow(fmt.Sprintf("198.51.100.%d.%d", i/256, i%256))
			clock = clock.Add(time.Millisecond)
		}
	}
	fill()
	clock = clock.Add(limiterIdleTTL / 2)
	fill()

	// A peer that has never been seen must still get through.
	if !limiter.allow("203.0.113.7") {
		t.Fatal("a new peer was locked out by a full table")
	}
	// And it must have been recorded, not admitted untracked.
	limiter.mutex.Lock()
	_, tracked := limiter.buckets[limiterKey("203.0.113.7")]
	size := len(limiter.buckets)
	limiter.mutex.Unlock()
	if !tracked {
		t.Fatal("the new peer was admitted without being tracked, so it has no budget at all")
	}
	if size > maxTrackedSources {
		t.Fatalf("table grew to %d past the cap of %d", size, maxTrackedSources)
	}
}

// TestAnActivePeerIsNotEvicted pins which entry eviction chooses. A peer
// publishing every 15 seconds must never be the least recently active.
func TestAnActivePeerIsNotEvicted(t *testing.T) {
	clock := time.Now()
	limiter := fixedLimiter(&clock)
	const peer = "203.0.113.7"

	if !limiter.allow(peer) {
		t.Fatal("the peer's first request was refused")
	}
	for round := range 3 {
		// The attacker churns a full table's worth of addresses...
		for i := range maxTrackedSources {
			limiter.allow(fmt.Sprintf("198.51.%d.%d.%d", round, i/256, i%256))
			clock = clock.Add(time.Millisecond)
		}
		// ...and the peer speaks on its normal cadence.
		clock = clock.Add(15 * time.Second)
		if !limiter.allow(peer) {
			t.Fatalf("round %d: the peer was refused", round+1)
		}
	}
	limiter.mutex.Lock()
	_, present := limiter.buckets[limiterKey(peer)]
	limiter.mutex.Unlock()
	if !present {
		t.Fatal("an actively publishing peer was evicted by address churn")
	}
}

// TestRefusedRequestsDoNotKeepASourceAlive pins that only an allowed request
// counts as activity.
//
// Expiry and eviction both key on activity. If a refusal counted, a source
// that never succeeds could hold its slot indefinitely, which is exactly what
// an attacker filling the table wants.
//
// The assertion is on the timestamp rather than on eventual expiry: spacing
// requests far enough apart to reach the TTL also accrues enough budget for
// some of them to be allowed, so a timing-based version of this test would be
// measuring the refill, not the activity rule.
func TestRefusedRequestsDoNotKeepASourceAlive(t *testing.T) {
	clock := time.Now()
	limiter := fixedLimiter(&clock)
	const noisy = "198.51.100.1"

	for range peerBurst {
		if !limiter.allow(noisy) {
			t.Fatal("a request within the burst was refused")
		}
	}
	limiter.mutex.Lock()
	activeAfterBurst := limiter.buckets[limiterKey(noisy)].activeAt
	limiter.mutex.Unlock()

	// Time moves on, but not far enough to earn a whole token, so every one of
	// these is refused.
	for range 50 {
		clock = clock.Add(time.Millisecond)
		if limiter.allow(noisy) {
			t.Fatal("a request was allowed before a token had accrued")
		}
	}

	limiter.mutex.Lock()
	tracked := limiter.buckets[limiterKey(noisy)]
	limiter.mutex.Unlock()
	if !tracked.activeAt.Equal(activeAfterBurst) {
		t.Fatalf("activeAt moved from %v to %v on refused requests",
			activeAfterBurst, tracked.activeAt)
	}
	// The refill clock must still have moved, or no budget would ever return.
	if !tracked.refilledAt.After(activeAfterBurst) {
		t.Fatal("refilledAt did not advance, so the source would never recover")
	}
}

// TestIPv6IsGroupedByPrefix keeps one host from minting a budget per address.
func TestIPv6IsGroupedByPrefix(t *testing.T) {
	clock := time.Now()
	limiter := fixedLimiter(&clock)

	// Every address below is in one /64, which is what a single host is
	// routinely given.
	refused := false
	for i := range peerBurst * 3 {
		if !limiter.allow(fmt.Sprintf("2001:db8:0:1::%x", i+1)) {
			refused = true
			break
		}
	}
	if !refused {
		t.Fatal("one host varied its address within a /64 and got unlimited budget")
	}

	// A different /64 is a different budget.
	if !limiter.allow("2001:db8:0:2::1") {
		t.Fatal("a separate /64 was refused")
	}
	// And IPv4 is keyed exactly, not by prefix.
	if limiterKey("192.0.2.10") != "192.0.2.10" {
		t.Fatalf("IPv4 key = %q; want the address itself", limiterKey("192.0.2.10"))
	}
}

// TestTheLimiterIsSafeUnderConcurrentUse matches how it is actually called:
// one http.Server, many connections.
func TestTheLimiterIsSafeUnderConcurrentUse(t *testing.T) {
	limiter := newRateLimiter()
	var group sync.WaitGroup
	for worker := range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			for i := range 100 {
				limiter.allow(fmt.Sprintf("192.0.2.%d", (worker*100+i)%256))
			}
		}()
	}
	group.Wait()
}

// TestThePeerSurfaceRefusesAFlood drives the limiter through the real handler,
// so the middleware is checked rather than only the counter.
func TestThePeerSurfaceRefusesAFlood(t *testing.T) {
	_, _, peers := testSurfaces(t)

	refused := false
	for range peerBurst * 2 {
		request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		request.RemoteAddr = "192.0.2.10:34567"
		response := httptest.NewRecorder()
		peers.ServeHTTP(response, request)
		if response.Code == http.StatusTooManyRequests {
			refused = true
			if retry := response.Header().Get("Retry-After"); retry == "" {
				t.Error("a refusal did not say when to come back")
			}
			break
		}
	}
	if !refused {
		t.Fatal("the peer surface served an unbounded flood from one address")
	}

	// A different source is unaffected, through the real handler this time.
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	request.RemoteAddr = "192.0.2.11:34567"
	response := httptest.NewRecorder()
	peers.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("a quiet source got %d", response.Code)
	}
}

// TestTheOwnerSurfaceIsNotRateLimited keeps the throttle off the local API,
// where the caller already controls the process.
func TestTheOwnerSurfaceIsNotRateLimited(t *testing.T) {
	_, owner, _ := testSurfaces(t)
	for attempt := range peerBurst * 3 {
		request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		request.RemoteAddr = "127.0.0.1:34567"
		response := httptest.NewRecorder()
		owner.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("owner request %d got %d", attempt+1, response.Code)
		}
	}
}

// TestAForwardedHeaderCannotResetTheBudget pins that the source is the
// connection, not a header the caller writes.
func TestAForwardedHeaderCannotResetTheBudget(t *testing.T) {
	_, _, peers := testSurfaces(t)

	refused := false
	for i := range peerBurst * 2 {
		request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		request.RemoteAddr = "192.0.2.10:34567"
		request.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d", i%250+1))
		request.Header.Set("X-Real-IP", fmt.Sprintf("203.0.113.%d", i%250+1))
		response := httptest.NewRecorder()
		peers.ServeHTTP(response, request)
		if response.Code == http.StatusTooManyRequests {
			refused = true
			break
		}
	}
	if !refused {
		t.Fatal("a caller reset its own budget by varying a forwarded header")
	}
}
