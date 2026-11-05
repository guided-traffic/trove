package npmstub

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

// The replication service (R-REPO-059, the section "Mirror discovery" of docs/spec/formats/npm.md): the public registry's
// CouchDB replication paths, served by the same stub under a second host name, because the real service is another host
// than the registry (replicate.npmjs.com). A request whose Host header names one of Config.ReplicationHosts is a
// replication request; the same listener and certificate serve both hosts, and the registry host never answers these paths.
//
//	GET /                      {"db_name":"registry","doc_count":n,"update_seq":s}
//	GET /registry/_changes     ?since=<seq>&limit=<n, at most 10000>: the changed ids after since, one entry per id
//	GET /registry/_all_docs    ?startkey=<JSON string>&endkey=<JSON string>&limit=<n, at most 10000>: the ids in key order
//
// include_docs and skip are refused with 400, as the service refuses them (R-REPO-051). The sequence is a number; a change
// is added by Publish, Change and the control API. The requests are logged without a credential (Replications), so that a
// test can see that the replication host received no Authorization header.

// KindReplication is the kind of the requests to a replication host.
const KindReplication = "replication"

// DefaultReplicationHost is the replication host a Config without ReplicationHosts serves.
const DefaultReplicationHost = "replicate.npmjs.com"

const (
	replicationMaxLimit     = 10000
	replicationDefaultLimit = 1000
)

// change is one entry of the change feed.
type change struct {
	seq     int
	id      string
	deleted bool
}

// ReplicationRequest is one request the replication host received: what it asked for and the scheme of its credential (as
// in the counters, never the credential).
type ReplicationRequest struct {
	Host   string `json:"host"`
	Method string `json:"method"`
	Path   string `json:"path"`
	// Query is the raw query string as the client sent it.
	Query string `json:"query,omitempty"`
	// Auth is none, bearer, basic or other; anything but none means that an Authorization header arrived.
	Auth string `json:"auth"`
}

// Authorized reports whether the request carried an Authorization header.
func (r ReplicationRequest) Authorized() bool { return r.Auth != "none" }

// replicationHosts is the set of host names (without port, lower case) that are replication hosts.
func (c Config) replicationHosts() []string {
	if c.ReplicationHosts == nil {
		return []string{DefaultReplicationHost}
	}
	return c.ReplicationHosts
}

// isReplicationHost reports whether the Host header of a request names a replication host.
func (s *Server) isReplicationHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	for _, r := range s.cfg.replicationHosts() {
		if strings.EqualFold(r, host) {
			return true
		}
	}
	return false
}

// initialChangesLocked is the feed as it begins: one change per package, in name order.
func (s *Server) initialChangesLocked() []change {
	var out []change
	for i, n := range s.namesLocked() {
		out = append(out, change{seq: i + 1, id: n})
	}
	return out
}

// namesLocked lists the packages the registry holds, sorted by bytes, as _all_docs does: the corpus, the recorded set and
// the packages Publish created, without those a deleted change removed.
func (s *Server) namesLocked() []string {
	set := map[string]bool{}
	for _, n := range s.cfg.Corpus.Names() {
		set[n] = true
	}
	for n := range s.recorded {
		set[n] = true
	}
	for n := range s.overlay {
		set[n] = true
	}
	out := make([]string, 0, len(set))
	for n := range set {
		if !s.removed[n] && !s.unlisted[n] {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Server) appendChangeLocked(id string, deleted bool) {
	seq := 1
	if n := len(s.changes); n > 0 {
		seq = s.changes[n-1].seq + 1
	}
	s.changes = append(s.changes, change{seq: seq, id: id, deleted: deleted})
}

// Change adds an entry to the change feed for a package, as the registry does when a package changes. With deleted the
// package is removed: its document and tarballs answer 404 and it is no longer listed by _all_docs; without, a package
// that was deleted is back. The name need not be a package of the corpus (a change for an id the registry does not hold is
// a change for a package that the stub answers 404 for).
func (s *Server) Change(name string, deleted bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if deleted {
		s.removed[name] = true
	} else {
		delete(s.removed, name)
	}
	s.cache = map[cacheKey]*rendered{}
	s.appendChangeLocked(name, deleted)
}

// Unlist makes the replication service leave a package out of _all_docs, as a feed does that missed a name (R-REPO-051: a
// full pass also takes the packages the repository holds); the registry keeps serving its document and tarballs. Publish
// and Change do not list it again.
func (s *Server) Unlist(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unlisted[name] = true
}

// UpdateSeq is the sequence of the last change.
func (s *Server) UpdateSeq() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateSeqLocked()
}

func (s *Server) updateSeqLocked() int {
	if n := len(s.changes); n > 0 {
		return s.changes[n-1].seq
	}
	return 0
}

// Replications returns the log of the requests the replication hosts received, oldest first.
func (s *Server) Replications() []ReplicationRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append(make([]ReplicationRequest, 0, len(s.replog)), s.replog...)
}

// isDeleted reports whether a deleted change removed the package.
func (s *Server) isDeleted(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removed[name]
}

func (s *Server) serveReplication(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	q := r.URL.Query()
	for _, refused := range []string{"include_docs", "skip"} {
		if q.Has(refused) {
			writeError(w, http.StatusBadRequest, refused+" is not served by the replication service")
			return
		}
	}
	switch r.URL.Path {
	case "/":
		s.mu.Lock()
		doc := map[string]any{"db_name": "registry", "doc_count": len(s.namesLocked()), "update_seq": s.updateSeqLocked()}
		s.mu.Unlock()
		writeJSON(w, doc)
	case "/registry/_changes":
		s.serveChanges(w, q)
	case "/registry/_all_docs":
		s.serveAllDocs(w, q)
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

// limitOf reads the limit parameter: 1000 when absent, at most 10000.
func limitOf(q map[string][]string) (int, error) {
	vals := q["limit"]
	if len(vals) == 0 {
		return replicationDefaultLimit, nil
	}
	n, err := strconv.Atoi(vals[0])
	if err != nil || n < 1 || n > replicationMaxLimit {
		return 0, fmt.Errorf("limit %q: want 1 to %d", vals[0], replicationMaxLimit)
	}
	return n, nil
}

func (s *Server) serveChanges(w http.ResponseWriter, q map[string][]string) {
	limit, err := limitOf(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	since := 0
	if v := q["since"]; len(v) > 0 && v[0] != "" && v[0] != "0" {
		if since, err = strconv.Atoi(v[0]); err != nil || since < 0 {
			writeError(w, http.StatusBadRequest, "since is no sequence of this registry")
			return
		}
	}
	type result struct {
		Seq     int              `json:"seq"`
		ID      string           `json:"id"`
		Changes []map[string]any `json:"changes"`
		Deleted bool             `json:"deleted,omitempty"`
	}
	s.mu.Lock()
	latest := map[string]int{} // a feed has one entry per id: the last change
	for _, c := range s.changes {
		latest[c.id] = c.seq
	}
	results := []result{}
	last := since
	for _, c := range s.changes {
		if c.seq <= since || latest[c.id] != c.seq {
			continue
		}
		if len(results) == limit {
			break
		}
		results = append(results, result{Seq: c.seq, ID: c.id, Changes: []map[string]any{{"rev": strconv.Itoa(c.seq) + "-stub"}}, Deleted: c.deleted})
		last = c.seq
	}
	if len(results) < limit {
		last = max(last, s.updateSeqLocked())
	}
	s.mu.Unlock()
	writeJSON(w, map[string]any{"results": results, "last_seq": last, "pending": 0})
}

// keyParam reads a startkey or endkey: a JSON string.
func keyParam(q map[string][]string, name string) (string, bool, error) {
	v := q[name]
	if len(v) == 0 {
		return "", false, nil
	}
	var key string
	if err := json.Unmarshal([]byte(v[0]), &key); err != nil {
		return "", false, fmt.Errorf("%s is no JSON string", name)
	}
	return key, true, nil
}

func (s *Server) serveAllDocs(w http.ResponseWriter, q map[string][]string) {
	limit, err := limitOf(q)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	start, hasStart, err := keyParam(q, "startkey")
	if err == nil {
		var end string
		var hasEnd bool
		if end, hasEnd, err = keyParam(q, "endkey"); err == nil {
			s.writeAllDocs(w, start, hasStart, end, hasEnd, limit)
			return
		}
	}
	writeError(w, http.StatusBadRequest, err.Error())
}

func (s *Server) writeAllDocs(w http.ResponseWriter, start string, hasStart bool, end string, hasEnd bool, limit int) {
	type row struct {
		ID    string            `json:"id"`
		Key   string            `json:"key"`
		Value map[string]string `json:"value"`
	}
	s.mu.Lock()
	names := s.namesLocked()
	s.mu.Unlock()
	rows := []row{}
	offset := 0
	for _, n := range names {
		if hasStart && n < start {
			offset++
			continue
		}
		if hasEnd && n > end {
			break
		}
		if len(rows) == limit {
			break
		}
		rows = append(rows, row{ID: n, Key: n, Value: map[string]string{"rev": "1-stub"}})
	}
	writeJSON(w, map[string]any{"total_rows": len(names), "offset": offset, "rows": rows})
}
