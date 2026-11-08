package debstub

import (
	"sort"
	"strconv"
	"strings"
)

// Entry is the number of requests that had the same method, path, query and conditionality. Path is the path as the client sent it
// (a flat root arrives as "/flatroot/./InRelease"), Kind its class of request. Statuses counts the answers by status code; an
// answer that was cut ("reset", a client that left) is counted under "0".
type Entry struct {
	Method      string         `json:"method"`
	Host        string         `json:"host"`
	Path        string         `json:"path"`
	Query       string         `json:"query,omitempty"`
	Conditional bool           `json:"conditional"`
	Kind        string         `json:"kind"`
	Count       int            `json:"count"`
	Statuses    map[string]int `json:"statuses,omitempty"`
}

// Counts is a snapshot of the request counters. Control requests are not counted.
type Counts struct {
	Entries []Entry `json:"entries"`
}

type countKey struct {
	method, host, path, query string
	conditional               bool
}

func (c Counts) sum(match func(Entry) bool) int {
	n := 0
	for _, e := range c.Entries {
		if match(e) {
			n += e.Count
		}
	}
	return n
}

func (e Entry) status(codes ...int) int {
	n := 0
	for _, c := range codes {
		n += e.Statuses[strconv.Itoa(c)]
	}
	return n
}

// Total is the number of requests.
func (c Counts) Total() int { return c.sum(func(Entry) bool { return true }) }

// Requests is the number of requests with a method for a path as the client wrote it.
func (c Counts) Requests(method, path string) int {
	return c.sum(func(e Entry) bool { return e.Method == method && e.Path == path })
}

// OfKind is the number of requests of a kind (the Class constants) with a method (empty: any).
func (c Counts) OfKind(kind, method string) int {
	return c.sum(func(e Entry) bool { return e.Kind == kind && (method == "" || e.Method == method) })
}

// Prefixed is the number of requests whose path starts with prefix, with a method (empty: any).
func (c Counts) Prefixed(prefix, method string) int {
	return c.sum(func(e Entry) bool { return strings.HasPrefix(e.Path, prefix) && (method == "" || e.Method == method) })
}

// Conditional is the number of requests of a kind that carried If-None-Match or If-Modified-Since.
func (c Counts) Conditional(kind string) int {
	return c.sum(func(e Entry) bool { return e.Kind == kind && e.Conditional })
}

// Delivered is the number of GET requests of a kind that delivered bytes (status 200 or 206).
func (c Counts) Delivered(kind string) int {
	n := 0
	for _, e := range c.Entries {
		if e.Kind == kind && e.Method == "GET" {
			n += e.status(200, 206)
		}
	}
	return n
}

// NotModified is the number of requests answered 304.
func (c Counts) NotModified() int {
	n := 0
	for _, e := range c.Entries {
		n += e.status(304)
	}
	return n
}

// classify names the class of a request from its path alone.
func classify(path string) string {
	i := strings.LastIndexByte(path, '/')
	base := path[i+1:]
	switch {
	case strings.Contains(path, "/by-hash/"):
		return ClassByHash
	case strings.Contains(path, ".diff/"):
		return ClassDiff
	case base == "InRelease" || base == "Release" || base == "Release.gpg":
		return ClassRelease
	case strings.HasPrefix(base, "Packages") || strings.HasPrefix(base, "Sources"):
		return ClassIndex
	case strings.HasPrefix(path, ArchiveBase+"/pool/"), strings.HasSuffix(base, ".deb"), strings.HasSuffix(base, ".dsc"), strings.HasSuffix(base, ".tar.gz"):
		return ClassPool
	}
	return ClassOther
}

// Counts returns a snapshot of the request counters, sorted by path.
func (s *Server) Counts() Counts {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Counts{Entries: make([]Entry, 0, len(s.counts))}
	for _, e := range s.counts {
		c := *e
		c.Statuses = make(map[string]int, len(e.Statuses))
		for k, v := range e.Statuses {
			c.Statuses[k] = v
		}
		out.Entries = append(out.Entries, c)
	}
	sort.Slice(out.Entries, func(i, j int) bool {
		a, b := out.Entries[i], out.Entries[j]
		switch {
		case a.Path != b.Path:
			return a.Path < b.Path
		case a.Query != b.Query:
			return a.Query < b.Query
		case a.Method != b.Method:
			return a.Method < b.Method
		}
		return !a.Conditional && b.Conditional
	})
	return out
}
