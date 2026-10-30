package npmstub

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/guided-traffic/trove/test/npmstub/corpus"
)

// controlPrefix is the path prefix of the control API; nothing below it is a package.
const controlPrefix = "/-/stub/"

type cacheKey struct {
	name, base  string
	abbreviated bool
}

// rendered is a document as served: the bytes, their validator and the gzip form on demand.
type rendered struct {
	body     []byte
	etag     string
	modified time.Time

	gzOnce sync.Once
	gz     []byte
}

func (d *rendered) gzipped() []byte {
	d.gzOnce.Do(func() {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed) // the level is valid
		_, _ = zw.Write(d.body)
		_ = zw.Close()
		d.gz = buf.Bytes()
	})
	return d.gz
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	escaped := r.URL.EscapedPath()
	if strings.HasPrefix(escaped, controlPrefix) {
		s.serveControl(w, r)
		return
	}
	rt := parseRoute(escaped)
	mode := s.record(r, escaped, rt)
	s.cfg.Logger.Info("request", "method", r.Method, "path", escaped, "accept", r.Header.Get("Accept"),
		"conditional", isConditional(r), "auth", authScheme(r.Header.Get("Authorization")))

	if !s.gate(w, r, rt, mode) {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	switch rt.kind {
	case KindPing:
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	case KindDocument:
		s.serveDocument(w, r, rt.name)
	case KindTarball:
		s.serveTarball(w, r, rt)
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

// record counts the request and returns the mode in force for it.
func (s *Server) record(r *http.Request, escaped string, rt route) Mode {
	key := countKey{method: r.Method, path: escaped, auth: authScheme(r.Header.Get("Authorization")), conditional: isConditional(r)}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.counts[key]
	if e == nil {
		e = &Entry{Method: key.method, Path: key.path, Conditional: key.conditional, Auth: key.auth, Kind: rt.kind, Name: rt.name, File: rt.file}
		s.counts[key] = e
	}
	e.Count++
	e.Accept = r.Header.Get("Accept")
	return s.mode
}

// gate applies the failure switches in front of every route and reports whether the request
// goes on.
func (s *Server) gate(w http.ResponseWriter, r *http.Request, rt route, m Mode) bool {
	switch m.Down {
	case DownUnavailable:
		writeError(w, http.StatusServiceUnavailable, "the test upstream is down")
		return false
	case DownReset:
		panic(http.ErrAbortHandler) // closes the connection without an answer
	case DownHang:
		select {
		case <-r.Context().Done():
		case <-time.After(MaxHang):
		}
		panic(http.ErrAbortHandler)
	}
	if m.RequireAuthorization != "" && r.Header.Get("Authorization") != m.RequireAuthorization {
		w.Header().Set("WWW-Authenticate", `Bearer realm="npmstub"`)
		writeError(w, http.StatusUnauthorized, "authentication required")
		return false
	}
	if d := m.delay(rt.kind); d > 0 {
		select {
		case <-time.After(d):
		case <-r.Context().Done():
			return false
		}
	}
	switch {
	case rt.name != "" && m.has(m.NotFound, rt.name):
		writeError(w, http.StatusNotFound, "Not found")
		return false
	case rt.name != "" && m.has(m.Gone, rt.name):
		writeError(w, http.StatusGone, "Gone")
		return false
	}
	return true
}

func (s *Server) serveDocument(w http.ResponseWriter, r *http.Request, name string) {
	abbreviated := strings.Contains(r.Header.Get("Accept"), corpus.AbbreviatedType)
	doc, ok := s.document(name, s.baseURL(r), abbreviated)
	if !ok {
		writeError(w, http.StatusNotFound, "Not found")
		return
	}
	h := w.Header()
	h.Set("Vary", "Accept, Accept-Encoding")
	h.Set("ETag", doc.etag)
	h.Set("Last-Modified", doc.modified.UTC().Format(http.TimeFormat))
	h.Set("Cache-Control", "public, max-age=300")
	if abbreviated {
		h.Set("Content-Type", corpus.AbbreviatedType)
	} else {
		h.Set("Content-Type", "application/json")
	}
	if notModified(r, doc.etag, doc.modified) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := doc.body
	if len(body) > 1024 && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		body = doc.gzipped()
	}
	_, _ = w.Write(body)
}

// document returns the rendered document of a package, from the cache when nothing changed since.
func (s *Server) document(name, base string, abbreviated bool) (*rendered, bool) {
	key := cacheKey{name, base, abbreviated}
	s.mu.Lock()
	defer s.mu.Unlock()
	if d := s.cache[key]; d != nil {
		return d, true
	}
	var body []byte
	var modified time.Time
	if rec, ok := s.recorded[name]; ok {
		var err error
		if body, err = rec.render(base, abbreviated); err != nil {
			return nil, false
		}
		modified, _ = time.Parse(time.RFC3339, strings.Trim(string(rec.modified()), `"`))
	} else {
		p, ok := s.viewLocked(name)
		if !ok {
			return nil, false
		}
		body, modified = p.Document(base, abbreviated), p.Modified
	}
	if s.mode.has(s.mode.WrongName, name) {
		body = withName(body, name+"-impostor")
	}
	sum := sha256.Sum256(body)
	d := &rendered{body: body, etag: `W/"` + hex.EncodeToString(sum[:16]) + `"`, modified: modified}
	s.cache[key] = d
	return d, true
}

// withName replaces the top-level name of a document.
func withName(doc []byte, name string) []byte {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(doc, &m); err != nil {
		return doc
	}
	m["name"], _ = json.Marshal(name)
	out, err := json.Marshal(m)
	if err != nil {
		return doc
	}
	return out
}

func (s *Server) serveTarball(w http.ResponseWriter, r *http.Request, rt route) {
	data, ok := s.tarball(rt.name, rt.file)
	if !ok {
		writeError(w, http.StatusNotFound, "Not found")
		return
	}
	sum := sha256.Sum256(data)
	etag := `"` + hex.EncodeToString(sum[:16]) + `"`
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Cache-Control", "public, max-age=300")
	if notModified(r, etag, time.Time{}) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = w.Write(data)
}

// tarball returns the bytes of a tarball file, or false when there is none.
func (s *Server) tarball(name, file string) ([]byte, bool) {
	s.mu.Lock() // published packages are changed in place, so the lookup and the build stay inside
	defer s.mu.Unlock()
	rec, isRecorded := s.recorded[name]
	base, known := s.baseLocked(name)
	mode, dropped := s.mode, s.dropped[name]

	if isRecorded {
		data, err := fs.ReadFile(s.recFS, path.Join(rec.dir, file))
		return data, err == nil
	}
	if !known {
		return nil, false
	}
	version, ok := corpus.VersionOfFile(name, file)
	if !ok {
		return nil, false
	}
	i := base.Find(version)
	if i < 0 || (dropped[version] && mode.DropTarballs) {
		return nil, false
	}
	return corpus.Tarball(name, version, base.Versions[i].Dependencies), true
}

func (s *Server) baseURL(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return strings.TrimSuffix(s.cfg.PublicURL, "/")
	}
	return "https://" + r.Host
}

func isConditional(r *http.Request) bool {
	return r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != ""
}

// notModified evaluates the validators of a request: If-None-Match decides when present, and
// If-Modified-Since only otherwise.
func notModified(r *http.Request, etag string, modified time.Time) bool {
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		for _, candidate := range strings.Split(inm, ",") {
			candidate = strings.TrimSpace(candidate)
			if candidate == "*" || strings.TrimPrefix(candidate, "W/") == strings.TrimPrefix(etag, "W/") {
				return true
			}
		}
		return false
	}
	if ims := r.Header.Get("If-Modified-Since"); ims != "" && !modified.IsZero() {
		t, err := http.ParseTime(ims)
		return err == nil && !modified.Truncate(time.Second).After(t)
	}
	return false
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body, _ := json.Marshal(map[string]string{"error": msg})
	_, _ = w.Write(body)
}
