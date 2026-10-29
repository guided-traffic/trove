//go:build linux

package postgres

import (
	"net"
	"syscall"
	"time"
)

// setUserTimeout sets TCP_USER_TIMEOUT so a connection with unacknowledged data
// fails after d instead of the operating-system default of about 15 minutes
// (R-HA-015, database.keepalive.user_timeout).
func setUserTimeout(c *net.TCPConn, d time.Duration) {
	if d <= 0 {
		return
	}
	raw, err := c.SyscallConn()
	if err != nil {
		return
	}
	_ = raw.Control(func(fd uintptr) {
		_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, 0x12, int(d.Milliseconds())) // TCP_USER_TIMEOUT
	})
}
