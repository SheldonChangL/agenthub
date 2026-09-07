package pairing

import (
	"context"
	"log"
	"net/netip"
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
}

func NewAnnouncer(mode *Mode, group, nodeID, instance string, port int, addresses Addresses, offer discovery.Offer) *Announcer {
	return &Announcer{
		mode: mode, group: group, nodeID: nodeID, instance: instance,
		port: port, addresses: addresses, offer: offer,
		announce: discovery.AnnounceOffering,
		interval: AnnounceInterval,
	}
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
		// Checked before announcing and again after each tick, so a window that
		// closed between ticks stops the very next announcement.
		a.announceIfOpen(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (a *Announcer) announceIfOpen(ctx context.Context) {
	if !a.mode.IsOpen() {
		return
	}
	addresses := a.addresses()
	if len(addresses) == 0 {
		// Nothing to announce, and announcing without an address would list
		// this machine as a candidate nobody can reach.
		log.Print("pairing mode is open but this node has no address to announce")
		return
	}
	if err := a.announce(ctx, a.group, a.nodeID, a.instance, a.port, addresses, a.offer); err != nil {
		log.Printf("pairing announcement failed: %v", err)
	}
}
