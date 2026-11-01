package nugetstub

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// controlPrefix is the path prefix of the control API; no resource of the protocol is below it.
const controlPrefix = "/-/stub/"

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	escaped := r.URL.EscapedPath()
	if strings.HasPrefix(escaped, controlPrefix) {
		s.serveControl(w, r)
		return
	}
	rt := parseRoute(escaped)
	mode := s.record(r, escaped, rt)
	s.cfg.Logger.Info("request", "method", r.Method, "host", r.Host, "path", escaped,
		"conditional", isConditional(r), "auth", authScheme(r.Header.Get("Authorization")))

	b := s.bases(r)
	if !s.gate(w, r, rt, mode, b) {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	switch rt.kind {
	case KindIndex:
		s.writeDocument(w, r, s.indexDoc(b, mode), s.indexTime(), false)
	case KindVersions:
		s.serveVersions(w, r, rt)
	case KindNuspec, KindNupkg:
		s.serveFile(w, r, rt, mode)
	case KindRegIndex, KindRegPage, KindRegLeaf:
		s.serveRegistration(w, r, rt, mode, b)
	case KindSearch:
		s.writeDocument(w, r, s.searchDoc(b, r.URL.Query()), s.indexTime(), false)
	default:
		writeError(w, http.StatusNotFound, "not found")
	}
}

// indexTime is the modification time of documents that do not belong to one package: when the
// stub's package set was last changed.
func (s *Server) indexTime() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := epoch
	for _, c := range s.touched {
		if c.After(t) {
			t = c
		}
	}
	return t
}

// record counts the request and returns the mode in force for it.
func (s *Server) record(r *http.Request, escaped string, rt route) Mode {
	key := countKey{method: r.Method, host: r.Host, path: escaped, auth: authScheme(r.Header.Get("Authorization")), conditional: isConditional(r)}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.counts[key]
	if e == nil {
		e = &Entry{Method: key.method, Host: key.host, Path: key.path, Conditional: key.conditional, Auth: key.auth, Kind: rt.kind, ID: rt.id}
		if rt.kind == KindNupkg || rt.kind == KindNuspec || rt.kind == KindRegLeaf {
			e.Version = rt.version
		}
		s.counts[key] = e
	}
	e.Count++
	return s.mode
}

// gate applies the failure switches in front of every route and reports whether the request
// goes on.
func (s *Server) gate(w http.ResponseWriter, r *http.Request, rt route, m Mode, b bases) bool {
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
		w.Header().Set("WWW-Authenticate", `Basic realm="nugetstub"`)
		writeError(w, http.StatusUnauthorized, "authentication required")
		return false
	}
	if m.StrictHosts && b.wrongHost(rt.kind, r.Host) {
		writeError(w, http.StatusNotFound, "this resource is served on the other host")
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
	case m.has(m.NotFound, rt.id):
		writeError(w, http.StatusNotFound, "Not found")
		return false
	case m.has(m.Gone, rt.id):
		writeError(w, http.StatusGone, "Gone")
		return false
	}
	return true
}

func (s *Server) serveVersions(w http.ResponseWriter, r *http.Request, rt route) {
	p, modified, ok := s.view(rt.id)
	if !ok {
		writeError(w, http.StatusNotFound, "Not found")
		return
	}
	s.writeDocument(w, r, versionsDoc(p), modified, false)
}

func (s *Server) serveRegistration(w http.ResponseWriter, r *http.Request, rt route, m Mode, b bases) {
	p, modified, ok := s.view(rt.id)
	if !ok {
		writeError(w, http.StatusNotFound, "Not found")
		return
	}
	bad := m.has(m.BadDigest, rt.id)
	var body []byte
	switch rt.kind {
	case KindRegIndex:
		body = s.registrationIndexDoc(b, rt.hive, p, bad)
	case KindRegPage:
		lower, upper, _ := strings.Cut(rt.version, "/")
		if !validVersion(lower) || !validVersion(upper) {
			writeError(w, http.StatusNotFound, "Not found")
			return
		}
		body = s.registrationPageDoc(b, rt.hive, p, pageSpan{lower, upper}, bad)
	default:
		i := p.find(rt.version)
		if i < 0 {
			writeError(w, http.StatusNotFound, "Not found")
			return
		}
		body = s.registrationLeafDoc(b, rt.hive, p, p.Versions[i])
	}
	s.writeDocument(w, r, body, modified, rt.hive == registrationGz)
}

// serveFile answers the .nuspec and the .nupkg of a version. The version need not be visible in the
// documents: a dropped version keeps its files.
func (s *Server) serveFile(w http.ResponseWriter, r *http.Request, rt route, m Mode) {
	s.mu.Lock()
	base, ok := s.baseLocked(rt.id)
	s.mu.Unlock()
	if !ok || base.find(rt.version) < 0 {
		writeError(w, http.StatusNotFound, "Not found")
		return
	}
	p, v := base, base.Versions[base.find(rt.version)]
	if rt.kind == KindNupkg && m.has(m.WrongContent, rt.id) {
		if other, ok := s.cfg.Corpus.Get(OtherID); ok {
			p, v = other, other.Versions[0]
		}
	}
	var data []byte
	if rt.kind == KindNupkg {
		data = s.build(p, v).nupkg
	} else {
		data = Nuspec(p, v)
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

// writeDocument answers a request with a JSON document: a weak ETag over the body, which gzip
// content coding does not change, Last-Modified, and 304 for a matching validator. A gzip document
// is compressed when the client's Accept-Encoding allows it.
func (s *Server) writeDocument(w http.ResponseWriter, r *http.Request, body []byte, modified time.Time, gz bool) {
	sum := sha256.Sum256(body)
	etag := `W/"` + hex.EncodeToString(sum[:16]) + `"`
	h := w.Header()
	h.Set("ETag", etag)
	h.Set("Last-Modified", modified.UTC().Format(http.TimeFormat))
	h.Set("Cache-Control", "public, max-age=300")
	h.Set("Content-Type", "application/json")
	if gz {
		h.Set("Vary", "Accept-Encoding")
	}
	if notModified(r, etag, modified) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if gz && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		var buf bytes.Buffer
		zw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed) // the level is valid
		_, _ = zw.Write(body)
		_ = zw.Close()
		h.Set("Content-Encoding", "gzip")
		body = buf.Bytes()
	}
	_, _ = w.Write(body) //nolint:gosec // G705: a JSON document generated from the test corpus; the Host header only picks the addresses in it
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
