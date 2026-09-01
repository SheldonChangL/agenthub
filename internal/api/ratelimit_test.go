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

	clock = clock.Add(time.Minute)
	allowed := 0
	for range peerRequestsPerMinute {
		if limiter.allow("192.0.2.10") {
			allowed++
		}
	}
	if allowed == 0 {
		t.Fatal("no budget came back after a minute")
	}
	if allowed > peerBurst {
		t.Fatalf("allowed %d after a minute; the refill is not capped at the burst", allowed)
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
