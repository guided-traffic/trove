package npm

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1" //nolint:gosec // dist.shasum is a SHA-1
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/npm/npmtest"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
)

// The handlers of documents, tarballs, dist-tags, unpublish and the identity
// endpoints run here over the conformance kit's in-memory Host and the twin of the
// data port: the route table, the dispatcher's response wrapper and the storage hooks
// are the real ones; PostgreSQL is the integration tests' (test/integration/npm).

// countingStore counts the document reads, which is how a test tells a rendering from
// a validator answer and from a rendition cache hit.
type countingStore struct {
	*npmtest.Store
	docReads atomic.Int64
}

func (s *countingStore) LocalDocument(ctx context.Context, repo int64, name string) (rows.LocalPackage, bool, error) {
	s.docReads.Add(1)
	return s.Store.LocalDocument(ctx, repo, name)
}

// repoFixture is one local repository with the people of the tests.
type repoFixture struct {
	t    *testing.T
	f    *Format
	h    *formattest.Host
	st   *countingStore
	opts formattest.HostOptions
}

func alice() authz.Subject {
	return authz.Subject{Kind: "user", ID: "11111111-1111-1111-1111-111111111111", Name: "alice"}
}

func newRepo(t *testing.T, mods ...func(*formattest.HostOptions)) *repoFixture {
	t.Helper()
	st := &countingStore{Store: npmtest.New()}
	opts := formattest.HostOptions{Data: st, Resolver: noShadow{}, Subject: alice(), BaseURL: "https://trove.example.com"}
	for _, m := range mods {
		m(&opts)
	}
	rf := &repoFixture{t: t, f: New(), st: st, opts: opts}
	rf.h = formattest.NewHost(rf.f, opts)
	st.Revisions = func(name string) int64 { return int64(rf.h.RevSeq(name)) }
	return rf
}

// pubBody is the body of a publish of name@version: a tarball with a README, the
// manifest fields of extra, and the tag.
func pubBody(t testing.TB, name, version, tag string, extra map[string]any) []byte {
	t.Helper()
	packaged := map[string]any{"name": name, "version": version}
	for k, v := range extra {
		packaged[k] = v
	}
	pj, _ := json.Marshal(packaged)
	tg := tgz(t, map[string]string{"package/package.json": string(pj), "package/README.md": "# " + name + " " + version + "\n"})
	s512, s1 := sha512.Sum512(tg), sha1.Sum(tg) //nolint:gosec // see the import
	m := map[string]any{
		"name": name, "version": version,
		"dist": map[string]any{"integrity": "sha512-" + base64.StdEncoding.EncodeToString(s512[:]), "shasum": hex.EncodeToString(s1[:])},
	}
	for k, v := range extra {
		m[k] = v
	}
	file := fmt.Sprintf("%s-%s.tgz", name, version)
	doc := map[string]any{
		"_id": name, "name": name, "dist-tags": map[string]string{tag: version}, "versions": map[string]any{version: m},
		"_attachments": map[string]any{file: map[string]any{
			"content_type": "application/octet-stream", "data": base64.StdEncoding.EncodeToString(tg), "length": len(tg),
		}},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// publish publishes name@version under latest and requires 201.
func (rf *repoFixture) publish(name, version string, extra ...map[string]any) {
	rf.t.Helper()
	rf.publishTag(name, version, "latest", extra...)
}

func (rf *repoFixture) publishTag(name, version, tag string, extra ...map[string]any) {
	rf.t.Helper()
	var x map[string]any
	if len(extra) > 0 {
		x = extra[0]
	}
	if err := serveOK(rf.h, http.MethodPut, escape(name), pubBody(rf.t, name, version, tag, x), 201); err != nil {
		rf.t.Fatal(err)
	}
}

func escape(name string) string { return strings.ReplaceAll(name, "/", "%2f") }

// do sends a request as the subject of the fixture.
func (rf *repoFixture) do(method, path string, body string, hdr ...string) *formattest.Response {
	rf.t.Helper()
	return rf.doAs(rf.opts.Subject, method, path, body, hdr...)
}

func (rf *repoFixture) doAs(sub authz.Subject, method, path, body string, hdr ...string) *formattest.Response {
	rf.t.Helper()
	rq, err := rf.h.NewRequest(context.Background(), method, path, strings.NewReader(body))
	if err != nil {
		rf.t.Fatalf("%s %s: %v", method, path, err)
	}
	rq.Subject = sub
	for i := 0; i+1 < len(hdr); i += 2 {
		rq.Header.Set(hdr[i], hdr[i+1])
	}
	return rf.h.ServeRequest(rq, false)
}

func (rf *repoFixture) get(path string, hdr ...string) *formattest.Response {
	rf.t.Helper()
	return rf.do(http.MethodGet, path, "", hdr...)
}

// jsonOf decodes a response body.
func jsonOf(t *testing.T, r *formattest.Response) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &m); err != nil {
		t.Fatalf("%d %q: %v", r.Code, r.Body.String(), err)
	}
	return m
}

func (rf *repoFixture) rev(name string) string {
	rf.t.Helper()
	row, ok, err := rf.h.Packages().Package(context.Background(), rf.h.Ref(), name)
	if err != nil || !ok {
		rf.t.Fatalf("package %s: %v %v", name, ok, err)
	}
	return Rev(rf.h.Ref().ID, row.Ref.ID, row.Ref.RevSeq)
}

func versionsOf(m map[string]any) []string {
	var out []string
	vs, _ := m["versions"].(map[string]any)
	for v := range vs {
		out = append(out, v)
	}
	return out
}

var revPattern = regexp.MustCompile(`^[0-9]+-[0-9a-f]{32}$`)

// TestR_NPM_010_FullDocumentIsRenderedFromTheRows: a published version is in the full
// document at once, with its links on the response base URL, the times, the
// maintainers and the README of the version latest points to.
func TestR_NPM_010_FullDocumentIsRenderedFromTheRows(t *testing.T) {
	rf := newRepo(t)
	rf.publish("@acme/ui", "1.1.0", map[string]any{"description": "Acme UI kit", "license": "MIT", "dependencies": map[string]string{"react": "^19.0.0"}})
	rf.publish("@acme/ui", "1.2.0", map[string]any{"description": "Acme UI kit 1.2"})
	rf.publishTag("@acme/ui", "2.0.0-rc.1", "next")

	r := rf.get("@acme%2fui")
	if r.Code != 200 || r.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%d %s %s", r.Code, r.Header().Get("Content-Type"), r.Body.String())
	}
	doc := jsonOf(t, r)
	if doc["_id"] != "@acme/ui" || doc["name"] != "@acme/ui" || !revPattern.MatchString(fmt.Sprint(doc["_rev"])) {
		t.Fatalf("identity %v %v %v", doc["_id"], doc["name"], doc["_rev"])
	}
	if doc["_rev"] != rf.rev("@acme/ui") {
		t.Fatalf("_rev %v, rev of the package %s", doc["_rev"], rf.rev("@acme/ui"))
	}
	tags, _ := doc["dist-tags"].(map[string]any)
	if tags["latest"] != "1.2.0" || tags["next"] != "2.0.0-rc.1" {
		t.Fatalf("dist-tags %v", tags)
	}
	t.Run("every version, with the tarball on the response base URL and an unencoded scope separator", func(t *testing.T) {
		vs, _ := doc["versions"].(map[string]any)
		if len(vs) != 3 {
			t.Fatalf("versions %v", versionsOf(doc))
		}
		v, _ := vs["1.2.0"].(map[string]any)
		dist, _ := v["dist"].(map[string]any)
		if dist["tarball"] != "https://trove.example.com/api/npm/main/@acme/ui/-/ui-1.2.0.tgz" {
			t.Fatalf("tarball %v", dist["tarball"])
		}
		if dist["integrity"] == nil || dist["shasum"] == nil || v["_id"] != nil && v["_id"] != "@acme/ui@1.2.0" {
			t.Fatalf("version %v", v)
		}
	})
	t.Run("the top-level fields and the README are those of the version latest points to", func(t *testing.T) {
		if doc["description"] != "Acme UI kit 1.2" || doc["readme"] != "# @acme/ui 1.2.0\n" || doc["readmeFilename"] != "README.md" {
			t.Fatalf("description %v readme %q file %v", doc["description"], doc["readme"], doc["readmeFilename"])
		}
		if _, ok := doc["license"]; ok {
			t.Fatalf("license of an older version leaked into the top level: %v", doc["license"])
		}
		vs, _ := doc["versions"].(map[string]any)
		v, _ := vs["1.2.0"].(map[string]any)
		if _, ok := v["readme"]; ok {
			t.Fatal("a version of the full document carries a README")
		}
	})
	t.Run("R-NPM-012 time holds created, modified and every version, in ISO 8601 UTC with milliseconds", func(t *testing.T) {
		tm, _ := doc["time"].(map[string]any)
		iso := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)
		for _, k := range []string{"created", "modified", "1.1.0", "1.2.0", "2.0.0-rc.1"} {
			if s, _ := tm[k].(string); !iso.MatchString(s) {
				t.Fatalf("time[%s] = %v", k, tm[k])
			}
		}
		if lm := r.Header().Get("Last-Modified"); lm == "" {
			t.Fatal("no Last-Modified")
		}
	})
	t.Run("the maintainers are the distinct publishers, without e-mail", func(t *testing.T) {
		ms, _ := doc["maintainers"].([]any)
		if len(ms) != 1 || fmt.Sprint(ms[0]) != "map[name:alice]" {
			t.Fatalf("maintainers %v", ms)
		}
	})
	t.Run("an unknown package is 404 in the native error shape", func(t *testing.T) {
		r := rf.get("nope")
		if r.Code != 404 || errorBodyOf(t, r.ResponseRecorder).Code != "not_found" {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
	})
	t.Run("a package without a live version is 404", func(t *testing.T) {
		rf := newRepo(t)
		rf.publish("gone", "1.0.0")
		if r := rf.do(http.MethodDelete, "gone/-rev/"+rf.rev("gone"), ""); r.Code != 200 {
			t.Fatalf("unpublish %d %s", r.Code, r.Body.String())
		}
		if r := rf.get("gone"); r.Code != 404 {
			t.Fatalf("%d %s", r.Code, r.Body.String())
		}
	})
}

// TestR_NPM_011_AbbreviatedDocument: Accept decides the form by quality, label and body
// agree, and the abbreviated form holds what an installer reads and nothing else.
func TestR_NPM_011_AbbreviatedDocument(t *testing.T) {
	rf := newRepo(t)
	rf.publish("lodash", "4.17.21", map[string]any{
		"description": "Lodash", "dependencies": map[string]string{"a": "^1"}, "engines": map[string]string{"node": ">=4"},
		"scripts": map[string]string{"test": "x"}, "main": "index.js",
	})
	const npmAccept = "application/vnd.npm.install-v1+json; q=1.0, application/json; q=0.8, */*"
	r := rf.get("lodash", "Accept", npmAccept)
	if r.Code != 200 || r.Header().Get("Content-Type") != "application/vnd.npm.install-v1+json" {
		t.Fatalf("%d %s", r.Code, r.Header().Get("Content-Type"))
	}
	doc := jsonOf(t, r)
	for _, k := range []string{"_rev", "_id", "readme", "readmeFilename", "maintainers", "description"} {
		if _, ok := doc[k]; ok {
			t.Errorf("the abbreviated document holds %q", k)
		}
	}
	if doc["name"] != "lodash" || doc["modified"] == nil || doc["time"] == nil {
		t.Fatalf("top level %v", doc)
	}
	v, _ := doc["versions"].(map[string]any)["4.17.21"].(map[string]any)
	dist, _ := v["dist"].(map[string]any)
	if v["name"] != "lodash" || v["version"] != "4.17.21" || v["dependencies"] == nil || v["engines"] == nil || v["_hasShrinkwrap"] != false {
		t.Fatalf("version %v", v)
	}
	if v["scripts"] != nil || v["main"] != nil || v["description"] != nil {
		t.Fatalf("fields an installer does not read: %v", v)
	}
	if dist["tarball"] != "https://trove.example.com/api/npm/main/lodash/-/lodash-4.17.21.tgz" || dist["integrity"] == nil || dist["shasum"] == nil || dist["fileCount"] == nil {
		t.Fatalf("dist %v", dist)
	}
	t.Run("the other forms of the request get the full document", func(t *testing.T) {
		for _, accept := range []string{"", "application/json", "*/*", "application/vnd.npm.install-v1+json; q=0.5, application/json", "text/html"} {
			r := rf.get("lodash", "Accept", accept)
			if r.Header().Get("Content-Type") != "application/json" || jsonOf(t, r)["_rev"] == nil {
				t.Errorf("Accept %q: %s", accept, r.Header().Get("Content-Type"))
			}
		}
	})
	t.Run("only the abbreviated Accept names the abbreviated form", func(t *testing.T) {
		r := rf.get("lodash", "Accept", "application/vnd.npm.install-v1+json")
		if r.Header().Get("Content-Type") != "application/vnd.npm.install-v1+json" {
			t.Fatal(r.Header().Get("Content-Type"))
		}
	})
	t.Run("npm.abbreviated_time off leaves the time map out", func(t *testing.T) {
		rf := newRepo(t)
		rf.publish("lodash", "4.17.21")
		rep := defaultReplica()
		rep.cfg.AbbreviatedTime = false
		rf.f.replicas.Store(rf.h, rep)
		if doc := jsonOf(t, rf.get("lodash", "Accept", npmAccept)); doc["time"] != nil {
			t.Fatalf("time %v", doc["time"])
		}
	})
}

// TestR_NPM_009_ReadForWrite: GET ?write=true returns the full document with its _rev
// whatever Accept says, read inside the request and never cached.
func TestR_NPM_009_ReadForWrite(t *testing.T) {
	rf := newRepo(t)
	rf.publish("lodash", "4.17.21")
	rq, err := rf.h.NewRequest(context.Background(), http.MethodGet, "lodash", nil)
	if err != nil {
		t.Fatal(err)
	}
	rq.URL.RawQuery = "write=true"
	rq.Subject = alice()
	rq.Header.Set("Accept", "application/vnd.npm.install-v1+json")
	before := rf.st.docReads.Load()
	r := rf.h.ServeRequest(rq, false)
	if r.Code != 200 || r.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("%d %s %s", r.Code, r.Header().Get("Content-Type"), r.Body.String())
	}
	doc := jsonOf(t, r)
	if doc["_rev"] != rf.rev("lodash") || doc["_attachments"] != nil {
		t.Fatalf("_rev %v attachments %v", doc["_rev"], doc["_attachments"])
	}
	if cc := r.Header().Get("Cache-Control"); cc != "no-store" || r.Header().Get("ETag") != "" {
		t.Fatalf("Cache-Control %q ETag %q", cc, r.Header().Get("ETag"))
	}
	rf.h.ServeRequest(rq, false)
	if got := rf.st.docReads.Load() - before; got != 2 {
		t.Fatalf("%d reads of the rows for 2 requests: a write-read is never served from a cache", got)
	}
	t.Run("the _rev is rev_seq with a hash of the repository, the package and the counter", func(t *testing.T) {
		row, _, _ := rf.h.Packages().Package(context.Background(), rf.h.Ref(), "lodash")
		want := Rev(rf.h.Ref().ID, row.Ref.ID, row.Ref.RevSeq)
		if !revPattern.MatchString(want) || !strings.HasPrefix(want, fmt.Sprintf("%d-", row.Ref.RevSeq)) {
			t.Fatal(want)
		}
		if Rev(rf.h.Ref().ID, row.Ref.ID+1, row.Ref.RevSeq) == want || Rev(rf.h.Ref().ID+1, row.Ref.ID, row.Ref.RevSeq) == want {
			t.Fatal("a revision of one package matches the same number of another")
		}
	})
}

// gunzip reads a gzip body.
func gunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
