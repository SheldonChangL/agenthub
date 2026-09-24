package nodeconfig

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
)

// PeerListenRetryInterval is how often a ListenerSet tries again the addresses
// it is configured to serve and has not bound.
//
// launchd starts the node at login, which is before Wi-Fi has joined anything:
// the address is configured and correct and not on this machine yet. Thirty
// seconds is short beside a login and long beside a bind that costs nothing.
const PeerListenRetryInterval = 30 * time.Second

// The state of one configured peer address, as the owner's API reports it.
const (
	// ListenerBound: this address is being served.
	ListenerBound = "bound"
	// ListenerFailed: the last attempt to bind it failed, for the reason
	// beside it. It is tried again every PeerListenRetryInterval.
	ListenerFailed = "failed"
	// ListenerPending: not attempted yet. Only before the set's first Bind.
	ListenerPending = "pending"
)

// ListenerState is one configured peer address and what became of it.
type ListenerState struct {
	Address string `json:"address"`
	State   string `json:"state"`
	// Reason is one of ListenAddressGone, ListenPortInUse or ListenUnusable
	// when the state is failed.
	Reason string `json:"reason,omitempty"`
	// Detail is the system's own words for the failure.
	Detail string `json:"detail,omitempty"`
	// Message is the same fact as a sentence, for a reader that does not know
	// the reason codes.
	Message string `json:"message,omitempty"`
}

// ListenProblem is what a node reports when none of the peer addresses it was
// configured to serve could be bound and it is serving loopback instead.
//
// Reason is one of the reason codes above, which is what a desktop switches on
// to offer the right repair — the addresses this machine does have, or a
// different port. Message is the same fact as a sentence, so a caller that does
// not recognise the code still has something true to show. Detail is the
// system's own words, kept because "can't assign requested address" is what an
// owner will search for.
//
// It names the first configured address, which is the one an older reader
// shows as the setting; peerListeners carries the rest.
type ListenProblem struct {
	Address   string `json:"address"`
	Reason    string `json:"reason"`
	Detail    string `json:"detail,omitempty"`
	RunningOn string `json:"runningOn"`
	Message   string `json:"message"`
}

// ListenerSetOptions are the parts of a ListenerSet a test replaces.
type ListenerSetOptions struct {
	// Fallback is the loopback address served when nothing configured binds.
	// DefaultPeerListen when empty.
	Fallback string
	// Limit is the connection cap shared by every listener in the set.
	// MaxPeerConnections when not positive.
	Limit int
	// Listen opens one socket. net.Listen when nil.
	Listen func(network, address string) (net.Listener, error)
	// Interfaces is what this machine holds, for classifying a failure.
	// InterfaceAddresses when nil.
	Interfaces func() ([]string, error)
	// Probe asks whether this machine will serve an address at all.
	// ProbeListen when nil.
	Probe func(network, address string) error
	// Logf is where the set says what it did. Discarded when nil.
	Logf func(string, ...any)
}

// ListenerSet is the peer surface's listeners: one per configured address, one
// connection cap across all of them, and the loopback fallback that keeps a
// node alive when none of them binds (ADR-005 §2).
//
// The rules, in the order they matter:
//
//   - It binds only what it was configured with. Retrying never adds an
//     address, and it does not watch the routing table: a node that picked up
//     whatever address appeared would be serving a network nobody chose.
//   - If any configured address binds there is no fallback. The fallback is
//     for a node that would otherwise have no peer listener at all.
//   - Only when every configured address fails does the existing fallback
//     order run: the loopback default, then loopback on any free port.
//   - An address that is configured and not bound — gone, busy, refused — is
//     tried again every PeerListenRetryInterval. When one binds, a fallback in
//     use is closed: it served nobody, and a node that is now reachable is not
//     degraded. An address that disappears after binding is left alone; a
//     bound socket keeps accepting nothing rather than failing.
type ListenerSet struct {
	options ListenerSetOptions
	limit   *ConnectionLimit

	mu       sync.Mutex
	entries  []*listenerEntry
	fallback *LimitedListener
	problem  *ListenProblem
	// retired are listeners this set closed on purpose, so the serving loop
	// can tell "closed because another address came up" from a failure.
	retired map[net.Listener]bool
	serve   func(net.Listener)
	closed  bool
}

type listenerEntry struct {
	address  string
	listener *LimitedListener
	state    string
	reason   string
	detail   string
}

// NewListenerSet prepares a set for the configured addresses. Nothing is bound
// until Bind.
func NewListenerSet(configured []string, options ListenerSetOptions) *ListenerSet {
	if options.Fallback == "" {
		options.Fallback = DefaultPeerListen
	}
	if options.Listen == nil {
		options.Listen = net.Listen
	}
	if options.Interfaces == nil {
		options.Interfaces = InterfaceAddresses
	}
	if options.Probe == nil {
		options.Probe = ProbeListen
	}
	if options.Logf == nil {
		options.Logf = func(string, ...any) {}
	}
	set := &ListenerSet{
		options: options,
		limit:   NewConnectionLimit(options.Limit),
		retired: map[net.Listener]bool{},
	}
	for _, address := range configured {
		set.entries = append(set.entries, &listenerEntry{address: address, state: ListenerPending})
	}
	return set
}

// Bind opens every configured address, and the fallback only if none of them
// opened.
//
// It fails only when the fallback's last resort fails too — loopback on any
// free port — and then something is wrong with this machine's networking that
// no button on a settings page can fix. Every address tried is named in that
// error, because the last failure alone would send the owner looking at a port
// nobody chose.
func (s *ListenerSet) Bind() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var firstErr error
	for _, entry := range s.entries {
		if err := s.bindEntry(entry); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.boundCount() > 0 {
		for _, entry := range s.entries {
			if entry.state == ListenerFailed {
				s.options.Logf("peer listener %s could not be bound (%s: %s); serving %s, and trying it "+
					"again every %s", entry.address, entry.reason, entry.detail,
					strings.Join(s.boundLocked(), ", "), PeerListenRetryInterval)
			}
		}
		return nil
	}
	if len(s.entries) == 0 {
		firstErr = errors.New("no peer listener address is configured")
	}
	return s.bindFallback(firstErr)
}

// bindFallback runs the fallback order, when nothing configured bound.
//
// The fallback is skipped when it is one of the addresses that just failed:
// trying it again would fail again, and a degradation naming its own failure
// as the remedy is a loop with a reassuring message on it. The last resort
// still applies, and has to — "the default loopback port is taken" is the
// ordinary way a second node on one machine cannot start, and that node's
// owner needs the settings page as much as anyone.
func (s *ListenerSet) bindFallback(configuredErr error) error {
	fallback := s.options.Fallback
	fallbackErr := configuredErr
	var raw net.Listener
	if !s.isConfigured(fallback) {
		raw, fallbackErr = s.options.Listen("tcp", fallback)
	}
	if fallbackErr != nil {
		lastResort, lastResortErr := s.options.Listen("tcp", anyLoopbackPort)
		if lastResortErr != nil {
			return fmt.Errorf("peer listener %s: %v; the loopback default %s: %v; "+
				"and loopback on any free port: %v", strings.Join(s.configuredLocked(), ", "),
				configuredErr, fallback, fallbackErr, lastResortErr)
		}
		raw = lastResort
	}
	s.fallback = s.limit.Wrap(raw)
	// The listener's own address rather than the string asked for: they
	// differ wherever the fallback names a port the machine chose, and an
	// owner told which address is being served has to be told the real one.
	runningOn := raw.Addr().String()
	address, reason, detail := "", ListenUnusable, ""
	if len(s.entries) > 0 {
		first := s.entries[0]
		address, reason, detail = first.address, first.reason, first.detail
	}
	message := ListenFailureReason(reason, address, runningOn)
	s.problem = &ListenProblem{
		Address: address, Reason: reason, Detail: detail, RunningOn: runningOn, Message: message,
	}
	s.options.Logf("%s", message)
	for _, entry := range s.entries[min(1, len(s.entries)):] {
		s.options.Logf("peer listener %s could not be bound either (%s: %s)", entry.address, entry.reason, entry.detail)
	}
	// The owner's own surface is the point of staying up, so it is named here
	// rather than left for them to discover.
	s.options.Logf("this node is running with its outward half degraded; the settings page and `ah settings set` "+
		"can change the address without restarting into the same failure, and the configured "+
		"addresses are tried again every %s", PeerListenRetryInterval)
	return nil
}

// anyLoopbackPort is the last resort: a listener that exists so the process
// does, on an address no peer was ever told about.
const anyLoopbackPort = "127.0.0.1:0"

// bindEntry tries one configured address and records what happened.
func (s *ListenerSet) bindEntry(entry *listenerEntry) error {
	raw, err := s.options.Listen("tcp", entry.address)
	if err == nil {
		entry.listener = s.limit.Wrap(raw)
		entry.state, entry.reason, entry.detail = ListenerBound, "", ""
		return nil
	}
	reason := ListenUnusable
	// An enumeration failure is not a reason: it would make every address read
	// as gone. The generic reason is the honest one when this process cannot
	// see its own interfaces.
	if interfaces, addressErr := s.options.Interfaces(); addressErr == nil {
		reason = ClassifyListenFailure(entry.address, interfaces, s.options.Probe)
	}
	entry.state, entry.reason, entry.detail = ListenerFailed, reason, err.Error()
	return err
}

// Serve hands every listener bound so far to serve, and every one a retry
// binds later. serve is called with the set's lock released and must not
// block: it is expected to start a goroutine.
func (s *ListenerSet) Serve(serve func(net.Listener)) {
	s.mu.Lock()
	s.serve = serve
	listeners := make([]net.Listener, 0, len(s.entries)+1)
	for _, entry := range s.entries {
		if entry.listener != nil {
			listeners = append(listeners, entry.listener)
		}
	}
	if s.fallback != nil {
		listeners = append(listeners, s.fallback)
	}
	s.mu.Unlock()
	for _, listener := range listeners {
		serve(listener)
	}
}

// Retry tries once more every configured address that is not bound, and
// nothing else.
func (s *ListenerSet) Retry() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	var opened []net.Listener
	for _, entry := range s.entries {
		if entry.state == ListenerBound {
			continue
		}
		before := entry.reason
		if err := s.bindEntry(entry); err != nil {
			// Said when the reason changes, not every thirty seconds: a node
			// waiting for a cable would otherwise fill its log with one line
			// repeated until someone plugs it in.
			if entry.reason != before {
				s.options.Logf("peer listener %s still cannot be bound (%s: %s)", entry.address, entry.reason, entry.detail)
			}
			continue
		}
		s.options.Logf("peer listener %s is bound now", entry.address)
		opened = append(opened, entry.listener)
	}
	var retire net.Listener
	if len(opened) > 0 && s.fallback != nil {
		// Nobody was told about the fallback, and a node that is reachable now
		// is not degraded: keeping it would leave the settings page saying
		// "only this machine" beside an address that works.
		retire = s.fallback
		s.retired[s.fallback] = true
		s.options.Logf("closing the loopback fallback %s: a configured peer address is bound",
			s.fallback.Addr().String())
		s.fallback, s.problem = nil, nil
	}
	serve := s.serve
	s.mu.Unlock()
	if retire != nil {
		_ = retire.Close()
	}
	if serve != nil {
		for _, listener := range opened {
			serve(listener)
		}
	}
}

// Run retries on every tick until ctx ends. The ticks are passed in so a test
// can deliver one without waiting thirty seconds.
func (s *ListenerSet) Run(ctx context.Context, ticks <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			s.Retry()
		}
	}
}

// Retired says whether listener was closed by this set on purpose. The serving
// loop asks, so that closing a fallback nobody needs does not end the node.
func (s *ListenerSet) Retired(listener net.Listener) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed || s.retired[listener]
}

// Close closes every listener in the set and stops retries.
func (s *ListenerSet) Close() error {
	s.mu.Lock()
	s.closed = true
	listeners := make([]net.Listener, 0, len(s.entries)+1)
	for _, entry := range s.entries {
		if entry.listener != nil {
			listeners = append(listeners, entry.listener)
		}
	}
	if s.fallback != nil {
		listeners = append(listeners, s.fallback)
	}
	s.mu.Unlock()
	var first error
	for _, listener := range listeners {
		if err := listener.Close(); err != nil && first == nil && !errors.Is(err, net.ErrClosed) {
			first = err
		}
	}
	return first
}

// Configured is the list this set was built for, in order.
func (s *ListenerSet) Configured() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.configuredLocked()
}

// Bound is the configured addresses being served right now, in configured
// order, as they were configured. The fallback is not among them: it is not an
// address anybody chose.
func (s *ListenerSet) Bound() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.boundLocked()
}

// Running is the single address an older reader is told this node serves: the
// first bound configured address, or the fallback, or the first configured
// address before anything was bound.
func (s *ListenerSet) Running() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if bound := s.boundLocked(); len(bound) > 0 {
		return bound[0]
	}
	if s.fallback != nil {
		return s.fallback.Addr().String()
	}
	if len(s.entries) > 0 {
		return s.entries[0].address
	}
	return s.options.Fallback
}

// Problem is the degradation in effect, or nil when a configured address is
// being served. A copy: the caller may keep it.
func (s *ListenerSet) Problem() *ListenProblem {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.problem == nil {
		return nil
	}
	problem := *s.problem
	return &problem
}

// States reports every configured address and what became of it.
func (s *ListenerSet) States() []ListenerState {
	s.mu.Lock()
	defer s.mu.Unlock()
	serving := strings.Join(s.boundLocked(), ", ")
	if serving == "" && s.fallback != nil {
		serving = s.fallback.Addr().String()
	}
	states := make([]ListenerState, 0, len(s.entries))
	for _, entry := range s.entries {
		state := ListenerState{Address: entry.address, State: entry.state}
		if entry.state == ListenerFailed {
			state.Reason, state.Detail = entry.reason, entry.detail
			state.Message = ListenFailureReason(entry.reason, entry.address, serving)
		}
		states = append(states, state)
	}
	return states
}

// Limit is the connection cap every listener in this set shares.
func (s *ListenerSet) Limit() *ConnectionLimit { return s.limit }

func (s *ListenerSet) boundLocked() []string {
	bound := make([]string, 0, len(s.entries))
	for _, entry := range s.entries {
		if entry.state == ListenerBound {
			bound = append(bound, entry.address)
		}
	}
	return bound
}

func (s *ListenerSet) boundCount() int { return len(s.boundLocked()) }

func (s *ListenerSet) configuredLocked() []string {
	configured := make([]string, 0, len(s.entries))
	for _, entry := range s.entries {
		configured = append(configured, entry.address)
	}
	return configured
}

func (s *ListenerSet) isConfigured(address string) bool {
	for _, entry := range s.entries {
		if entry.address == address {
			return true
		}
	}
	return false
}

// ProbeListen asks whether this machine will serve an address at all, by
// taking one and giving it straight back.
func ProbeListen(network, address string) error {
	listener, err := net.Listen(network, address)
	if err != nil {
		return err
	}
	return listener.Close()
}
