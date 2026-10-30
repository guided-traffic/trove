package npm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/npm/document"
	"github.com/guided-traffic/trove/internal/formats/npm/rows"
)

// The exposure adapter of npm (R-NPM-048, R-EP-018): the version syntax of the rules, the
// filter of local rows, the filter of merged and rewritten documents, and the handlers on an
// endpoint with exposure rules.

// exampleRule is the view of the example rule of R-EP-012 on an endpoint: @acme/sdk and
// @acme/sdk-* without @acme/sdk-internal, versions in ">=2.0.0" without 2.0.1, no
// pre-releases, the tags latest and lts. Ranges and pre-releases are decided with the
// format's adapter, as the decision point decides them.
type exampleRule struct{ x *exposure }

func (r exampleRule) Package(n string) bool {
	return n == "@acme/sdk" || strings.HasPrefix(n, "@acme/sdk-") && n != "@acme/sdk-internal"
}

func (r exampleRule) Version(n, v string) bool {
	if !r.Package(n) || v == "2.0.1" || !r.x.InRange(v, ">=2.0.0") {
		return false
	}
	pre, ok := r.x.Prerelease(v)
	return ok && !pre
}

func (r exampleRule) Tag(n, tag, v string) bool {
	return r.Package(n) && (tag == "latest" || tag == "lts") && r.Version(n, v)
}

func fixtureView() exampleRule { return exampleRule{x: newExposure()} }

func TestR_NPM_048_RangesOfRules(t *testing.T) {
	x := New().Exposure()
	t.Run("R-NPM-048 Q-NPM-16 ValidateRange accepts the strict syntax only", func(t *testing.T) {
		for _, ok := range []string{">=2.0.0 <4", "^1.2.3 || ~2.0", "2.x", ">=2.0.0"} {
			if err := x.ValidateRange(ok); err != nil {
				t.Errorf("%q refused: %v", ok, err)
			}
		}
		for _, bad := range []string{">=2.0.0 <", ">=2.0.0 <4..0", "not a range", "=  v1.2.3x", strings.Repeat("1", 4097)} {
			if err := x.ValidateRange(bad); err == nil {
				t.Errorf("%q accepted", bad[:min(len(bad), 20)])
			}
		}
	})
	t.Run("R-NPM-048 Q-NPM-16 a range is evaluated with includePrerelease; the rule's flag decides", func(t *testing.T) {
		if !x.InRange("2.1.0-beta", ">=2.0.0 <4") || x.InRange("4.0.0-rc.1", ">=2.0.0 <4") || x.InRange("1.9.0", ">=2.0.0 <4") {
			t.Fatal("includePrerelease evaluation")
		}
		if x.InRange("2.0.0", ">=2.0.0 <") {
			t.Fatal("a range that does not parse admits a version")
		}
		if pre, ok := x.Prerelease("2.1.0-rc.1"); !pre || !ok {
			t.Fatal("2.1.0-rc.1 is a pre-release")
		}
		if pre, ok := x.Prerelease("2.1.0"); pre || !ok {
			t.Fatal("2.1.0 is a release")
		}
		if _, ok := x.Prerelease("latest"); ok {
			t.Fatal("a tag is no version")
		}
	})
	t.Run("R-EP-018 the catalog takes the dimensions and the versioning from the adapter", func(t *testing.T) {
		e := formats.CatalogEntry(New())
		if !e.Exposure.Exposable || !e.Exposure.Packages || !e.Exposure.Versions || !e.Exposure.Tags || e.Exposure.Versioning == nil {
			t.Fatalf("exposure facts %+v", e.Exposure)
		}
		if err := formats.Validate(New()); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-EP-013 a range is parsed once and the memo stays bounded", func(t *testing.T) {
		m := newExposure()
		for i := range maxRanges + 10 {
			m.InRange("1.0.0", fmt.Sprintf(">=%d.0.0", i))
		}
		m.mu.RLock()
		n := len(m.ranges)
		m.mu.RUnlock()
		if n == 0 || n > maxRanges {
			t.Fatalf("%d ranges held", n)
		}
		first := m.rangeOf(">=2.0.0")
		if first == nil || m.rangeOf(">=2.0.0") != first {
			t.Fatal("the range is parsed again")
		}
	})
}

// fixtureRows are the rows of @acme/sdk in the fixture of R-EP-012 with latest on 2.1.0 and
// next on 2.1.0-rc.1, published by two people.
func fixtureRows() rows.LocalPackage {
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	p := rows.LocalPackage{
		PackageID: 1, RevSeq: 9, CreatedAt: at, ModifiedAt: at.Add(time.Hour),
		Tags:   map[string]string{"latest": "2.1.0", "next": "2.1.0-rc.1", "lts": "2.0.0", "old": "1.9.0"},
		Readme: "# sdk 2.1.0", ReadmeFilename: "README.md",
	}
	for i, v := range []string{"1.9.0", "2.0.0", "2.0.1", "2.1.0-rc.1", "2.1.0"} {
		who := "alice"
		if v == "2.0.1" {
			who = "mallory"
		}
		m := fmt.Sprintf(`{"name":"@acme/sdk","version":%q,"description":"sdk %s","_npmUser":{"name":%q},"dist":{"integrity":"sha512-x"}}`, v, v, who)
		p.Versions = append(p.Versions, rows.LocalVersion{Version: v, Manifest: []byte(m), PublishedAt: at.Add(time.Duration(i) * time.Minute)})
	}
	return p
}

func renderOpts() document.Options {
	return document.Options{Name: "@acme/sdk", Basename: "sdk", TarballPrefix: "https://packages.example.com/api/npm/npm-customers/@acme/sdk/-/", Rev: "9-abc", AbbreviatedTime: true}
}

func decodeDoc(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return m
}

func keysOf(m map[string]any, member string) []string {
	var out []string
	sub, _ := m[member].(map[string]any)
	for k := range sub {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

func TestR_NPM_048_TheFilterOfALocalDocument(t *testing.T) {
	view := fixtureView()
	p := fixtureRows()
	if !exposeRows(view, "@acme/sdk", &p) {
		t.Fatal("nothing exposed")
	}
	full, err := document.FullDocument(&p, renderOpts())
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeDoc(t, full)
	t.Run("R-NPM-048 versions and time list the exposed versions; created and modified stay", func(t *testing.T) {
		if got := keysOf(doc, "versions"); !slices.Equal(got, []string{"2.0.0", "2.1.0"}) {
			t.Fatalf("versions %v", got)
		}
		if got := keysOf(doc, "time"); !slices.Equal(got, []string{"2.0.0", "2.1.0", "created", "modified"}) {
			t.Fatalf("time %v", got)
		}
		tm, _ := doc["time"].(map[string]any)
		if tm["modified"] != document.Timestamp(fixtureRows().ModifiedAt) {
			t.Fatalf("modified %v", tm["modified"])
		}
	})
	t.Run("R-NPM-048 the tags: latest kept, next dropped (pre-release), old dropped (name and version), lts kept", func(t *testing.T) {
		tags, _ := doc["dist-tags"].(map[string]any)
		if !reflect.DeepEqual(tags, map[string]any{"latest": "2.1.0", "lts": "2.0.0"}) {
			t.Fatalf("dist-tags %v", tags)
		}
	})
	t.Run("R-NPM-048 maintainers are the publishers of exposed versions", func(t *testing.T) {
		if ms := fmt.Sprint(doc["maintainers"]); ms != "[map[name:alice]]" {
			t.Fatalf("maintainers %s", ms)
		}
	})
	t.Run("R-NPM-048 latest on a hidden version moves to the highest exposed release, and the README of the former latest goes", func(t *testing.T) {
		p := fixtureRows()
		p.Tags["latest"] = "1.9.0"
		p.Readme = "# sdk 1.9.0 internal notes"
		if !exposeRows(view, "@acme/sdk", &p) || p.Tags["latest"] != "2.1.0" || p.Readme != "" || p.ReadmeFilename != "" {
			t.Fatalf("tags %v readme %q", p.Tags, p.Readme)
		}
		full, _ := document.FullDocument(&p, renderOpts())
		if d := decodeDoc(t, full); d["description"] != "sdk 2.1.0" || d["readme"] != nil || strings.Contains(string(full), "internal notes") {
			t.Fatalf("top level %v %v", d["description"], d["readme"])
		}
	})
	t.Run("R-NPM-048 with only pre-releases exposed, latest is the highest of them", func(t *testing.T) {
		pre := prereleaseView{exampleRule: view}
		p := fixtureRows()
		p.Tags = map[string]string{"latest": "1.9.0"}
		if !exposeRows(pre, "@acme/sdk", &p) || p.Tags["latest"] != "2.1.0-rc.1" {
			t.Fatalf("tags %v", p.Tags)
		}
	})
	t.Run("R-NPM-048 a package without an exposed version, and a package no rule admits, are missing", func(t *testing.T) {
		p := fixtureRows()
		p.Versions = p.Versions[:1] // 1.9.0 only
		if exposeRows(view, "@acme/sdk", &p) {
			t.Fatal("a package without an exposed version is served")
		}
		p = fixtureRows()
		if exposeRows(view, "@acme/billing", &p) {
			t.Fatal("a package without a rule is served")
		}
	})
	t.Run("R-NPM-048 the abbreviated document lists the same versions", func(t *testing.T) {
		abbr, _ := document.AbbreviatedDocument(&p, renderOpts())
		if got := keysOf(decodeDoc(t, abbr), "versions"); !slices.Equal(got, []string{"2.0.0", "2.1.0"}) {
			t.Fatalf("versions %v", got)
		}
	})
	t.Run("R-NPM-048 the filter of the bytes keeps what the filter of the rows keeps", func(t *testing.T) {
		x := newExposure()
		for _, latest := range []string{"2.1.0", "1.9.0"} {
			all := fixtureRows()
			all.Tags["latest"] = latest
			filtered := all
			filtered.Versions = slices.Clone(all.Versions)
			if !exposeRows(view, "@acme/sdk", &filtered) {
				t.Fatal("nothing exposed")
			}
			for _, render := range []func(*rows.LocalPackage, document.Options) ([]byte, error){document.FullDocument, document.AbbreviatedDocument} {
				unfiltered, _ := render(&all, renderOpts())
				want, _ := render(&filtered, renderOpts())
				got, err := x.FilterDocument(context.Background(), formats.Path{Path: "@acme/sdk"}, unfiltered, view)
				if err != nil {
					t.Fatal(err)
				}
				if g, w := decodeDoc(t, got), decodeDoc(t, want); !reflect.DeepEqual(g, w) {
					t.Fatalf("latest %s:\nbytes %s\nrows  %s", latest, got, want)
				}
			}
		}
	})
}

// prereleaseView is the example rule with prereleases: true.
type prereleaseView struct{ exampleRule }

func (r prereleaseView) Version(n, v string) bool {
	return r.Package(n) && v != "2.0.1" && r.x.InRange(v, ">=2.0.0") && v != "2.0.0" && v != "2.1.0"
}

func TestR_NPM_048_TheFilterOfAMergedOrRewrittenDocument(t *testing.T) {
	x, view := newExposure(), fixtureView()
	filter := func(path, doc string) ([]byte, error) {
		return x.FilterDocument(context.Background(), formats.Path{Path: path}, []byte(doc), view)
	}
	upstream := `{
	  "_id": "@acme/sdk", "_rev": "12-abc", "name": "@acme/sdk",
	  "dist-tags": {"latest": "2.0.1", "beta": "2.1.0", "lts": "2.0.0", "broken": 7},
	  "versions": {
	    "1.9.0": {"version": "1.9.0", "_npmUser": {"name": "old", "email": "old@example.com"}},
	    "2.0.0": {"version": "2.0.0", "license": "MIT", "_npmUser": {"name": "alice", "email": "a@example.com"}},
	    "2.0.1": {"version": "2.0.1", "description": "hidden notes", "license": "UNLICENSED", "_npmUser": {"name": "mallory"}},
	    "2.1.0": {"version": "2.1.0", "description": "the sdk", "readme": "# sdk", "_npmUser": {"name": "bob"}}
	  },
	  "time": {"created": "2026-01-01T00:00:00.000Z", "modified": "2026-02-01T00:00:00.000Z", "1.9.0": "x", "2.0.0": "x", "2.0.1": "x", "2.1.0": "x", "3.0.0": "unpublished", "unpublished": {"versions": ["3.0.0"]}},
	  "maintainers": [{"name": "mallory", "email": "m@example.com"}],
	  "description": "hidden notes", "license": "UNLICENSED", "readme": "# hidden", "readmeFilename": "README.md",
	  "users": {"someone": true}
	}`
	out, err := filter("@acme/sdk", upstream)
	if err != nil {
		t.Fatal(err)
	}
	doc := decodeDoc(t, out)
	t.Run("R-NPM-048 versions, time and tags of an upstream document", func(t *testing.T) {
		if got := keysOf(doc, "versions"); !slices.Equal(got, []string{"2.0.0", "2.1.0"}) {
			t.Fatalf("versions %v", got)
		}
		if got := keysOf(doc, "time"); !slices.Equal(got, []string{"2.0.0", "2.1.0", "created", "modified"}) {
			t.Fatalf("time %v", got)
		}
		if tags, _ := doc["dist-tags"].(map[string]any); !reflect.DeepEqual(tags, map[string]any{"latest": "2.1.0", "lts": "2.0.0"}) {
			t.Fatalf("dist-tags %v", tags)
		}
	})
	t.Run("R-NPM-048 the top-level fields come from the re-pointed latest; nothing of a hidden version stays", func(t *testing.T) {
		if doc["description"] != "the sdk" || doc["readme"] != "# sdk" || doc["license"] != nil || doc["readmeFilename"] != nil {
			t.Fatalf("top level %v %v %v %v", doc["description"], doc["readme"], doc["license"], doc["readmeFilename"])
		}
		for _, leak := range []string{"hidden", "mallory", "UNLICENSED", "3.0.0", "old@example.com", "m@example.com"} {
			if strings.Contains(string(out), leak) {
				t.Fatalf("%q in the filtered document: %s", leak, out)
			}
		}
		if fmt.Sprint(doc["maintainers"]) != "[map[name:alice] map[name:bob]]" {
			t.Fatalf("maintainers %v", doc["maintainers"])
		}
		if doc["_rev"] != "12-abc" || doc["users"] == nil {
			t.Fatal("a member the filter does not know was changed")
		}
	})
	t.Run("R-NPM-048 a member that occurs twice is filtered in its last value, as a client reads it", func(t *testing.T) {
		dup := `{"_id":"@acme/sdk","name":"@acme/sdk","versions":{"2.1.0":{"version":"2.1.0"}},"dist-tags":{"latest":"2.1.0"},` +
			`"versions":{"2.1.0":{"version":"2.1.0"},"1.9.0":{"version":"1.9.0"},"1.9.0":{"version":"1.9.0","x":1}}}`
		out, err := filter("@acme/sdk", dup)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(out), "1.9.0") || strings.Count(string(out), `"versions"`) != 1 {
			t.Fatalf("filtered %s", out)
		}
	})
	t.Run("R-NPM-048 a document without an exposed version, and a package no rule admits, are the 404 of a missing package", func(t *testing.T) {
		for _, c := range []struct{ path, doc string }{
			{"@acme/sdk", `{"_id":"@acme/sdk","versions":{"1.9.0":{}},"dist-tags":{"latest":"1.9.0"}}`},
			{"@acme/billing", `{"_id":"@acme/billing","versions":{"3.0.0":{}},"dist-tags":{"latest":"3.0.0"}}`},
			{"@acme/sdk", `{"_id":"@acme/sdk"}`},
		} {
			_, err := filter(c.path, c.doc)
			if e, ok := errs.As(err); !ok || e.Kind != errs.NotFound || e.Detail != notFound().Detail {
				t.Errorf("%s: %v", c.path, err)
			}
		}
	})
	t.Run("R-NPM-048 a single-version document: hidden version and dropped tag are missing", func(t *testing.T) {
		cases := []struct {
			path, version string
			ok            bool
		}{
			{"@acme/sdk/2.1.0", "2.1.0", true},
			{"@acme/sdk/1.9.0", "1.9.0", false},
			{"@acme/sdk/lts", "2.0.0", true},
			{"@acme/sdk/next", "2.1.0", false},
			{"@acme/sdk/latest", "2.1.0", true},
			{"@acme/sdk/latest", "1.9.0", false},
		}
		for _, c := range cases {
			_, err := filter(c.path, fmt.Sprintf(`{"name":"@acme/sdk","version":%q}`, c.version))
			if (err == nil) != c.ok {
				t.Errorf("%s -> %s: %v", c.path, c.version, err)
			}
		}
	})
	t.Run("R-EP-018 a document the filter was not written for is refused, never served unfiltered", func(t *testing.T) {
		for _, path := range []string{"-/package/@acme/sdk/dist-tags", "@acme/sdk/-/sdk-2.1.0.tgz", "@acme", "", "-/all"} {
			if _, err := filter(path, `{"latest":"2.1.0"}`); err == nil {
				t.Errorf("%q filtered", path)
			}
		}
		if _, err := filter("@acme/sdk", `[1,2]`); err == nil {
			t.Error("an array filtered")
		}
		if _, err := filter("@acme/sdk", `{"versions":{}} {"versions":{"1.9.0":{}}}`); err == nil {
			t.Error("data after the document accepted")
		}
	})
	t.Run("R-EP-018 without a view the document is unchanged", func(t *testing.T) {
		got, err := x.FilterDocument(context.Background(), formats.Path{Path: "@acme/sdk"}, []byte(upstream), nil)
		if err != nil || string(got) != upstream {
			t.Fatal("changed without a view")
		}
	})
}

// onCustomers puts a request on an endpoint with exposure rules: the facts the dispatcher
// derives from the listener and the request's index.
func onCustomers(view formats.ExposureView, fingerprint string) func(*formats.Request) {
	return func(rq *formats.Request) {
		rq.Endpoint = formats.EndpointFacts{Name: "customers", Rules: true, ExposureFingerprint: fingerprint, Exposure: view}
	}
}

func onDefault(rq *formats.Request) {
	rq.Endpoint = formats.EndpointFacts{Name: "default", CacheFill: true}
}

func (rf *repoFixture) on(at func(*formats.Request), method, path, body string, hdr ...string) *formatsResponse {
	rf.t.Helper()
	rq, err := rf.h.NewRequest(context.Background(), method, path, strings.NewReader(body))
	if err != nil {
		rf.t.Fatalf("%s %s: %v", method, path, err)
	}
	rq.Subject = rf.opts.Subject
	at(rq)
	for i := 0; i+1 < len(hdr); i += 2 {
		rq.Header.Set(hdr[i], hdr[i+1])
	}
	r := rf.h.ServeRequest(rq, false)
	return &formatsResponse{Code: r.Code, Header: r.Header(), Body: r.Body.Bytes()}
}

type formatsResponse struct {
	Code   int
	Header http.Header
	Body   []byte
}

func TestR_NPM_048_HandlersOnAnEndpointWithRules(t *testing.T) {
	rf := newRepo(t)
	rf.publish("@acme/sdk", "1.9.0")
	rf.publish("@acme/sdk", "2.0.0")
	rf.publish("@acme/sdk", "2.0.1")
	rf.publish("@acme/sdk", "2.1.0", map[string]any{"description": "the sdk"})
	rf.publishTag("@acme/sdk", "2.1.0-rc.1", "next")
	rf.publish("@acme/billing", "3.0.0")
	cust := onCustomers(fixtureView(), "sha256:one")

	t.Run("R-NPM-048 the full and the abbreviated document list the exposed versions; default lists all", func(t *testing.T) {
		full := decodeDoc(t, rf.on(cust, http.MethodGet, "@acme%2fsdk", "").Body)
		abbr := decodeDoc(t, rf.on(cust, http.MethodGet, "@acme%2fsdk", "", "Accept", "application/vnd.npm.install-v1+json").Body)
		for _, d := range []map[string]any{full, abbr} {
			if got := keysOf(d, "versions"); !slices.Equal(got, []string{"2.0.0", "2.1.0"}) {
				t.Fatalf("versions %v", got)
			}
			if tags, _ := d["dist-tags"].(map[string]any); !reflect.DeepEqual(tags, map[string]any{"latest": "2.1.0"}) {
				t.Fatalf("dist-tags %v", tags)
			}
		}
		if got := keysOf(decodeDoc(t, rf.on(onDefault, http.MethodGet, "@acme%2fsdk", "").Body), "versions"); len(got) != 5 {
			t.Fatalf("default lists %v", got)
		}
	})
	t.Run("R-EP-018 the ETag differs per endpoint and per exposure fingerprint; a rule change fails an earlier validator", func(t *testing.T) {
		a := rf.on(cust, http.MethodGet, "@acme%2fsdk", "")
		d := rf.on(onDefault, http.MethodGet, "@acme%2fsdk", "")
		if a.Header.Get("ETag") == "" || a.Header.Get("ETag") == d.Header.Get("ETag") {
			t.Fatalf("ETag customers %q default %q", a.Header.Get("ETag"), d.Header.Get("ETag"))
		}
		if r := rf.on(cust, http.MethodGet, "@acme%2fsdk", "", "If-None-Match", a.Header.Get("ETag")); r.Code != http.StatusNotModified {
			t.Fatalf("the same rules: %d", r.Code)
		}
		narrowed := onCustomers(hide200{fixtureView()}, "sha256:two")
		r := rf.on(narrowed, http.MethodGet, "@acme%2fsdk", "", "If-None-Match", a.Header.Get("ETag"))
		if r.Code != http.StatusOK || !slices.Equal(keysOf(decodeDoc(t, r.Body), "versions"), []string{"2.1.0"}) {
			t.Fatalf("after the rule change: %d %s", r.Code, r.Body)
		}
	})
	t.Run("R-NPM-048 a hidden version answers like a missing one, a package without a rule like a missing package", func(t *testing.T) {
		missing := rf.on(cust, http.MethodGet, "@acme%2fsdk/9.9.9", "")
		for _, path := range []string{"@acme%2fsdk/1.9.0", "@acme%2fsdk/2.1.0-rc.1", "@acme%2fsdk/next", "@acme%2fsdk/-/sdk-1.9.0.tgz", "@acme%2fsdk/-/sdk-9.9.9.tgz", "@acme%2fbilling", "-/package/@acme%2fbilling/dist-tags"} {
			r := rf.on(cust, http.MethodGet, path, "")
			if r.Code != http.StatusNotFound || string(r.Body) != string(missing.Body) {
				t.Errorf("%s: %d %s, missing is %s", path, r.Code, r.Body, missing.Body)
			}
		}
		if r := rf.on(cust, http.MethodGet, "@acme%2fsdk/2.1.0", ""); r.Code != http.StatusOK {
			t.Fatalf("an exposed version: %d", r.Code)
		}
		if r := rf.on(cust, http.MethodGet, "@acme%2fsdk/-/sdk-2.0.0.tgz", ""); r.Code != http.StatusOK {
			t.Fatalf("an exposed tarball: %d %s", r.Code, r.Body)
		}
	})
	t.Run("R-NPM-048 dist-tags and the latest of the version route follow the filter after latest moved on default", func(t *testing.T) {
		if r := rf.on(onDefault, http.MethodPut, "-/package/@acme%2fsdk/dist-tags/latest", `"1.9.0"`); r.Code != http.StatusOK {
			t.Fatalf("dist-tag add: %d %s", r.Code, r.Body)
		}
		tags := decodeDoc(t, rf.on(cust, http.MethodGet, "-/package/@acme%2fsdk/dist-tags", "").Body)
		if !reflect.DeepEqual(tags, map[string]any{"latest": "2.1.0"}) {
			t.Fatalf("dist-tags on customers %v", tags)
		}
		if all := decodeDoc(t, rf.on(onDefault, http.MethodGet, "-/package/@acme%2fsdk/dist-tags", "").Body); all["latest"] != "1.9.0" {
			t.Fatalf("dist-tags on default %v", all)
		}
		v := decodeDoc(t, rf.on(cust, http.MethodGet, "@acme%2fsdk/latest", "").Body)
		if v["version"] != "2.1.0" {
			t.Fatalf("latest on customers is %v", v["version"])
		}
		doc := decodeDoc(t, rf.on(cust, http.MethodGet, "@acme%2fsdk", "").Body)
		if tags, _ := doc["dist-tags"].(map[string]any); tags["latest"] != "2.1.0" || doc["description"] != "the sdk" {
			t.Fatalf("document on customers: %v %v", tags, doc["description"])
		}
	})
	t.Run("R-NPM-048 R-EP-013 ?write=true answers 403 endpoint_read_only", func(t *testing.T) {
		withWrite := func(rq *formats.Request) { cust(rq); rq.URL.RawQuery = "write=true" }
		r := rf.on(withWrite, http.MethodGet, "@acme%2fsdk", "")
		if r.Code != http.StatusForbidden || !strings.Contains(string(r.Body), errs.CodeEndpointReadOnly) {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
		asked := rf.h.Authorizations()
		if last := asked[len(asked)-1]; !slices.Contains(last.Target.Actions, formats.ActionDeploy) {
			t.Fatalf("the read of a write was not decided as a write: %+v", last)
		}
	})
	t.Run("R-EP-012 an endpoint with rules but without a view exposes nothing", func(t *testing.T) {
		r := rf.on(onCustomers(nil, "sha256:none"), http.MethodGet, "@acme%2fsdk", "")
		if r.Code != http.StatusNotFound {
			t.Fatalf("%d %s", r.Code, r.Body)
		}
	})
}

// hide200 is the example rule narrowed to hide 2.0.0 as well.
type hide200 struct{ exampleRule }

func (r hide200) Version(n, v string) bool { return v != "2.0.0" && r.exampleRule.Version(n, v) }

func (r hide200) Tag(n, tag, v string) bool { return v != "2.0.0" && r.exampleRule.Tag(n, tag, v) }
