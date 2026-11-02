package nuget

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/observability"
)

// The forwarded search of a remote repository addressed directly (R-NUGET-016) over the in-memory
// pass-through: what reaches the upstream, what the caller sees of its answer, and when the remote
// answers from its cache instead. The road through a virtual repository and the engine's part
// (discovery, cache, circuit, populate check) run against a database and a test upstream in
// test/integration/nuget (forwarded_search_test.go).

const feedOfOrg = "https://trove.test/api/nuget/nuget-org/v3/"

// upstreamAnswer is an upstream's answer to a search: three results, the first with every member
// a forwarded search keeps (its deprecation and vulnerabilities in nuget.org's JSON-LD form, one
// advisory a file:// address), the third with a SemVer 2.0.0 version that carries build metadata.
const upstreamAnswer = `{"totalHits":7,"data":[
 {"@id":"https://up.test/reg/newtonsoft.json/index.json","@type":"Package","registration":"https://up.test/reg/newtonsoft.json/index.json",
  "id":"Newtonsoft.Json","version":"13.0.3","description":"Json.NET is a JSON framework","summary":"","title":"Json.NET",
  "iconUrl":"https://up.test/icon.png","licenseUrl":"https://up.test/license","projectUrl":"https://up.test/project",
  "tags":["json"],"authors":["James Newton-King"],"owners":["jamesnk","newtonsoft"],"totalDownloads":5000000,"verified":true,
  "packageTypes":[{"name":"Dependency"}],
  "versions":[{"version":"13.0.1","downloads":1000,"@id":"https://up.test/reg/newtonsoft.json/13.0.1.json"},
              {"version":"13.0.3","downloads":3000,"@id":"https://up.test/reg/newtonsoft.json/13.0.3.json"}],
  "deprecation":{"@id":"https://up.test/catalog/newtonsoft.json.13.0.3.json#deprecation","@type":"deprecation","reasons":["Legacy"],
    "alternatePackage":{"@id":"https://up.test/reg/system.text.json/index.json","@type":"alternatePackage","id":"System.Text.Json","range":"[8.0.0, )"}},
  "vulnerabilities":[{"@id":"https://up.test/vuln/1","@type":"Vulnerability","advisoryUrl":"https://advisories.test/GHSA-1","severity":"2"},
                     {"@id":"https://up.test/vuln/2","advisoryUrl":"file://advisories.test/share/GHSA-2","severity":"1"}]},
 {"id":"Contoso.Secret","version":"1.0.0","versions":[{"version":"1.0.0","downloads":5}],"totalDownloads":5},
 {"id":"Semver.Two","version":"2.0.0-beta.1+build.7","versions":[{"version":"2.0.0-beta.1+build.7","downloads":7}],"totalDownloads":7}
]}`

// remoteResolver is the resolver of a remote addressed directly: the read scope and the claims the
// test gives, with every name KeepsAway was asked about.
type remoteResolver struct {
	formats.Resolver
	scope formats.ReadScope
	keeps func(term string) bool

	mu    sync.Mutex
	asked []string
}

func (r *remoteResolver) ReadScope(context.Context, *formats.Request, []string) (formats.ReadScope, error) {
	return r.scope, nil
}

func (r *remoteResolver) KeepsAway(_ context.Context, rq *formats.Request, member formats.RepoRef, term string) (bool, error) {
	if member.Key != rq.Repo.Key {
		return false, errs.E(errs.Internal, errs.CodeInternal, "not the request's repository")
	}
	r.mu.Lock()
	r.asked = append(r.asked, term)
	r.mu.Unlock()
	return r.keeps != nil && r.keeps(term), nil
}

// forwarding is a remote repository whose pass-through answers with answer (or err), and whose
// cache holds the hits of cached.
type forwarding struct {
	h       *formattest.Host
	st      *searchFake
	res     *remoteResolver
	metrics *observability.Metrics

	mu      sync.Mutex
	answer  string
	err     error
	queries []formats.Query
}

func newForwarding(t *testing.T, subject authz.Subject, cached ...rows.SearchHit) *forwarding {
	t.Helper()
	fw := &forwarding{st: listOf(cached...), res: &remoteResolver{scope: allScope()}, answer: upstreamAnswer, metrics: observability.NewMetrics("web")}
	m := &formattest.MemRemote{Queries: func(_ context.Context, rq *formats.Request, q formats.Query) (*formats.QueryResult, error) {
		fw.mu.Lock()
		defer fw.mu.Unlock()
		if rq.Repo.Key != "nuget-org" {
			t.Errorf("the query ran on %q", rq.Repo.Key)
		}
		fw.queries = append(fw.queries, q)
		if fw.err != nil {
			return nil, fw.err
		}
		return &formats.QueryResult{ContentType: "application/json", Body: []byte(fw.answer), FetchedAt: time.Now()}, nil
	}}
	fw.h = formattest.NewHost(New(), formattest.HostOptions{
		Class: formats.Remote, RepoKey: "nuget-org", Remote: m, Data: fw.st, Resolver: fw.res, Subject: subject,
	})
	fw.h.Format().(*Format).Bind(fw.h, &config.Config{NuGet: config.NuGet{Search: config.NuGetSearch{
		DefaultTake: 20, MaxTake: 1000, MaxSkip: 3000, VirtualMergeWindow: 1000, RemoteResultTTL: 45 * time.Second,
	}}}, fw.metrics)
	return fw
}

func (fw *forwarding) asked() []formats.Query {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	return slices.Clone(fw.queries)
}

// forwardedDoc is the search document with the members of a forwarded result.
type forwardedDoc struct {
	TotalHits int `json:"totalHits"`
	Data      []struct {
		ID              string          `json:"@id"`
		Registration    string          `json:"registration"`
		Name            string          `json:"id"`
		Version         string          `json:"version"`
		Description     string          `json:"description"`
		Title           string          `json:"title"`
		Authors         string          `json:"authors"`
		Owners          []string        `json:"owners"`
		Verified        *bool           `json:"verified"`
		TotalDownloads  int64           `json:"totalDownloads"`
		Deprecation     json.RawMessage `json:"deprecation"`
		Vulnerabilities json.RawMessage `json:"vulnerabilities"`
		Versions        []struct {
			ID        string `json:"@id"`
			Version   string `json:"version"`
			Downloads int64  `json:"downloads"`
		} `json:"versions"`
	} `json:"data"`
}

func forwardedOf(t *testing.T, r *formattest.Response) forwardedDoc {
	t.Helper()
	if r.Code != http.StatusOK {
		t.Fatalf("status %d: %s", r.Code, r.Body.String())
	}
	var d forwardedDoc
	if err := json.Unmarshal(r.Body.Bytes(), &d); err != nil {
		t.Fatalf("%v: %s", err, r.Body.String())
	}
	return d
}

func namesOf(d forwardedDoc) []string {
	var out []string
	for _, r := range d.Data {
		out = append(out, r.Name)
	}
	return out
}

// TestR_NUGET_016_AForwardedSearchIsRewrittenIntoTheRemoteAndKeepsTheUpstreamMembers: the query
// goes to the upstream with q, the page and the filters as trove validated them, for the period of
// nuget.search.remote_result_ttl; the results come back with every address in the remote's hive
// that matches semVerLevel, and with the download counts, the owners, the verified flag, the
// deprecation and the vulnerabilities of the upstream; the cache is not searched.
func TestR_NUGET_016_AForwardedSearchIsRewrittenIntoTheRemoteAndKeepsTheUpstreamMembers(t *testing.T) {
	fw := newForwarding(t, authz.Subject{}, pkg("Acme.Cached", "1.0.0"))
	d := forwardedOf(t, doSearch(t, fw.h, "q=json+library&semVerLevel=2.0.0&prerelease=TRUE&take=10&packageType=Dependency"))

	qs := fw.asked()
	if len(qs) != 1 {
		t.Fatalf("%d queries, want 1", len(qs))
	}
	q := qs[0]
	if q.Path.Path != "v3/search" || q.TTL != 45*time.Second {
		t.Errorf("query path %q, period %v", q.Path.Path, q.TTL)
	}
	if got := q.Params.Encode(); got != "packageType=dependency&prerelease=true&q=json+library&semVerLevel=2.0.0&skip=0&take=10" {
		t.Errorf("parameters %s", got)
	}
	if n := len(fw.st.calls()); n != 0 {
		t.Errorf("the cache was searched %d times for a forwarded query", n)
	}
	if !slices.Equal(namesOf(d), []string{"Newtonsoft.Json", "Contoso.Secret", "Semver.Two"}) || d.TotalHits != 7 {
		t.Fatalf("results %v, totalHits %d", namesOf(d), d.TotalHits)
	}
	n := d.Data[0]
	if want := feedOfOrg + "registration-semver2/newtonsoft.json/index.json"; n.Registration != want || n.ID != want {
		t.Errorf("registration %q, @id %q, want %q", n.Registration, n.ID, want)
	}
	if len(n.Versions) != 2 || n.Versions[1].ID != feedOfOrg+"registration-semver2/newtonsoft.json/13.0.3.json" ||
		n.Versions[0].Downloads != 1000 || n.Versions[1].Downloads != 3000 {
		t.Errorf("versions %+v", n.Versions)
	}
	if n.Version != "13.0.3" || n.Title != "Json.NET" || n.Authors != "James Newton-King" || n.Description == "" {
		t.Errorf("package fields %+v", n)
	}
	if !slices.Equal(n.Owners, []string{"jamesnk", "newtonsoft"}) || n.Verified == nil || !*n.Verified || n.TotalDownloads != 5000000 {
		t.Errorf("owners %v, verified %v, totalDownloads %d", n.Owners, n.Verified, n.TotalDownloads)
	}
	// The deprecation and the vulnerabilities come without the upstream's addresses (Q-NUGET-85).
	if want := `{"alternatePackage":{"id":"System.Text.Json","range":"[8.0.0, )"},"reasons":["Legacy"]}`; string(n.Deprecation) != want {
		t.Errorf("deprecation %s, want %s", n.Deprecation, want)
	}
	if want := `[{"advisoryUrl":"https://advisories.test/GHSA-1","severity":"2"},{"severity":"1"}]`; string(n.Vulnerabilities) != want {
		t.Errorf("vulnerabilities %s, want %s", n.Vulnerabilities, want)
	}
	s2 := d.Data[2]
	if s2.Version != "2.0.0-beta.1+build.7" || s2.Versions[0].ID != feedOfOrg+"registration-semver2/semver.two/2.0.0-beta.1.json" {
		t.Errorf("SemVer 2.0.0 result %+v", s2)
	}

	t.Run("without semVerLevel the addresses are in the legacy hive", func(t *testing.T) {
		d := forwardedOf(t, doSearch(t, fw.h, "q=json"))
		if want := feedOfOrg + "registration/newtonsoft.json/index.json"; d.Data[0].Registration != want {
			t.Errorf("registration %q, want %q", d.Data[0].Registration, want)
		}
		if got := fw.asked()[1].Params.Encode(); got != "prerelease=false&q=json&skip=0&take=20" {
			t.Errorf("parameters %s", got)
		}
	})
}

// TestR_NUGET_016_AResultAClaimOrAnExcludeKeepsAwayIsRemovedAndTotalHitsReduced: a result whose ID
// KeepsAway names for the remote (an exclude pattern, a global claim under remote refusal) is not
// shown, and totalHits is the upstream's count less the results removed from the page.
func TestR_NUGET_016_AResultAClaimOrAnExcludeKeepsAwayIsRemovedAndTotalHitsReduced(t *testing.T) {
	fw := newForwarding(t, authz.Subject{})
	fw.res.keeps = func(term string) bool { return term == "contoso.secret" }
	d := forwardedOf(t, doSearch(t, fw.h, "q=json"))
	if !slices.Equal(namesOf(d), []string{"Newtonsoft.Json", "Semver.Two"}) || d.TotalHits != 6 {
		t.Errorf("results %v, totalHits %d, want two and 6", namesOf(d), d.TotalHits)
	}
}

// TestR_NUGET_016_AWithheldQueryIsAnsweredFromTheCacheAndCounted: a query with a name KeepsAway
// protects is not forwarded; the remote answers with its cache-only search and counts it.
func TestR_NUGET_016_AWithheldQueryIsAnsweredFromTheCacheAndCounted(t *testing.T) {
	fw := newForwarding(t, authz.Subject{}, pkg("Acme.Cached", "1.0.0"))
	fw.res.keeps = func(term string) bool { return term == "acme" || term == "acme.payments" }
	for _, q := range []string{"q=acme.payments", "q=packageid:Acme.Payments", "q=%22tools+for+acme%22", "q=acme%21"} {
		d := forwardedOf(t, doSearch(t, fw.h, q))
		if !slices.Equal(namesOf(d), []string{"Acme.Cached"}) {
			t.Errorf("%s: results %v, want the cached package", q, namesOf(d))
		}
	}
	if n := len(fw.asked()); n != 0 {
		t.Errorf("%d withheld queries reached the upstream", n)
	}
	if got := counted(t, fw.metrics, "trove_nuget_search_withheld_total"); got != 4 {
		t.Errorf("trove_nuget_search_withheld_total = %v, want 4", got)
	}
	if d := forwardedOf(t, doSearch(t, fw.h, "q=newtonsoft")); len(d.Data) != 3 || len(fw.asked()) != 1 {
		t.Errorf("an unprotected query was not forwarded: %v", namesOf(d))
	}
}

// TestR_NUGET_016_TheRemoteSearchesItsCacheWhereItMayNotForward: forwardSearch false, the cache view
// and an endpoint that fills no caches (R-NUGET-034) answer from the cache without asking the
// upstream.
func TestR_NUGET_016_TheRemoteSearchesItsCacheWhereItMayNotForward(t *testing.T) {
	cached := pkg("Acme.Cached", "1.0.0")
	check := func(t *testing.T, fw *forwarding, r *formattest.Response) {
		t.Helper()
		if d := forwardedOf(t, r); !slices.Equal(namesOf(d), []string{"Acme.Cached"}) {
			t.Errorf("results %v, want the cached package", namesOf(d))
		}
		if n := len(fw.asked()); n != 0 {
			t.Errorf("%d queries reached the upstream", n)
		}
	}
	t.Run("forwardSearch false", func(t *testing.T) {
		fw := newForwarding(t, authz.Subject{}, cached)
		fw.h.Repo().Config = []byte(`{"nuget":{"forwardSearch":false}}`)
		check(t, fw, doSearch(t, fw.h, "q=json"))
	})
	t.Run("the cache view", func(t *testing.T) {
		fw := newForwarding(t, authz.Subject{}, cached)
		rq, err := fw.h.NewRequest(context.Background(), http.MethodGet, "v3/search", nil)
		if err != nil {
			t.Fatal(err)
		}
		rq.URL.RawQuery, rq.CacheView = "q=json", true
		check(t, fw, fw.h.ServeRequest(rq, false))
	})
	t.Run("an endpoint that fills no caches", func(t *testing.T) {
		fw := newForwarding(t, authz.Subject{Endpoint: "customers", CacheFill: false}, cached)
		check(t, fw, doSearch(t, fw.h, "q=json"))
	})
	t.Run("an endpoint that fills caches forwards", func(t *testing.T) {
		fw := newForwarding(t, authz.Subject{Endpoint: "partners", CacheFill: true}, cached)
		if d := forwardedOf(t, doSearch(t, fw.h, "q=json")); len(d.Data) != 3 || len(fw.asked()) != 1 {
			t.Errorf("results %v after %d queries", namesOf(d), len(fw.asked()))
		}
	})
}

// TestR_NUGET_016_AnUpstreamThatCannotBeAskedOrFailsLeavesTheCacheSearch: a remote that may not
// contact its upstream for the caller and holds no copy (ErrOffline), an upstream failure or an
// open circuit, and an answer that is no search document fall back to the cache-only search with
// 200; the failures are counted, ErrOffline is not. An internal error is not hidden.
func TestR_NUGET_016_AnUpstreamThatCannotBeAskedOrFailsLeavesTheCacheSearch(t *testing.T) {
	fw := newForwarding(t, authz.Subject{}, pkg("Acme.Cached", "1.0.0"))
	cases := []struct {
		name     string
		err      error
		answer   string
		failures float64
	}{
		{"no copy and no contact", formats.ErrOffline, upstreamAnswer, 0},
		{"an upstream failure", errs.E(errs.UpstreamFailed, errs.CodeUpstreamError, "502 from the upstream"), upstreamAnswer, 1},
		{"an open circuit", errs.E(errs.Unavailable, errs.CodeUpstreamAssumedOffline, "assumed offline"), upstreamAnswer, 2},
		{"an answer that is no search document", nil, `<html>captive portal</html>`, 3},
		{"a search document with members of the wrong type", nil, `{"totalHits":"many","data":{}}`, 4},
	}
	for _, c := range cases {
		fw.err, fw.answer = c.err, c.answer
		d := forwardedOf(t, doSearch(t, fw.h, "q=json"))
		if !slices.Equal(namesOf(d), []string{"Acme.Cached"}) {
			t.Errorf("%s: results %v, want the cached package", c.name, namesOf(d))
		}
		if got := counted(t, fw.metrics, "trove_nuget_search_forward_failures_total"); got != c.failures {
			t.Errorf("%s: trove_nuget_search_forward_failures_total = %v, want %v", c.name, got, c.failures)
		}
	}
	fw.err = errs.E(errs.Internal, errs.CodeInternal, "a bug")
	if r := doSearch(t, fw.h, "q=json"); r.Code != http.StatusInternalServerError {
		t.Errorf("an internal error answered %d", r.Code)
	}
}

// TestR_NUGET_034_AForwardedResultShowsOnlyTheVersionsTheCallerMayRead: for a caller whose read is
// decided per version, a forwarded result lists the readable versions, its version is the latest
// of them and its total downloads their sum; the metadata, the deprecation and the vulnerabilities
// of a hidden latest version are left out; a result without a readable version is removed and
// totalHits reduced. The query has no term, so no match rests on the hidden version.
func TestR_NUGET_034_AForwardedResultShowsOnlyTheVersionsTheCallerMayRead(t *testing.T) {
	fw := newForwarding(t, authz.Subject{})
	fw.res.scope = scopeFake{shape: formats.ReadRows, may: func(pkg, version string) bool {
		return pkg == "newtonsoft.json" && version == "13.0.1" || pkg == "semver.two"
	}}
	d := forwardedOf(t, doSearch(t, fw.h, "semVerLevel=2.0.0"))
	if !slices.Equal(namesOf(d), []string{"Newtonsoft.Json", "Semver.Two"}) || d.TotalHits != 6 {
		t.Fatalf("results %v, totalHits %d", namesOf(d), d.TotalHits)
	}
	n := d.Data[0]
	if n.Version != "13.0.1" || len(n.Versions) != 1 || n.TotalDownloads != 1000 {
		t.Errorf("version %s, versions %+v, totalDownloads %d", n.Version, n.Versions, n.TotalDownloads)
	}
	if n.Description != "" || n.Title != "" || n.Deprecation != nil || n.Vulnerabilities != nil {
		t.Errorf("the members of the hidden 13.0.3 are shown: %+v", n)
	}
	if !slices.Equal(n.Owners, []string{"jamesnk", "newtonsoft"}) {
		t.Errorf("owners %v", n.Owners)
	}
}

// TestR_NUGET_016_TheSearchPathOfAQueryNamesNoPackage: the path a forwarded search passes through
// the engine is repository-level, so its populate check is decided for the repository, although
// "v3" is a valid package ID (Q-NUGET-79).
func TestR_NUGET_016_TheSearchPathOfAQueryNamesNoPackage(t *testing.T) {
	f := New()
	for _, p := range []string{"v3/search", "v3/autocomplete"} {
		if got := f.Classify(formats.Path{Path: p}); got.Package != "" || got.Kind != formats.PathVolatile {
			t.Errorf("Classify(%q) = %+v, want a volatile path without a package", p, got)
		}
	}
	for p, want := range map[string]string{"v3/index.json": "v3", "v3/1.0.0/v3.1.0.0.nupkg": "v3", "search/index.json": "search"} {
		if got := f.Classify(formats.Path{Path: p}); got.Package != want {
			t.Errorf("Classify(%q) names %q, want %q", p, got.Package, want)
		}
	}
}

// TestR_NUGET_018_ARemoteMemberIsSearchedThroughTheVirtualRepositorysMembers: the member of the plan
// is looked up among the members of the virtual repository, and a planned member that is none is an
// internal error, not a silent omission.
func TestR_NUGET_018_ARemoteMemberIsSearchedThroughTheVirtualRepositorysMembers(t *testing.T) {
	res := &mergeResolver{}
	h, _ := virtualSearchHost(t, 0, []formats.PlannedMember{
		memberAt(11, "local", formats.TierLocal, 0), memberAt(20, "remote", formats.TierRemote, 1),
	}, map[int64][]rows.SearchHit{11: {pkg("Acme.Widgets", "1.0.0")}}, res)
	// The virtual repository holds the local member only, so the planned remote member is none
	// of its members.
	withMembers(h, []formats.PlannedMember{memberAt(11, "local", formats.TierLocal, 0)})
	if r := doSearch(t, h, "q=acme"); r.Code != http.StatusInternalServerError {
		t.Errorf("a planned member that is no member answered %d: %s", r.Code, r.Body.String())
	}
}

// TestR_NUGET_016_AForwardedResultComesWithoutTheUpstreamsAddresses: the deprecation and the
// vulnerabilities of a forwarded result are cleaned as the remote's registration cleans them
// (Q-NUGET-85): every "@id" and "@type" is dropped at any depth, the alternate package's included;
// an advisoryUrl stays only as an absolute http(s) link without user info, and an advisory left
// with nothing is dropped.
func TestR_NUGET_016_AForwardedResultComesWithoutTheUpstreamsAddresses(t *testing.T) {
	fw := newForwarding(t, authz.Subject{})
	fw.answer = `{"totalHits":1,"data":[{"id":"Newtonsoft.Json","version":"13.0.1",
	  "versions":[{"version":"13.0.1","downloads":10}],"totalDownloads":10,
	  "deprecation":{"@id":"https://up.test/catalog/deprecation","@type":"deprecation","reasons":["Legacy"],"message":"use the other",
	    "alternatePackage":{"@id":"https://up.test/reg/other/index.json","@type":"alternatePackage","id":"Other.Json","range":"[1.0.0, )"}},
	  "vulnerabilities":[
	    {"@id":"https://up.test/catalog/vuln/1","@type":"vulnerability","advisoryUrl":"https://github.com/advisories/GHSA-5crp-9r3c-p9vr","severity":"2"},
	    {"advisoryUrl":"https://user:secret@advisories.test/GHSA-2","severity":"1"},
	    {"@id":"https://up.test/catalog/vuln/3","advisoryUrl":"ftp://advisories.test/GHSA-3"}]}]}`
	d := forwardedOf(t, doSearch(t, fw.h, "q=newtonsoft"))
	if len(d.Data) != 1 {
		t.Fatalf("results %v", namesOf(d))
	}
	n := d.Data[0]
	for name, raw := range map[string]json.RawMessage{"deprecation": n.Deprecation, "vulnerabilities": n.Vulnerabilities} {
		if s := string(raw); strings.Contains(s, "up.test") || strings.Contains(s, "@id") || strings.Contains(s, "@type") ||
			strings.Contains(s, "secret") || strings.Contains(s, "ftp:") {
			t.Errorf("%s keeps an upstream address: %s", name, s)
		}
	}
	if want := `{"alternatePackage":{"id":"Other.Json","range":"[1.0.0, )"},"message":"use the other","reasons":["Legacy"]}`; string(n.Deprecation) != want {
		t.Errorf("deprecation %s, want %s", n.Deprecation, want)
	}
	if want := `[{"advisoryUrl":"https://github.com/advisories/GHSA-5crp-9r3c-p9vr","severity":"2"},{"severity":"1"}]`; string(n.Vulnerabilities) != want {
		t.Errorf("vulnerabilities %s, want %s", n.Vulnerabilities, want)
	}
}

// TestR_NUGET_016_TheUpstreamIsSentOnlyTheTermsTheWithheldRuleChecked: the withheld rule asks about
// the terms Parse read, so the upstream is sent those terms and not the client's text (Q-NUGET-80).
// A version: term whose text is no version is folded to nothing and matches nothing (R-NUGET-010):
// such a query is not forwarded, so a protected name written as one (version:Acme.Payments, or a
// quoted phrase after version:) never reaches the upstream. Other queries reach it in the form the
// rule saw.
func TestR_NUGET_016_TheUpstreamIsSentOnlyTheTermsTheWithheldRuleChecked(t *testing.T) {
	fw := newForwarding(t, authz.Subject{}, pkg("Acme.Cached", "1.0.0"))
	fw.res.keeps = func(term string) bool { return term == "acme.payments" || term == "acme" }
	for _, q := range []string{"q=acme.payments", "q=version:Acme.Payments", "q=version:%22acme+payments%22", "q=json+version:Acme.Payments"} {
		r := doSearch(t, fw.h, q)
		if r.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", q, r.Code, r.Body.String())
		}
		if n := len(fw.asked()); n != 0 {
			t.Fatalf("%s reached the upstream as q=%q", q, fw.asked()[0].Params.Get("q"))
		}
	}
	for q, want := range map[string]string{
		"q=Json++Library":                     "json library",
		"q=Json+%22Fast+Library%22":           "json \"fast library\"",
		"q=ID:Newtonsoft.Json+version:13.0.3": "id:newtonsoft.json version:13.0.3",
		"q=version:13.0.3.0":                  "version:13.0.3",
	} {
		before := len(fw.asked())
		doSearch(t, fw.h, q)
		qs := fw.asked()
		if len(qs) != before+1 {
			t.Fatalf("%s was not forwarded", q)
		}
		if got := qs[len(qs)-1].Params.Get("q"); got != want {
			t.Errorf("%s: the upstream was sent q=%q, want %q", q, got, want)
		}
	}
}

// TestR_NUGET_034_AForwardedResultMatchedOnAHiddenVersionIsRemoved: the upstream matches the terms
// against versions the caller may not read, so for a caller whose read is decided per version a
// result is removed, and totalHits reduced, when a version: term names no readable version, and
// when the upstream's latest version, whose metadata the text terms met, is hidden and the query
// has a text term. Otherwise the result's presence would tell that a hidden version exists or what
// it says, as Q-NUGET-74 rules out for the local search.
func TestR_NUGET_034_AForwardedResultMatchedOnAHiddenVersionIsRemoved(t *testing.T) {
	fw := newForwarding(t, authz.Subject{})
	fw.answer = `{"totalHits":1,"data":[{"id":"Newtonsoft.Json","version":"13.0.3","description":"Json.NET",
	  "versions":[{"version":"13.0.1","downloads":1},{"version":"13.0.3","downloads":3}],"totalDownloads":4}]}`
	hidesLatest := scopeFake{shape: formats.ReadRows, may: func(_, version string) bool { return version == "13.0.1" }}
	readsAll := scopeFake{shape: formats.ReadRows, may: func(string, string) bool { return true }}
	for _, c := range []struct {
		name  string
		scope formats.ReadScope
		query string
		kept  bool
	}{
		{"a version: term of the hidden version", hidesLatest, "q=version:13.0.3", false},
		{"a version: term of a readable version", hidesLatest, "q=version:13.0.1", true},
		{"a text term met by the hidden latest version", hidesLatest, "q=json.net", false},
		{"a text term and a readable version: term", hidesLatest, "q=json+version:13.0.1", false},
		{"no term", hidesLatest, "", true},
		{"a text term, every version readable", readsAll, "q=json.net", true},
		{"a version: term of a readable latest version", readsAll, "q=version:13.0.3", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			fw.res.scope = c.scope
			d := forwardedOf(t, doSearch(t, fw.h, c.query))
			if got := len(d.Data) == 1; got != c.kept {
				t.Fatalf("results %v, want kept %v", namesOf(d), c.kept)
			}
			if want := map[bool]int{true: 1, false: 0}[c.kept]; d.TotalHits != want {
				t.Errorf("totalHits %d, want %d", d.TotalHits, want)
			}
		})
	}
}

// TestR_NUGET_016_AnUpstreamAnswerIsReadWithinItsBounds: the forwarded answer is parsed again on
// every use, so it is bounded far below a stored document: a gzip-coded answer that inflates
// beyond maxSearchAnswerBytes is no answer (the cache-only search answers, a failure is counted),
// a result that lists more versions than trove reads of one ID is removed, and a version listed
// many times is one version, found without comparing every pair.
func TestR_NUGET_016_AnUpstreamAnswerIsReadWithinItsBounds(t *testing.T) {
	fw := newForwarding(t, authz.Subject{}, pkg("Acme.Cached", "1.0.0"))
	var bomb bytes.Buffer
	zw := gzip.NewWriter(&bomb)
	_, _ = zw.Write([]byte(`{"totalHits":1,"data":[],"padding":"`))
	_, _ = zw.Write(bytes.Repeat([]byte("a"), 8<<20+1)) // maxSearchAnswerBytes
	_, _ = zw.Write([]byte(`"}`))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	fw.answer = bomb.String()
	d := forwardedOf(t, doSearch(t, fw.h, "q=json"))
	if !slices.Equal(namesOf(d), []string{"Acme.Cached"}) {
		t.Errorf("an answer beyond the bound: results %v, want the cached package", namesOf(d))
	}
	if got := counted(t, fw.metrics, "trove_nuget_search_forward_failures_total"); got != 1 {
		t.Errorf("trove_nuget_search_forward_failures_total = %v, want 1", got)
	}

	many := make([]string, 0, 40_000)
	for i := range 40_000 {
		many = append(many, fmt.Sprintf(`{"version":"1.0.%d","downloads":1}`, i))
	}
	same := strings.Repeat(`{"version":"2.0.0","downloads":1},`, 20_000) + `{"version":"2.0.0","downloads":1}`
	fw.answer = `{"totalHits":2,"data":[{"id":"Acme.Many","version":"1.0.39999","versions":[` + strings.Join(many, ",") + `]},` +
		`{"id":"Acme.Same","version":"2.0.0","versions":[` + same + `]}]}`
	d = forwardedOf(t, doSearch(t, fw.h, "q=acme"))
	if !slices.Equal(namesOf(d), []string{"Acme.Same"}) || d.TotalHits != 1 {
		t.Fatalf("results %v, totalHits %d, want Acme.Same alone and 1", namesOf(d), d.TotalHits)
	}
	if len(d.Data[0].Versions) != 1 {
		t.Errorf("Acme.Same lists %d versions, want 1", len(d.Data[0].Versions))
	}
}

// TestR_NUGET_003_AForwardedResultWithAnIDNoClientRequestsIsRemoved: a result whose ID holds a
// character a client's lowercasing changes (U+212A KELVIN SIGN, a capital letter outside ASCII)
// names no path a client requests, and patterns and claims compare names character by character,
// so they would not see the ID it lowercases to: the result is removed and totalHits reduced.
func TestR_NUGET_003_AForwardedResultWithAnIDNoClientRequestsIsRemoved(t *testing.T) {
	fw := newForwarding(t, authz.Subject{})
	fw.answer = `{"totalHits":3,"data":[
	  {"id":"Kontoso.Lib","version":"1.0.0","versions":[{"version":"1.0.0","downloads":1}]},
	  {"id":"Ünïcode.Pkg","version":"1.0.0","versions":[{"version":"1.0.0","downloads":1}]},
	  {"id":"Kontoso.Lib","version":"1.0.0","versions":[{"version":"1.0.0","downloads":1}]}]}`
	d := forwardedOf(t, doSearch(t, fw.h, "q=ontoso"))
	if !slices.Equal(namesOf(d), []string{"Kontoso.Lib"}) || d.TotalHits != 1 {
		t.Errorf("results %v, totalHits %d, want Kontoso.Lib alone and 1", namesOf(d), d.TotalHits)
	}
}
