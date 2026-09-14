package discovery

import (
	"sync"
	"time"
)

// trustCacheTTL is how long an answer from the trust store may be reused before
// it is asked for again.
//
// Why there is a cache at all: both readers of a packet ask the trust store
// about ids they hold no row for, and an id that is already paired never gets a
// row in the candidate list — it is not a pairing candidate. So a replayed
// announcement carrying a paired node's id costs one read per packet, for as
// long as the packets keep coming, and nothing ever fills up to stop it
// (issue #92). The browser has the same shape by the other handler: it reads
// the whole trusted set per packet, including for this node's own announcements
// coming back on the multicast loopback.
//
// Why three seconds. The number sits between the two timescales it has to
// separate. Packets arrive as fast as whoever is sending them can send them, so
// bounding the reads at one per three seconds turns a per-packet read into a
// rounding error however hard a flood pushes. In the other direction this is
// exactly how long an answer may be wrong: a node revoked while its
// announcements are arriving can stay excluded from the candidate list, and can
// keep having its address recorded, for up to three seconds after the revoke.
//
// Three seconds is the whole of that bound, and it does not move with how fast
// packets arrive: an entry is stamped now+TTL when it is written, a hit never
// pushes that stamp forward, and expire() runs at the top of both readers, so
// the entry is gone on the first read past its deadline no matter how many
// packets landed in between. A sender flooding the group buys itself nothing
// but reads it does not get. (What the 20-second announce interval of an open
// pairing window says is only that in ordinary use the wait for the next packet
// dominates, so the three seconds are never reached — it is not what bounds
// them, because an attacker picks their own send rate.)
//
// Note what the bound is not attached to: unpairing does not invalidate these
// entries explicitly. This TTL is the only thing that ends a stale exclusion,
// which is why it is pinned by a test against a real duration rather than
// against its own symbol — see TestTheTrustCacheTTLStaysWithinItsClaimedBound.
//
// Not longer, because that bound is the entire cost of the cache and it only
// buys fewer reads against a flood that is a nuisance rather than a denial of
// service. Not shorter, because below a second the cache stops absorbing
// anything a slow store would notice while the bound gains nothing a person
// could perceive.
const trustCacheTTL = 3 * time.Second

// pairedCache remembers, for trustCacheTTL, that a node id was found in the
// trust store.
//
// One direction only, and deliberately. A "paired" answer is remembered because
// that is the answer which leaves nothing behind to stop the next packet asking
// again; a "not paired" answer is never remembered, because the caller acts on
// it by creating a candidate row and that row is what suppresses the next read.
//
// It stays a negative cache — a short-lived reason to skip a read — rather than
// becoming a positive record of who is paired: an entry is written once and
// expires on schedule, and a hit does not extend it. If hits renewed the entry,
// a steady replay would hold the exclusion open for as long as it kept sending
// and a node the owner had just unpaired would never be re-checked, which is
// the one thing this must not do.
//
// Its size is bounded by the trust store: only ids the store answered "paired"
// for are ever written, so invented ids cannot grow it.
type pairedCache struct {
	now func() time.Time

	mu    sync.Mutex
	until map[string]time.Time
}

func newPairedCache(now func() time.Time) *pairedCache {
	return &pairedCache{now: now, until: map[string]time.Time{}}
}

// excluded reports whether this id was found paired recently enough that asking
// again would tell the caller nothing new.
func (p *pairedCache) excluded(nodeID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expire()
	_, cached := p.until[nodeID]
	return cached
}

// remember records that the trust store said this id is paired.
func (p *pairedCache) remember(nodeID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.expire()
	if _, cached := p.until[nodeID]; cached {
		// Already remembered, and its deadline is not moved: see the type's
		// comment — an entry that a hit could renew is an exclusion a replay
		// can hold open indefinitely.
		return
	}
	p.until[nodeID] = p.now().Add(trustCacheTTL)
}

// expire drops entries whose TTL has passed. Called under the lock by both
// readers, so an expired entry is never visible.
func (p *pairedCache) expire() {
	now := p.now()
	for id, until := range p.until {
		if !until.After(now) {
			delete(p.until, id)
		}
	}
}
