package nodeconfig

import (
	"log"
	"net"
	"sync"
	"sync/atomic"
)

// MaxPeerConnections bounds how many connections the peer surface holds at once.
//
// A working peer needs one at a time: it publishes every 15 seconds, opens a
// connection for the challenge and one for the heartbeat, and closes both. A
// hundred is far beyond any real fleet and far below what an attacker needs to
// exhaust the process.
const MaxPeerConnections = 100

// LimitedListener caps concurrent connections.
//
// The request rate limiter cannot do this job. It runs inside the HTTP handler,
// which is reached only after the TLS handshake has completed — so the CPU that
// handshake costs, and the goroutine and file descriptor the connection holds,
// are all spent before anything is counted. An attacker who opens connections
// and never sends a request is invisible to a request limiter and can still
// exhaust the process.
//
// A refused connection is closed immediately rather than queued: a queue is the
// same exhaustion with an extra step.
type LimitedListener struct {
	net.Listener
	limit *ConnectionLimit
	once  sync.Once
}

// ConnectionLimit is one cap shared by every listener it wraps.
//
// A node serving a cable and Wi-Fi has two listeners and one process, and it is
// the process that an attacker exhausts. A cap per listener would let a second
// address double what the node holds open, so there is one count, and every
// listener takes its slots from it (ADR-005 §2).
type ConnectionLimit struct {
	slots   chan struct{}
	refused atomic.Int64
}

// NewConnectionLimit makes a cap of limit concurrent connections, or
// MaxPeerConnections when limit is not positive.
func NewConnectionLimit(limit int) *ConnectionLimit {
	if limit <= 0 {
		limit = MaxPeerConnections
	}
	return &ConnectionLimit{slots: make(chan struct{}, limit)}
}

// Wrap puts one listener under this cap.
func (c *ConnectionLimit) Wrap(inner net.Listener) *LimitedListener {
	return &LimitedListener{Listener: inner, limit: c}
}

// Refused reports how many connections this cap has turned away, across every
// listener it wraps.
func (c *ConnectionLimit) Refused() int64 { return c.refused.Load() }

// Refused reports how many connections have been turned away at capacity.
// Exported so a test, or an operator-facing command, can see it without
// scraping logs. The count is the shared cap's, not this listener's alone.
func (l *LimitedListener) Refused() int64 { return l.limit.Refused() }

// LimitConnections wraps a listener with a concurrency cap of its own.
func LimitConnections(inner net.Listener, limit int) *LimitedListener {
	return NewConnectionLimit(limit).Wrap(inner)
}

func (l *LimitedListener) Accept() (net.Conn, error) {
	for {
		connection, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.limit.slots <- struct{}{}:
			return &limitedConn{Conn: connection, release: l.limit.release}, nil
		default:
			// At capacity. Close and keep accepting: returning the error would
			// stop the server entirely, which is the outcome the attacker wants.
			_ = connection.Close()
			refused := l.limit.refused.Add(1)
			// Said out loud, but not once per refusal: a flood would then write
			// a log line per connection, which is its own denial of service.
			// Without any signal at all an attack is indistinguishable from the
			// network being down, and the operator has no way to tell.
			if refused == 1 || refused%100 == 0 {
				log.Printf("peer listener at capacity (%d connections); refused %d so far, most recently from %v",
					cap(l.limit.slots), refused, connection.RemoteAddr())
			}
		}
	}
}

func (c *ConnectionLimit) release() {
	select {
	case <-c.slots:
	default:
	}
}

// Close stops the listener. The slot channel is left alone: a connection still
// closing afterwards releases into it harmlessly.
func (l *LimitedListener) Close() error {
	var err error
	l.once.Do(func() { err = l.Listener.Close() })
	return err
}

// limitedConn returns its slot exactly once, however many times it is closed.
type limitedConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
