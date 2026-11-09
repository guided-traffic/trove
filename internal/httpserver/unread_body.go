package httpserver

import (
	"io"
	"net/http"
	"strings"
	"time"
)

// Why a response to a request with an unread body waits for the body (Q-ARCH-446).
//
// A handler that refuses a request before it reads the body (the rate limit, a
// failed authentication, a size check) returns with the body still on its way.
// net/http throws away at most 256 KiB of it and then closes the connection. A
// close with unread bytes in the receive buffer makes the kernel send an RST
// instead of a FIN. The RST can overtake the answer: the proxy in front, which is
// still writing the body to us, sees its connection reset before it has parsed
// the status line, and the client gets a 502 (HAProxy flag SH) instead of the 429
// or 401 the handler wrote. The answer is sent first; then the rest of the body
// is read and discarded, up to a bound and a time, so the connection ends with a
// clean FIN.

const (
	// unreadBodyBound is how much of an unread body is read and discarded. A body
	// larger than that is the client's problem: reading a multi-gigabyte upload
	// to be polite to its sender would cost more than the connection is worth.
	unreadBodyBound = 8 << 20
	// unreadBodyTime bounds the whole discard, however slowly the body arrives.
	unreadBodyTime = 5 * time.Second
)

// settleUnreadBody runs after the handler returned while the request body was not
// read to its end. It sends the answer now and then discards the rest of the body
// within the bound. It does nothing over HTTP/2 (a stream ends with its own frame,
// not with a connection reset) and for a request that waits for "100 Continue":
// that client has not sent the body, and reading it here would invite it.
func settleUnreadBody(w http.ResponseWriter, r *http.Request, raw io.Reader) {
	if r.ProtoMajor != 1 || r.Context().Err() != nil ||
		strings.EqualFold(strings.TrimSpace(r.Header.Get("Expect")), "100-continue") {
		return
	}
	rc := http.NewResponseController(w)
	if rc.Flush() != nil {
		return
	}
	_ = rc.SetReadDeadline(time.Now().Add(unreadBodyTime))
	_, _ = io.CopyN(io.Discard, raw, unreadBodyBound)
}
