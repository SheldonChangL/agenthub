package discovery

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sort"
	"sync"
	"time"

	"agenthub.local/agenthub/internal/identity"
	"agenthub.local/agenthub/internal/model"
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
//
// Refusing has its own failure, and it is why ErrCandidatesFull exists: a
// colleague who opens pairing mode while the list is full never appears. Silent
// invisibility is worse than a visible refusal, so the caller is told, and the
// owner can be shown that the list is full and pair by hand instead.
const MaxCandidates = 64

// ErrCandidatesFull reports that a new candidate could not be listed.
//
// Returned rather than swallowed so a UI can say "the list is full" instead of
// showing a short list that looks complete.
var ErrCandidatesFull = errors.New("the candidate list is full")

// Candidates is what the owner sees while looking for a machine to pair with.
//
// It holds claims. Nothing here has been verified, nothing here is trusted, and
// appearing in this list changes no audience and no trust record. The whole
// list is throwaway state: it lives in memory, and a restart empties it.
type Candidates struct {
	// localNodeID is this node's own id. Its announcements come back on the
	// loopback of the multicast group it sends to, and a machine offering to
	// pair with itself is a row that can only waste the owner's time.
	localNodeID string
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

func NewCandidates(localNodeID string, paired func(ctx context.Context, nodeID string) (bool, error), policy AddressPolicy) *Candidates {
	return &Candidates{
		localNodeID: localNodeID,
		paired:      paired,
		policy:      policy,
		now:         func() time.Time { return time.Now().UTC() },
		seen:        map[string]Candidate{},
	}
}

// ObserveAll records every offer in one packet.
//
// One datagram can carry hundreds of address records for a handful of node ids,
// so the packet is reduced to one offer per node before anything asks the trust
// store — the same reasoning as ApplyAll, for the same reason: a trust read per
// record hands anyone who can send multicast a way to drive database reads at
// packet rate.
//
// source is the address the datagram came from. A node's announced address must
// be one it is actually answering at; an offer claiming to be somewhere else is
// how one host sprays a list with candidates that all point at it.
//
// A caller holding a packet always has its source, so an invalid one is a bug
// in the caller rather than a case to tolerate — and tolerating it would
// silently disable the check.
func (c *Candidates) ObserveAll(ctx context.Context, source netip.Addr, announcements []Announcement) (int, error) {
	if !source.IsValid() {
		return 0, errors.New("the datagram's source address is required to observe offers")
	}
	// First offer per node id wins within a packet. A node with six addresses
	// in one datagram is one candidate, not six, and the sender does not get to
	// choose which by ordering — see the address pinning in observe.
	offers := make([]Announcement, 0, len(announcements))
	seenInPacket := make(map[string]struct{}, len(announcements))
	for _, announcement := range announcements {
		if !announcement.Offering() {
			continue
		}
		// Before the reduction, not after. A multi-homed node announces every
		// address it has in one packet, and only one of them is the one the
		// datagram came from — keeping the first and checking afterwards drops
		// the node entirely, which is every dual-stack machine on the network.
		if !announcedFrom(announcement.Address, source) {
			continue
		}
		if _, repeated := seenInPacket[announcement.NodeID]; repeated {
			continue
		}
		seenInPacket[announcement.NodeID] = struct{}{}
		offers = append(offers, announcement)
	}
	if len(offers) == 0 {
		return 0, nil
	}

	changed := 0
	var full error
	for _, offer := range offers {
		didChange, err := c.observe(ctx, source, offer)
		if errors.Is(err, ErrCandidatesFull) {
			// Recorded and carried out, but the rest of the packet is still
			// worth reading: an already-listed candidate refreshing is not
			// affected by the list being full.
			full = err
			continue
		}
		if err != nil {
			return changed, err
		}
		if didChange {
			changed++
		}
	}
	return changed, full
}

func (c *Candidates) observe(ctx context.Context, source netip.Addr, announcement Announcement) (bool, error) {
	if !announcement.Offering() || announcement.Address == "" {
		return false, nil
	}
	// The id is the map key and a field on a person's screen, so it has to be
	// an id. Before this list existed a hostile id was inert — it could only
	// fail to match the trust store — and now it is displayed, which is what
	// ValidateNodeID exists to stop: "node_a" and "node_a " and a lookalike
	// built from non-ASCII characters must not be three rows that look like
	// one.
	if model.ValidateNodeID(announcement.NodeID) != nil {
		return false, nil
	}
	// This node's own announcement, returned by the multicast loopback. Checked
	// before anything else it would cost: it is the one id guaranteed to be
	// announcing whenever this list is being filled.
	if announcement.NodeID == c.localNodeID {
		return false, nil
	}
	// The fingerprint is canonical by the time it is stored, whichever way the
	// announcement was built: comparison between rows is equality, and that
	// only means anything if every value went through the same parser.
	canonical, err := identity.ParseFingerprint(announcement.Fingerprint)
	if err != nil {
		return false, nil
	}
	announcement.Fingerprint = canonical
	// An offer has to come from where it says it is. Without this, one host
	// sprays a list full of candidates that all resolve to itself, and the
	// owner picks one of them.
	if source.IsValid() && !announcedFrom(announcement.Address, source) {
		return false, nil
	}
	// The cheap, local checks first, and the bound before any of them reaches
	// the trust store: a full list must not still cost a database read per
	// packet.
	if err := c.policy(announcement.Address); err != nil {
		// Not logged here: on a shared network this is most packets, and the
		// caller decides what is worth saying.
		return false, nil
	}

	c.mu.Lock()
	c.expire()
	_, known := c.seen[announcement.NodeID]
	if !known && len(c.seen) >= MaxCandidates {
		c.mu.Unlock()
		return false, ErrCandidatesFull
	}
	c.mu.Unlock()
	// Whether the trust store was asked, which is not the same as whether the
	// row existed a moment ago: the row can be forgotten — pairing does exactly
	// that — or expire between the two sections below, and inserting then would
	// list a peer this owner has already paired with, never to be re-checked
	// because refreshes do not ask again.
	askedTrustStore := !known

	// Only for a row that is not there yet. A listed candidate's paired status
	// was checked when it was inserted, and pairing with one calls Forget — so
	// asking again on every refresh would be one database read per packet per
	// row, which is the amplification this file exists to avoid.
	if !known {
		paired, err := c.paired(ctx, announcement.NodeID)
		if err != nil {
			return false, err
		}
		if paired {
			return false, nil
		}
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire()

	now := c.now()
	existing, known := c.seen[announcement.NodeID]
	if !known && !askedTrustStore {
		// The row went away while the trust store was not being asked. Drop
		// this packet rather than insert unchecked; a node in pairing mode
		// announces again in seconds, and that one takes the full path.
		return false, nil
	}
	if !known && len(c.seen) >= MaxCandidates {
		return false, ErrCandidatesFull
	}
	if known {
		// The address and the fingerprint are fixed at the first sighting and do
		// not move while the row lives. Anyone on the group can read a
		// candidate's id — every node broadcasts it — so if the newest packet
		// won, an attacker would rewrite the row the owner is looking at to
		// point at themselves and the owner would click the name they
		// recognise.
		//
		// A packet that disagrees does not refresh the row either. That is the
		// difference between pinning and first-writer-wins: without it a forger
		// keeps a row alive after the machine it names has gone, and a node
		// that genuinely moved could never expire because its own new packets
		// kept the stale row young. Now the stale row goes quiet and dies on
		// schedule, and the mover reappears at its new address.
		//
		// Disagreement is not silent. Someone claiming this id is either the
		// node moving or an impersonator, and the person choosing a row is the
		// only one who can tell.
		switch {
		case announcement.Fingerprint != existing.Fingerprint:
			// A different key under this id. Either the node was re-keyed or
			// someone else is claiming it, and only the person comparing
			// fingerprints in the handshake can tell.
			return c.contest(announcement.NodeID, existing)
		case relateAddresses(announcement.Address, existing.Address) == addressConflicts:
			// The node moved, or someone is claiming its id at another address
			// in the same family.
			return c.contest(announcement.NodeID, existing)
		case relateAddresses(announcement.Address, existing.Address) == addressOtherFamily:
			// The same id seen over the other address family. A dual-stack
			// machine announces on both groups and each datagram reduces to the
			// address matching its own source, so this is the ordinary case for
			// one node — and it is indistinguishable, at this layer, from
			// someone claiming the id from the family this row is not pinned to.
			// It is deliberately not flagged: flagging it would mark every
			// dual-stack machine on the network, which would make the flag
			// noise exactly where it is meant to be read. The fingerprint
			// comparison in the handshake is what separates the two cases.
			//
			// It does not refresh either, so a forger on the other family
			// cannot keep this row alive after the node it names has gone.
			return false, nil
		}
		existing.LastSeen = now
		c.seen[announcement.NodeID] = existing
		return false, nil
	}
	c.seen[announcement.NodeID] = Candidate{
		NodeID:      announcement.NodeID,
		Address:     announcement.Address,
		DisplayName: announcement.DisplayName,
		Platform:    announcement.Platform,
		Fingerprint: announcement.Fingerprint,
		FirstSeen:   now,
		LastSeen:    now,
	}
	return true, nil
}

// addressRelation says how an announced address stands to the one a row is
// pinned to. Three outcomes, one function: deciding them with two predicates
// over two representations of an address is how a port change ended up in the
// branch for a different address family.
type addressRelation int

const (
	// addressSame is the same node at the same place: refresh the row.
	addressSame addressRelation = iota
	// addressOtherFamily is the same node reached over its other address
	// family, or someone claiming the id there — see the caller.
	addressOtherFamily
	// addressConflicts is anything else: a different host in the same family, a
	// different port on the same host, or an address that does not parse and is
	// not identical.
	addressConflicts
)

func relateAddresses(announced, existing string) addressRelation {
	a, aOK := parseHostPort(announced)
	b, bOK := parseHostPort(existing)
	if !aOK || !bOK {
		if announced == existing {
			return addressSame
		}
		return addressConflicts
	}
	switch {
	case a == b:
		return addressSame
	case a.Addr().Is4() != b.Addr().Is4():
		return addressOtherFamily
	default:
		return addressConflicts
	}
}

// parseHostPort normalises an address for comparison. Unmap because a v4 host
// can be written as a v4-mapped v6 one, and the zone goes because it describes
// the local interface a packet arrived on rather than the peer.
func parseHostPort(address string) (netip.AddrPort, bool) {
	parsed, err := netip.ParseAddrPort(address)
	if err != nil {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(parsed.Addr().Unmap().WithZone(""), parsed.Port()), true
}

// contest marks a row whose id something else has claimed, once.
func (c *Candidates) contest(nodeID string, existing Candidate) (bool, error) {
	if existing.Contested {
		return false, nil
	}
	existing.Contested = true
	c.seen[nodeID] = existing
	return true, nil
}

// announcedFrom reports whether an announced address names the host the packet
// came from. The port is not compared: a node answers peers on a different port
// than it sends multicast from.
func announcedFrom(address string, source netip.Addr) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	announced, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	// Unmap because a dual-stack socket reports an IPv4 sender as ::ffff:a.b.c.d
	// while the A record parses as a.b.c.d. WithZone("") because a link-local
	// source arrives as fe80::1%en0 and an AAAA record cannot carry the zone,
	// so comparing them with it would refuse every IPv6 link-local node.
	return announced.Unmap().WithZone("") == source.Unmap().WithZone("")
}

// List returns the candidates still within their TTL, oldest sighting first so
// the order does not jump around as packets arrive.
//
// A row whose display name or announced fingerprint matches another's is marked
// Duplicate. That is not a guess about which is genuine — it cannot be one —
// but it is the signal a person needs: two rows claiming the same fingerprint
// means at least one of them is lying, and the answer is not to pick either.
func (c *Candidates) List() []Candidate {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire()

	out := make([]Candidate, 0, len(c.seen))
	names := map[string]int{}
	prints := map[string]int{}
	for _, candidate := range c.seen {
		out = append(out, candidate)
		// Compared by the profile's own key rather than by bytes: "café"
		// composed and decomposed are different bytes and the same name, and so
		// are "Laptop" and "laptop". An impersonator would use exactly those.
		if candidate.DisplayName != "" {
			names[fieldKey(candidate.DisplayName)]++
		}
		prints[candidate.Fingerprint]++
	}
	for i := range out {
		sharedName := out[i].DisplayName != "" && names[fieldKey(out[i].DisplayName)] > 1
		out[i].Duplicate = sharedName || prints[out[i].Fingerprint] > 1
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].FirstSeen.Equal(out[j].FirstSeen) {
			return out[i].FirstSeen.Before(out[j].FirstSeen)
		}
		return out[i].NodeID < out[j].NodeID
	})
	return out
}

// Full reports whether the list is at its bound, so a caller can say so rather
// than showing a short list that looks complete.
func (c *Candidates) Full() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.expire()
	return len(c.seen) >= MaxCandidates
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
