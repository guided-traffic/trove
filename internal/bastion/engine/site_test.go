package engine

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/guided-traffic/trove/internal/bastion/iface"
	"github.com/guided-traffic/trove/internal/bastion/mainclient"
)

// readCounter reads the value of a counter or gauge.
func readCounter(c prometheus.Metric) float64 {
	var d dto.Metric
	if err := c.Write(&d); err != nil {
		return -1
	}
	if d.Counter != nil {
		return d.Counter.GetValue()
	}
	return d.Gauge.GetValue()
}

// reader is what the site knows about a credential: its token ID, its shape on every repository and the paths
// a partial reader may see.
type reader struct {
	id      string
	read    iface.Read
	allowed func(path string) bool
	expires *time.Time
	// sameDoc makes a partial reader's document byte-identical to the shared one (it may read everything the
	// document lists): the main installation marks it private and answers 304 to the shared validator.
	sameDoc bool
}

// site is a double of the main installation's read routes: artifacts by content address, documents shared for
// full readers and filtered for partial ones, absent and hidden names, and the check.
type site struct {
	mu        sync.Mutex
	repo      string
	readers   map[string]*reader // by token string; "" is the anonymous caller
	artifacts map[string][]byte  // path -> bytes
	docs      map[string]string  // path -> document
	revision  int
	// corrupt, when set, makes the artifact of that path answer other bytes than its ETag names.
	corrupt map[string]bool
	probe   func(f mainclient.Forward) (*mainclient.Response, error)
}

func newSite(repo string) *site {
	return &site{repo: repo, readers: map[string]*reader{}, artifacts: map[string][]byte{}, docs: map[string]string{}, corrupt: map[string]bool{}}
}

func (s *site) addReader(tok string, r *reader) { s.mu.Lock(); s.readers[tok] = r; s.mu.Unlock() }
func (s *site) bump()                           { s.mu.Lock(); s.revision++; s.mu.Unlock() }

func tokenOf(h http.Header) string {
	v := h.Get("Authorization")
	scheme, rest, _ := strings.Cut(v, " ")
	switch {
	case strings.EqualFold(scheme, "Bearer"):
		return rest
	case strings.EqualFold(scheme, "Basic"):
		dec, _ := base64.StdEncoding.DecodeString(rest)
		_, pw, _ := strings.Cut(string(dec), ":")
		return pw
	}
	return ""
}

func (s *site) subject(tok string) (iface.Subject, *reader, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.readers[tok]
	if !ok {
		if tok == "" {
			return iface.Subject{Kind: iface.SubjectAnonymousRefused}, nil, false
		}
		return iface.Subject{Kind: iface.SubjectInvalid}, nil, false
	}
	if tok == "" {
		return iface.Subject{Kind: iface.SubjectAnonymous}, r, true
	}
	return tokenSubject(r.id, r.expires), r, true
}

func (s *site) checkFn(req iface.CheckRequest) (iface.CheckAnswer, error) {
	tok := ""
	if req.Credential != nil {
		tok = *req.Credential
	}
	sub, r, ok := s.subject(tok)
	if !ok {
		return iface.CheckAnswer{Subject: sub, Repositories: map[string]iface.RepositoryRead{}}, nil
	}
	out := iface.CheckAnswer{Subject: sub, Repositories: map[string]iface.RepositoryRead{}}
	for _, repo := range req.Repositories {
		if repo != s.repo {
			out.Repositories[repo] = iface.RepositoryRead{Read: iface.ReadNone, Status: 404}
			continue
		}
		if r.read == iface.ReadNone {
			out.Repositories[repo] = iface.RepositoryRead{Read: iface.ReadNone, Status: 404}
			continue
		}
		out.Repositories[repo] = iface.RepositoryRead{Read: r.read}
	}
	return out, nil
}

func (s *site) repoOf(target string) string {
	p := strings.TrimPrefix(target, "/")
	parts := strings.Split(p, "/")
	if parts[0] == "files" && len(parts) > 1 {
		return parts[1]
	}
	if len(parts) > 2 {
		return parts[2]
	}
	return ""
}

func (s *site) doFn(f mainclient.Forward) (*mainclient.Response, error) {
	if f.Probe {
		if s.probe != nil {
			return s.probe(f)
		}
		return resp(http.StatusForbidden, marking(iface.ClassPass, "", s.repoOf(f.Request.URL.Path), iface.Subject{Kind: iface.SubjectAnonymous}), http.Header{"Content-Type": {"application/problem+json"}},
			`{"code":"bastion_read_only","detail":"read-only"}`), nil
	}
	path := f.Request.URL.Path
	tok := tokenOf(f.Request.Header)
	sub, r, ok := s.subject(tok)
	if !ok {
		return resp(http.StatusUnauthorized, marking(iface.ClassPass, "", "", iface.Subject{Kind: iface.SubjectInvalid}), http.Header{"Www-Authenticate": {`Basic realm="trove"`}}, "invalid"), nil
	}
	repo := s.repoOf(path)
	if repo != s.repo || r.read == iface.ReadNone {
		return resp(http.StatusNotFound, marking(iface.ClassPass, iface.ReadNone, repo, sub), nil, `{"code":"not_found"}`), nil
	}
	s.mu.Lock()
	body, isArtifact := s.artifacts[path]
	doc, isDoc := s.docs[path]
	rev := s.revision
	corrupt := s.corrupt[path]
	s.mu.Unlock()
	switch {
	case isArtifact:
		if r.allowed != nil && !r.allowed(path) {
			return resp(http.StatusNotFound, marking(iface.ClassPass, r.read, repo, sub), nil, `{"code":"not_found"}`), nil
		}
		etag := `"` + sha(body) + `"`
		hdr := http.Header{"Etag": {etag}, "Content-Type": {"application/octet-stream"}, "X-Trove-Replica": {"web-1"}, "Cache-Status": {"main;hit"}, "Set-Cookie": {"a=b"}}
		if f.IfNoneMatch == etag {
			return resp(http.StatusNotModified, marking(iface.ClassArtifact, r.read, repo, sub), hdr, ""), nil
		}
		out := body
		if corrupt {
			out = append([]byte(nil), body...)
			out[len(out)-1] ^= 0xff
		}
		return resp(http.StatusOK, marking(iface.ClassArtifact, r.read, repo, sub), hdr, string(out)), nil
	case isDoc:
		class, etag, content := iface.ClassShared, `"d`+itoa(rev)+`"`, doc
		if r.read != iface.ReadFull {
			class, etag, content = iface.ClassPrivate, `"p`+itoa(rev)+`-`+r.id+`"`, "filtered:"+doc
			if r.sameDoc {
				etag, content = `"d`+itoa(rev)+`"`, doc
			}
		}
		hdr := http.Header{"Etag": {etag}, "Content-Type": {"application/json"}, "Vary": {"Accept"}}
		if f.IfNoneMatch == etag {
			return resp(http.StatusNotModified, marking(class, r.read, repo, sub), hdr, ""), nil
		}
		return resp(http.StatusOK, marking(class, r.read, repo, sub), hdr, content), nil
	}
	if strings.Contains(path, "/-/v1/search") {
		return resp(http.StatusOK, marking(iface.ClassPass, r.read, repo, sub), http.Header{"Content-Type": {"application/json"}}, `{"objects":[]}`), nil
	}
	class := iface.ClassPass
	if r.read == iface.ReadFull {
		class = iface.ClassAbsent
	}
	return resp(http.StatusNotFound, marking(class, r.read, repo, sub), http.Header{"Content-Type": {"application/problem+json"}}, `{"code":"not_found"}`), nil
}

func itoa(n int) string { return strconv.Itoa(n) }

// install wires the site into a main double.
func (s *site) install(m *fakeMain) {
	m.doFn = s.doFn
	m.checkFn = s.checkFn
}

// newSiteHarness builds a harness in front of a site with the readers $FULL, $PART and $NONE.
type siteHarness struct {
	*harness
	site                  *site
	full, part, none, bad string
}

func newSiteHarness(t *testing.T, o options) *siteHarness {
	t.Helper()
	sh := &siteHarness{site: newSite("npm-all")}
	sh.full, sh.part, sh.none = newToken(t, "pat"), newToken(t, "pat"), newToken(t, "pat")
	sh.bad = newToken(t, "pat")
	sh.site.addReader(sh.full, &reader{id: "full", read: iface.ReadFull})
	sh.site.addReader(sh.part, &reader{id: "part", read: iface.ReadPartial, allowed: func(p string) bool { return strings.Contains(p, "/@acme/") }})
	sh.site.addReader(sh.none, &reader{id: "none", read: iface.ReadNone})
	sh.site.addReader("", &reader{id: "anon", read: iface.ReadFull})
	if o.main == nil {
		o.main = &fakeMain{}
	}
	sh.site.install(o.main)
	sh.harness = newHarness(t, o)
	return sh
}
