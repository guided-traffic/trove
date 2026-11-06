package npmstub

import (
	"net/http"
	"strconv"
	"time"
)

// The log of the registry's requests with their times (R-REPO-054, R-REPO-059): the failure suite of the mirror (R-HA-052)
// asserts that the pass sends nothing to an upstream that asked it to wait, however the pass moved between processes in the
// meantime, and that needs the time at which each request arrived and what it was answered. The counters of counts.go have
// neither. The log holds the requests of the registry host (not the replication host's, which ReplicationRequest records, and
// not the control API's), oldest first, and is emptied by Reset.

// maxRequestLog bounds the log; a request beyond it is counted in RequestLog.Dropped and not recorded.
const maxRequestLog = 50000

// RequestRecord is one request of the registry host.
type RequestRecord struct {
	// At is the time the request arrived, before any delay of the mode.
	At time.Time `json:"at"`
	// Done is the time the answer was complete.
	Done   time.Time `json:"done"`
	Method string    `json:"method"`
	Path   string    `json:"path"`
	Kind   string    `json:"kind"`
	Name   string    `json:"name,omitempty"`
	// Status is the status the stub answered; 0 while the request is open or when the connection was closed without an answer.
	Status int `json:"status"`
	// RetryAfterSeconds is the Retry-After the answer carried, in seconds; 0 when it carried none.
	RetryAfterSeconds int `json:"retry_after_seconds,omitempty"`
}

// RequestLog is the answer of GET /-/stub/requests.
type RequestLog struct {
	Requests []RequestRecord `json:"requests"`
	// Dropped is the number of requests beyond the bound of the log.
	Dropped int `json:"dropped,omitempty"`
}

// Requests returns the log of the registry host's requests since the last reset.
func (s *Server) Requests() RequestLog {
	s.reqmu.Lock()
	defer s.reqmu.Unlock()
	return RequestLog{Requests: append(make([]RequestRecord, 0, len(s.reqlog)), s.reqlog...), Dropped: s.reqDropped}
}

func (s *Server) resetRequests() {
	s.reqmu.Lock()
	defer s.reqmu.Unlock()
	s.reqlog, s.reqDropped = nil, 0
	s.reqEpoch++
}

// logRequest records the arrival of a request and returns the writer that notes its answer and the function that completes
// the record.
func (s *Server) logRequest(w http.ResponseWriter, r *http.Request, rt route) (http.ResponseWriter, func()) {
	s.reqmu.Lock()
	if len(s.reqlog) >= maxRequestLog {
		s.reqDropped++
		s.reqmu.Unlock()
		return w, func() {}
	}
	idx, epoch := len(s.reqlog), s.reqEpoch
	s.reqlog = append(s.reqlog, RequestRecord{At: time.Now(), Method: r.Method, Path: r.URL.EscapedPath(), Kind: rt.kind, Name: rt.name})
	s.reqmu.Unlock()
	sw := &statusWriter{ResponseWriter: w}
	return sw, func() {
		s.reqmu.Lock()
		defer s.reqmu.Unlock()
		if s.reqEpoch != epoch || idx >= len(s.reqlog) {
			return
		}
		rec := &s.reqlog[idx]
		rec.Done, rec.Status, rec.RetryAfterSeconds = time.Now(), sw.status, sw.retryAfter
	}
}

// statusWriter notes the status and Retry-After of the answer.
type statusWriter struct {
	http.ResponseWriter
	status     int
	retryAfter int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
		if v := w.Header().Get("Retry-After"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				w.retryAfter = n
			}
		}
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
