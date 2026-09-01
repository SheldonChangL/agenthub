package api

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Rates for the peer surface.
//
// These are generous compared with what a working peer needs — a node publishes
// on a 15-second interval, so two requests per peer per round — and tight
// compared with what an unauthenticated caller should get for free. The point
// is not to make abuse impossible; it is to keep one host from occupying the
// listener, and to make the cost of trying visible in the refusals.
const (
	peerRequestsPerMinute = 120
	peerBurst             = 20
	// limiterIdleTTL is how long a source is remembered after it goes quiet.
	// Without expiry the limiter is itself a memory leak an attacker drives by
	// sending one request from each of many addresses.
	limiterIdleTTL = 10 * time.Minute
	// maxTrackedSources bounds the table regardless of TTL, for the case where
	// an attacker cycles addresses faster than they expire.
	maxTrackedSources = 4096
)

// rateLimiter throttles by source address using a token bucket per source.
//
// It is deliberately per-source rather than global: a global limit lets one
// host starve every other peer, which turns a throttle into an outage.
type rateLimiter struct {
	mutex   sync.Mutex
	buckets map[string]*bucket
	now     func() time.Time

	perMinute float64
	burst     float64
	ttl       time.Duration
	maxKeys   int
}

type bucket struct {
	tokens   float64
	lastSeen time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{
		buckets:   map[string]*bucket{},
		now:       time.Now,
		perMinute: peerRequestsPerMinute,
		burst:     peerBurst,
		ttl:       limiterIdleTTL,
		maxKeys:   maxTrackedSources,
	}
}

// allow reports whether a request from source may proceed.
func (l *rateLimiter) allow(source string) bool {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	now := l.now()

	existing, found := l.buckets[source]
	if !found {
		// Only sweep when adding a key. Sweeping on every request would make
		// the common path proportional to the table size, which is the sort of
		// thing an attacker measures and then exploits.
		l.sweepLocked(now)
		if len(l.buckets) >= l.maxKeys {
			// The table is full of other sources. Refusing is the safe answer:
			// admitting an untracked request would make the cap a way to bypass
			// the limiter rather than a bound on it.
			return false
		}
		l.buckets[source] = &bucket{tokens: l.burst - 1, lastSeen: now}
		return true
	}

	elapsed := now.Sub(existing.lastSeen).Minutes()
	existing.tokens = min(existing.tokens+elapsed*l.perMinute, l.burst)
	existing.lastSeen = now
	if existing.tokens < 1 {
		return false
	}
	existing.tokens--
	return true
}

func (l *rateLimiter) sweepLocked(now time.Time) {
	for source, tracked := range l.buckets {
		if now.Sub(tracked.lastSeen) > l.ttl {
			delete(l.buckets, source)
		}
	}
}

// limitPeers refuses requests from a source that is asking too often.
//
// The source is the connecting address, not a header. X-Forwarded-For and its
// relatives are written by whoever is connecting, so limiting by them would let
// a caller reset its own budget by inventing a new value per request.
func limitPeers(limiter *rateLimiter, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		source, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			source = r.RemoteAddr
		}
		if !limiter.allow(source) {
			w.Header().Set("Retry-After", "60")
			writeError(w, http.StatusTooManyRequests, "TOO_MANY_REQUESTS",
				"too many requests from this address")
			return
		}
		next.ServeHTTP(w, r)
	})
}
