package npmstub

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/npmstub/corpus"
)

// fixture is a running stub with a client that trusts its CA.
type fixture struct {
	t      *testing.T
	stub   *Server
	run    *Running
	client *http.Client
	ss     *SelfSigned
}

func newFixture(t *testing.T, cfg Config) *fixture {
	t.Helper()
	stub, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ss, err := NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	run, err := stub.Start("127.0.0.1:0", ss.ServerConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	tr := &http.Transport{TLSClientConfig: ss.ClientConfig(), DisableCompression: true}
	t.Cleanup(tr.CloseIdleConnections)
	return &fixture{t: t, stub: stub, run: run, client: &http.Client{Transport: tr, Timeout: 20 * time.Second}, ss: ss}
}

type reply struct {
	status int
	header http.Header
	body   []byte
}

func (f *fixture) do(method, path string, hdr map[string]string, body io.Reader) reply {
	f.t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), method, f.run.URL+path, body)
	if err != nil {
		f.t.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		f.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // test client
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	return reply{resp.StatusCode, resp.Header, data}
}

func (f *fixture) get(path string, hdr ...string) reply {
	f.t.Helper()
	h := map[string]string{}
	for i := 0; i+1 < len(hdr); i += 2 {
		h[hdr[i]] = hdr[i+1]
	}
	return f.do(http.MethodGet, path, h, nil)
}

func (f *fixture) status(path string, hdr ...string) int {
	f.t.Helper()
	return f.get(path, hdr...).status
}

// scopedName and plainName return corpus packages of each kind.
func names(t *testing.T) (scoped, plain string) {
	t.Helper()
	for _, p := range corpus.Default().Packages {
		switch {
		case strings.HasPrefix(p.Name, "@") && scoped == "":
			scoped = p.Name
		case !strings.HasPrefix(p.Name, "@") && plain == "" && len(p.Versions) > 2:
			plain = p.Name
		}
	}
	if scoped == "" || plain == "" {
		t.Fatal("the corpus has no scoped or no plain package")
	}
	return scoped, plain
}

func docPath(name string) string { return "/" + strings.Replace(name, "/", "%2f", 1) }

const abbreviated = corpus.AbbreviatedType + "; q=1.0, application/json; q=0.8, */*"

func TestServesFullAndAbbreviatedDocuments(t *testing.T) {
	// R-NPM-045: the test upstream serves the full and the install document.
	f := newFixture(t, Config{})
	_, name := names(t)
	pkg, _ := corpus.Default().Get(name)

	full := f.get(docPath(name))
	if full.status != 200 || !strings.HasPrefix(full.header.Get("Content-Type"), "application/json") {
		t.Fatalf("full: %d %s", full.status, full.header.Get("Content-Type"))
	}
	var fd map[string]json.RawMessage
	if err := json.Unmarshal(full.body, &fd); err != nil {
		t.Fatal(err)
	}
	if fd["time"] == nil || fd["readme"] == nil {
		t.Error("the full document lacks time or readme")
	}

	abbr := f.get(docPath(name), "Accept", abbreviated)
	if abbr.header.Get("Content-Type") != corpus.AbbreviatedType {
		t.Fatalf("abbreviated: content type %q", abbr.header.Get("Content-Type"))
	}
	var ad struct {
		Name     string
		Modified string
		DistTags map[string]string `json:"dist-tags"`
		Versions map[string]struct {
			Dist struct{ Tarball, Integrity string }
		}
	}
	if err := json.Unmarshal(abbr.body, &ad); err != nil {
		t.Fatal(err)
	}
	if ad.Name != name || ad.Modified == "" || ad.DistTags["latest"] != pkg.DistTags["latest"] || len(ad.Versions) != len(pkg.Versions) {
		t.Fatalf("abbreviated document %+v does not match the corpus", ad)
	}
	if len(abbr.body) >= len(full.body) {
		t.Errorf("abbreviated %d bytes, full %d", len(abbr.body), len(full.body))
	}
	want := f.run.URL + "/" + name + "/-/" + corpus.TarballFile(name, pkg.DistTags["latest"])
	if got := ad.Versions[pkg.DistTags["latest"]].Dist.Tarball; got != want {
		t.Errorf("tarball address %q, want %q", got, want)
	}
}

func TestPublicURLSetsTarballAddresses(t *testing.T) {
	f := newFixture(t, Config{PublicURL: "https://localhost:8080/api/npm/npm-all/"})
	_, name := names(t)
	var d struct {
		Versions map[string]struct{ Dist struct{ Tarball string } }
	}
	if err := json.Unmarshal(f.get(docPath(name), "Accept", abbreviated).body, &d); err != nil {
		t.Fatal(err)
	}
	for v, e := range d.Versions {
		if want := "https://localhost:8080/api/npm/npm-all/" + name + "/-/" + corpus.TarballFile(name, v); e.Dist.Tarball != want {
			t.Fatalf("%s: %s", v, e.Dist.Tarball)
		}
	}
}

func TestTarballMatchesDocumentIntegrity(t *testing.T) {
	f := newFixture(t, Config{})
	scoped, plain := names(t)
	for _, name := range []string{scoped, plain} {
		pkg, _ := corpus.Default().Get(name)
		v := pkg.Versions[len(pkg.Versions)-1]
		path := "/" + name + "/-/" + corpus.TarballFile(name, v.Version)
		r := f.get(path)
		if r.status != 200 {
			t.Fatalf("%s: %d", path, r.status)
		}
		sum := sha512.Sum512(r.body)
		if got := "sha512-" + base64.StdEncoding.EncodeToString(sum[:]); got != v.Dist.Integrity {
			t.Fatalf("%s: integrity %s, document %s", path, got, v.Dist.Integrity)
		}
	}
	if s := f.status("/" + plain + "/-/" + plain + "-0.0.0-nope.tgz"); s != 404 {
		t.Errorf("unknown version: %d", s)
	}
	if s := f.status("/" + plain + "/-/other-1.0.0.tgz"); s != 404 {
		t.Errorf("foreign file name: %d", s)
	}
}

func TestScopedNamesWithEscapedSlash(t *testing.T) {
	// R-NPM-045 (scoped packages): npm writes "@scope%2fname", other clients "@scope/name".
	f := newFixture(t, Config{})
	scoped, _ := names(t)
	escaped := f.get("/" + strings.Replace(scoped, "/", "%2f", 1))
	upper := f.get("/" + strings.Replace(scoped, "/", "%2F", 1))
	slash := f.get("/" + scoped)
	for _, r := range []reply{escaped, upper, slash} {
		if r.status != 200 || !bytes.Equal(r.body, escaped.body) {
			t.Fatalf("the spellings of %s give different answers: %d", scoped, r.status)
		}
	}
	c := f.stub.Counts()
	if c.Documents(scoped) != 3 {
		t.Fatalf("documents counted %d, want 3", c.Documents(scoped))
	}
	if c.Requests(http.MethodGet, "/"+strings.Replace(scoped, "/", "%2f", 1)) != 1 || c.Requests(http.MethodGet, "/"+scoped) != 1 {
		t.Fatalf("the path is counted as the client wrote it: %+v", c.Entries)
	}
}

func TestConditionalRequestsAnswer304(t *testing.T) {
	// R-NPM-045: conditional requests are answered 304 and counted as conditional.
	f := newFixture(t, Config{})
	_, name := names(t)
	first := f.get(docPath(name), "Accept", abbreviated)
	etag, lastModified := first.header.Get("ETag"), first.header.Get("Last-Modified")
	if etag == "" || lastModified == "" {
		t.Fatalf("no validators: %q %q", etag, lastModified)
	}
	if r := f.get(docPath(name), "Accept", abbreviated, "If-None-Match", etag); r.status != 304 || len(r.body) != 0 {
		t.Fatalf("If-None-Match: %d with %d bytes", r.status, len(r.body))
	}
	if s := f.status(docPath(name), "Accept", abbreviated, "If-Modified-Since", lastModified); s != 304 {
		t.Errorf("If-Modified-Since: %d", s)
	}
	if s := f.status(docPath(name), "Accept", abbreviated, "If-None-Match", `W/"other"`); s != 200 {
		t.Errorf("a stale validator: %d", s)
	}
	if s := f.status(docPath(name), "Accept", abbreviated, "If-None-Match", `W/"other"`, "If-Modified-Since", lastModified); s != 200 {
		t.Errorf("If-None-Match decides over If-Modified-Since: %d", s)
	}
	if full := f.get(docPath(name)); full.header.Get("ETag") == etag {
		t.Error("the full and the abbreviated document share a validator")
	}
	c := f.stub.Counts()
	if c.Documents(name) != 6 || c.ConditionalDocuments(name) != 4 {
		t.Fatalf("counted %d documents, %d conditional", c.Documents(name), c.ConditionalDocuments(name))
	}
	tgz := "/" + name + "/-/" + corpus.TarballFile(name, mustVersion(name))
	if s := f.status(tgz, "If-None-Match", f.get(tgz).header.Get("ETag")); s != 304 {
		t.Errorf("tarball If-None-Match: %d", s)
	}
}

func mustVersion(name string) string {
	p, _ := corpus.Default().Get(name)
	return p.Versions[0].Version
}

func TestGzipOnRequest(t *testing.T) {
	f := newFixture(t, Config{})
	_, name := names(t)
	plain := f.get(docPath(name))
	zipped := f.get(docPath(name), "Accept-Encoding", "gzip")
	if zipped.header.Get("Content-Encoding") != "gzip" {
		t.Fatal("no gzip answer to Accept-Encoding: gzip")
	}
	zr, err := gzip.NewReader(bytes.NewReader(zipped.body))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(zr)
	if err != nil || !bytes.Equal(got, plain.body) {
		t.Fatalf("the gzip body differs from the plain one: %v", err)
	}
	if zipped.header.Get("ETag") != plain.header.Get("ETag") {
		t.Error("the encodings carry different validators")
	}
}

func TestCountsPerPathAndReset(t *testing.T) {
	// E2.2: the stub tells exactly one request per tarball and at most one per document.
	f := newFixture(t, Config{})
	scoped, plain := names(t)
	pkg, _ := corpus.Default().Get(plain)
	tgz := "/" + plain + "/-/" + corpus.TarballFile(plain, pkg.Versions[0].Version)

	f.get(docPath(plain))
	f.get(docPath(scoped))
	f.get(tgz)
	if v := f.stub.Counts().Violations(1, 1); len(v) != 0 {
		t.Fatalf("violations with one request each: %v", v)
	}
	f.get(tgz)
	f.get(docPath(plain))
	c := f.stub.Counts()
	if c.Tarball(plain, corpus.TarballFile(plain, pkg.Versions[0].Version)) != 2 || c.Tarballs(plain) != 2 || c.Documents(plain) != 2 || c.Documents(scoped) != 1 {
		t.Fatalf("counts %+v", c.Entries)
	}
	if v := c.Violations(1, 1); len(v) != 2 {
		t.Fatalf("want two violations, got %v", v)
	}
	if len(c.Violations(2, 2)) != 0 {
		t.Fatal("the limits are inclusive")
	}
	f.stub.Reset()
	if got := f.stub.Counts().Total(); got != 0 {
		t.Fatalf("%d requests after Reset", got)
	}
}

func TestPingUnknownPathsAndMethods(t *testing.T) {
	f := newFixture(t, Config{})
	if r := f.get("/-/ping"); r.status != 200 || string(r.body) != "{}" {
		t.Fatalf("ping: %d %s", r.status, r.body)
	}
	if s := f.status("/no-such-package-anywhere"); s != 404 {
		t.Errorf("unknown package: %d", s)
	}
	if s := f.status("/-/v1/search?text=x"); s != 404 {
		t.Errorf("search: %d", s)
	}
	_, name := names(t)
	if r := f.do(http.MethodPut, docPath(name), nil, strings.NewReader("{}")); r.status != 405 || r.header.Get("Allow") == "" {
		t.Errorf("PUT: %d", r.status)
	}
	if r := f.do(http.MethodHead, docPath(name), nil, nil); r.status != 200 || len(r.body) != 0 {
		t.Errorf("HEAD: %d with %d bytes", r.status, len(r.body))
	}
	if n := f.stub.Counts().Requests(http.MethodPut, docPath(name)); n != 1 {
		t.Errorf("the refused PUT is counted %d times", n)
	}
}

func TestOnlyTLS(t *testing.T) {
	// R-NPM-045, TLS everywhere: there is no plain-HTTP listener, and starting without a
	// certificate is refused.
	f := newFixture(t, Config{})
	plainURL := "http://" + f.run.Addr + "/-/ping"
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, plainURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := f.client.Do(req); err == nil {
		defer resp.Body.Close() //nolint:errcheck // test client
		if resp.StatusCode == 200 {
			t.Fatal("a plain HTTP request was served")
		}
	}
	if n := f.stub.Counts().Total(); n != 0 {
		t.Fatalf("the plain request reached the handler: %d counted", n)
	}

	stub, _ := New(Config{})
	if _, err := stub.Start("127.0.0.1:0", nil); err == nil {
		t.Fatal("Start accepted no TLS configuration")
	}
	if _, err := stub.Start("127.0.0.1:0", &tls.Config{}); err == nil {
		t.Fatal("Start accepted a TLS configuration without a certificate")
	}

	// A client that does not trust the CA cannot talk to it.
	untrusting := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: x509.NewCertPool(), MinVersion: tls.VersionTLS12}}}
	req, _ = http.NewRequestWithContext(context.Background(), http.MethodGet, f.run.URL+"/-/ping", nil)
	if resp, err := untrusting.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatal("an untrusting client got an answer")
	}
	if n := f.stub.Counts().Total(); n != 0 {
		t.Fatalf("a failed handshake reached the handler: %d counted", n)
	}
}

func TestDownModes(t *testing.T) {
	f := newFixture(t, Config{})
	_, name := names(t)
	if err := f.stub.SetMode(Mode{Down: DownUnavailable}); err != nil {
		t.Fatal(err)
	}
	if s := f.status(docPath(name)); s != 503 {
		t.Fatalf("503 mode: %d", s)
	}
	if err := f.stub.SetMode(Mode{Down: DownReset}); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, f.run.URL+docPath(name), nil)
	if resp, err := f.client.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatalf("reset mode answered %d", resp.StatusCode)
	}
	if err := f.stub.SetMode(Mode{Down: DownHang}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	req, _ = http.NewRequestWithContext(ctx, http.MethodGet, f.run.URL+docPath(name), nil)
	start := time.Now()
	if resp, err := f.client.Do(req); err == nil {
		_ = resp.Body.Close()
		t.Fatalf("hang mode answered %d", resp.StatusCode)
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Error("hang mode returned before the client's deadline")
	}
	// Every attempt reached the stub and was counted (the client library repeats a reset GET once, so at least three); the mode clears.
	if got := f.stub.Counts().Documents(name); got < 3 {
		t.Errorf("%d attempts counted, want at least 3", got)
	}
	if err := f.stub.SetMode(Mode{}); err != nil {
		t.Fatal(err)
	}
	if s := f.status(docPath(name)); s != 200 {
		t.Fatalf("after clearing: %d", s)
	}
	if err := f.stub.SetMode(Mode{Down: "sideways"}); err == nil {
		t.Fatal("an unknown down mode was accepted")
	}
}

func TestNotFoundGoneAndAuth(t *testing.T) {
	f := newFixture(t, Config{})
	scoped, plain := names(t)
	pkg, _ := corpus.Default().Get(plain)
	tgz := "/" + plain + "/-/" + corpus.TarballFile(plain, pkg.Versions[0].Version)

	if err := f.stub.SetMode(Mode{NotFound: []string{plain}, Gone: []string{scoped}}); err != nil {
		t.Fatal(err)
	}
	if f.status(docPath(plain)) != 404 || f.status(tgz) != 404 {
		t.Error("a NotFound name still answers")
	}
	if f.status(docPath(scoped)) != 410 {
		t.Error("a Gone name does not answer 410")
	}
	if err := f.stub.SetMode(Mode{RequireAuthorization: "Bearer s3cret"}); err != nil {
		t.Fatal(err)
	}
	if r := f.get(docPath(plain)); r.status != 401 || r.header.Get("WWW-Authenticate") == "" {
		t.Fatalf("without credentials: %d", r.status)
	}
	if f.status(docPath(plain), "Authorization", "Bearer wrong") != 401 {
		t.Error("a wrong token passes")
	}
	if f.status(docPath(plain), "Authorization", "Bearer s3cret") != 200 {
		t.Error("the right token fails")
	}
	c := f.stub.Counts()
	schemes := map[string]int{}
	for _, e := range c.Entries {
		schemes[e.Auth] += e.Count
	}
	if schemes["none"] < 1 || schemes["bearer"] != 2 {
		t.Errorf("credential schemes counted %v", schemes)
	}
}

func TestDelayByClass(t *testing.T) {
	f := newFixture(t, Config{})
	_, name := names(t)
	if err := f.stub.SetMode(Mode{DelayMS: map[string]int{"document": 200}}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	f.get(docPath(name))
	if time.Since(start) < 200*time.Millisecond {
		t.Error("the document answered before its delay")
	}
	start = time.Now()
	f.get("/-/ping")
	if time.Since(start) > 150*time.Millisecond {
		t.Error("the delay of documents also slowed ping")
	}
	if err := f.stub.SetMode(Mode{DelayMS: map[string]int{"bogus": 5}}); err == nil {
		t.Error("an unknown delay class was accepted")
	}
}

func TestPublishOffersAHigherVersion(t *testing.T) {
	// E2.5, R-NPM-033: the upstream offers a higher version of a claimed name.
	f := newFixture(t, Config{})
	_, name := names(t)
	before := f.get(docPath(name), "Accept", abbreviated)

	if err := f.stub.Publish(name, "99.0.0", map[string]string{"left": "^1.0.0"}); err != nil {
		t.Fatal(err)
	}
	if err := f.stub.Publish(name, "99.0.0", nil); err == nil {
		t.Fatal("a second publish of the version was accepted")
	}
	after := f.get(docPath(name), "Accept", abbreviated)
	if after.header.Get("ETag") == before.header.Get("ETag") {
		t.Fatal("the validator did not change")
	}
	if f.status(docPath(name), "Accept", abbreviated, "If-None-Match", before.header.Get("ETag")) != 200 {
		t.Fatal("the old validator still yields 304")
	}
	var d struct {
		DistTags map[string]string `json:"dist-tags"`
		Versions map[string]struct{ Dist struct{ Integrity string } }
	}
	if err := json.Unmarshal(after.body, &d); err != nil {
		t.Fatal(err)
	}
	if d.DistTags["latest"] != "99.0.0" {
		t.Fatalf("latest is %s", d.DistTags["latest"])
	}
	tb := f.get("/" + name + "/-/" + corpus.TarballFile(name, "99.0.0"))
	sum := sha512.Sum512(tb.body)
	if tb.status != 200 || "sha512-"+base64.StdEncoding.EncodeToString(sum[:]) != d.Versions["99.0.0"].Dist.Integrity {
		t.Fatalf("tarball of the published version: %d", tb.status)
	}

	// A name the corpus does not have: the dependency-confusion case.
	if f.status("/acme-internal-lib") != 404 {
		t.Fatal("the name exists before it is published")
	}
	if err := f.stub.Publish("@acme/internal-lib", "99.0.0", nil, "next"); err != nil {
		t.Fatal(err)
	}
	if s := f.status("/@acme%2finternal-lib"); s != 200 {
		t.Fatalf("published scoped name: %d", s)
	}
	f.stub.ResetAll()
	if s := f.status("/@acme%2finternal-lib"); s != 404 {
		t.Fatalf("after ResetAll: %d", s)
	}
}

func TestDropAndRestoreAVersion(t *testing.T) {
	// R-NPM-026: the upstream removes a version from its document.
	f := newFixture(t, Config{})
	_, name := names(t)
	pkg, _ := corpus.Default().Get(name)
	latest := pkg.DistTags["latest"]
	versionsOf := func() map[string]json.RawMessage {
		var d struct{ Versions map[string]json.RawMessage }
		if err := json.Unmarshal(f.get(docPath(name), "Accept", abbreviated).body, &d); err != nil {
			t.Fatal(err)
		}
		return d.Versions
	}
	first := f.get(docPath(name), "Accept", abbreviated)
	if err := f.stub.Drop(name, latest); err != nil {
		t.Fatal(err)
	}
	if _, ok := versionsOf()[latest]; ok {
		t.Fatal("the dropped version is still listed")
	}
	if f.status(docPath(name), "Accept", abbreviated, "If-None-Match", first.header.Get("ETag")) != 200 {
		t.Fatal("the validator of the document with the version still yields 304")
	}
	tgz := "/" + name + "/-/" + corpus.TarballFile(name, latest)
	if f.status(tgz) != 200 {
		t.Fatal("the tarball of a dropped version is gone by default")
	}
	if err := f.stub.SetMode(Mode{DropTarballs: true}); err != nil {
		t.Fatal(err)
	}
	if f.status(tgz) != 404 {
		t.Fatal("DropTarballs left the tarball")
	}
	if err := f.stub.Restore(name, latest); err != nil {
		t.Fatal(err)
	}
	if _, ok := versionsOf()[latest]; !ok {
		t.Fatal("the restored version is not listed")
	}
	if f.stub.Drop(name, "0.0.0-none") == nil || f.stub.Restore(name, latest) == nil {
		t.Fatal("Drop of a missing and Restore of a listed version must fail")
	}
}

func TestBadIntegrityAndWrongName(t *testing.T) {
	// R-NPM-025: an integrity the tarball does not have, and a document that names another package.
	f := newFixture(t, Config{})
	_, name := names(t)
	if err := f.stub.SetMode(Mode{BadIntegrity: []string{name}, WrongName: []string{name}}); err != nil {
		t.Fatal(err)
	}
	var d struct {
		Name     string
		Versions map[string]struct{ Dist struct{ Integrity string } }
	}
	r := f.get(docPath(name), "Accept", abbreviated)
	if err := json.Unmarshal(r.body, &d); err != nil {
		t.Fatal(err)
	}
	if d.Name == name {
		t.Error("the document still carries the requested name")
	}
	pkg, _ := corpus.Default().Get(name)
	for v, e := range d.Versions {
		if e.Dist.Integrity == pkg.Versions[pkg.Find(v)].Dist.Integrity {
			t.Fatalf("%s kept its real integrity", v)
		}
	}
	tb := f.get("/" + name + "/-/" + corpus.TarballFile(name, pkg.Versions[0].Version))
	sum := sha512.Sum512(tb.body)
	if "sha512-"+base64.StdEncoding.EncodeToString(sum[:]) != pkg.Versions[0].Dist.Integrity {
		t.Error("the tarball itself changed")
	}
}

func TestRecordedSet(t *testing.T) {
	// R-NPM-027, R-NPM-002: the recorded documents keep provenance fields and the legacy
	// capitalised name; their tarballs are the real ones.
	f := newFixture(t, Config{})
	r := f.get("/sigstore")
	if r.status != 200 {
		t.Fatalf("sigstore: %d", r.status)
	}
	for _, field := range []string{"trustedPublisher", "attestations", "signatures"} {
		if !bytes.Contains(r.body, []byte(field)) {
			t.Errorf("the recorded full document lost %s", field)
		}
	}
	abbr := f.get("/sigstore", "Accept", abbreviated)
	if !bytes.Contains(abbr.body, []byte("attestations")) || bytes.Contains(abbr.body, []byte("trustedPublisher")) {
		t.Error("the abbreviated recorded document must keep dist.attestations and drop _npmUser")
	}
	var d struct {
		DistTags map[string]string `json:"dist-tags"`
		Versions map[string]struct {
			Dist struct{ Tarball, Integrity string }
		}
	}
	if err := json.Unmarshal(abbr.body, &d); err != nil {
		t.Fatal(err)
	}
	for v, e := range d.Versions {
		if want := f.run.URL + "/sigstore/-/sigstore-" + v + ".tgz"; e.Dist.Tarball != want {
			t.Fatalf("tarball address %s, want %s", e.Dist.Tarball, want)
		}
		tb := f.get("/sigstore/-/sigstore-" + v + ".tgz")
		sum := sha512.Sum512(tb.body)
		if tb.status != 200 || "sha512-"+base64.StdEncoding.EncodeToString(sum[:]) != e.Dist.Integrity {
			t.Fatalf("recorded tarball of %s: %d", v, tb.status)
		}
	}
	if f.status("/JSONStream") != 200 || f.status("/jsonstream") != 404 {
		t.Error("the legacy capitalised name must be served exactly as written")
	}
	if err := f.stub.Publish("sigstore", "9.9.9", nil); err == nil {
		t.Error("a recorded package accepted a publish")
	}
	if f.status("/left-pad") != 200 || f.status("/lodash") != 200 || f.status("/react") != 200 {
		t.Error("a recorded package is missing")
	}
}

func TestControlAPI(t *testing.T) {
	const token = "control-secret"
	f := newFixture(t, Config{ControlToken: token})
	_, name := names(t)
	auth := map[string]string{"Authorization": "Bearer " + token}
	ctl := func(method, path, body string) reply {
		t.Helper()
		return f.do(method, path, auth, strings.NewReader(body))
	}

	if s := f.status("/-/stub/counts"); s != 401 {
		t.Fatalf("control without token: %d", s)
	}
	if s := f.status("/-/stub/counts", "Authorization", "Bearer nope"); s != 401 {
		t.Fatalf("control with a wrong token: %d", s)
	}

	f.get(docPath(name))
	var c Counts
	r := ctl(http.MethodGet, "/-/stub/counts", "")
	if err := json.Unmarshal(r.body, &c); err != nil || r.status != 200 || c.Documents(name) != 1 || c.Total() != 1 {
		t.Fatalf("counts: %d %s", r.status, r.body)
	}

	if r := ctl(http.MethodPost, "/-/stub/mode", `{"not_found":["`+name+`"],"delay_ms":{"tarball":5}}`); r.status != 200 {
		t.Fatalf("set mode: %d %s", r.status, r.body)
	}
	if f.status(docPath(name)) != 404 {
		t.Fatal("the mode set over the API is not in force")
	}
	if r := ctl(http.MethodPost, "/-/stub/mode", `{"down":"sideways"}`); r.status != 400 {
		t.Fatalf("invalid mode: %d", r.status)
	}
	if r := ctl(http.MethodPost, "/-/stub/mode", `{"surprise":1}`); r.status != 400 {
		t.Fatalf("unknown field: %d", r.status)
	}
	if r := ctl(http.MethodPost, "/-/stub/mode", `{}`); r.status != 200 {
		t.Fatalf("clear mode: %d", r.status)
	}

	if r := ctl(http.MethodPost, "/-/stub/publish", `{"name":"`+name+`","version":"98.0.0","tags":["canary"]}`); r.status != 204 {
		t.Fatalf("publish: %d %s", r.status, r.body)
	}
	if r := ctl(http.MethodPost, "/-/stub/publish", `{"name":"`+name+`","version":"98.0.0"}`); r.status != 409 {
		t.Fatalf("publish twice: %d", r.status)
	}
	if !bytes.Contains(f.get(docPath(name)).body, []byte(`"canary":"98.0.0"`)) {
		t.Fatal("the version published over the API is not offered")
	}
	if r := ctl(http.MethodPost, "/-/stub/drop", `{"name":"`+name+`","version":"98.0.0"}`); r.status != 204 {
		t.Fatalf("drop: %d", r.status)
	}
	if r := ctl(http.MethodPost, "/-/stub/restore", `{"name":"`+name+`","version":"98.0.0"}`); r.status != 204 {
		t.Fatalf("restore: %d", r.status)
	}
	if r := ctl(http.MethodPost, "/-/stub/drop", `{"name":"`+name+`"}`); r.status != 400 {
		t.Fatalf("drop without version: %d", r.status)
	}

	if r := ctl(http.MethodPost, "/-/stub/reset", ""); r.status != 204 || f.stub.Counts().Total() != 0 {
		t.Fatalf("reset: %d, %d counted", r.status, f.stub.Counts().Total())
	}
	if r := ctl(http.MethodPost, "/-/stub/reset?all=true", ""); r.status != 204 {
		t.Fatalf("reset all: %d", r.status)
	}
	if bytes.Contains(f.get(docPath(name)).body, []byte("98.0.0")) {
		t.Fatal("reset all kept the published version")
	}
	if got := f.stub.Counts(); got.Total() != 1 {
		t.Fatalf("control requests were counted: %s", got.Summary())
	}

	off := newFixture(t, Config{})
	if s := off.status("/-/stub/counts", "Authorization", "Bearer "+token); s != 404 {
		t.Fatalf("control API without a configured token: %d", s)
	}
}

func TestConcurrentRequestsAreCountedExactly(t *testing.T) {
	f := newFixture(t, Config{})
	_, name := names(t)
	const workers, each = 16, 25
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range each {
				f.get(docPath(name))
			}
		}()
	}
	// Changes while requests run: the race detector watches the state behind the lock.
	for i := range 5 {
		_ = f.stub.SetMode(Mode{})
		_ = f.stub.Publish(name, "97.0."+string(rune('0'+i)), nil)
	}
	wg.Wait()
	if got := f.stub.Counts().Documents(name); got != workers*each {
		t.Fatalf("counted %d, want %d", got, workers*each)
	}
}

func TestReloadingKeyPairServesRotatedCertificate(t *testing.T) {
	// The stub's certificate is read again from its files, so a rotation needs no restart.
	dir := t.TempDir()
	certFile, keyFile := filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	write := func(ss *SelfSigned) {
		t.Helper()
		keyDER, err := x509.MarshalPKCS8PrivateKey(ss.Cert.PrivateKey)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ss.Cert.Certificate[0]}), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	first, err := NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	write(first)
	get, err := ReloadingKeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	stub, _ := New(Config{})
	run, err := stub.Start("127.0.0.1:0", &tls.Config{GetCertificate: get, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatal(err)
	}
	defer run.Close() //nolint:errcheck // test cleanup

	served := func() []byte {
		t.Helper()
		conn, err := tls.Dial("tcp", run.Addr, &tls.Config{RootCAs: first.Pool, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) //nolint:gosec // the test reads the presented certificate
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close() //nolint:errcheck // test client
		return conn.ConnectionState().PeerCertificates[0].Raw
	}
	if !bytes.Equal(served(), first.Cert.Certificate[0]) {
		t.Fatal("the first certificate is not served")
	}
	second, err := NewSelfSigned("localhost", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	write(second)
	future := time.Now().Add(time.Minute)
	for _, f := range []string{certFile, keyFile} {
		if err := os.Chtimes(f, future, future); err != nil {
			t.Fatal(err)
		}
	}
	if !bytes.Equal(served(), second.Cert.Certificate[0]) {
		t.Fatal("the rotated certificate is not served")
	}
	if err := os.WriteFile(certFile, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	later := future.Add(time.Minute)
	_ = os.Chtimes(certFile, later, later)
	if !bytes.Equal(served(), second.Cert.Certificate[0]) {
		t.Fatal("a broken rotation must keep serving the last good certificate")
	}
}

func TestPathParsing(t *testing.T) {
	cases := []struct {
		path string
		want route
	}{
		{"/-/ping", route{kind: KindPing}},
		{"/left-pad", route{kind: KindDocument, name: "left-pad"}},
		{"/@a%2fb", route{kind: KindDocument, name: "@a/b"}},
		{"/@a/b", route{kind: KindDocument, name: "@a/b"}},
		{"/@a/b/-/b-1.0.0.tgz", route{kind: KindTarball, name: "@a/b", file: "b-1.0.0.tgz"}},
		{"/@a%2Fb/-/b-1.0.0.tgz", route{kind: KindTarball, name: "@a/b", file: "b-1.0.0.tgz"}},
		{"/b/-/b-1.0.0.tgz", route{kind: KindTarball, name: "b", file: "b-1.0.0.tgz"}},
		{"/b/-/..%2f..%2fx.tgz", route{kind: KindOther, name: "b"}},
		{"/@a", route{kind: KindOther}},
		{"/", route{kind: KindOther}},
		{"/-/v1/search", route{kind: KindOther}},
		{"/b/%zz", route{kind: KindOther}},
	}
	for _, c := range cases {
		if got := parseRoute(c.path); got != c.want {
			t.Errorf("parseRoute(%q) = %+v, want %+v", c.path, got, c.want)
		}
	}
	if u, err := url.PathUnescape("%2f"); err != nil || u != "/" {
		t.Fatal("test assumption broken")
	}
}
