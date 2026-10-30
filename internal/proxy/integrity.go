package proxy

import (
	"bytes"
	"encoding/hex"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/repository"
)

// withheldTail is the part of an artifact the leader's own client does not get until
// the whole body has been verified and its entry has committed (R-REPO-041).
const withheldTail = 64 << 10

// writeTimeout bounds one write to the leader's own client: a stalled client must
// not hold the fetch, which others wait for, for longer.
const writeTimeout = time.Minute

// verdict is the outcome of comparing the announced digests with the computed ones.
type verdict uint8

const (
	verdictOK verdict = iota
	verdictMismatch
	verdictMissing
)

// binding reports whether the announced digests decide for a request (R-REPO-041):
// the policies verify-if-available and require compare them, ignore and pass-through
// do not, and a format that addresses by digest always does.
func binding(policy string, always bool) bool {
	if always {
		return true
	}
	return policy == repository.ChecksumVerifyIfAvailable || policy == repository.ChecksumRequire
}

// admitsDigests refuses a request before any byte is fetched when the policy
// require finds no announced SHA-256 or stronger digest (R-REPO-041).
func admitsDigests(rq Request) bool {
	return rq.Remote.Settings.Integrity.ChecksumPolicy != repository.ChecksumRequire || rq.Expected.Strong()
}

// verify compares the digests the upstream announced with those computed while the
// body was spooled.
func verify(rq Request, got blob.Digests) verdict {
	if !binding(rq.Remote.Settings.Integrity.ChecksumPolicy, rq.AlwaysVerify) || rq.Expected.Empty() {
		return verdictOK
	}
	want := rq.Expected
	if len(want.SHA256) > 0 && !bytes.Equal(want.SHA256, got.SHA256[:]) {
		return verdictMismatch
	}
	if len(want.SHA512) > 0 && !bytes.Equal(want.SHA512, got.SHA512[:]) {
		return verdictMismatch
	}
	if len(want.SHA1) > 0 && !bytes.Equal(want.SHA1, got.SHA1[:]) {
		return verdictMismatch
	}
	return verdictOK
}

// announcedJSON is the announced digests as the audit events name them.
func announcedJSON(d Digests) map[string]string {
	out := map[string]string{}
	if len(d.SHA256) > 0 {
		out["sha256"] = hex.EncodeToString(d.SHA256)
	}
	if len(d.SHA512) > 0 {
		out["sha512"] = hex.EncodeToString(d.SHA512)
	}
	if len(d.SHA1) > 0 {
		out["sha1"] = hex.EncodeToString(d.SHA1)
	}
	return out
}

// storedMatches reports whether the announced digests agree with a stored blob's, for
// the check that keeps an artifact immutable (R-REPO-041).
func storedMatches(want Digests, have blob.Digests) bool {
	return verify(Request{AlwaysVerify: true, Expected: want}, have) == verdictOK
}

// stream is the leader's own client during a fetch: the body goes to it while it is
// fetched, except for the last withheldTail bytes, which are released only by finish
// after the verification and the commit. A client that goes away, or stalls, stops
// receiving and never stops the fetch (R-REPO-026 "detached fetch").
//
// The status line goes out with the first byte that is not withheld. A body that
// ends inside the withheld tail therefore has not started a response, and the
// caller answers it from the stored entry, with Range, conditionals and the strong
// ETag, or with a clean error.
type stream struct {
	mu      sync.Mutex
	w       http.ResponseWriter
	ctl     *http.ResponseController
	header  http.Header
	hold    []byte
	begun   bool
	started bool
	dead    bool
	// done is set by finish: every byte went out.
	done bool
}

func newStream(w http.ResponseWriter) *stream {
	return &stream{w: w, ctl: http.NewResponseController(w)}
}

// begin fixes the headers of the response: the upstream's media type and, when it
// announced one, its length.
func (s *stream) begin(contentType string, length int64, cacheStatus string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.header = http.Header{}
	if contentType != "" {
		s.header.Set("Content-Type", contentType)
	}
	if length >= 0 {
		s.header.Set("Content-Length", strconv.FormatInt(length, 10))
	}
	s.header.Set("Cache-Status", cacheStatus)
	s.begun = true
}

// Write takes a chunk of the body. It never fails: a client that went away is
// dropped and the fetch goes on.
func (s *stream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead || !s.begun {
		return len(p), nil
	}
	s.hold = append(s.hold, p...)
	if n := len(s.hold) - withheldTail; n > 0 {
		if !s.flushLocked(s.hold[:n]) {
			s.dead = true
			s.hold = nil
			return len(p), nil
		}
		s.hold = append(s.hold[:0], s.hold[n:]...)
	}
	return len(p), nil
}

func (s *stream) flushLocked(b []byte) bool {
	if !s.started {
		h := s.w.Header()
		for k, v := range s.header {
			h[k] = v
		}
		s.w.WriteHeader(http.StatusOK)
		s.started = true
	}
	_ = s.ctl.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err := s.w.Write(b)
	return err == nil
}

// finish releases the withheld tail after the entry has committed. It reports
// whether the client received a complete response; false with started==false means
// no byte went out and the caller serves the entry itself.
func (s *stream) finish() (complete bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dead || !s.begun {
		return false
	}
	if !s.flushLocked(s.hold) {
		s.dead = true
		return false
	}
	s.hold = nil
	s.done = true
	return true
}

// detach ends the client's share before its handler returns: nothing is written to
// the response afterwards.
func (s *stream) detach() {
	s.mu.Lock()
	s.dead = true
	s.hold = nil
	s.mu.Unlock()
}

// state tells the handler what happened to its response: started (the status line
// is out), done (complete).
func (s *stream) state() (started, done bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started, s.done
}
