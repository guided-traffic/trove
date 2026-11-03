package osvstub

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// maxLog bounds the request log; the per-path counts are not bounded.
const maxLog = 5000

// Request is one entry of the request log.
type Request struct {
	Seq         int    `json:"seq"`
	Time        string `json:"time"`
	Method      string `json:"method"`
	Path        string `json:"path"`
	Ecosystem   string `json:"ecosystem,omitempty"`
	Kind        string `json:"kind"`
	Status      int    `json:"status"`
	Range       string `json:"range,omitempty"`
	Conditional bool   `json:"conditional,omitempty"`
	Bytes       int64  `json:"bytes"`
}

// PathCount is what one path received.
type PathCount struct {
	Requests    int `json:"requests"`
	Conditional int `json:"conditional"` // with If-None-Match or If-Modified-Since
	NotModified int `json:"notModified"` // answered 304
	Ranged      int `json:"ranged"`      // with a Range header
	Partial     int `json:"partial"`     // answered 206
	Errors      int `json:"errors"`      // answered 400 or above
}

// Counts is a snapshot of the request log. Control requests are not counted.
type Counts struct {
	Total int `json:"total"`
	// Kinds counts by "<ecosystem>/<kind>" ("npm/archive", "NuGet/record", ...) and by "ecosystems" and "kev".
	Kinds map[string]int       `json:"kinds"`
	Paths map[string]PathCount `json:"paths"`
	// Log is the requests in order, newest last, at most 5000.
	Log []Request `json:"log,omitempty"`
}

// Path is the count of one path.
func (c Counts) Path(p string) PathCount { return c.Paths[p] }

// Kind is the count of "<ecosystem>/<kind>".
func (c Counts) Kind(ecosystem, kind string) int { return c.Kinds[ecosystem+"/"+kind] }

// Records is the number of requests for single records of an ecosystem.
func (c Counts) Records(ecosystem string) int { return c.Kind(ecosystem, KindRecord) }

// Under is the number of requests whose path starts with prefix.
func (c Counts) Under(prefix string) int {
	n := 0
	for p, pc := range c.Paths {
		if strings.HasPrefix(p, prefix) {
			n += pc.Requests
		}
	}
	return n
}

type requestLog struct {
	mu    sync.Mutex
	seq   int
	log   []Request
	paths map[string]*PathCount
	kinds map[string]int
}

func (l *requestLog) add(r Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.paths == nil {
		l.paths = map[string]*PathCount{}
		l.kinds = map[string]int{}
	}
	l.seq++
	r.Seq = l.seq
	r.Time = stamp(time.Now())
	if len(l.log) >= maxLog {
		l.log = append(l.log[:0], l.log[len(l.log)-maxLog+1:]...)
	}
	l.log = append(l.log, r)
	pc := l.paths[r.Path]
	if pc == nil {
		pc = &PathCount{}
		l.paths[r.Path] = pc
	}
	pc.Requests++
	if r.Conditional {
		pc.Conditional++
	}
	if r.Range != "" {
		pc.Ranged++
	}
	switch {
	case r.Status == 304:
		pc.NotModified++
	case r.Status == 206:
		pc.Partial++
	case r.Status >= 400:
		pc.Errors++
	}
	key := r.Kind
	if r.Ecosystem != "" {
		key = r.Ecosystem + "/" + r.Kind
	}
	l.kinds[key]++
}

// Counts returns a snapshot of the request log.
func (s *Stub) Counts() Counts {
	l := &s.log
	l.mu.Lock()
	defer l.mu.Unlock()
	c := Counts{Total: l.seq, Kinds: map[string]int{}, Paths: map[string]PathCount{}, Log: append([]Request(nil), l.log...)}
	for k, v := range l.kinds {
		c.Kinds[k] = v
	}
	for p, v := range l.paths {
		c.Paths[p] = *v
	}
	return c
}

// ResetCounts empties the request log and zeroes the counts; the dataset and the faults stay.
func (s *Stub) ResetCounts() {
	l := &s.log
	l.mu.Lock()
	defer l.mu.Unlock()
	l.seq, l.log, l.paths, l.kinds = 0, nil, nil, nil
}

// SortedPaths lists the paths that were requested, ascending.
func (c Counts) SortedPaths() []string {
	var out []string
	for p := range c.Paths {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
