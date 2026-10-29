package observability

import (
	"io"
	"net/http"
	"sync/atomic"
)

// recorder wraps a ResponseWriter to count the bytes written and remember the
// status. It forwards Flush and exposes the wrapped writer for
// http.ResponseController (Hijack, SetWriteDeadline, ...).
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 && (code >= 200 || code == http.StatusSwitchingProtocols) {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += int64(n)
	return n, err
}

// Flush implements http.Flusher.
func (r *recorder) Flush() {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	http.NewResponseController(r.ResponseWriter).Flush() //nolint:errcheck // not flushable is fine
}

// Unwrap returns the wrapped writer for http.ResponseController.
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// statusOrOK returns the status written, or 200 if the handler wrote nothing.
func (r *recorder) statusOrOK() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

// recorderFor returns w as a recorder, wrapping it unless an outer middleware
// did already.
func recorderFor(w http.ResponseWriter) *recorder {
	if rec, ok := w.(*recorder); ok {
		return rec
	}
	return &recorder{ResponseWriter: w}
}

// countingBody counts the bytes the handler reads from the request body.
type countingBody struct {
	io.ReadCloser
	n atomic.Int64
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.ReadCloser.Read(p)
	c.n.Add(int64(n))
	return n, err
}
