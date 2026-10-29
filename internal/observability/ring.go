package observability

import "sync"

// Ring keeps the last records of the log stream in memory, after redaction
// (R-OPS-009). Support bundles read their logs from it. It dies with the pod.
type Ring struct {
	mu   sync.Mutex
	recs [][]byte
	next int
	full bool
}

// NewRing returns a ring of the given capacity; capacity 0 keeps nothing.
func NewRing(capacity int) *Ring {
	if capacity < 0 {
		capacity = 0
	}
	return &Ring{recs: make([][]byte, capacity)}
}

// Write stores one record (one line, as written by the slog handler). It
// implements io.Writer so the ring can sit next to the output in a MultiWriter.
func (r *Ring) Write(p []byte) (int, error) {
	if len(r.recs) == 0 {
		return len(p), nil
	}
	rec := make([]byte, len(p))
	copy(rec, p)
	r.mu.Lock()
	r.recs[r.next] = rec
	r.next++
	if r.next == len(r.recs) {
		r.next = 0
		r.full = true
	}
	r.mu.Unlock()
	return len(p), nil
}

// Records returns the stored records, oldest first, without the trailing newline.
func (r *Ring) Records() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	add := func(b []byte) {
		for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
			b = b[:len(b)-1]
		}
		out = append(out, string(b))
	}
	if r.full {
		for _, b := range r.recs[r.next:] {
			add(b)
		}
	}
	for _, b := range r.recs[:r.next] {
		add(b)
	}
	return out
}

// Len returns the number of stored records.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.full {
		return len(r.recs)
	}
	return r.next
}
