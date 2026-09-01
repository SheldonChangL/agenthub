package api

import (
	"net"
	"net/http"
	"net/netip"
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
	sweptAt   time.Time
}

type bucket struct {
	tokens float64
	// refilledAt drives the token maths and moves on every request, allowed or
	// not, because credit accrues with time regardless of what is asked for.
	refilledAt time.Time
	// activeAt moves only when a request is allowed, and is what decides expiry
	// and eviction. Refused requests must not keep an entry alive: if they did,
	// a source that only ever gets refused could hold its slot forever at the
	// cost of one request per TTL.
	activeAt time.Time
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
	key := limiterKey(source)
	l.mutex.Lock()
	defer l.mutex.Unlock()
	now := l.now()

	existing, found := l.buckets[key]
	if !found {
		l.makeRoomLocked(now)
		l.buckets[key] = &bucket{tokens: l.burst - 1, refilledAt: now, activeAt: now}
		return true
	}

	// A clock that went backwards must not subtract credit. Only an injected
	// clock can do this — time.Now carries a monotonic reading — but an
	// unguarded negative would drive tokens arbitrarily below zero and lock the
	// source out for as long as it took to climb back.
	elapsed := now.Sub(existing.refilledAt).Minutes()
	if elapsed < 0 {
		elapsed = 0
	}
	existing.tokens = min(existing.tokens+elapsed*l.perMinute, l.burst)
	existing.refilledAt = now
	if existing.tokens < 1 {
		return false
	}
	existing.tokens--
	existing.activeAt = now
	return true
}

// makeRoomLocked ensures a new source can always be tracked.
//
// Refusing when the table is full would be worse than not limiting at all. An
// attacker with 4096 addresses — one IPv6 host has as many as it wants — could
// fill the table and then every peer not already tracked would be refused
// permanently, which is a switch for locking out every peer rather than a bound
// on cost. So a full table evicts instead: the least recently *allowed* entry
// goes. A working peer speaks every 15 seconds, so it is never the oldest,
// while an attacker cycling addresses evicts only its own earlier entries.
func (l *rateLimiter) makeRoomLocked(now time.Time) {
	// Sweeping is throttled because it is O(n) under the one mutex every peer
	// request needs, and an attacker who keeps the table full otherwise decides
	// when that cost is paid.
	if now.Sub(l.sweptAt) >= l.ttl/2 {
		l.sweepLocked(now)
		l.sweptAt = now
	}
	for len(l.buckets) >= l.maxKeys {
		oldestKey, oldest := "", time.Time{}
		for candidate, tracked := range l.buckets {
			if oldestKey == "" || tracked.activeAt.Before(oldest) {
				oldestKey, oldest = candidate, tracked.activeAt
			}
		}
		if oldestKey == "" {
			return
		}
		delete(l.buckets, oldestKey)
	}
}

// limiterKey groups a source into the unit being limited.
//
// IPv6 is grouped by /64. A single host is routinely given a whole /64 and can
// bind as many addresses in it as it likes, so keying on the full address would
// let one machine mint an unbounded number of budgets. IPv4 is keyed exactly:
// addresses there are scarce enough that a /24 would sweep up unrelated hosts.
func limiterKey(source string) string {
	address, err := netip.ParseAddr(source)
	if err != nil || !address.Is6() || address.Is4In6() {
		return source
	}
	prefix, err := address.Prefix(64)
	if err != nil {
		return source
	}
	return prefix.String()
}

func (l *rateLimiter) sweepLocked(now time.Time) {
	for source, tracked := range l.buckets {
		if now.Sub(tracked.activeAt) > l.ttl {
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
