package nodeconfig

import (
	"net"
	"sync"
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
	slots chan struct{}
	once  sync.Once
}

// LimitConnections wraps a listener with a concurrency cap.
func LimitConnections(inner net.Listener, limit int) *LimitedListener {
	if limit <= 0 {
		limit = MaxPeerConnections
	}
	return &LimitedListener{Listener: inner, slots: make(chan struct{}, limit)}
}

func (l *LimitedListener) Accept() (net.Conn, error) {
	for {
		connection, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.slots <- struct{}{}:
			return &limitedConn{Conn: connection, release: l.release}, nil
		default:
			// At capacity. Close and keep accepting: returning the error would
			// stop the server entirely, which is the outcome the attacker wants.
			_ = connection.Close()
		}
	}
}

func (l *LimitedListener) release() {
	select {
	case <-l.slots:
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
