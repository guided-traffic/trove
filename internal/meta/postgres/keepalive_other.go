//go:build !linux

package postgres

import (
	"net"
	"time"
)

// setUserTimeout is Linux-only (TCP_USER_TIMEOUT); the keepalive settings still
// apply elsewhere.
func setUserTimeout(*net.TCPConn, time.Duration) {}
