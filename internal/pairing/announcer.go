package pairing

import (
	"context"
	"log"
	"net/netip"
	"sync"
	"time"

	"agenthub.local/agenthub/internal/discovery"
)

// AnnounceInterval is how often an open window re-announces.
//
// Bounded by discovery.CandidateTTL rather than chosen freely: a candidate is
// dropped that long after its last packet, so announcing less often than a
// third of it would make a listener show a machine flickering in and out as
// packets are lost. mDNS is UDP.
const AnnounceInterval = 20 * time.Second

// Addresses reports where this node answers peer traffic, for the announcement
// to carry.
//
// A function rather than a value because a machine's addresses change while it
// is running — a laptop moving from wifi to ethernet keeps its identity and
// loses its address — and an announcement carrying yesterday's address is an
// invitation to connect to nothing.
type Addresses func() []netip.Addr

// Status is what actually happened, as opposed to what was asked for.
//
// An open window and a machine that is advertising are different facts, and the
// gap between them is silent otherwise: a node whose peer listener is on
// loopback has no address a peer could use, so it announces nothing while the
// window says open, and the only sign is a log line nobody is reading.
type Status struct {
	// Addresses is how many this node would announce right now. Zero is the
	// whole explanation when nothing is going out.
	Addresses int `json:"announceableAddresses"`
	// LastAttempt and LastSuccess are absent until the loop has run.
	LastAttempt time.Time `json:"lastAttemptAt,omitzero"`
	LastSuccess time.Time `json:"lastAnnouncedAt,omitzero"`
	// LastError is the reason the last attempt failed, if it did.
	LastError string `json:"lastError,omitempty"`
}

// Announcer says on the local network that this node is willing to pair, but
// only while the window is open.
type Announcer struct {
	mode      *Mode
	group     string
	nodeID    string
	instance  string
	port      int
	addresses Addresses
	offer     discovery.Offer

	// announce is the call under test. Production passes
	// discovery.AnnounceOffering.
	announce func(ctx context.Context, group, nodeID, instance string, port int, addresses []netip.Addr, offer discovery.Offer) error
	interval time.Duration
	// wake lets opening the window announce at once rather than at the next
	// tick. Buffered so a caller never blocks and a second wake before the loop
	// reads the first is not two announcements.
	wake chan struct{}

	mu     sync.Mutex
	status Status
}

func NewAnnouncer(mode *Mode, group, nodeID, instance string, port int, addresses Addresses, offer discovery.Offer) *Announcer {
	return &Announcer{
		mode: mode, group: group, nodeID: nodeID, instance: instance,
		port: port, addresses: addresses, offer: offer,
		announce: discovery.AnnounceOffering,
		interval: AnnounceInterval,
		wake:     make(chan struct{}, 1),
	}
}

// Wake asks the loop to announce now.
//
// Without it, opening a window is followed by up to a full interval of silence
// while the UI says open — and the shortest window this node allows is thirty
// seconds, so that silence could be most of it.
func (a *Announcer) Wake() {
	select {
	case a.wake <- struct{}{}:
	default:
		// Already pending. One announcement answers both.
	}
}

// Status reports what the loop last managed to do.
func (a *Announcer) Status() Status {
	a.mu.Lock()
	status := a.status
	a.mu.Unlock()
	// Read directly rather than from the last attempt: this is the field an
	// owner looks at to understand why nothing is going out, and it should
	// answer before the loop has ticked even once.
	status.Addresses = len(a.addresses())
	return status
}

// Announceable reports whether this node has an address a peer could reach it
// at — which is its peer listener's own bound address, and nothing else. See
// PeerEndpoint for why the two are not the same question.
//
// Asked before opening a window, so an owner is told that pairing cannot work
// on this configuration instead of being given a window that announces nothing.
func (a *Announcer) Announceable() bool {
	return len(a.addresses()) > 0
}

// Run announces on every tick the window is open, and says nothing on every
// tick it is not.
//
// It runs for the process's life rather than being started and stopped with the
// window: a loop that is started on demand is a loop that can be started twice,
// and the thing that must be certain here is that nothing is announced while
// the window is closed. That is one condition, checked in one place.
func (a *Announcer) Run(ctx context.Context) {
	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()
	for {
		// Checked at the top as well as in announceIfOpen: a tick and a
		// cancellation ready together would otherwise send one more packet
		// after the process was told to stop.
		if ctx.Err() != nil {
			return
		}
		// Checked before announcing and again after each tick, so a window that
		// closed between ticks stops the very next announcement.
		a.announceIfOpen(ctx)
		// The interval is measured from the announcement, not from the last
		// tick. Without this a wake that lands just before a pending tick sends
		// two packets moments apart — observed on a live pair, seven seconds
		// between them — and the interval exists to space announcements out.
		ticker.Reset(a.interval)
		select {
		case <-ctx.Done():
			return
		case <-a.wake:
		case <-ticker.C:
		}
	}
}

func (a *Announcer) announceIfOpen(ctx context.Context) {
	if !a.mode.IsOpen() {
		return
	}
	addresses := a.addresses()
	now := time.Now().UTC()
	if len(addresses) == 0 {
		// Nothing to announce, and announcing without an address would list
		// this machine as a candidate nobody can reach. Recorded rather than
		// only logged: an owner asking why nothing is happening reads the API,
		// not the log.
		a.record(Status{LastAttempt: now,
			LastError: "this node has no address a peer could reach, so nothing is being announced"})
		return
	}
	if err := a.announce(ctx, a.group, a.nodeID, a.instance, a.port, addresses, a.offer); err != nil {
		a.record(Status{LastAttempt: now, LastError: err.Error()})
		log.Printf("pairing announcement failed: %v", err)
		return
	}
	a.record(Status{LastAttempt: now, LastSuccess: now})
}

// record keeps the last attempt, preserving the last success so an owner can
// see that announcements were working until something changed.
func (a *Announcer) record(status Status) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if status.LastSuccess.IsZero() {
		status.LastSuccess = a.status.LastSuccess
	}
	a.status = status
}
