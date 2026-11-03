package osvstub

import (
	"bytes"
	"crypto/md5" //nolint:gosec // the bucket's ETag and x-goog-hash are MD5
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash/crc32"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// KEVPath is the path of the known-exploited catalog.
const KEVPath = "/sites/default/files/feeds/known_exploited_vulnerabilities.json"

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// object is what a request resolved to, with the fault of its ecosystem.
type object struct {
	ecosystem string
	kind      string
	body      []byte
	modTime   time.Time
	ctype     string
	bucket    bool // a bucket object: x-goog-hash and Cache-Control
	status    int  // not 0: answer this instead
	fault     Fault
}

// recorder keeps what the request log needs and applies NoRange.
type recorder struct {
	http.ResponseWriter
	status  int
	bytes   int64
	noRange bool
	sent    bool
}

func (r *recorder) WriteHeader(code int) {
	if r.sent {
		return
	}
	r.sent = true
	r.status = code
	if r.noRange {
		r.Header().Del("Accept-Ranges")
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if !r.sent {
		r.WriteHeader(http.StatusOK)
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}

// Unwrap lets http.ResponseController reach the connection.
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

// ServeHTTP answers the bucket layout and the catalog, and the control API below /-/stub/.
func (s *Stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/-/stub/") {
		s.serveControl(w, r)
		return
	}
	rec := &recorder{ResponseWriter: w}
	obj := object{kind: KindOther}
	defer func() {
		s.log.add(Request{
			Method: r.Method, Path: r.URL.Path, Ecosystem: obj.ecosystem, Kind: obj.kind, Status: max(rec.status, 0),
			Range: r.Header.Get("Range"), Conditional: r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "",
			Bytes: rec.bytes,
		})
	}()
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		rec.Header().Set("Allow", "GET, HEAD")
		http.Error(rec, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	obj = s.resolve(r.URL.Path)
	f := obj.fault
	if f.scoped(obj.kind) {
		if f.DelayMS > 0 {
			t := time.NewTimer(time.Duration(f.DelayMS) * time.Millisecond)
			select {
			case <-t.C:
			case <-r.Context().Done():
				t.Stop()
				rec.status = 499
				return
			}
		}
		if f.Error != 0 {
			http.Error(rec, "osvstub: fault "+strconv.Itoa(f.Error), f.Error)
			return
		}
	}
	if obj.status != 0 {
		http.Error(rec, http.StatusText(obj.status), obj.status)
		return
	}

	body := obj.body
	full := body
	if f.scoped(obj.kind) && (f.Truncate || f.Abort) && obj.kind != KindEcosystems && obj.kind != KindOther {
		body = body[:len(body)/2]
	}
	h := rec.Header()
	sum := md5.Sum(body) //nolint:gosec // as the bucket
	h.Set("ETag", `"`+hex.EncodeToString(sum[:])+`"`)
	h.Set("Content-Type", obj.ctype)
	if obj.bucket {
		hashSum := sum
		if f.DigestMismatch && obj.kind == KindArchive {
			hashSum = md5.Sum(append([]byte("wrong"), body...)) //nolint:gosec // a deliberately wrong digest
		}
		var crc [4]byte
		binary.BigEndian.PutUint32(crc[:], crc32.Checksum(body, castagnoli))
		// The bucket sends two header lines; a client must read all of them.
		h.Add("X-Goog-Hash", "crc32c="+base64.StdEncoding.EncodeToString(crc[:]))
		h.Add("X-Goog-Hash", "md5="+base64.StdEncoding.EncodeToString(hashSum[:]))
		h.Set("Cache-Control", "public, max-age=3600")
	}

	if f.scoped(obj.kind) && f.Abort && obj.kind != KindEcosystems && obj.kind != KindOther {
		h.Set("Content-Length", strconv.Itoa(len(full)))
		rec.WriteHeader(http.StatusOK)
		_, _ = rec.Write(body)
		panic(http.ErrAbortHandler)
	}

	req := r
	if f.NoRange {
		req = r.Clone(r.Context())
		req.Header.Del("Range")
		rec.noRange = true
	}
	http.ServeContent(rec, req, "", obj.modTime, bytes.NewReader(body))
}

// resolve maps a path to the object it names, with the ecosystem's fault, under the lock.
func (s *Stub) resolve(path string) object {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch path {
	case "/ecosystems.txt":
		var b strings.Builder
		for _, n := range s.order {
			b.WriteString(n + "\n")
		}
		return object{kind: KindEcosystems, body: []byte(b.String()), modTime: FixtureTime, ctype: "text/plain", bucket: true}
	case KEVPath:
		return object{kind: KindKEV, body: s.kevBodyLocked(), modTime: s.kevAt, ctype: "application/json", fault: s.kevFault}
	}
	rest, ok := strings.CutPrefix(path, "/")
	if !ok {
		return object{kind: KindOther, status: http.StatusNotFound}
	}
	name, file, ok := strings.Cut(rest, "/")
	st, known := s.eco[name]
	if !ok || !known || file == "" || strings.Contains(file, "/") {
		return object{kind: KindOther, status: http.StatusNotFound}
	}
	o := object{ecosystem: name, fault: st.fault, bucket: true, modTime: st.changed}
	s.buildLocked(st)
	switch {
	case file == "all.zip":
		o.kind, o.ctype = KindArchive, "application/zip"
		o.body = st.archive
		if share(len(st.sorted), o.fault.ShrinkPercent) > 0 || share(len(st.sorted), o.fault.InvalidPercent) > 0 || len(o.fault.Invalid) > 0 {
			o.body = zipOf(s.damagedLocked(st))
		}
	case file == "modified_id.csv":
		o.kind, o.ctype, o.body = KindChanges, "text/csv", st.changes
	case strings.HasSuffix(file, ".json"):
		o.kind, o.ctype = KindRecord, "application/json"
		id := strings.TrimSuffix(file, ".json")
		e, found := st.recs[id]
		if !found || contains(o.fault.NotFound, id) {
			o.status = http.StatusNotFound
			return o
		}
		o.body, o.modTime = e.body, e.modified
		if s.invalidLocked(st)[id] {
			o.body = invalidBody(id)
		}
	default:
		return object{kind: KindOther, ecosystem: name, status: http.StatusNotFound}
	}
	return o
}

// invalidLocked is the set of record ids the invalid-record fault replaces: the named ones and InvalidPercent of
// the sorted identifiers of the whole dataset, evenly spread (so the archive and a single fetch agree).
func (s *Stub) invalidLocked(st *ecoState) map[string]bool {
	f := st.fault
	bad := map[string]bool{}
	for i := range spread(len(st.sorted), share(len(st.sorted), f.InvalidPercent)) {
		bad[st.sorted[i].id] = true
	}
	for _, id := range f.Invalid {
		bad[id] = true
	}
	return bad
}

func invalidBody(id string) []byte {
	b, _ := json.Marshal(map[string]string{"id": id, "modified": "not-a-timestamp"})
	return b
}

// damagedLocked returns the records of the archive with the shrink and invalid-record faults applied.
func (s *Stub) damagedLocked(st *ecoState) []*entry {
	drop := spread(len(st.sorted), share(len(st.sorted), st.fault.ShrinkPercent))
	bad := s.invalidLocked(st)
	var out []*entry
	for i, e := range st.sorted {
		switch {
		case drop[i]:
		case bad[e.id]:
			out = append(out, &entry{id: e.id, modified: e.modified, body: invalidBody(e.id)})
		default:
			out = append(out, e)
		}
	}
	return out
}

func (s *Stub) kevBodyLocked() []byte {
	list := make([]KEVEntry, 0, len(s.kev))
	for _, k := range s.kev {
		list = append(list, k)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].DateAdded != list[j].DateAdded {
			return list[i].DateAdded > list[j].DateAdded
		}
		return list[i].CVEID < list[j].CVEID
	})
	b, _ := json.Marshal(map[string]any{
		"title":           "CISA Catalog of Known Exploited Vulnerabilities (trove test source)",
		"catalogVersion":  s.kevAt.Format("2006.01.02"),
		"dateReleased":    stamp(s.kevAt),
		"count":           len(list),
		"vulnerabilities": list,
	})
	return b
}
