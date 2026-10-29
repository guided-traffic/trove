package observability

import (
	"encoding/json"
	"strconv"
	"sync"
)

// MaxRingRecord is the largest record the ring keeps as written. A longer one
// is replaced by a stub (see Ring.Write), so one huge record cannot take the
// byte budget of thousands of ordinary ones.
const MaxRingRecord = 16 << 10

// ringHead is how much of an oversized record its stub keeps. JSON escaping
// can grow text sixfold (\u00XX), so a head of an eighth of the record limit
// keeps the stub below it.
const ringHead = MaxRingRecord / 8

// Ring keeps the last records of the log stream in memory, after redaction
// (R-OPS-009). Support bundles read their logs from it. It dies with the pod.
//
// It is bounded twice: by the number of records (log.ring_buffer_records) and
// by the bytes they hold (log.ring_buffer_bytes). The oldest records go first
// when either bound is reached.
type Ring struct {
	mu        sync.Mutex
	recs      [][]byte // circular; len is the record bound
	head      int      // index of the oldest record
	n         int      // records held
	size      int64    // bytes held
	maxBytes  int64
	maxRecord int
}

// NewRing returns a ring that holds at most records records and maxBytes bytes.
// Either bound at 0 keeps nothing.
func NewRing(records int, maxBytes int64) *Ring {
	if records < 0 || maxBytes <= 0 {
		records = 0
	}
	maxRecord := int64(MaxRingRecord)
	if maxBytes < maxRecord {
		maxRecord = maxBytes
	}
	return &Ring{recs: make([][]byte, records), maxBytes: maxBytes, maxRecord: int(maxRecord)}
}

// Write stores one record (one line, as written by the slog handler). It
// implements io.Writer so the ring can sit next to the output in a MultiWriter.
// A record longer than the per-record limit is stored as the JSON object
// {"ring_truncated":true,"original_bytes":N,"head":"<first bytes>"}: the
// support bundle shows that something was cut and where it began, and the
// line stays valid JSON.
func (r *Ring) Write(p []byte) (int, error) {
	if len(r.recs) == 0 {
		return len(p), nil
	}
	rec := r.fit(p)
	if rec == nil {
		return len(p), nil
	}
	r.mu.Lock()
	for r.n > 0 && (r.n == len(r.recs) || r.size+int64(len(rec)) > r.maxBytes) {
		r.size -= int64(len(r.recs[r.head]))
		r.recs[r.head] = nil
		r.head = (r.head + 1) % len(r.recs)
		r.n--
	}
	r.recs[(r.head+r.n)%len(r.recs)] = rec
	r.n++
	r.size += int64(len(rec))
	r.mu.Unlock()
	return len(p), nil
}

// fit copies a record, or builds the stub of an oversized one.
func (r *Ring) fit(p []byte) []byte {
	if len(p) <= r.maxRecord {
		return append([]byte(nil), p...)
	}
	head := p[:min(ringHead, r.maxRecord/8, len(p))]
	h, _ := json.Marshal(string(head)) // invalid UTF-8 becomes U+FFFD
	stub := `{"ring_truncated":true,"original_bytes":` + strconv.Itoa(len(p)) + `,"head":` + string(h) + `}`
	if len(stub) > r.maxRecord { // only with a tiny ring_buffer_bytes
		stub = `{"ring_truncated":true,"original_bytes":` + strconv.Itoa(len(p)) + `}`
	}
	if len(stub) > r.maxRecord {
		return nil
	}
	return []byte(stub)
}

// Records returns the stored records, oldest first, without the trailing newline.
func (r *Ring) Records() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, r.n)
	for i := range r.n {
		b := r.recs[(r.head+i)%len(r.recs)]
		for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
			b = b[:len(b)-1]
		}
		out = append(out, string(b))
	}
	return out
}

// Len returns the number of stored records.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// Bytes returns the number of bytes the stored records hold.
func (r *Ring) Bytes() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.size
}
