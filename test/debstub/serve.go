package debstub

import (
	"bytes"
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/guided-traffic/trove/test/debstub/debgen"
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

// ServeHTTP implements http.Handler. It does not clean the path: a flat root is requested as /flatroot/./InRelease, and the stub
// counts the path as the client wrote it.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.EscapedPath(), controlPrefix) {
		s.serveControl(w, r)
		return
	}
	path := r.URL.Path
	class := classify(path)
	key, mode := s.record(r, class)
	sw := &statusWriter{ResponseWriter: w, note: func(status int) { s.noteStatus(key, status) }}
	defer func() {
		if sw.status == 0 {
			sw.note(0) // cut before any answer
		}
	}()
	defer s.fl.enter()()
	s.cfg.Logger.Info("request", "method", r.Method, "host", r.Host, "path", r.URL.EscapedPath(), "query", r.URL.RawQuery)

	if !s.gate(sw, r, class, mode) {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		sw.Header().Set("Allow", "GET, HEAD")
		writeText(sw, http.StatusMethodNotAllowed, "the test upstream serves reads only")
		return
	}
	s.serveObject(sw, r, path, mode)
}

// record counts the request and returns its counter and the mode in force for it.
func (s *Server) record(r *http.Request, class string) (countKey, Mode) {
	key := countKey{
		method: r.Method, host: r.Host, path: r.URL.EscapedPath(), query: r.URL.RawQuery,
		conditional: r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "",
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.counts[key]
	if e == nil {
		e = &Entry{Method: key.method, Host: key.host, Path: key.path, Query: key.query, Conditional: key.conditional, Kind: class, Statuses: map[string]int{}}
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

// signedDocs are the three documents of a suite's release as one mode signs them.
type signedDocs struct {
	release, inrelease, gpg *object
}

func (s *Server) serveObject(w http.ResponseWriter, r *http.Request, path string, m Mode) {
	s.mu.Lock()
	o, ctype, err := s.resolve(path, m)
	s.mu.Unlock()
	switch {
	case err != nil:
		writeText(w, http.StatusInternalServerError, err.Error())
		return
	case o == nil:
		writeText(w, http.StatusNotFound, "not found")
		return
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	var modified time.Time
	if !m.NoValidators {
		h.Set("ETag", `"`+o.sum.SHA256[:32]+`"`)
		modified = o.modified
	}
	http.ServeContent(w, r, "", modified, bytes.NewReader(o.data))
}

// resolve finds the object a path names, or nil. The caller holds s.mu.
func (s *Server) resolve(path string, m Mode) (*object, string, error) {
	if data, ok := s.static[path]; ok {
		if wrongPackage(m, path) {
			data = bytes.Clone(data)
			data[len(data)-1] ^= 0xff
		}
		return &object{data: data, modified: debgen.ModTime, sum: debgen.Sum(data)}, contentType(path), nil
	}
	for _, a := range s.archives {
		for _, dir := range a.dirs {
			if !strings.HasPrefix(path, dir) {
				continue
			}
			if slices.Contains(m.NotFound, a.id) || slices.Contains(m.NotFound, dirName(dir)) {
				return nil, "", nil
			}
			rest := path[len(dir):]
			for strings.HasPrefix(rest, "./") {
				rest = rest[2:]
			}
			if !a.built {
				a.build(s.dateFor(time.Time{}))
			}
			return s.inArchive(a, rest, m)
		}
	}
	return nil, "", nil
}

func dirName(dir string) string {
	parts := strings.Split(strings.TrimSuffix(dir, "/"), "/")
	return parts[len(parts)-1]
}

func wrongPackage(m Mode, path string) bool {
	if len(m.WrongPackage) == 0 {
		return false
	}
	base := path[strings.LastIndexByte(path, '/')+1:]
	return slices.Contains(m.WrongPackage, "all") || slices.Contains(m.WrongPackage, base)
}

func (s *Server) inArchive(a *archive, rest string, m Mode) (*object, string, error) {
	g := a.current()
	switch rest {
	case "InRelease", "Release", "Release.gpg":
		if rest == "InRelease" && m.NoInRelease {
			return nil, "", nil
		}
		docs, err := s.docsFor(a, m)
		if err != nil {
			return nil, "", err
		}
		switch rest {
		case "InRelease":
			return docs.inrelease, "text/plain", nil
		case "Release":
			return docs.release, "text/plain", nil
		}
		return docs.gpg, "application/pgp-signature", nil
	}
	if strings.Contains(rest, "by-hash/") {
		if m.NoByHash {
			return nil, "", nil
		}
		return a.byHash[rest], "application/octet-stream", nil
	}
	o := g.files[rest]
	if o == nil {
		o = g.aux[rest]
	}
	if o == nil {
		return nil, "", nil
	}
	if m.IndexMismatch && isIndex(rest) {
		o = mismatched(rest, o)
	}
	return o, contentType(rest), nil
}

// isIndex reports whether a path below a Release directory is a Packages or Sources file, plain or compressed.
func isIndex(rest string) bool {
	base := rest[strings.LastIndexByte(rest, '/')+1:]
	return (strings.HasPrefix(base, "Packages") || strings.HasPrefix(base, "Sources")) && !strings.Contains(rest, ".diff/")
}

// mismatched returns the index with a paragraph appended, so that it differs from what the Release lists.
func mismatched(rest string, o *object) *object {
	const bogus = "Package: trove-not-in-the-release\nVersion: 9-9\nArchitecture: all\nDescription: appended by the index_mismatch switch\n\n"
	data := o.data
	if strings.HasSuffix(rest, ".gz") {
		return newObject(debgen.Gzip(append(gunzip(data), bogus...)), o.modified)
	}
	return newObject(append(bytes.Clone(data), bogus...), o.modified)
}

func contentType(path string) string {
	switch {
	case strings.HasSuffix(path, ".deb"):
		return "application/vnd.debian.binary-package"
	case strings.HasSuffix(path, ".gz"):
		return "application/x-gzip"
	case strings.HasSuffix(path, ".dsc"), strings.HasSuffix(path, "Packages"), strings.HasSuffix(path, "Sources"), strings.HasSuffix(path, "Index"):
		return "text/plain"
	}
	return "application/octet-stream"
}

// docsFor signs the current release of an archive under the mode, once per distinct signing configuration and generation, so that
// repeated requests see equal bytes (and a conditional request can be answered 304). The caller holds s.mu.
func (s *Server) docsFor(a *archive, m Mode) (*signedDocs, error) {
	g := a.current()
	now := s.cfg.Now()
	key := m.signingKey(a.id, g.n)
	if m.ValidUntil == ValidUntilPassed || m.Date == DateAhead {
		key += "/" + now.Truncate(time.Minute).Format(time.RFC3339)
	}
	if d, ok := s.docs[key]; ok {
		return d, nil
	}
	hash, _, err := hashByName(m.SignHash)
	if err != nil {
		return nil, err
	}
	var signers []signer
	for _, name := range m.signKeys() {
		at := g.date.Add(time.Duration(m.SignatureSkewSeconds) * time.Second)
		if name == "d" {
			at = ExpiredSignatureTime
		}
		signers = append(signers, signer{key: s.keys[name], at: at, hash: hash})
	}
	text := a.releaseText(m, now)
	signedText := text
	if m.BrokenSignature {
		signedText = append(bytes.Clone(text), "tampered\n"...)
	}
	in, err := clearSign(signers, text, m.NoHashHeader)
	if err != nil {
		return nil, err
	}
	if m.BrokenSignature {
		in = bytes.Replace(in, []byte("Origin: trove-debstub"), []byte("Origin: trove-debstux"), 1)
	}
	gpg, err := detachSign(signers, signedText)
	if err != nil {
		return nil, err
	}
	d := &signedDocs{release: newObject(text, g.date), inrelease: newObject(in, g.date), gpg: newObject(gpg, g.date)}
	s.docs[key] = d
	return d, nil
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

func sortFiles(fs []File) {
	sort.Slice(fs, func(i, j int) bool { return fs[i].Path < fs[j].Path })
}
