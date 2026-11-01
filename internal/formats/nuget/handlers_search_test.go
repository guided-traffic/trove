package nuget

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/nuget/nugettest"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
)

// searchFake serves the search port from a fixed list of packages in rank order. It does
// not evaluate terms: what the handler hands the store is recorded, and the SQL side is
// checked against a database in test/integration/nuget.
type searchFake struct {
	*nugettest.Store
	mu      sync.Mutex
	n       int
	at      func(i int) rows.SearchHit
	queries []rows.SearchQuery
}

func listOf(hits ...rows.SearchHit) *searchFake {
	return &searchFake{Store: nugettest.New(), n: len(hits), at: func(i int) rows.SearchHit { return hits[i] }}
}

func (s *searchFake) Search(_ context.Context, q rows.SearchQuery) ([]rows.SearchHit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, q)
	var out []rows.SearchHit
	if len(q.Only) > 0 {
		for i := range s.n {
			h := s.at(i)
			var keep []int
			for j, v := range h.Listed {
				if slices.Contains(q.Only, rows.SearchVersion{LowerID: h.LowerID, LowerVersion: v.LowerVersion}) {
					keep = append(keep, j)
				}
			}
			if len(keep) == 0 {
				continue
			}
			picked := h
			picked.Listed, picked.ListedTypes = nil, nil
			for _, j := range keep {
				picked.Listed = append(picked.Listed, h.Listed[j])
				picked.ListedTypes = append(picked.ListedTypes, h.ListedTypes[j])
			}
			picked.Latest = rows.RegistrationVersion{VersionEntry: picked.Listed[len(picked.Listed)-1], PackageTypes: picked.ListedTypes[len(picked.Listed)-1], Metadata: h.Latest.Metadata}
			out = append(out, picked)
		}
		return out, nil
	}
	for i := q.Offset; i < min(q.Offset+q.Limit, s.n); i++ {
		out = append(out, s.at(i))
	}
	return out, nil
}

func (s *searchFake) SearchCount(_ context.Context, q rows.SearchQuery) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queries = append(s.queries, q)
	return s.n, nil
}

func (s *searchFake) calls() []rows.SearchQuery {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.queries)
}

// scopeFake is the read scope of one caller.
type scopeFake struct {
	shape formats.ReadShape
	may   func(pkg, version string) bool
}

func (s scopeFake) Shape(string) formats.ReadShape { return s.shape }
func (s scopeFake) MayRead(_, pkg, version string) bool {
	return s.may == nil || s.may(pkg, version)
}

type resolverFake struct {
	formats.Resolver
	scope formats.ReadScope
	err   error
}

func (r resolverFake) ReadScope(context.Context, *formats.Request, []string) (formats.ReadScope, error) {
	return r.scope, r.err
}

func ver(lower string, pre bool) rows.VersionEntry {
	return rows.VersionEntry{
		VersionID: 1, LowerVersion: lower, FullVersion: lower, Listed: true, IsPrerelease: pre,
		PublishedAt: time.Date(2026, 10, 28, 9, 14, 3, 0, time.UTC),
	}
}

// pkg is a hit with the versions given, the last of them the latest.
func pkg(display string, versions ...string) rows.SearchHit {
	h := rows.SearchHit{LowerID: strings.ToLower(display), DisplayName: display}
	for _, v := range versions {
		h.Listed = append(h.Listed, ver(v, strings.Contains(v, "-")))
		h.ListedTypes = append(h.ListedTypes, []string{"dependency"})
	}
	last := len(h.Listed) - 1
	h.Latest = rows.RegistrationVersion{
		VersionEntry: h.Listed[last], PackageTypes: []string{"dependency"},
		Metadata: []byte(`{"title":"` + display + ` title","summary":"s","description":"d","authors":"Acme Team","tags":["net","json"],"iconUrl":"https://i/x.png","projectUrl":"https://p"}`),
	}
	return h
}

func withTypes(h rows.SearchHit, types ...string) rows.SearchHit {
	for i := range h.ListedTypes {
		h.ListedTypes[i] = types
	}
	h.Latest.PackageTypes = types
	return h
}

func searchHost(t *testing.T, st *searchFake, scope formats.ReadScope) *formattest.Host {
	t.Helper()
	return newHost(t, formattest.HostOptions{Data: st, Resolver: resolverFake{scope: scope}})
}

func allScope() formats.ReadScope { return scopeFake{shape: formats.ReadAll} }

func doSearch(t *testing.T, h *formattest.Host, query string) *formattest.Response {
	t.Helper()
	return get(t, h, http.MethodGet, "v3/search", func(r *http.Request) { r.URL.RawQuery = query })
}

type searchDoc struct {
	TotalHits int `json:"totalHits"`
	Data      []struct {
		ID             string   `json:"@id"`
		Type           string   `json:"@type"`
		Registration   string   `json:"registration"`
		Name           string   `json:"id"`
		Version        string   `json:"version"`
		Title          string   `json:"title"`
		Authors        string   `json:"authors"`
		Tags           []string `json:"tags"`
		TotalDownloads int      `json:"totalDownloads"`
		PackageTypes   []struct {
			Name string `json:"name"`
		} `json:"packageTypes"`
		Versions []struct {
			ID        string `json:"@id"`
			Version   string `json:"version"`
			Downloads int    `json:"downloads"`
		} `json:"versions"`
	} `json:"data"`
}

func decodeSearch(t *testing.T, r *formattest.Response) searchDoc {
	t.Helper()
	if r.Code != http.StatusOK {
		t.Fatalf("status %d: %s", r.Code, r.Body.String())
	}
	var d searchDoc
	if err := json.Unmarshal(r.Body.Bytes(), &d); err != nil {
		t.Fatalf("%v: %s", err, r.Body.String())
	}
	return d
}

// TestR_NUGET_010_SearchRendersThePackageDocument: totalHits, the package object with the
// latest visible version, its versions ascending with leaf URLs in the legacy hive, the
// package types, and no property a local package does not have.
func TestR_NUGET_010_SearchRendersThePackageDocument(t *testing.T) {
	st := listOf(pkg("Acme.Widgets", "1.0.0", "1.2.3"), pkg("Acme.Core", "2.0.0"))
	h := searchHost(t, st, allScope())
	r := doSearch(t, h, "q=acme")
	d := decodeSearch(t, r)
	if d.TotalHits != 2 || len(d.Data) != 2 {
		t.Fatalf("totalHits %d, data %d", d.TotalHits, len(d.Data))
	}
	w := d.Data[0]
	if w.Name != "Acme.Widgets" || w.Version != "1.2.3" || w.Type != "Package" {
		t.Errorf("package = %+v", w)
	}
	if want := feedOfMain + "registration/acme.widgets/index.json"; w.Registration != want || w.ID != want {
		t.Errorf("registration = %q, @id = %q, want %q", w.Registration, w.ID, want)
	}
	if len(w.Versions) != 2 || w.Versions[0].Version != "1.0.0" || w.Versions[1].ID != feedOfMain+"registration/acme.widgets/1.2.3.json" {
		t.Errorf("versions = %+v", w.Versions)
	}
	if w.Authors != "Acme Team" || w.Title != "Acme.Widgets title" || !slices.Equal(w.Tags, []string{"net", "json"}) {
		t.Errorf("metadata: %+v", w)
	}
	if len(w.PackageTypes) != 1 || w.PackageTypes[0].Name != "dependency" {
		t.Errorf("packageTypes = %+v", w.PackageTypes)
	}
	for _, k := range []string{`"owners"`, `"verified"`, `"deprecation"`, `"vulnerabilities"`} {
		if strings.Contains(r.Body.String(), k) {
			t.Errorf("a local package has no %s", k)
		}
	}
	if ct := r.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	if r.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", r.Header().Get("Cache-Control"))
	}
}

// TestR_NUGET_010_SemVerLevelSelectsTheHiveAndTheStoreFilter: semVerLevel 2.0.0 and
// prerelease=TRUE reach the store as the visibility of the query and the links point into the
// SemVer 2.0.0 hive; an unparseable level stays 1.0.0.
func TestR_NUGET_010_SemVerLevelSelectsTheHiveAndTheStoreFilter(t *testing.T) {
	cases := []struct {
		query    string
		pre, sv2 bool
		hive     string
	}{
		{"q=a", false, false, "registration/"},
		{"q=a&prerelease=TRUE", true, false, "registration/"},
		{"q=a&prerelease=yes", false, false, "registration/"},
		{"q=a&semVerLevel=2.0.0", false, true, "registration-semver2/"},
		{"q=a&semVerLevel=2.0.0-beta", false, false, "registration/"},
		{"q=a&semVerLevel=3.1", false, true, "registration-semver2/"},
		{"q=a&semVerLevel=1.0.0", false, false, "registration/"},
		{"q=a&semVerLevel=junk", false, false, "registration/"},
	}
	for _, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			st := listOf(pkg("Acme.Widgets", "1.0.0"))
			d := decodeSearch(t, doSearch(t, searchHost(t, st, allScope()), c.query))
			q := st.calls()[0]
			if q.Prerelease != c.pre || q.SemVer2 != c.sv2 {
				t.Errorf("store query prerelease %v semver2 %v, want %v %v", q.Prerelease, q.SemVer2, c.pre, c.sv2)
			}
			if want := feedOfMain + c.hive + "acme.widgets/index.json"; d.Data[0].Registration != want {
				t.Errorf("registration = %q, want %q", d.Data[0].Registration, want)
			}
		})
	}
}

// TestR_NUGET_010_SkipAndTakeAreBounded: the bounds of the settings, an absent take is
// default_take, a larger take is clamped, take=0 asks the store for the count alone.
func TestR_NUGET_010_SkipAndTakeAreBounded(t *testing.T) {
	bad := []string{"skip=3001", "skip=-1", "skip=x", "skip=1.5", "take=-1", "take=ten", "take=%2B5", "skip=99999999999999999999"}
	for _, q := range bad {
		t.Run(q, func(t *testing.T) {
			r := doSearch(t, searchHost(t, listOf(pkg("A", "1.0.0")), allScope()), q)
			if r.Code != http.StatusBadRequest {
				t.Fatalf("status %d: %s", r.Code, r.Body.String())
			}
			if !strings.HasPrefix(r.Body.String(), "malformed_request: ") {
				t.Errorf("body = %q", r.Body.String())
			}
		})
	}
	many := make([]rows.SearchHit, 50)
	for i := range many {
		many[i] = pkg(fmt.Sprintf("P%02d", i), "1.0.0")
	}
	page := func(query string) (searchDoc, rows.SearchQuery) {
		st := listOf(many...)
		d := decodeSearch(t, doSearch(t, searchHost(t, st, allScope()), query))
		cs := st.calls()
		return d, cs[len(cs)-1]
	}
	if d, q := page(""); len(d.Data) != 20 || q.Limit != 20 || q.Offset != 0 || d.TotalHits != 50 {
		t.Errorf("default: %d results, limit %d, offset %d, total %d", len(d.Data), q.Limit, q.Offset, d.TotalHits)
	}
	if d, q := page("take=5000"); len(d.Data) != 50 || q.Limit != 1000 {
		t.Errorf("clamped: %d results, limit %d", len(d.Data), q.Limit)
	}
	if d, q := page("skip=3000&take=2"); len(d.Data) != 0 || d.TotalHits != 50 || q.Limit != 0 {
		t.Errorf("past the end: %d results, total %d; the store was asked for a page: %+v", len(d.Data), d.TotalHits, q)
	}
	if d, q := page("skip=10&take=3"); len(d.Data) != 3 || d.Data[0].Name != "P10" || q.Offset != 10 || d.TotalHits != 50 {
		t.Errorf("page: %+v offset %d total %d", d.Data, q.Offset, d.TotalHits)
	}
	st := listOf(many...)
	d := decodeSearch(t, doSearch(t, searchHost(t, st, allScope()), "take=0"))
	if len(d.Data) != 0 || d.TotalHits != 50 {
		t.Errorf("take=0: %d results, total %d", len(d.Data), d.TotalHits)
	}
	if len(st.calls()) != 1 {
		t.Errorf("take=0 asked the store for a page: %+v", st.calls())
	}
}

// TestR_NUGET_010_TheBoundsFollowTheSettings: nuget.search.max_skip, max_take and
// default_take of the replica are the ones applied.
func TestR_NUGET_010_TheBoundsFollowTheSettings(t *testing.T) {
	f := New()
	h := formattest.NewHost(f, formattest.HostOptions{Data: listOf(pkg("A", "1.0.0")), Resolver: resolverFake{scope: allScope()}})
	f.Bind(h, &config.Config{NuGet: config.NuGet{Search: config.NuGetSearch{DefaultTake: 2, MaxTake: 3, MaxSkip: 5}}}, nil)
	if got := doSearch(t, h, "skip=6").Code; got != http.StatusBadRequest {
		t.Errorf("skip above the replica's max_skip: status %d", got)
	}
	if got := doSearch(t, h, "skip=5").Code; got != http.StatusOK {
		t.Errorf("skip at max_skip: status %d", got)
	}
	st := h.FormatData(ID).(*searchFake)
	last := func() int { cs := st.calls(); return cs[len(cs)-1].Limit }
	doSearch(t, h, "take=9")
	if last() != 3 {
		t.Errorf("take above max_take: limit %d, want 3", last())
	}
	doSearch(t, h, "")
	if last() != 2 {
		t.Errorf("no take: limit %d, want default_take 2", last())
	}
}

// TestR_NUGET_010_TheQueryIsBounded: a query longer than the bound or with too many terms
// answers 400 before the store is asked.
func TestR_NUGET_010_TheQueryIsBounded(t *testing.T) {
	st := listOf(pkg("A", "1.0.0"))
	h := searchHost(t, st, allScope())
	long := "q=" + strings.Repeat("a", 1025)
	terms := "q=" + strings.Repeat("a+", 17)
	for _, q := range []string{long, terms} {
		if r := doSearch(t, h, q); r.Code != http.StatusBadRequest {
			t.Errorf("%.30s...: status %d", q, r.Code)
		}
	}
	if len(st.calls()) != 0 {
		t.Errorf("the store was asked: %+v", st.calls())
	}
	if r := doSearch(t, h, "q="+strings.Repeat("a+", 16)); r.Code != http.StatusOK {
		t.Errorf("16 terms: status %d", r.Code)
	}
}

// TestR_NUGET_010_ThePackageTypeFilterReachesTheStore: the type is lower-cased, an invalid
// name matches nothing without asking the store, an empty one is no filter.
func TestR_NUGET_010_ThePackageTypeFilterReachesTheStore(t *testing.T) {
	st := listOf(pkg("A", "1.0.0"))
	h := searchHost(t, st, allScope())
	decodeSearch(t, doSearch(t, h, "packageType=DotnetTool"))
	if q := st.calls()[0]; q.PackageType != "dotnettool" {
		t.Errorf("packageType = %q", q.PackageType)
	}
	decodeSearch(t, doSearch(t, h, "packageType="))
	if q := st.calls()[2]; q.PackageType != "" {
		t.Errorf("empty packageType = %q", q.PackageType)
	}
	n := len(st.calls())
	d := decodeSearch(t, doSearch(t, h, "packageType=has+space"))
	if d.TotalHits != 0 || len(d.Data) != 0 || len(st.calls()) != n {
		t.Errorf("an invalid package type: %+v, store calls %d -> %d", d, n, len(st.calls()))
	}
}

// TestR_NUGET_010_TermsReachTheStoreFolded: the parser's terms are what the store is asked.
func TestR_NUGET_010_TermsReachTheStoreFolded(t *testing.T) {
	st := listOf(pkg("A", "1.0.0"))
	decodeSearch(t, doSearch(t, searchHost(t, st, allScope()), `q=Acme+title%3A"Big+Widget"+version%3A1.0.0.0`))
	got := st.calls()[0].Terms
	want := []rows.SearchTerm{{Value: "acme"}, {Field: "title", Value: "big widget", Phrase: true}, {Field: "version", Value: "1.0.0"}}
	if !slices.Equal(got, want) {
		t.Errorf("terms = %+v, want %+v", got, want)
	}
}

// TestR_AUTHZ_016_SearchCountsOnlyWhatTheCallerMayRead: a caller whose reads are decided per
// package sees totalHits of the readable packages alone, and the versions of a package are
// those the caller may read.
func TestR_AUTHZ_016_SearchCountsOnlyWhatTheCallerMayRead(t *testing.T) {
	st := listOf(pkg("Acme.Widgets", "1.0.0", "1.1.0"), pkg("Acme.Core", "2.0.0"), pkg("Acme.Secret", "1.0.0"))
	scope := scopeFake{shape: formats.ReadRows, may: func(p, v string) bool {
		return p == "acme.core" || (p == "acme.widgets" && v == "1.0.0")
	}}
	r := doSearch(t, searchHost(t, st, scope), "q=acme")
	d := decodeSearch(t, r)
	if d.TotalHits != 2 || len(d.Data) != 2 {
		t.Fatalf("totalHits %d, data %d: %s", d.TotalHits, len(d.Data), r.Body.String())
	}
	if d.Data[0].Name != "Acme.Widgets" || d.Data[0].Version != "1.0.0" || len(d.Data[0].Versions) != 1 {
		t.Errorf("the latest readable version of Acme.Widgets: %+v", d.Data[0])
	}
	if strings.Contains(r.Body.String(), "Acme.Secret") || strings.Contains(r.Body.String(), "1.1.0") {
		t.Errorf("the result names what the caller may not read: %s", r.Body.String())
	}
}

// TestR_AUTHZ_016_SearchPagesOverTheReadableRowsOnly: skip and take count readable packages;
// a page past the readable ones is empty with the exact count; take=0 returns the count.
func TestR_AUTHZ_016_SearchPagesOverTheReadableRowsOnly(t *testing.T) {
	var hits []rows.SearchHit
	for i := range 40 {
		hits = append(hits, pkg(fmt.Sprintf("P%02d", i), "1.0.0"))
	}
	even := scopeFake{shape: formats.ReadRows, may: func(p, _ string) bool {
		return strings.HasSuffix(p, "0") || strings.HasSuffix(p, "2") || strings.HasSuffix(p, "4") || strings.HasSuffix(p, "6") || strings.HasSuffix(p, "8")
	}}
	h := func() *formattest.Host { return searchHost(t, listOf(hits...), even) }
	d := decodeSearch(t, doSearch(t, h(), "skip=3&take=4"))
	names := make([]string, 0, len(d.Data))
	for _, x := range d.Data {
		names = append(names, x.Name)
	}
	if d.TotalHits != 20 || !slices.Equal(names, []string{"P06", "P08", "P10", "P12"}) {
		t.Errorf("total %d, page %v", d.TotalHits, names)
	}
	if d := decodeSearch(t, doSearch(t, h(), "skip=30&take=4")); d.TotalHits != 20 || len(d.Data) != 0 {
		t.Errorf("past the readable rows: total %d, data %d", d.TotalHits, len(d.Data))
	}
	if d := decodeSearch(t, doSearch(t, h(), "take=0")); d.TotalHits != 20 || len(d.Data) != 0 {
		t.Errorf("take=0: total %d, data %d", d.TotalHits, len(d.Data))
	}
}

// TestR_AUTHZ_016_TheScanReadsKeysAndFetchesOnlyThePage: the scan asks for keys without
// metadata in batches, the page is read once, for the readable versions alone, so the
// memory of a request is bounded by take.
func TestR_AUTHZ_016_TheScanReadsKeysAndFetchesOnlyThePage(t *testing.T) {
	st := &searchFake{Store: nugettest.New(), n: 1200, at: func(i int) rows.SearchHit { return pkg(fmt.Sprintf("P%04d", i), "1.0.0") }}
	scope := scopeFake{shape: formats.ReadRows, may: func(p, _ string) bool { return p != "p0001" }}
	d := decodeSearch(t, doSearch(t, searchHost(t, st, scope), "take=2"))
	if d.TotalHits != 1199 || len(d.Data) != 2 || d.Data[1].Name != "P0002" {
		t.Fatalf("total %d, page %+v", d.TotalHits, d.Data)
	}
	var scans, fetches int
	for _, q := range st.calls() {
		switch {
		case len(q.Only) > 0:
			fetches++
			if len(q.Only) != 2 {
				t.Errorf("the page fetch names %d versions, want 2: %+v", len(q.Only), q.Only)
			}
		case q.KeysOnly:
			scans++
			if q.Limit != searchScanBatch || q.PackageType != "" {
				t.Errorf("scan query %+v", q)
			}
		default:
			t.Errorf("a ReadRows request asked for a page with metadata: %+v", q)
		}
	}
	if scans != 3 || fetches != 1 {
		t.Errorf("scans %d (1200 keys in batches of %d), page fetches %d", scans, searchScanBatch, fetches)
	}
}

// TestR_AUTHZ_016_ThePackageTypeFilterUsesTheLatestReadableVersion: for a caller who reads
// row by row the type of a package is that of the highest version the caller may read, never
// that of a version they may not.
func TestR_AUTHZ_016_ThePackageTypeFilterUsesTheLatestReadableVersion(t *testing.T) {
	tool := pkg("Acme.Tool", "1.0.0", "2.0.0")
	tool.ListedTypes[0] = []string{"dependency"}
	tool.ListedTypes[1] = []string{"dotnettool"}
	scope := scopeFake{shape: formats.ReadRows, may: func(_, v string) bool { return v != "2.0.0" }}
	st := listOf(tool, withTypes(pkg("Acme.Other", "1.0.0"), "dotnettool"))
	d := decodeSearch(t, doSearch(t, searchHost(t, st, scope), "packageType=DotnetTool"))
	if d.TotalHits != 1 || d.Data[0].Name != "Acme.Other" {
		t.Errorf("an unreadable newer version decided the type: %+v", d)
	}
	// With the newer version readable the tool is the latest and matches.
	st = listOf(tool, withTypes(pkg("Acme.Other", "1.0.0"), "dotnettool"))
	d = decodeSearch(t, doSearch(t, searchHost(t, st, scopeFake{shape: formats.ReadRows}), "packageType=DotnetTool"))
	if d.TotalHits != 2 {
		t.Errorf("total = %d", d.TotalHits)
	}
}

// TestR_AUTHZ_016_ACallerWhoReadsNothingGetsAnEmptyResult: the shape none lists nothing and
// never asks the store.
func TestR_AUTHZ_016_ACallerWhoReadsNothingGetsAnEmptyResult(t *testing.T) {
	st := listOf(pkg("A", "1.0.0"))
	r := doSearch(t, searchHost(t, st, scopeFake{shape: formats.ReadNone}), "q=a")
	d := decodeSearch(t, r)
	if d.TotalHits != 0 || len(d.Data) != 0 || !strings.Contains(r.Body.String(), `"data":[]`) {
		t.Errorf("body = %s", r.Body.String())
	}
	if len(st.calls()) != 0 {
		t.Errorf("the store was asked: %+v", st.calls())
	}
}

// TestR_AUTHZ_016_AScanBeyondItsBoundIsRefusedNotCutShort: more matches than the scan bound
// answer 400 instead of a count that is partial.
func TestR_AUTHZ_016_AScanBeyondItsBoundIsRefusedNotCutShort(t *testing.T) {
	st := &searchFake{Store: nugettest.New(), n: maxSearchScan + 1, at: func(i int) rows.SearchHit {
		return rows.SearchHit{LowerID: fmt.Sprintf("p%d", i), Listed: []rows.VersionEntry{ver("1.0.0", false)}, ListedTypes: [][]string{{"dependency"}}}
	}}
	r := doSearch(t, searchHost(t, st, scopeFake{shape: formats.ReadRows}), "q=p")
	if r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), "Narrow the query") {
		t.Errorf("status %d: %s", r.Code, r.Body.String())
	}
}

// TestR_AUTHZ_016_ARefusalToDecideIsNotAnEmptyResult: when the decision point cannot give the
// scope the request fails with its error instead of listing nothing.
func TestR_AUTHZ_016_ARefusalToDecideIsNotAnEmptyResult(t *testing.T) {
	h := newHost(t, formattest.HostOptions{Data: listOf(pkg("A", "1.0.0")), Resolver: resolverFake{err: fmt.Errorf("authz_stale")}})
	if r := doSearch(t, h, "q=a"); r.Code < 500 {
		t.Errorf("status %d: %s", r.Code, r.Body.String())
	}
}

// TestR_NUGET_010_HeadAnswersTheHeadersOfTheGet: HEAD returns the same headers and no body.
func TestR_NUGET_010_HeadAnswersTheHeadersOfTheGet(t *testing.T) {
	h := searchHost(t, listOf(pkg("A", "1.0.0")), allScope())
	g := get(t, h, http.MethodGet, "v3/search")
	hd := get(t, h, http.MethodHead, "v3/search")
	if hd.Code != http.StatusOK || hd.Body.Len() != 0 || hd.Header().Get("Content-Length") != g.Header().Get("Content-Length") {
		t.Errorf("HEAD: status %d, %d body bytes, Content-Length %q (GET %q)", hd.Code, hd.Body.Len(), hd.Header().Get("Content-Length"), g.Header().Get("Content-Length"))
	}
}

// TestR_NUGET_010_SearchWithoutADataPortFailsClosed: a Host that was bound no data port gives
// no store, so the request answers 500 and never an empty result.
func TestR_NUGET_010_SearchWithoutADataPortFailsClosed(t *testing.T) {
	if _, err := searchStoreOf(noPort{}); err == nil {
		t.Error("a host without a port gave a store")
	}
}

type noPort struct{ formats.Host }

func (noPort) FormatData(formats.ID) any { return nil }
