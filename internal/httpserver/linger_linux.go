//go:build linux

package httpserver

import (
	"net"

	"golang.org/x/sys/unix"
)

// unsentBytes returns the bytes of the connection's send queue that the peer has not
// acknowledged (SIOCOUTQ: unsent plus unacknowledged). ok is false when the queue
// cannot be read; the caller then treats the connection as drained.
func unsentBytes(c *net.TCPConn) (n int, ok bool) {
	rc, err := c.SyscallConn()
	if err != nil {
		return 0, false
	}
	var ioErr error
	if err := rc.Control(func(fd uintptr) {
		n, ioErr = unix.IoctlGetInt(int(fd), unix.TIOCOUTQ)
	}); err != nil || ioErr != nil {
		return 0, false
	}
	return n, true
}

// resetOnClose: the pods are Linux, where SO_LINGER 0 sends an RST at once.
const resetOnClose = true
