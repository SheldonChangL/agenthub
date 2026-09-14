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
// That bound has to be invisible to a person, and it is — an open pairing
// window re-announces every 20 seconds (pairing.AnnounceInterval), so waiting
// for the next packet already dominates this by most of a minute, and the
// pairing flow is a human walking between two machines.
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
