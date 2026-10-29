package httpserver

import (
	"net"
	"sync"
	"time"
)

// Why a connection is not closed at once during the shutdown (Q-ARCH-187).
//
// When a handler returns, its last bytes are in the kernel's send queue, not at the
// peer: a slow reader (an edge proxy relaying to a client at a few MB/s) has not
// taken them. net/http closes the connection at that point, and the process exits
// a moment later. The pod's network namespace goes with its containers, and the
// sockets that close() left behind in the background, with their unsent bytes,
// die with it without a FIN or an RST. The peer has a response that stops a few
// MiB short and waits for the rest until its own timeout.
//
// So during the shutdown a connection that closes with bytes still unacknowledged
// is held open (lingering) until the peer has acknowledged them. The same
// mechanism in reverse serves the connections that are cut at the deadline: they
// are closed with SO_LINGER 0, so the peer receives an RST at once and learns of
// the end now, not at its own timeout.

// phase is where the listener is in its shutdown.
type phase int32

const (
	// The zero value is normal operation: a closing connection closes at once.
	_ phase = iota
	// phaseDraining holds a connection that closes with unacknowledged bytes.
	phaseDraining
	// phaseCutting resets every connection that closes: the deadline has passed.
	phaseCutting
)

// lingerPoll is how often the lingering connections are looked at.
const lingerPoll = 10 * time.Millisecond

// lingerListener wraps the accepted TCP connections in lingerConn.
type lingerListener struct {
	net.Listener
	s *Server
}

func (l *lingerListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if tc, ok := c.(*net.TCPConn); ok {
		return &lingerConn{Conn: tc, tcp: tc, s: l.s}, nil
	}
	return c, nil
}

// lingerConn is an accepted TCP connection whose Close is decided by the phase of
// the server.
type lingerConn struct {
	net.Conn
	tcp  *net.TCPConn
	s    *Server
	once sync.Once
	err  error
}

// Close closes the connection now, resets it, or hands it to the lingering set,
// depending on the phase.
func (c *lingerConn) Close() error {
	c.once.Do(func() { c.err = c.s.closeConn(c) })
	return c.err
}

// closeConn is Close of a lingerConn.
func (s *Server) closeConn(c *lingerConn) error {
	switch phase(s.phase.Load()) {
	case phaseCutting:
		reset(c.tcp)
	case phaseDraining:
		if n, ok := unsentBytes(c.tcp); ok && n > 0 {
			s.mu.Lock()
			// The phase can have moved on between the check and the lock; a cut
			// that already ran would never look at this connection.
			if phase(s.phase.Load()) == phaseDraining {
				s.lingering[c] = struct{}{}
				if s.heldConns == 0 {
					s.heldSince = time.Now()
				}
				s.heldConns++
				s.heldBytes += n
				s.mu.Unlock()
				return nil
			}
			s.mu.Unlock()
			reset(c.tcp)
		}
	}
	return c.Conn.Close()
}

// flushLingering closes the lingering connections as their bytes are
// acknowledged. At by it resets the rest and returns how many it reset: their
// peer did not take the bytes in time (a stalled reader), and waiting further
// would spend the grace period of the pod.
func (s *Server) flushLingering(by time.Time) int {
	for {
		s.mu.Lock()
		for c := range s.lingering {
			if n, ok := unsentBytes(c.tcp); !ok || n == 0 {
				delete(s.lingering, c)
				_ = c.Conn.Close()
			}
		}
		left := len(s.lingering)
		s.mu.Unlock()
		if left == 0 {
			return 0
		}
		if !time.Now().Before(by) {
			break
		}
		time.Sleep(lingerPoll)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.lingering)
	for c := range s.lingering {
		delete(s.lingering, c)
		reset(c.tcp)
		_ = c.Conn.Close()
	}
	return n
}

// reset makes the next Close of c send an RST instead of a FIN.
func reset(c *net.TCPConn) {
	if resetOnClose {
		_ = c.SetLinger(0)
	}
}
