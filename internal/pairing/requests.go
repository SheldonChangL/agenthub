package pairing

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

// RequestTTL bounds one pairing request.
//
// A request is a person walking to the other machine and reading six groups of
// hex off it. Five minutes is the same budget as DefaultWindow, and a request
// that outlived its window would be an approval the owner could grant after
// they had told this node to stop pairing.
const RequestTTL = 5 * time.Minute

// MaxPending bounds how many requests may be waiting for a decision, in each
// direction.
//
// Every incoming one arrives from an unauthenticated caller, so the list is
// something a stranger can fill. Bounded, because the failure that matters is
// the owner's list of fingerprints to compare becoming a page of noise the real
// machine is buried in — not memory.
const MaxPending = 16

// MaxPendingPerSource bounds how many incoming requests one address may have
// waiting.
//
// MaxPending alone is not a bound on an attacker: node identifiers are chosen
// by whoever sends the request, so sixteen fresh ones from one machine filled
// the list and the owner's real machine was answered PAIRING_BUSY during the
// very window they had opened to pair it. The source address is the one thing
// in an incoming request the sender cannot invent freely, so it is what the
// bound is keyed on. Three, because a person retrying a request they think was
// lost is normal and a fourth simultaneous one from the same machine is not.
const MaxPendingPerSource = 3

// retainDecided is how long a decided or expired request stays visible.
//
// Kept rather than deleted, because "rejected" and "expired" are different
// answers and a row that vanishes tells the owner neither. Long enough that
// both sides can poll and see what became of it.
const retainDecided = 10 * time.Minute

// MaxRetained bounds every row this node holds at once, decided ones included.
//
// MaxPending and MaxPendingPerSource count only rows still waiting, so they
// were never a bound on the table. Displacement is where it leaked: once the
// pending list is full, a source that still holds a row there gives up its own
// oldest to make room for its next one, which leaves the source's pending count
// exactly where it was and a decided row behind, kept for retainDecided with
// nothing counting it. The loop has no end, and up to MaxPending source
// addresses can be in it at once, each sending at the 120/min the peer limiter
// allows — so the table grew for ten minutes at a time with no ceiling.
//
// Four times the pending bound: enough that the ordinary history of a busy
// window survives to be polled, small enough that the worst case is a few dozen
// rows rather than a few hundred thousand.
const MaxRetained = 4 * MaxPending

// MaxRetainedPerSource bounds the decided rows kept for any one source address.
//
// The total bound alone would let one flooder's history push out everybody
// else's, which is the eviction MaxPendingPerSource exists to deny — so the
// per-source bound is applied first and a flooder can only crowd itself out.
// Twice MaxPendingPerSource: a sender may have three rows waiting, and their
// answers plus one previous round is the history worth keeping.
const MaxRetainedPerSource = 2 * MaxPendingPerSource

// Direction says which side of the exchange a request is.
type Direction string

const (
	// Incoming is a request another machine sent here, waiting on this owner.
	Incoming Direction = "incoming"
	// Outgoing is a request this machine sent, waiting on the other owner and
	// then on this one.
	Outgoing Direction = "outgoing"
)

// RequestState is where a request has got to.
//
// The states are deliberately different per direction. An incoming request ends
// at the receiving owner's decision; an outgoing one is not finished when the
// far side approves, because this owner has still not compared anything —
// StateAwaitingConfirm is that gap, and it is the whole reason an approval
// cannot pair a machine by itself.
type RequestState string

const (
	StatePending         RequestState = "pending"
	StateAwaitingConfirm RequestState = "awaiting-confirm"
	StateApproved        RequestState = "approved"
	StateRejected        RequestState = "rejected"
	StateExpired         RequestState = "expired"
)

// Reasons carried by pair.reject, and by a request this node gave up on. The
// values are the schema's enum: docs/broker-protocol.schema.json, $defs.pairReject.
const (
	ReasonDeclined            = "declined"
	ReasonExpired             = "expired"
	ReasonFingerprintMismatch = "fingerprint_mismatch"
	// ReasonDisplaced is a pending incoming request pushed out of a full list
	// by a newer one. Distinct from "expired" because the owner did not run out
	// of time: something filled the list.
	ReasonDisplaced = "displaced"
)

var (
	// ErrTooManyRequests reports that the pending list is full.
	ErrTooManyRequests = fmt.Errorf("no more than %d pairing requests may be pending at once", MaxPending)
	// ErrTooManyFromSource reports that one address already has as many
	// requests waiting here as it may have.
	ErrTooManyFromSource = fmt.Errorf(
		"no more than %d pairing requests from one address may be pending at once", MaxPendingPerSource)
	// ErrNoSuchRequest reports an id that names nothing here.
	ErrNoSuchRequest = errors.New("no such pairing request")
	// ErrWrongState reports a request that cannot take this decision now.
	ErrWrongState = errors.New("pairing request is not waiting for that")
	// ErrDuplicateRequest reports a second request from a node that already has
	// one pending. One machine, one row to compare.
	ErrDuplicateRequest = errors.New("that node already has a pairing request pending here")
)

// Request is one pairing exchange in progress, as both the owner and the wire
// need it.
//
// Fingerprint is derived here from PublicKey and is never copied from anything
// that arrived over the network. A fingerprint a peer sent is a claim about a
// key, and displaying it would let a substituted key carry the fingerprint of
// the key it replaced — which is the one thing the human comparison exists to
// catch.
type Request struct {
	ID        string    `json:"id"`
	Direction Direction `json:"direction"`
	// NodeID and the fields under it are the other machine's descriptor.
	NodeID      string `json:"nodeId"`
	DisplayName string `json:"displayName"`
	Platform    string `json:"platform"`
	PublicKey   string `json:"publicKey"`
	// Fingerprint is the other machine's, derived locally from PublicKey. This
	// is the string the owner compares.
	Fingerprint string `json:"fingerprint"`
	// LocalFingerprint is this machine's own. Shown beside the other one so
	// both screens display the same pair of fingerprints and the owner can tell
	// which is which.
	LocalFingerprint string `json:"localFingerprint"`
	// Address is where the other machine answers, as host:port: the address
	// this node dialled for an outgoing request, and the one the requester
	// claimed for an incoming one. Empty when there is none to record.
	Address string `json:"address,omitempty"`
	// Alternates are the other addresses the other machine answers on, each
	// already through this node's delivery policy (ADR-005 §4): what an
	// incoming request listed beside its address, or what the approval of an
	// outgoing one listed. Recorded with Address when the pairing is trusted.
	Alternates []string `json:"alternateAddresses,omitempty"`
	// SourceHost is the host half of the address an incoming request actually
	// arrived from, as the listener saw it. Unlike NodeID it is not the
	// sender's to choose, which is why the flood bound is keyed on it. Empty on
	// an outgoing request: this machine is the source.
	SourceHost string       `json:"sourceHost,omitempty"`
	State      RequestState `json:"state"`
	Reason     string       `json:"reason,omitempty"`
	// TrustedByRequest records that this exact request is what wrote the other
	// machine into this node's trust store.
	//
	// It is the guard on undoing that write. A node paired months ago by some
	// other route must not be revoked because a pairing request naming it was
	// refused, and without this flag a refusal would have no way to tell the
	// two apart.
	TrustedByRequest bool `json:"trustedByRequest,omitempty"`
	// TrustLeftInPlace says, in the words the owner is shown, why a refusal
	// that should have taken that trust back did not. Empty when there was
	// nothing to take back or it was taken back.
	//
	// Two situations reach it and they are not the same fact: the key stored
	// under that node id was not the key this request carried — the trust came
	// from somewhere else, and this refusal is not the thing that may remove
	// it — or the store refused the revoke. A sentence that named only the
	// first would misdescribe the second, so the clause travels rather than
	// being reconstructed from a flag.
	//
	// Recorded rather than recomputed, because what the owner is told has to be
	// what the node did, and only the handler that ran the revoke knows that.
	TrustLeftInPlace string    `json:"trustLeftInPlace,omitempty"`
	CreatedAt        time.Time `json:"createdAt"`
	ExpiresAt        time.Time `json:"expiresAt"`
}

// Decided reports whether this request is finished, one way or another.
func (r Request) Decided() bool {
	return r.State == StateApproved || r.State == StateRejected || r.State == StateExpired
}

// Requests is the set of pairing exchanges this node is part of.
//
// In memory only, like the candidate list: a request is a person standing at
// two machines, and one that survived a restart would be an approval waiting
// for somebody who has gone home. Safe for concurrent use.
type Requests struct {
	now func() time.Time

	mu    sync.Mutex
	rows  map[string]*Request
	order []string
}

func NewRequests() *Requests { return NewRequestsWithClock(time.Now) }

// NewRequestsWithClock is NewRequests against a clock the caller supplies, so a
// test can put the expiry boundary where it wants it rather than sleeping to it.
func NewRequestsWithClock(now func() time.Time) *Requests {
	return &Requests{now: now, rows: make(map[string]*Request)}
}

// Now is the clock these requests are measured against, so a caller presenting
// a countdown reads the same clock the expiry was computed from.
func (q *Requests) Now() time.Time { return q.now() }

// Add records a new request. The caller has already derived the fingerprints
// and decided the expiry.
//
// A full incoming list makes room for the newcomer by displacing the oldest
// request still waiting from the newcomer's own source address, and refuses
// with ErrTooManyRequests when that address has no row to give up. Displacing
// the oldest row overall was worse than refusing: a flooder rotating source
// hosts needs only MaxPending/MaxPendingPerSource addresses to fill the list,
// and every further request would then evict somebody else's row — the owner's
// real machine among them, shown to them as displaced. Keyed on SourceHost, a
// sender can only ever push out its own earlier attempt. The residual is that
// an attacker with unlimited source addresses can still fill the list and make
// this node answer PAIRING_BUSY; it can never evict a stranger's row.
func (q *Requests) Add(request Request) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sweep()

	pending := 0
	fromSource := 0
	oldestFromSource := ""
	for _, id := range q.order {
		row := q.rows[id]
		if row.Direction != request.Direction || row.Decided() {
			continue
		}
		pending++
		if row.NodeID == request.NodeID {
			return ErrDuplicateRequest
		}
		if request.SourceHost == "" || row.SourceHost != request.SourceHost {
			continue
		}
		fromSource++
		if oldestFromSource == "" || q.rows[oldestFromSource].CreatedAt.After(row.CreatedAt) {
			oldestFromSource = id
		}
	}
	if fromSource >= MaxPendingPerSource {
		return ErrTooManyFromSource
	}
	if pending >= MaxPending {
		// An outgoing request is this owner's own deliberate act and there is
		// no attacker to absorb: sixteen of them means the owner asked for
		// sixteen, and silently cancelling one of those would be this node
		// deciding which of their pairings to abandon. An incoming one may only
		// displace a row from its own address, so a full list is refused unless
		// this sender already has one waiting here.
		if request.Direction != Incoming || oldestFromSource == "" {
			return ErrTooManyRequests
		}
		displaced := q.rows[oldestFromSource]
		displaced.State = StateExpired
		displaced.Reason = ReasonDisplaced
	}
	if _, clash := q.rows[request.ID]; clash {
		return fmt.Errorf("pairing request %q already exists", request.ID)
	}
	stored := request
	q.rows[request.ID] = &stored
	q.order = append(q.order, request.ID)
	// Again after the append, so the table is inside its bound on the way out
	// of every Add rather than at the next read. Only decided rows go, so the
	// newcomer is never what pays for itself.
	q.capRetained()
	return nil
}

// Get returns one request, having first let the clock expire it.
func (q *Requests) Get(id string) (Request, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sweep()
	row, ok := q.rows[id]
	if !ok {
		return Request{}, false
	}
	return *row, true
}

// List returns every request this node knows about, newest first.
func (q *Requests) List() []Request {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sweep()
	rows := make([]Request, 0, len(q.order))
	for _, id := range q.order {
		rows = append(rows, *q.rows[id])
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].CreatedAt.After(rows[j].CreatedAt) })
	return rows
}

// Settle moves a request to a new state, refusing a transition that is not the
// one this request is waiting for.
//
// One method rather than an Approve, a Reject and a Confirm, because the guard
// is the same in all three and what differs is only which state was expected.
// A transition that has already happened is refused rather than repeated: an
// owner who approves twice must not write the trust store twice.
func (q *Requests) Settle(id string, from, to RequestState, reason string) (Request, error) {
	row, _, err := q.SettleFrom(id, []RequestState{from}, to, reason, nil)
	return row, err
}

// SettleFrom is Settle from any one of several expected states: it reports
// which one the request was actually in, and applies apply to the row — all
// under one hold of the lock.
//
// One call rather than a Get, a Settle and an Update, because the two callers
// that need it are the two that must not interleave with each other.
//
// The approval has to become "approved, and this request is what wrote the
// trust" in a single step. Written as two, a refusal landing between them read
// a row that said approved by nobody, skipped the revoke on that ground, and
// left in the trust store exactly the key its owner had just refused.
//
// The refusal has to learn what the row was in the same breath as changing it.
// "Was it approved?" answered before the transition is a question about a row
// the node has already left, and the answer decides whether a trust row is
// revoked — the one decision here that destroys something.
func (q *Requests) SettleFrom(
	id string, from []RequestState, to RequestState, reason string, apply func(*Request),
) (Request, RequestState, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sweep()
	row, ok := q.rows[id]
	if !ok {
		return Request{}, "", ErrNoSuchRequest
	}
	previous := row.State
	if !slices.Contains(from, previous) {
		wanted := make([]string, 0, len(from))
		for _, state := range from {
			wanted = append(wanted, string(state))
		}
		return Request{}, previous, fmt.Errorf("%w: it is %s, not %s",
			ErrWrongState, previous, strings.Join(wanted, " or "))
	}
	row.State = to
	row.Reason = reason
	if apply != nil {
		apply(row)
	}
	return *row, previous, nil
}

// Update replaces the peer details of a pending outgoing request.
//
// The approval carries the far side's descriptor a second time, signed. It must
// agree with what the TLS handshake and the first reply said, and the caller
// checks that; this records the result so what the owner confirms is what was
// signed.
func (q *Requests) Update(id string, apply func(*Request)) (Request, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sweep()
	row, ok := q.rows[id]
	if !ok {
		return Request{}, ErrNoSuchRequest
	}
	apply(row)
	return *row, nil
}

// ExpirePending expires every request in one direction that is still waiting.
//
// This is what closing the pairing window does to the requests it collected. A
// window the owner shut is a decision to stop pairing, and a request left
// pending behind it would be an approval they could still grant after saying
// no to the whole thing.
func (q *Requests) ExpirePending(direction Direction) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, id := range q.order {
		row := q.rows[id]
		if row.Direction == direction && !row.Decided() {
			row.State = StateExpired
			row.Reason = ReasonExpired
		}
	}
	q.sweep()
}

// sweep expires what the clock has expired and forgets what has been decided
// long enough ago. Called under the lock by every reader, so nothing has to
// remember to run it.
func (q *Requests) sweep() {
	now := q.now()
	kept := q.order[:0]
	for _, id := range q.order {
		row := q.rows[id]
		if !row.Decided() && !now.Before(row.ExpiresAt) {
			row.State = StateExpired
			row.Reason = ReasonExpired
		}
		if row.Decided() && now.After(row.ExpiresAt.Add(retainDecided)) {
			delete(q.rows, id)
			continue
		}
		kept = append(kept, id)
	}
	q.order = kept
	q.capRetained()
}

// capRetained drops decided rows until the table is inside MaxRetainedPerSource
// and MaxRetained.
//
// Never an undecided row. A bound that could evict something still waiting for
// the owner would hand back exactly the eviction MaxPendingPerSource was added
// to deny: the owner's real machine dropped off the list of fingerprints to
// compare while a stranger filled it. Undecided rows are already bounded by
// MaxPending in each direction, which is below MaxRetained, so dropping decided
// rows can always reach the total bound.
//
// Oldest first, in insertion order — which is CreatedAt order, since a row is
// appended when it is made. What is lost is the ability to poll for the answer
// to a request that has since been crowded out; the alternative was holding
// every answer a flooder can generate.
func (q *Requests) capRetained() {
	drop := make(map[string]bool)

	// Per source first, newest kept: a flooder is one address, and its own
	// history is what must go before anybody else's.
	perSource := make(map[string]int)
	for i := len(q.order) - 1; i >= 0; i-- {
		id := q.order[i]
		row := q.rows[id]
		if !row.Decided() || row.SourceHost == "" {
			continue
		}
		perSource[row.SourceHost]++
		if perSource[row.SourceHost] > MaxRetainedPerSource {
			drop[id] = true
		}
	}

	remaining := len(q.order) - len(drop)
	for _, id := range q.order {
		if remaining <= MaxRetained {
			break
		}
		if drop[id] || !q.rows[id].Decided() {
			continue
		}
		drop[id] = true
		remaining--
	}

	if len(drop) == 0 {
		return
	}
	kept := q.order[:0]
	for _, id := range q.order {
		if drop[id] {
			delete(q.rows, id)
			continue
		}
		kept = append(kept, id)
	}
	q.order = kept
}
