package ansiblestub

import (
	"sort"
	"strconv"
	"strings"
)

// Entry is the number of requests that had the same method, host, path, query, conditionality and authorization scheme. Path is the
// path as the client sent it, Kind its class of request (the Class constants). Auth is the scheme of the Authorization header
// ("Bearer", "Token", "Basic", or another word) and empty when the request had none: what a host rule test reads to see where a
// credential went. Statuses counts the answers by status code; an answer that was cut ("reset", a client that left) is counted
// under "0".
type Entry struct {
	Method      string         `json:"method"`
	Host        string         `json:"host"`
	Path        string         `json:"path"`
	Query       string         `json:"query,omitempty"`
	Conditional bool           `json:"conditional"`
	Auth        string         `json:"auth,omitempty"`
	Kind        string         `json:"kind"`
	Count       int            `json:"count"`
	Statuses    map[string]int `json:"statuses,omitempty"`
}

// Counts is a snapshot of the request counters. Control requests are not counted.
type Counts struct {
	Entries []Entry `json:"entries"`
}

type countKey struct {
	method, host, path, query, auth string
	conditional                     bool
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

// Requests is the number of requests with a method for a path as the client wrote it, on any host (query ignored).
func (c Counts) Requests(method, path string) int {
	return c.sum(func(e Entry) bool { return e.Method == method && e.Path == path })
}

// Query is the number of requests with a method for a path and the raw query as the client wrote it.
func (c Counts) Query(method, path, query string) int {
	return c.sum(func(e Entry) bool { return e.Method == method && e.Path == path && e.Query == query })
}

// OfKind is the number of requests of a kind (the Class constants) with a method (empty: any).
func (c Counts) OfKind(kind, method string) int {
	return c.sum(func(e Entry) bool { return e.Kind == kind && (method == "" || e.Method == method) })
}

// Prefixed is the number of requests whose path starts with prefix, with a method (empty: any).
func (c Counts) Prefixed(prefix, method string) int {
	return c.sum(func(e Entry) bool { return strings.HasPrefix(e.Path, prefix) && (method == "" || e.Method == method) })
}

// OnHost is the number of requests to a host name (without port).
func (c Counts) OnHost(host string) int {
	return c.sum(func(e Entry) bool { return hostName(e.Host) == strings.ToLower(host) })
}

// WithAuth is the number of requests to a host name (without port) that carried the authorization scheme; the scheme "" counts the
// requests without an Authorization header.
func (c Counts) WithAuth(host, scheme string) int {
	return c.sum(func(e Entry) bool { return hostName(e.Host) == strings.ToLower(host) && e.Auth == scheme })
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
		case a.Host != b.Host:
			return a.Host < b.Host
		case a.Path != b.Path:
			return a.Path < b.Path
		case a.Query != b.Query:
			return a.Query < b.Query
		case a.Method != b.Method:
			return a.Method < b.Method
		case a.Auth != b.Auth:
			return a.Auth < b.Auth
		}
		return !a.Conditional && b.Conditional
	})
	return out
}
