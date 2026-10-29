//go:build !linux

package httpserver

import "net"

// unsentBytes cannot read the send queue off Linux (the pods run on Linux): the
// shutdown then closes a connection at once, as net/http does.
func unsentBytes(*net.TCPConn) (int, bool) { return 0, false }

// resetOnClose is false off Linux: a cut closes as net/http does (observed on
// macOS loopback: the RST of a connection that was streaming never reached the
// peer, so the peer waited for its own timeout instead of hearing the end).
const resetOnClose = false
