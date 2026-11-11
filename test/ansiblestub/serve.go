package ansiblestub

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const controlPrefix = "/-/stub/"

// statusWriter remembers the status an answer was given and reports it to the counters at that moment: the answer is not on the wire
// before the handler has set its status or written its first byte, and a test that reads the counters when its client has the
// answer must find the status counted. Reporting it when the handler returns would race with the client.
type statusWriter struct {
	http.ResponseWriter
	status int
	note   func(status int)
}

func (w *statusWriter) set(code int) {
	if w.status == 0 {
		w.status = code
		w.note(code)
	}
}

func (w *statusWriter) WriteHeader(code int) {
	w.set(code)
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	w.set(http.StatusOK)
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.EscapedPath(), controlPrefix) {
		s.serveControl(w, r)
		return
	}
	site := s.siteOf(hostName(r.Host))
	var rt route
	class := ClassRoleArchive
	switch site {
	case siteGalaxy:
		rt = parseGalaxy(r.URL.Path)
		class = rt.class()
	case siteRealm:
		class = ClassRealm
	}
	key, mode := s.record(r, class)
	sw := &statusWriter{ResponseWriter: w, note: func(status int) { s.noteStatus(key, status) }}
	defer func() {
		if sw.status == 0 {
			sw.note(0) // cut before any answer
		}
	}()
	defer s.fl.enter()()
	s.cfg.Logger.Info("request", "method", r.Method, "host", r.Host, "path", r.URL.EscapedPath(), "query", r.URL.RawQuery)

	if site == siteRealm {
		s.serveRealm(sw, r)
		return
	}
	if !s.gate(sw, r, class, mode) {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		sw.Header().Set("Allow", "GET, HEAD")
		writeText(sw, http.StatusMethodNotAllowed, "the test upstream serves reads only")
		return
	}
	if site != siteGalaxy {
		s.serveRoleArchive(sw, r, site)
		return
	}
	s.serveGalaxy(sw, r, rt, mode)
}

// record counts the request and returns its counter and the mode in force for it.
func (s *Server) record(r *http.Request, class string) (countKey, Mode) {
	auth, _, _ := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
	key := countKey{
		method: r.Method, host: r.Host, path: r.URL.EscapedPath(), query: r.URL.RawQuery, auth: auth,
		conditional: r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "",
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.counts[key]
	if e == nil {
		e = &Entry{Method: key.method, Host: key.host, Path: key.path, Query: key.query, Conditional: key.conditional, Auth: auth, Kind: class, Statuses: map[string]int{}}
		s.counts[key] = e
	}
	e.Count++
	return key, s.mode
}

// noteStatus adds an answer's status to its counter; an answer that was cut counts as 0.
func (s *Server) noteStatus(key countKey, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.counts[key]; e != nil {
		e.Statuses[strconv.Itoa(status)]++
	}
}

// gate applies the switches in front of every route and reports whether the request goes on.
func (s *Server) gate(w http.ResponseWriter, r *http.Request, class string, m Mode) bool {
	if s.throttleGate(w, r, class) {
		return false
	}
	switch m.Down {
	case DownUnavailable:
		writeText(w, http.StatusServiceUnavailable, "the test upstream is down")
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
	if d := m.delay(class); d > 0 {
		select {
		case <-time.After(d):
		case <-r.Context().Done():
			return false
		}
	}
	if status := m.failStatus(class); status != 0 {
		writeText(w, status, "the test upstream fails this request")
		return false
	}
	return true
}

func (s *Server) serveGalaxy(w http.ResponseWriter, r *http.Request, rt route, m Mode) {
	if m.RejectToken {
		w.Header().Set("WWW-Authenticate", `Token realm="ansiblestub"`)
		writeDoc(w, r, m, http.StatusUnauthorized, unauthenticated())
		return
	}
	if !s.checkBearer(r.Header.Get("Authorization")) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="ansiblestub"`)
		writeDoc(w, r, m, http.StatusUnauthorized, unauthenticated())
		return
	}
	if strings.HasPrefix(rt.kind, "role") && m.NoV1 {
		writeDoc(w, r, m, http.StatusNotFound, roleNotFound())
		return
	}
	if m.Redirect && rt.short && isRedirectableShort(rt.kind) {
		target := pluginRoot + "index/" + strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/v3/collections"), "/")
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusFound) //nolint:gosec // the target starts with the fixed path pluginRoot
		return
	}
	base := "https://" + r.Host
	q := r.URL.Query()

	switch rt.kind {
	case kRoot:
		writeDoc(w, r, m, http.StatusOK, rootDocument(m.NoV1))
	case kIndex:
		writeDoc(w, r, m, http.StatusOK, indexDocument(s.collections(), q))
	case kCollection:
		c := s.findCollection(rt.ns, rt.name)
		if c == nil {
			writeDoc(w, r, m, http.StatusNotFound, notFound())
			return
		}
		writeDoc(w, r, m, http.StatusOK, collectionDocument(c))
	case kVersions:
		c := s.findCollection(rt.ns, rt.name)
		if c == nil {
			writeDoc(w, r, m, http.StatusNotFound, notFound())
			return
		}
		writeDoc(w, r, m, http.StatusOK, versionsDocument(c, q))
	case kVersion:
		c := s.findCollection(rt.ns, rt.name)
		var v *versionDef
		if c != nil {
			v = c.find(rt.vers)
		}
		if v == nil {
			writeDoc(w, r, m, http.StatusNotFound, notFound())
			return
		}
		_, ann, err := s.served(c, v, m)
		if err != nil {
			writeText(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeDoc(w, r, m, http.StatusOK, versionDocument(base, c, v, ann, m.DocsForeignHref))
	case kDocs:
		s.serveDocs(w, r, rt, m)
	case kArtifact:
		if m.Redirect {
			http.Redirect(w, r, signedAddress(rt.file), http.StatusFound)
			return
		}
		s.serveArchive(w, r, rt.file, m)
	case kPulp:
		if r.URL.Query().Get("validate_token") == "" {
			writeDoc(w, r, m, http.StatusForbidden, forbidden())
			return
		}
		s.serveArchive(w, r, rt.file, m)
	case kRoleLookup:
		writeDoc(w, r, m, http.StatusOK, roleLookup(s.roleList(), q))
	case kRoleDoc:
		role := s.findRole(rt.roleID)
		if role == nil {
			writeDoc(w, r, m, http.StatusNotFound, roleNotFound())
			return
		}
		writeDoc(w, r, m, http.StatusOK, roleDocument(role))
	case kRoleVersions:
		role := s.findRole(rt.roleID)
		if role == nil {
			writeDoc(w, r, m, http.StatusNotFound, roleNotFound())
			return
		}
		writeDoc(w, r, m, http.StatusOK, s.roleVersions(r.Host, role, q))
	default:
		writeDoc(w, r, m, http.StatusNotFound, notFound())
	}
}

// serveArchive serves the collection archive named by "<ns>-<name>-<version>.tar.gz" under the digest modes.
func (s *Server) serveArchive(w http.ResponseWriter, r *http.Request, file string, m Mode) {
	ns, name, version, ok := parseArtifactFile(file)
	var c *collectionDef
	var v *versionDef
	if ok {
		if c = s.findCollection(ns, name); c != nil {
			v = c.find(version)
		}
	}
	if v == nil {
		writeDoc(w, r, m, http.StatusNotFound, notFound())
		return
	}
	data, _, err := s.served(c, v, m)
	if err != nil {
		writeText(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+file+`"`)
	http.ServeContent(w, r, "", v.created(), bytes.NewReader(data.Data))
}

// serveRoleArchive serves a role archive on the GitHub stand-in (a redirect to the codeload host), the codeload host and the
// unlisted host (bytes). The tag must be one of the role's versions.
func (s *Server) serveRoleArchive(w http.ResponseWriter, r *http.Request, site string) {
	user, repo, tag, ok := roleArchive(site, r.URL.Path)
	if ok {
		ok = s.hasRoleTag(user, repo, tag)
	}
	if !ok {
		writeText(w, http.StatusNotFound, "not found")
		return
	}
	if site == siteGitHub {
		target := s.siteBase(siteCodeload, r.Host) + "/" + user + "/" + repo + "/tar.gz/" + tag
		http.Redirect(w, r, target, http.StatusFound) //nolint:gosec // the host is the configured codeload host, the rest a role tag the corpus has
		return
	}
	data, err := s.archives.role(user, repo, tag)
	if err != nil {
		writeText(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/x-gzip")
	http.ServeContent(w, r, "", corpusStart, bytes.NewReader(data))
}

func (s *Server) roleList() []*roleDef {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*roleDef(nil), s.roles...)
}

func (s *Server) findRole(id int) *roleDef {
	for _, r := range s.roleList() {
		if r.id == id {
			return r
		}
	}
	return nil
}

func (s *Server) hasRoleTag(user, repo, tag string) bool {
	for _, r := range s.roleList() {
		if r.user == user && r.repo == repo {
			_, ok := r.find(tag)
			return ok
		}
	}
	return false
}

// writeDoc writes a JSON document of the Galaxy, compressed when the mode says so and the client accepts it.
func writeDoc(w http.ResponseWriter, r *http.Request, m Mode, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	body = append(body, '\n')
	h := w.Header()
	h.Set("Content-Type", "application/json")
	if m.Gzip && acceptsGzip(r) {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write(body)
		_ = zw.Close()
		body = buf.Bytes()
		h.Set("Content-Encoding", "gzip")
		h.Add("Vary", "Accept-Encoding")
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body) //nolint:gosec // JSON of the stub's own making
}

func writeText(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(message + "\n")) //nolint:gosec // text/plain, and the message is the stub's own text
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

// hugeDocsBytes is larger than the 16 MiB a remote stores of a documentation document.
const hugeDocsBytes = 17 << 20

// writeHugeDocs writes a document of the right shape that is larger than the bound: the contents are an array padded with white space.
func writeHugeDocs(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(hugeDocsBytes))
	w.WriteHeader(http.StatusOK)
	const head, tail = `{"docs_blob":{"collection_readme":null,"documentation_files":[],"contents":[`, `]}}`
	pad := bytes.Repeat([]byte(" "), 64<<10)
	_, _ = w.Write([]byte(head))
	for left := hugeDocsBytes - len(head) - len(tail); left > 0; {
		n := min(left, len(pad))
		if _, err := w.Write(pad[:n]); err != nil {
			return
		}
		left -= n
	}
	_, _ = w.Write([]byte(tail))
}

// serveDocs answers the docs-blob route under the modes of Mode.Docs.
func (s *Server) serveDocs(w http.ResponseWriter, r *http.Request, rt route, m Mode) {
	c := s.findCollection(rt.ns, rt.name)
	var v *versionDef
	if c != nil {
		v = c.find(rt.vers)
	}
	switch {
	case v == nil || m.Docs == DocsNotFound:
		writeDoc(w, r, m, http.StatusNotFound, notFound())
	case m.Docs == DocsError:
		writeText(w, http.StatusInternalServerError, "the test upstream fails the documentation")
	case m.Docs == DocsHuge:
		writeHugeDocs(w)
	default:
		writeDoc(w, r, m, http.StatusOK, docsDocument(c, v, m.Docs))
	}
}
