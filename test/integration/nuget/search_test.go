//go:build integration

package nuget

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/formats/nuget/search"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The search of a local repository (R-NUGET-010, R-AUTHZ-016) against PostgreSQL: the ranking,
// the paging, the fields, the visibility filters and the per-package read decision. The rows
// are inserted with the statement shape of NuGetInsertVersion, because the push path is not
// the subject here; when it exists a test of the whole road (push, then search) joins this one.

type seedVersion struct {
	v        string
	unlisted bool
	trashed  bool
}

type seedPackage struct {
	display, title, authors, tags, summary, description string
	types                                               []string
	versions                                            []seedVersion
}

func vs(list ...string) []seedVersion {
	var out []seedVersion
	for _, v := range list {
		out = append(out, seedVersion{v: v})
	}
	return out
}

// insert stores a package of a repository with its versions.
func insert(st *testenv.Stack, repo string, p seedPackage) {
	lower := strings.ToLower(p.display)
	st.Exec(`INSERT INTO packages (repo_id, name, display_name) SELECT id, $2, $3 FROM repositories WHERE key = $1`, repo, lower, p.display)
	types := p.types
	if len(types) == 0 {
		types = []string{"dependency"}
	}
	pt := make([]map[string]string, 0, len(types))
	for _, ty := range types {
		pt = append(pt, map[string]string{"name": ty, "version": ""})
	}
	md, err := json.Marshal(map[string]any{
		"title": p.title, "summary": p.summary, "description": p.description, "authors": p.authors,
		"tags": strings.Fields(p.tags), "packageTypes": pt, "iconUrl": "https://icons.test/" + lower + ".png",
	})
	if err != nil {
		panic(err)
	}
	for _, sv := range p.versions {
		v := version.MustParse(sv.v)
		st.Exec(`INSERT INTO package_versions (package_id, version, display_version, trashed_at)
		         SELECT p.id, $3, $4, CASE WHEN $5::boolean THEN now() END FROM packages p JOIN repositories r ON r.id = p.repo_id
		          WHERE r.key = $1 AND p.name = $2`, repo, lower, v.URLForm(), v.FullNormalized(), sv.trashed)
		st.Exec(`INSERT INTO nuget_version (version_id, repository_id, lower_id, declared_id, original_version, sort_key,
		                                    is_prerelease, is_semver2, listed, published_at, package_types, metadata, search_doc, origin)
		         SELECT pv.id, r.id, $2, $3, $4, $5, $6, $7, $8, now(), $9, $10::text::jsonb,
		                setweight(to_tsvector('simple', $11::text), 'A')
		                || setweight(to_tsvector('simple', $12::text || ' ' || $13::text), 'B')
		                || setweight(to_tsvector('simple', $14::text || ' ' || $15::text), 'C')
		                || setweight(to_tsvector('simple', $16::text), 'D'),
		                'push'
		           FROM package_versions pv JOIN packages p ON p.id = pv.package_id JOIN repositories r ON r.id = p.repo_id
		          WHERE r.key = $1 AND p.name = $2 AND pv.version = $17`,
			repo, lower, p.display, sv.v, v.SortKey(), v.IsPrerelease(), v.IsSemver2(), !sv.unlisted, lowerAll(types), string(md),
			search.IDTokens(p.display), p.title, p.tags, p.authors, p.summary, p.description, v.URLForm())
	}
}

func lowerAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(s)
	}
	return out
}

type searchEnv struct {
	t  *testing.T
	st *testenv.Stack
	r  *testenv.Replica

	tokAll, tokCore, tokNoWidgets, tokNobody string
}

// newSearchEnv seeds the people, their grants and the packages, then starts the replica.
//
//	nuget-search: the packages under test
//	nuget-other:  a package that must never show in nuget-search
//
// all reads everything; core reads Acme.Core alone (a package include pattern); noWidgets reads
// everything but Acme.Widgets (an exclude pattern); nobody has no grant.
func newSearchEnv(t *testing.T) *searchEnv {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "nugetsearch"})
	e := &searchEnv{t: t, st: st}
	all, core, noWidgets, nobody := st.User("all"), st.User("core"), st.User("nowidgets"), st.User("nobody")
	e.tokAll = st.Token(all, scope.Default())
	e.tokCore = st.Token(core, scope.Default())
	e.tokNoWidgets = st.Token(noWidgets, scope.Default())
	e.tokNobody = st.Token(nobody, scope.Default())
	st.Repository("nuget-search", "local", "nuget", "{}")
	st.Repository("nuget-other", "local", "nuget", "{}")
	st.Policy("search-all", []string{"read"}, []string{"nuget-search", "nuget-other"}, all)
	st.Policy("search-core", []string{"read"}, []string{"nuget-search"}, core)
	st.Exec(`INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) SELECT id, 'package', 'include', 'acme.core' FROM authz_policy WHERE name = 'search-core'`)
	st.Policy("search-nowidgets", []string{"read"}, []string{"nuget-search"}, noWidgets)
	st.Exec(`INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) SELECT id, 'package', 'exclude', 'acme.widgets' FROM authz_policy WHERE name = 'search-nowidgets'`)

	for _, p := range []seedPackage{
		{
			display: "Acme.Widgets", title: "Widget toolkit", authors: "Acme Platform Team", tags: "ui controls", summary: "Buttons for apps", description: "Buttons and sliders",
			versions: vs("1.0.0", "1.1.0", "1.2.0+build.7", "1.3.0-rc1", "2.0.0-beta.1"),
		},
		{
			display: "Acme.Core", title: "Core primitives", authors: "Acme Platform Team", tags: "core", description: "Primitives that widgets build on",
			versions: append(vs("2.0.0"), seedVersion{v: "3.0.0", trashed: true}),
		},
		{display: "Acme.Tool", title: "Command line tool", authors: "Tool Smith", tags: "cli", description: "A global tool", types: []string{"DotnetTool"}, versions: vs("1.0.0")},
		{display: "Acme.Hidden", title: "Unlisted", description: "All versions unlisted", versions: []seedVersion{{v: "1.0.0", unlisted: true}}},
		{display: "Acme.Preview", title: "Only a pre-release", versions: vs("1.0.0-beta")},
		{display: "Widgets", title: "Exact", description: "The ID is widgets", versions: vs("1.0.0")},
		{display: "Widgets.Extra", title: "Prefix", versions: vs("1.0.0")},
		{display: "Newtonsoft.Json", title: "JSON for .NET", tags: "json serialization", versions: vs("13.0.3")},
		{display: "NewtonsoftJsonSchema", title: "Schema", versions: vs("3.0.0")},
	} {
		insert(st, "nuget-search", p)
	}
	insert(st, "nuget-other", seedPackage{display: "Acme.Other", title: "Other repository", versions: vs("1.0.0")})
	e.r = st.Start(t, testenv.ReplicaOptions{Name: "web-search"})
	return e
}

type result struct {
	ID       string                  `json:"id"`
	Version  string                  `json:"version"`
	Reg      string                  `json:"registration"`
	Tags     []string                `json:"tags"`
	Authors  string                  `json:"authors"`
	Types    []struct{ Name string } `json:"packageTypes"`
	Versions []struct {
		ID      string `json:"@id"`
		Version string
	} `json:"versions"`
}

type found struct {
	TotalHits int      `json:"totalHits"`
	Data      []result `json:"data"`
}

func (f found) ids() []string {
	out := make([]string, 0, len(f.Data))
	for _, r := range f.Data {
		out = append(out, r.ID)
	}
	return out
}

func (r result) versions() []string {
	out := make([]string, 0, len(r.Versions))
	for _, v := range r.Versions {
		out = append(out, v.Version)
	}
	return out
}

func (e *searchEnv) search(token, query string) found {
	e.t.Helper()
	a := e.st.Do(e.r, "GET", "/api/nuget/it/nuget-search/v3/search?"+query, token, nil)
	if a.Status != 200 {
		e.t.Fatalf("search?%s: %d %s", query, a.Status, a.Body)
	}
	if ct := a.Header.Get("Content-Type"); ct != "application/json; charset=utf-8" {
		e.t.Fatalf("Content-Type %q", ct)
	}
	var f found
	if err := json.Unmarshal(a.Body, &f); err != nil {
		e.t.Fatalf("%v: %s", err, a.Body)
	}
	return f
}

func q(terms string) string { return "q=" + url.QueryEscape(terms) }

// TestR_NUGET_010_RankingFollowsExactPrefixTokenAndOtherFields: exact ID, ID prefix, an ID
// token or a substring of the ID, then the other fields; ties by lower_id.
func TestR_NUGET_010_RankingFollowsExactPrefixTokenAndOtherFields(t *testing.T) {
	e := newSearchEnv(t)
	got := e.search(e.tokAll, q("widgets"))
	want := []string{"Widgets", "Widgets.Extra", "Acme.Widgets", "Acme.Core"}
	if !slices.Equal(got.ids(), want) || got.TotalHits != 4 {
		t.Errorf("q=widgets: %v (totalHits %d), want %v", got.ids(), got.TotalHits, want)
	}
	// Acme.Core matches by its description alone; the title of Acme.Widgets does not matter
	// to its tier, the ID does.
	if got := e.search(e.tokAll, q("acme.w")); !slices.Equal(got.ids(), []string{"Acme.Widgets"}) {
		t.Errorf("q=acme.w: %v", got.ids())
	}
	if got := e.search(e.tokAll, q("Acme")); !slices.Equal(got.ids(), []string{"Acme.Core", "Acme.Tool", "Acme.Widgets"}) {
		t.Errorf("q=Acme (ID prefix, ties by lower_id): %v", got.ids())
	}
}

// TestR_NUGET_010_IDTokensAndSubstringsMatch: the words of an ID, a prefix of a word, the split
// at a case change, and a substring of the ID all find the package.
func TestR_NUGET_010_IDTokensAndSubstringsMatch(t *testing.T) {
	e := newSearchEnv(t)
	for _, c := range []struct {
		q    string
		want []string
	}{
		{"json", []string{"Newtonsoft.Json", "NewtonsoftJsonSchema"}},
		{"JSON", []string{"Newtonsoft.Json", "NewtonsoftJsonSchema"}},
		{"newt", []string{"Newtonsoft.Json", "NewtonsoftJsonSchema"}},
		{"newtonsoft.json", []string{"Newtonsoft.Json", "NewtonsoftJsonSchema"}},
		{"schema", []string{"NewtonsoftJsonSchema"}},
		{"tonsoft", []string{"Newtonsoft.Json", "NewtonsoftJsonSchema"}},
		{"newtonsoftjson", []string{"NewtonsoftJsonSchema"}},
		{"nomatch", nil},
		{"json schema", []string{"NewtonsoftJsonSchema"}},
	} {
		if got := e.search(e.tokAll, q(c.q)); !slices.Equal(got.ids(), c.want) || got.TotalHits != len(c.want) {
			t.Errorf("q=%s: %v (totalHits %d), want %v", c.q, got.ids(), got.TotalHits, c.want)
		}
	}
}

// TestR_NUGET_010_FieldTermsLimitTheMatch: id, packageid, title, tags, author, description,
// summary and version; owner and a prefix that is no field.
func TestR_NUGET_010_FieldTermsLimitTheMatch(t *testing.T) {
	e := newSearchEnv(t)
	for _, c := range []struct {
		q    string
		want []string
	}{
		{"packageid:acme.widgets", []string{"Acme.Widgets"}},
		{"packageId:ACME.WIDGETS", []string{"Acme.Widgets"}},
		{"packageid:acme", nil},
		{"id:widg", []string{"Acme.Widgets", "Widgets", "Widgets.Extra"}},
		{"id:primitives", nil},
		{"title:toolkit", []string{"Acme.Widgets"}},
		{"title:widget", []string{"Acme.Widgets"}},
		{"title:acme", nil},
		{"tags:controls", []string{"Acme.Widgets"}},
		{"tags:ui", []string{"Acme.Widgets"}},
		{"tags:json", []string{"Newtonsoft.Json"}},
		{"author:platform", []string{"Acme.Core", "Acme.Widgets"}},
		{"author:Smith", []string{"Acme.Tool"}},
		{"description:sliders", []string{"Acme.Widgets"}},
		{"description:widgets", []string{"Acme.Core", "Widgets"}},
		{"summary:buttons", []string{"Acme.Widgets"}},
		{"version:1.1.0", []string{"Acme.Widgets"}},
		{"version:1.1.0.0", []string{"Acme.Widgets"}},
		{"version:13.0.3", []string{"Newtonsoft.Json"}},
		{"version:9.9.9", nil},
		{"version:nonsense", nil},
		{"owner:acme", nil},
		{"foo:bar", nil},
		{`title:"widget toolkit"`, []string{"Acme.Widgets"}},
		{`title:"toolkit widget"`, nil},
		{`"widget toolkit"`, []string{"Acme.Widgets"}},
		{`"toolkit widget"`, nil},
		{`acme tags:core`, []string{"Acme.Core"}},
		{`acme author:smith tags:cli`, []string{"Acme.Tool"}},
	} {
		if got := e.search(e.tokAll, q(c.q)); !slices.Equal(got.ids(), c.want) || got.TotalHits != len(c.want) {
			t.Errorf("q=%s: %v (totalHits %d), want %v", c.q, got.ids(), got.TotalHits, c.want)
		}
	}
}

// TestR_NUGET_010_VisibilityOfVersions: prerelease and semVerLevel decide which versions the
// result lists and which one is the latest (R-NUGET-004); unlisted and trashed versions never
// appear and a package with no visible version is absent.
func TestR_NUGET_010_VisibilityOfVersions(t *testing.T) {
	e := newSearchEnv(t)
	widgets := func(query string) result {
		t.Helper()
		f := e.search(e.tokAll, q("packageid:acme.widgets")+"&"+query)
		if len(f.Data) != 1 {
			t.Fatalf("%s: %v", query, f.ids())
		}
		return f.Data[0]
	}
	for _, c := range []struct {
		query, latest, hive string
		versions            []string
	}{
		{"", "1.1.0", "registration", []string{"1.0.0", "1.1.0"}},
		{"prerelease=true", "1.3.0-rc1", "registration", []string{"1.0.0", "1.1.0", "1.3.0-rc1"}},
		{"prerelease=TRUE", "1.3.0-rc1", "registration", []string{"1.0.0", "1.1.0", "1.3.0-rc1"}},
		{"prerelease=false", "1.1.0", "registration", []string{"1.0.0", "1.1.0"}},
		{"semVerLevel=2.0.0", "1.2.0+build.7", "registration-semver2", []string{"1.0.0", "1.1.0", "1.2.0+build.7"}},
		{"semVerLevel=1.0.0", "1.1.0", "registration", []string{"1.0.0", "1.1.0"}},
		{"prerelease=true&semVerLevel=2.0.0", "2.0.0-beta.1", "registration-semver2", []string{"1.0.0", "1.1.0", "1.2.0+build.7", "1.3.0-rc1", "2.0.0-beta.1"}},
	} {
		r := widgets(c.query)
		if r.Version != c.latest || !slices.Equal(r.versions(), c.versions) {
			t.Errorf("%q: version %q, versions %v, want %q %v", c.query, r.Version, r.versions(), c.latest, c.versions)
		}
		if want := feedBase + "/api/nuget/it/nuget-search/v3/" + c.hive + "/acme.widgets/index.json"; r.Reg != want {
			t.Errorf("%q: registration %q, want %q", c.query, r.Reg, want)
		}
		last := r.Versions[len(r.Versions)-1].ID
		if !strings.Contains(last, "/v3/"+c.hive+"/acme.widgets/") || !strings.HasSuffix(last, ".json") {
			t.Errorf("%q: leaf %q", c.query, last)
		}
	}
	if got := e.search(e.tokAll, q("acme")); slices.Contains(got.ids(), "Acme.Hidden") || slices.Contains(got.ids(), "Acme.Preview") {
		t.Errorf("an unlisted package or one with a pre-release only: %v", got.ids())
	}
	if got := e.search(e.tokAll, q("acme")+"&prerelease=true"); !slices.Contains(got.ids(), "Acme.Preview") || slices.Contains(got.ids(), "Acme.Hidden") {
		t.Errorf("prerelease=true: %v", got.ids())
	}
	core := e.search(e.tokAll, q("packageid:acme.core")).Data[0]
	if core.Version != "2.0.0" || !slices.Equal(core.versions(), []string{"2.0.0"}) {
		t.Errorf("the trashed 3.0.0 shows: %s %v", core.Version, core.versions())
	}
}

// TestR_NUGET_010_PackageTypeAndMetadata: the filter keeps packages whose latest visible version
// declares the type, in any case; an invalid name returns nothing; the result carries the
// metadata of the latest version.
func TestR_NUGET_010_PackageTypeAndMetadata(t *testing.T) {
	e := newSearchEnv(t)
	for _, ty := range []string{"DotnetTool", "dotnettool", "DOTNETTOOL"} {
		got := e.search(e.tokAll, q("acme")+"&packageType="+ty)
		if !slices.Equal(got.ids(), []string{"Acme.Tool"}) || got.TotalHits != 1 {
			t.Errorf("packageType=%s: %v (%d)", ty, got.ids(), got.TotalHits)
		}
	}
	if got := e.search(e.tokAll, q("acme")+"&packageType=Dependency"); !slices.Equal(got.ids(), []string{"Acme.Core", "Acme.Widgets"}) {
		t.Errorf("packageType=Dependency: %v", got.ids())
	}
	for _, ty := range []string{"has%20space", "Template"} {
		if got := e.search(e.tokAll, q("acme")+"&packageType="+ty); got.TotalHits != 0 || len(got.Data) != 0 {
			t.Errorf("packageType=%s: %v", ty, got.ids())
		}
	}
	tool := e.search(e.tokAll, q("packageid:acme.tool")).Data[0]
	if len(tool.Types) != 1 || tool.Types[0].Name != "DotnetTool" || tool.Authors != "Tool Smith" || !slices.Equal(tool.Tags, []string{"cli"}) {
		t.Errorf("metadata: %+v", tool)
	}
}

// TestR_NUGET_010_PagingIsStableAndTotalHitsDoesNotDependOnIt: skip and take cut the order of
// the empty query (lower_id), the pages together are the whole, totalHits is the same on every
// page, take=0 returns only the count; the bounds answer 400.
func TestR_NUGET_010_PagingIsStableAndTotalHitsDoesNotDependOnIt(t *testing.T) {
	e := newSearchEnv(t)
	whole := e.search(e.tokAll, "")
	wantAll := []string{"Acme.Core", "Acme.Tool", "Acme.Widgets", "Newtonsoft.Json", "NewtonsoftJsonSchema", "Widgets", "Widgets.Extra"}
	if !slices.Equal(whole.ids(), wantAll) || whole.TotalHits != len(wantAll) {
		t.Fatalf("empty query: %v (%d), want %v", whole.ids(), whole.TotalHits, wantAll)
	}
	var paged []string
	for skip := 0; skip < 8; skip += 3 {
		p := e.search(e.tokAll, fmt.Sprintf("skip=%d&take=3", skip))
		if p.TotalHits != len(wantAll) {
			t.Errorf("skip=%d: totalHits %d", skip, p.TotalHits)
		}
		paged = append(paged, p.ids()...)
	}
	if !slices.Equal(paged, wantAll) {
		t.Errorf("pages together: %v", paged)
	}
	if got := e.search(e.tokAll, "take=1&skip=1"); !slices.Equal(got.ids(), []string{"Acme.Tool"}) || got.TotalHits != len(wantAll) {
		t.Errorf("take=1&skip=1: %v (%d)", got.ids(), got.TotalHits)
	}
	if got := e.search(e.tokAll, "take=0"); len(got.Data) != 0 || got.TotalHits != len(wantAll) {
		t.Errorf("take=0: %v (%d)", got.ids(), got.TotalHits)
	}
	if got := e.search(e.tokAll, "skip=3000"); len(got.Data) != 0 || got.TotalHits != len(wantAll) {
		t.Errorf("skip past the end: %v (%d)", got.ids(), got.TotalHits)
	}
	if got := e.search(e.tokAll, "take=5000"); len(got.Data) != len(wantAll) {
		t.Errorf("take above max_take is clamped, not refused: %v", got.ids())
	}
	for _, bad := range []string{"skip=3001", "skip=-1", "take=-1", "take=x", "skip=1.5"} {
		a := e.st.Do(e.r, "GET", "/api/nuget/it/nuget-search/v3/search?"+bad, e.tokAll, nil)
		if a.Status != 400 || !strings.HasPrefix(string(a.Body), "malformed_request: ") {
			t.Errorf("%s: %d %s", bad, a.Status, a.Body)
		}
	}
	if a := e.st.Do(e.r, "HEAD", "/api/nuget/it/nuget-search/v3/search?take=1", e.tokAll, nil); a.Status != 200 || len(a.Body) != 0 {
		t.Errorf("HEAD: %d, %d bytes", a.Status, len(a.Body))
	}
}

// TestR_AUTHZ_016_TotalHitsCountsWhatTheCallerMayRead: a grant on one package gives totalHits 1
// for q=acme; an exclude pattern removes its package from the count and the pages; the
// versions of the page are fetched for the readable package alone; a caller without a grant is
// refused as for any read.
func TestR_AUTHZ_016_TotalHitsCountsWhatTheCallerMayRead(t *testing.T) {
	e := newSearchEnv(t)
	core := e.search(e.tokCore, q("acme"))
	if core.TotalHits != 1 || !slices.Equal(core.ids(), []string{"Acme.Core"}) || !slices.Equal(core.Data[0].versions(), []string{"2.0.0"}) {
		t.Errorf("a grant on acme.core: %v (%d)", core.ids(), core.TotalHits)
	}
	if got := e.search(e.tokCore, ""); got.TotalHits != 1 || !slices.Equal(got.ids(), []string{"Acme.Core"}) {
		t.Errorf("the empty query of that caller: %v (%d)", got.ids(), got.TotalHits)
	}
	if got := e.search(e.tokCore, q("widgets")); got.TotalHits != 1 || !slices.Equal(got.ids(), []string{"Acme.Core"}) {
		t.Errorf("q=widgets matches the readable package by its description only: %v (%d)", got.ids(), got.TotalHits)
	}
	if got := e.search(e.tokCore, q("packageid:acme.widgets")); got.TotalHits != 0 || len(got.Data) != 0 {
		t.Errorf("a package the caller may not read: %v (%d)", got.ids(), got.TotalHits)
	}
	if got := e.search(e.tokCore, "take=0"); got.TotalHits != 1 {
		t.Errorf("take=0: %d", got.TotalHits)
	}

	rest := e.search(e.tokNoWidgets, "")
	wantRest := []string{"Acme.Core", "Acme.Tool", "Newtonsoft.Json", "NewtonsoftJsonSchema", "Widgets", "Widgets.Extra"}
	if !slices.Equal(rest.ids(), wantRest) || rest.TotalHits != len(wantRest) {
		t.Errorf("an exclude pattern: %v (%d), want %v", rest.ids(), rest.TotalHits, wantRest)
	}
	var paged []string
	for skip := 0; skip < len(wantRest); skip += 2 {
		p := e.search(e.tokNoWidgets, fmt.Sprintf("skip=%d&take=2", skip))
		if p.TotalHits != len(wantRest) {
			t.Errorf("skip=%d: totalHits %d", skip, p.TotalHits)
		}
		paged = append(paged, p.ids()...)
	}
	if !slices.Equal(paged, wantRest) {
		t.Errorf("pages over the readable packages: %v", paged)
	}
	if got := e.search(e.tokNoWidgets, q("widgets")); !slices.Equal(got.ids(), []string{"Widgets", "Widgets.Extra", "Acme.Core"}) || got.TotalHits != 3 {
		t.Errorf("ranking holds under row-by-row reads: %v (%d)", got.ids(), got.TotalHits)
	}
	if got := e.search(e.tokNoWidgets, q("acme")+"&packageType=DotnetTool"); !slices.Equal(got.ids(), []string{"Acme.Tool"}) || got.TotalHits != 1 {
		t.Errorf("the package type under row-by-row reads: %v (%d)", got.ids(), got.TotalHits)
	}
	if a := e.st.Do(e.r, "GET", "/api/nuget/it/nuget-search/v3/search", e.tokNobody, nil); a.Status != 404 {
		t.Errorf("a caller without a grant: %d %s", a.Status, a.Body)
	}
}

// TestR_NUGET_010_OnlyTheRepositoryItselfIsSearched: a package of another repository never
// appears, and the other repository answers with its own.
func TestR_NUGET_010_OnlyTheRepositoryItselfIsSearched(t *testing.T) {
	e := newSearchEnv(t)
	if got := e.search(e.tokAll, q("other")); got.TotalHits != 0 {
		t.Errorf("q=other in nuget-search: %v", got.ids())
	}
	a := e.st.Do(e.r, "GET", "/api/nuget/it/nuget-other/v3/search?q=other", e.tokAll, nil)
	var f found
	if err := json.Unmarshal(a.Body, &f); err != nil || a.Status != 200 || !slices.Equal(f.ids(), []string{"Acme.Other"}) {
		t.Errorf("nuget-other: %d %s", a.Status, a.Body)
	}
	if !strings.Contains(string(a.Body), "/api/nuget/it/nuget-other/v3/registration/acme.other/index.json") {
		t.Errorf("the links name the repository that was addressed: %s", a.Body)
	}
}

// TestR_AUTHZ_016_ARowByRowScanCrossesItsBatches: a repository with more matches than one batch
// of the scan (500): the count is exact, a page that straddles a batch border is complete and in
// order, a caller who may read none of them gets zero, and the caller who reads the repository
// whole gets the same order from the store's own paging.
func TestR_AUTHZ_016_ARowByRowScanCrossesItsBatches(t *testing.T) {
	e := newSearchEnv(t)
	const n = 1200
	sortKey := version.MustParse("1.0.0").SortKey()
	e.st.Exec(`INSERT INTO packages (repo_id, name, display_name)
	           SELECT r.id, 'bulk.p' || lpad(i::text, 4, '0'), 'Bulk.P' || lpad(i::text, 4, '0')
	             FROM repositories r, generate_series(1, $1::int) AS i WHERE r.key = 'nuget-search'`, n)
	e.st.Exec(`INSERT INTO package_versions (package_id, version, display_version)
	           SELECT p.id, '1.0.0', '1.0.0' FROM packages p JOIN repositories r ON r.id = p.repo_id
	            WHERE r.key = 'nuget-search' AND p.name LIKE 'bulk.p%'`)
	e.st.Exec(`INSERT INTO nuget_version (version_id, repository_id, lower_id, declared_id, original_version, sort_key,
	                                      is_prerelease, is_semver2, published_at, metadata, search_doc, origin)
	           SELECT pv.id, p.repo_id, p.name, p.display_name, '1.0.0', $1, false, false, now(), '{}'::jsonb,
	                  setweight(to_tsvector('simple', replace(p.display_name, '.', ' ')), 'A'), 'push'
	             FROM package_versions pv JOIN packages p ON p.id = pv.package_id JOIN repositories r ON r.id = p.repo_id
	            WHERE r.key = 'nuget-search' AND p.name LIKE 'bulk.p%'`, sortKey)

	for name, tok := range map[string]string{"whole": e.tokAll, "row by row": e.tokNoWidgets} {
		t.Run(name, func(t *testing.T) {
			if got := e.search(tok, q("bulk")+"&take=0"); got.TotalHits != n {
				t.Errorf("totalHits %d, want %d", got.TotalHits, n)
			}
			got := e.search(tok, q("bulk")+"&skip=498&take=5")
			want := []string{"Bulk.P0499", "Bulk.P0500", "Bulk.P0501", "Bulk.P0502", "Bulk.P0503"}
			if !slices.Equal(got.ids(), want) || got.TotalHits != n {
				t.Errorf("page over the first batch border: %v (%d), want %v", got.ids(), got.TotalHits, want)
			}
			if got := e.search(tok, q("bulk")+"&skip=1198&take=10"); len(got.Data) != 2 || got.Data[1].ID != "Bulk.P1200" || got.TotalHits != n {
				t.Errorf("the last page: %v (%d)", got.ids(), got.TotalHits)
			}
		})
	}
	if got := e.search(e.tokCore, q("bulk")); got.TotalHits != 0 || len(got.Data) != 0 {
		t.Errorf("a caller who may read none of them: %v (%d)", got.ids(), got.TotalHits)
	}
}
