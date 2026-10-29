package httpserver

import (
	"bufio"
	"errors"
	"net"
	"net/http"
)

// writer wraps a ResponseWriter to remember whether the response has started,
// which status it carries and how many body bytes were written. It forwards
// Flush and exposes the wrapped writer through Unwrap, so http.ResponseController
// (the stall deadlines of R-ARCH-005) and Flusher assertions keep working.
type writer struct {
	http.ResponseWriter
	status int
	bytes  int64
	wrote  bool
}

func (w *writer) WriteHeader(code int) {
	if w.wrote {
		return
	}
	// 1xx informational responses (100 Continue, 103) do not start the response.
	if code >= 100 && code < 200 && code != http.StatusSwitchingProtocols {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	w.wrote, w.status = true, code
	w.ResponseWriter.WriteHeader(code)
}

func (w *writer) Write(p []byte) (int, error) {
	if !w.wrote {
		w.wrote, w.status = true, http.StatusOK
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += int64(n)
	return n, err
}

// Flush implements http.Flusher.
func (w *writer) Flush() {
	if !w.wrote {
		w.wrote, w.status = true, http.StatusOK
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}

// Unwrap returns the wrapped writer for http.ResponseController.
func (w *writer) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Hijack implements http.Hijacker for the rare handler that needs it.
func (w *writer) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, rw, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, errors.Join(errors.New("httpserver: hijack"), err)
	}
	w.wrote = true
	return c, rw, nil
}
