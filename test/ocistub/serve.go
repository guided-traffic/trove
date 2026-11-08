package ocistub

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// statusWriter remembers the status an answer was given.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.EscapedPath(), controlPrefix) {
		s.serveControl(w, r)
		return
	}
	rt := parseRoute(r.URL.Path)
	key, mode := s.record(r, rt)
	sw := &statusWriter{ResponseWriter: w}
	defer s.noteStatus(key, sw)
	defer s.fl.enter()()
	s.cfg.Logger.Info("request", "method", r.Method, "host", r.Host, "path", r.URL.EscapedPath(), "query", r.URL.RawQuery,
		"auth", authScheme(r.Header.Get("Authorization")))

	b := s.derive(r.Host)
	if !s.gate(sw, r, rt, mode, b) {
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		sw.Header().Set("Allow", "GET, HEAD")
		writeError(sw, http.StatusMethodNotAllowed, "UNSUPPORTED", "the test upstream serves reads only")
		return
	}
	s.dispatch(sw, r, rt, mode, b)
}

// record counts the request and returns its counter and the mode in force for it.
func (s *Server) record(r *http.Request, rt route) (countKey, Mode) {
	accept := parseAccept(r.Header.Values("Accept"))
	key := countKey{
		accept: strings.Join(accept, ","), method: r.Method, host: r.Host, path: r.URL.EscapedPath(), query: r.URL.RawQuery,
		auth: authScheme(r.Header.Get("Authorization")), conditional: r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "",
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.counts[key]
	if e == nil {
		e = &Entry{
			Method: key.method, Host: key.host, Path: key.path, Query: key.query, Conditional: key.conditional, Auth: key.auth, Accept: accept,
			Kind: rt.kind, Name: rt.name, Reference: rt.ref, Statuses: map[string]int{},
		}
		if rt.kind == KindCDN {
			e.Name = r.URL.Query().Get("repo")
		}
		s.counts[key] = e
	}
	e.Count++
	return key, s.mode
}

// noteStatus adds the status an answer ended with to its counter; an answer that was cut counts as 0.
func (s *Server) noteStatus(key countKey, w *statusWriter) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e := s.counts[key]; e != nil {
		e.Statuses[strconv.Itoa(w.status)]++
	}
}

// normHost lowercases a host and drops the default port.
func normHost(h string) string { return strings.TrimSuffix(strings.ToLower(h), ":443") }

func hostOf(base string) string {
	u, err := url.Parse(base)
	if err != nil {
		return ""
	}
	return normHost(u.Host)
}

// wrongHost reports whether a request reaches a resource on a host that does not serve it: the realm belongs to the realm host, the
// CDN paths to the CDN host and the API to the registry host.
func wrongHost(rt route, requestHost string, b bases) bool {
	want := b.registry
	switch rt.kind {
	case KindToken:
		want = b.realm
	case KindCDN:
		want = b.cdn
	case KindOther:
		return false
	}
	return normHost(requestHost) != hostOf(want)
}

// gate applies the switches in front of every route and reports whether the request goes on.
func (s *Server) gate(w http.ResponseWriter, r *http.Request, rt route, m Mode, b bases) bool {
	if wrongHost(rt, r.Host, b) {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "this resource is served on another host")
		return false
	}
	if s.throttleGate(w, r, rt.kind) {
		return false
	}
	switch m.Down {
	case DownUnavailable:
		writeError(w, http.StatusServiceUnavailable, "UNAVAILABLE", "the test upstream is down")
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
	if d := m.delay(rt.kind); d > 0 {
		select {
		case <-time.After(d):
		case <-r.Context().Done():
			return false
		}
	}
	if status := m.failStatus(rt.kind); status != 0 {
		writeError(w, status, "UNAVAILABLE", "the test upstream fails this request")
		return false
	}
	return true
}

func (s *Server) dispatch(w http.ResponseWriter, r *http.Request, rt route, m Mode, b bases) {
	switch rt.kind {
	case KindToken:
		s.serveToken(w, r, m)
		return
	case KindCDN:
		s.serveCDN(w, r, rt, m)
		return
	case KindOther:
		writeError(w, http.StatusNotFound, "NOT_FOUND", "not found")
		return
	}
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	if !s.authorize(w, r, rt, m, b) {
		return
	}
	if rt.bad {
		writeError(w, http.StatusBadRequest, "NAME_INVALID", "invalid repository name")
		return
	}
	if rt.name != "" && (slices.Contains(m.NotFound, rt.name) || !s.hasRepo(rt.name)) {
		writeError(w, http.StatusNotFound, "NAME_UNKNOWN", "repository name not known to registry")
		return
	}
	switch rt.kind {
	case KindBase:
		writeJSON(w, http.StatusOK, map[string]any{})
	case KindCatalog:
		s.serveCatalog(w, r, m)
	case KindTags:
		s.serveTags(w, r, rt)
	case KindManifest:
		s.serveManifest(w, r, rt, m)
	case KindBlob:
		s.serveBlob(w, r, rt, m, b)
	case KindReferrers:
		s.serveReferrers(w, r, rt, m)
	}
}

func (s *Server) hasRepo(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.c.repos[name] != nil
}

// page cuts the sorted items to the page a request asks for: n items after last, capped to the page size. It returns the Link
// header value of the next page, empty on the last one, or an error message for a bad n.
func (s *Server) page(items []string, q url.Values, path string) (out []string, link, errMsg string) {
	n := s.cfg.PageSize
	if v := q.Get("n"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed < 0 {
			return nil, "", "invalid n " + strconv.Quote(v)
		}
		n = min(parsed, n)
	}
	start := 0
	if last := q.Get("last"); last != "" {
		start = sort.SearchStrings(items, last)
		if start < len(items) && items[start] == last {
			start++
		}
	}
	rest := items[min(start, len(items)):]
	if n == 0 {
		return []string{}, "", ""
	}
	if len(rest) <= n {
		return append([]string{}, rest...), "", ""
	}
	out = append([]string{}, rest[:n]...)
	next := url.Values{"last": {out[len(out)-1]}, "n": {strconv.Itoa(n)}}
	return out, "<" + path + "?" + next.Encode() + `>; rel="next"`, ""
}

func (s *Server) serveCatalog(w http.ResponseWriter, r *http.Request, m Mode) {
	if !m.Catalog {
		writeError(w, http.StatusNotFound, "UNSUPPORTED", "the catalog is not offered")
		return
	}
	names := s.Repositories()
	page, link, bad := s.page(names, r.URL.Query(), "/v2/_catalog")
	if bad != "" {
		writeError(w, http.StatusBadRequest, "PAGINATION_NUMBER_INVALID", bad)
		return
	}
	if link != "" {
		w.Header().Set("Link", link)
	}
	writeJSON(w, http.StatusOK, map[string]any{"repositories": page})
}

func (s *Server) serveTags(w http.ResponseWriter, r *http.Request, rt route) {
	s.mu.Lock()
	tags := make([]string, 0, len(s.c.repos[rt.name].tags))
	for t := range s.c.repos[rt.name].tags {
		tags = append(tags, t)
	}
	s.mu.Unlock()
	sort.Strings(tags)
	page, link, bad := s.page(tags, r.URL.Query(), "/v2/"+rt.name+"/tags/list")
	if bad != "" {
		writeError(w, http.StatusBadRequest, "PAGINATION_NUMBER_INVALID", bad)
		return
	}
	if link != "" {
		w.Header().Set("Link", link)
	}
	writeJSON(w, http.StatusOK, map[string]any{"name": rt.name, "tags": page})
}

// lookup resolves a reference of a repository to a stored manifest.
func (s *Server) lookup(name, ref string) *object {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.c.repos[name]
	digest := ref
	if !digestRE.MatchString(ref) {
		digest = r.tags[ref]
	}
	if digest == "" || !r.members[digest] {
		return nil
	}
	return s.c.manifests[digest]
}

// parseAccept returns the media types of the Accept header lines of a request, sorted and without repeats. Like distribution it
// splits every line on commas, reads each entry with mime.ParseMediaType (parameters such as q are dropped, an entry that does not
// parse is skipped) and knows no wildcard.
func parseAccept(values []string) []string {
	var out []string
	for _, line := range values {
		for _, part := range strings.Split(line, ",") {
			t, _, err := mime.ParseMediaType(part)
			if err != nil {
				continue
			}
			out = append(out, t)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// negotiate applies the rules of distribution v3.1.1 (registry/handlers/manifests.go) to the manifest found for a request. It returns
// the manifest to serve, or the error to answer. An OCI manifest or index is served only to a client that lists its type, and a
// Docker manifest list requested by tag and not accepted is replaced by the linux/amd64 manifest, which needs the Docker schema 2
// type to be accepted. Nothing is rewritten for a request by digest, and there is no wildcard: a request with no Accept header
// gets neither OCI type.
func (s *Server) negotiate(o *object, byTag bool, accepted []string) (*object, int, string, string) {
	sup := func(t string) bool { return slices.Contains(accepted, t) }
	switch {
	case o.mediaType == MediaOCIManifest && !sup(MediaOCIManifest):
		return nil, http.StatusNotFound, "MANIFEST_UNKNOWN", "OCI manifest found, but accept header does not support OCI manifests"
	case o.mediaType == MediaOCIIndex && !sup(MediaOCIIndex):
		return nil, http.StatusNotFound, "MANIFEST_UNKNOWN", "OCI index found, but accept header does not support OCI indexes"
	case byTag && o.mediaType == MediaDockerList && !sup(MediaDockerList):
		child := s.pickPlatform(o)
		if child == nil {
			return nil, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown"
		}
		if child.mediaType == MediaDockerManifest && !sup(MediaDockerManifest) {
			return nil, http.StatusBadRequest, "MANIFEST_INVALID", "Schema 2 manifest not supported by client"
		}
		return child, 0, "", ""
	}
	return o, 0, "", ""
}

func (s *Server) serveManifest(w http.ResponseWriter, r *http.Request, rt route, m Mode) {
	o := s.lookup(rt.name, rt.ref)
	if o == nil {
		writeError(w, http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown")
		return
	}
	byTag := !digestRE.MatchString(rt.ref)
	if byTag && matches(m.Schema1, rt.name, rt.ref) {
		o = schema1(rt.name, rt.ref)
	} else {
		var status int
		var code, msg string
		if o, status, code, msg = s.negotiate(o, byTag, parseAccept(r.Header.Values("Accept"))); o == nil {
			writeError(w, status, code, msg)
			return
		}
	}
	if m.PullQuota != nil && !s.quota.take(*m.PullQuota, s.cfg.Now(), w.Header(), r.Method == http.MethodGet) {
		writeError(w, http.StatusTooManyRequests, "TOOMANYREQUESTS", "You have reached your pull rate limit.")
		return
	}
	data := o.data
	if matches(m.WrongDigest, rt.name, rt.ref) || matches(m.WrongDigest, rt.name, o.digest) {
		data = append(bytes.Clone(data), '\n') // other bytes under the digest header of the original
	}
	h := w.Header()
	h.Set("Content-Type", o.mediaType)
	h.Set("Docker-Content-Digest", o.digest)
	h.Set("Etag", `"`+o.digest+`"`)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// pickPlatform returns the linux/amd64 manifest of an index, or nil when there is none.
func (s *Server) pickPlatform(index *object) *object {
	var doc manifestDoc
	if err := json.Unmarshal(index.data, &doc); err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range doc.Manifests {
		if d.Platform != nil && d.Platform.OS == "linux" && d.Platform.Architecture == "amd64" {
			return s.c.manifests[d.Digest]
		}
	}
	return nil
}

// schema1 is a schema 1 manifest for a tag: the format a registry still serves for images from before schema 2.
func schema1(name, tag string) *object {
	layer := sha([]byte("schema1/" + name + "/" + tag))
	data, _ := json.MarshalIndent(map[string]any{
		"schemaVersion": 1, "name": name, "tag": tag, "architecture": "amd64",
		"fsLayers":   []map[string]string{{"blobSum": layer}},
		"history":    []map[string]string{{"v1Compatibility": `{"id":"` + strings.TrimPrefix(layer, "sha256:") + `"}`}},
		"signatures": []any{},
	}, "", "   ")
	return &object{mediaType: MediaDockerSchema1, data: data, digest: sha(data)}
}

// cdnSignature is the signature of a CDN URL: only the redirect of the registry knows how to make one.
func cdnSignature(name, digest string) string {
	mac := hmac.New(sha256.New, tokenKey)
	mac.Write([]byte("cdn\n" + name + "\n" + digest))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

func (s *Server) blob(name, digest string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.c.repos[name]
	data, ok := s.c.blobs[digest]
	return data, ok && r != nil && r.members[digest]
}

func (s *Server) serveBlob(w http.ResponseWriter, r *http.Request, rt route, m Mode, b bases) {
	data, ok := s.blob(rt.name, rt.ref)
	if !ok || !digestRE.MatchString(rt.ref) {
		writeError(w, http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown to registry")
		return
	}
	if !m.BlobsDirect && (r.Method == http.MethodGet || !m.HeadDirect) {
		q := url.Values{"repo": {rt.name}, "sig": {cdnSignature(rt.name, rt.ref)}}
		http.Redirect(w, r, b.cdn+cdnPrefix+rt.ref+"?"+q.Encode(), http.StatusTemporaryRedirect)
		return
	}
	s.writeBlob(w, r, rt.name, rt.ref, data, m)
}

func (s *Server) writeBlob(w http.ResponseWriter, r *http.Request, name, digest string, data []byte, m Mode) {
	if matches(m.WrongBlob, name, digest) {
		data = append(bytes.Clone(data), 0)
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Docker-Content-Digest", digest)
	h.Set("Etag", `"`+digest+`"`)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}

// serveCDN answers a signed blob URL on the CDN host.
func (s *Server) serveCDN(w http.ResponseWriter, r *http.Request, rt route, m Mode) {
	if !m.CDNAcceptAuth && r.Header.Get("Authorization") != "" {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<Error><Code>InvalidArgument</Code><Message>Only one auth mechanism allowed</Message></Error>`))
		return
	}
	name := r.URL.Query().Get("repo")
	if !hmac.Equal([]byte(r.URL.Query().Get("sig")), []byte(cdnSignature(name, rt.ref))) {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`<Error><Code>AccessDenied</Code><Message>Request has expired or the signature does not match</Message></Error>`))
		return
	}
	data, ok := s.blob(name, rt.ref)
	if !ok {
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`))
		return
	}
	s.writeBlob(w, r, name, rt.ref, data, m)
}

func (s *Server) serveReferrers(w http.ResponseWriter, r *http.Request, rt route, m Mode) {
	if !digestRE.MatchString(rt.ref) {
		writeError(w, http.StatusBadRequest, "DIGEST_INVALID", "invalid digest")
		return
	}
	if !m.Referrers {
		writeError(w, http.StatusNotFound, "UNSUPPORTED", "the referrers API is not offered")
		return
	}
	filter := r.URL.Query().Get("artifactType")
	s.mu.Lock()
	idx := s.c.referrersIndex(s.c.repos[rt.name], rt.ref, filter)
	s.mu.Unlock()
	if filter != "" {
		w.Header().Set("OCI-Filters-Applied", "artifactType")
	}
	w.Header().Set("Content-Type", MediaOCIIndex)
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(idx.data))
}

// writeError answers with the error document of the distribution specification.
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeErrorDetail(w, status, code, message, nil)
}

func writeErrorDetail(w http.ResponseWriter, status int, code, message string, detail any) {
	e := map[string]any{"code": code, "message": message}
	if detail != nil {
		e["detail"] = detail
	}
	writeJSON(w, status, map[string]any{"errors": []any{e}})
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
