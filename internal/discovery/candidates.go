package discovery

import (
	"context"
	"sort"
	"sync"
	"time"
)

// CandidateTTL is how long a candidate stays listed after its last packet.
//
// Nothing announces a withdrawal, so this is the only thing that removes a row.
// Short enough that a list left open does not fill with machines that have gone
// home, long enough to survive a few dropped multicast packets: mDNS is UDP on a
// network that may be busy, and a candidate vanishing between refreshes is a
// worse experience than one lingering for a minute.
const CandidateTTL = 90 * time.Second

// MaxCandidates bounds the list.
//
// Anyone who can send multicast can invent node ids, and a list that grows per
// packet is memory an attacker allocates. At the bound, new ids are refused
// rather than evicting existing ones: evicting would let a flood push the
// machine the owner is actually looking for off the list, which is the outcome
// an attacker wants. Candidates already listed keep refreshing normally.
const MaxCandidates = 64

// Candidates is what the owner sees while looking for a machine to pair with.
//
// It holds claims. Nothing here has been verified, nothing here is trusted, and
// appearing in this list changes no audience and no trust record. The whole
// list is throwaway state: it lives in memory, and a restart empties it.
type Candidates struct {
	// paired reports whether a node is already in the trust store, so the list
	// shows what the owner might still want rather than what they have.
	paired func(ctx context.Context, nodeID string) (bool, error)
	// policy is the same address rule delivery uses. A candidate this node
	// could never deliver to is not a candidate.
	policy AddressPolicy
	now    func() time.Time

	mu   sync.Mutex
	seen map[string]Candidate
}

func NewCandidates(paired func(ctx context.Context, nodeID string) (bool, error), policy AddressPolicy) *Candidates {
	return &Candidates{
		paired: paired,
		policy: policy,
		now:    func() time.Time { return time.Now().UTC() },
		seen:   map[string]Candidate{},
	}
}

// Observe records an announcement from a node offering to pair.
//
// It reports whether the list changed, which is what a caller logs on: a
// candidate re-announcing every second should not produce a line every second.
func (c *Candidates) Observe(ctx context.Context, announcement Announcement) (bool, error) {
	if !announcement.Offering() || announcement.NodeID == "" || announcement.Address == "" {
		return false, nil
	}
	// A node this owner already paired with is not a candidate. Checked before
	// anything is stored, so a paired node's announcements cannot occupy a slot
	// in a bounded list.
	paired, err := c.paired(ctx, announcement.NodeID)
	if err != nil {
		return false, err
	}
	if paired {
		return false, nil
	}
	if err := c.policy(announcement.Address); err != nil {
		// Not logged here: on a shared network this is most packets, and the
		// caller decides what is worth saying.
		return false, nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire()

	now := c.now()
	existing, known := c.seen[announcement.NodeID]
	if !known && len(c.seen) >= MaxCandidates {
		return false, nil
	}
	candidate := Candidate{
		NodeID:      announcement.NodeID,
		Address:     announcement.Address,
		DisplayName: announcement.DisplayName,
		Platform:    announcement.Platform,
		Fingerprint: announcement.Fingerprint,
		FirstSeen:   now,
		LastSeen:    now,
	}
	if known {
		candidate.FirstSeen = existing.FirstSeen
	}
	c.seen[announcement.NodeID] = candidate

	// "Changed" means a person looking at the list would see something
	// different. A refreshed timestamp is not that.
	return !known ||
		existing.Address != candidate.Address ||
		existing.DisplayName != candidate.DisplayName ||
		existing.Platform != candidate.Platform ||
		existing.Fingerprint != candidate.Fingerprint, nil
}

// List returns the candidates still within their TTL, oldest sighting first so
// the order does not jump around as packets arrive.
func (c *Candidates) List() []Candidate {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire()

	out := make([]Candidate, 0, len(c.seen))
	for _, candidate := range c.seen {
		out = append(out, candidate)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].FirstSeen.Equal(out[j].FirstSeen) {
			return out[i].FirstSeen.Before(out[j].FirstSeen)
		}
		return out[i].NodeID < out[j].NodeID
	})
	return out
}

// Forget drops a candidate. Pairing with one calls this: it has become a peer,
// and leaving it in the list would invite pairing with it twice.
func (c *Candidates) Forget(nodeID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.seen, nodeID)
}

// expire drops what has gone quiet. Called under the lock by everything that
// reads or writes, so a caller cannot see a candidate that has timed out.
func (c *Candidates) expire() {
	cutoff := c.now().Add(-CandidateTTL)
	for id, candidate := range c.seen {
		if candidate.LastSeen.Before(cutoff) {
			delete(c.seen, id)
		}
	}
}
