package nuget

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/nuget/remote"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// The tests of NuGet's mirror discovery run over a fake discover.Upstream: the format sends no request of its own, so every
// request it makes is visible in the fake (R-REPO-051; the NuGet stub of R-REPO-059 is tested in test/nugetstub).

const (
	fakeBase   = "https://feed.test"
	fakeCatLog = fakeBase + "/catalog/index.json"
	fakeSearch = fakeBase + "/query"
)

// fakeUpstream serves documents by canonical path and Get requests by URL, and records both.
type fakeUpstream struct {
	docs    map[string][]byte // canonical path -> document
	pages   map[string][]byte // URL without the query -> body, for Get
	search  func(q url.Values) (int, []byte)
	docErr  map[string]error
	getErr  error
	gets    []string
	docReqs []string
}

func newFakeUpstream(catalog, search bool) *fakeUpstream {
	f := &fakeUpstream{docs: map[string][]byte{}, pages: map[string][]byte{}, docErr: map[string]error{}}
	res := `{"@id":"` + fakeBase + `/reg/","@type":"RegistrationsBaseUrl/3.6.0"},{"@id":"` + fakeBase + `/flat/","@type":"PackageBaseAddress/3.0.0"}`
	if catalog {
		res += `,{"@id":"` + fakeCatLog + `","@type":"Catalog/3.0.0"}`
	}
	if search {
		res += `,{"@id":"` + fakeSearch + `","@type":"SearchQueryService/3.5.0"}`
	}
	f.docs[".nuget/index.json"] = []byte(`{"version":"3.0.0","resources":[` + res + `]}`)
	return f
}

func (f *fakeUpstream) Base() *url.URL { u, _ := url.Parse(fakeBase + "/index.json"); return u }

func (f *fakeUpstream) Settings() json.RawMessage { return nil }

func (f *fakeUpstream) Get(_ context.Context, u *url.URL, _ string, _ int64) (*discover.Response, error) {
	f.gets = append(f.gets, u.String())
	if f.getErr != nil {
		return nil, f.getErr
	}
	if strings.HasPrefix(u.String(), fakeSearch) && f.search != nil {
		status, body := f.search(u.Query())
		return &discover.Response{Status: status, Body: body}, nil
	}
	plain := *u
	plain.RawQuery = ""
	body, ok := f.pages[plain.String()]
	if !ok {
		return &discover.Response{Status: http.StatusNotFound}, nil
	}
	return &discover.Response{Status: http.StatusOK, Body: body}, nil
}

func (f *fakeUpstream) Document(_ context.Context, path, variant string) ([]byte, bool, error) {
	f.docReqs = append(f.docReqs, path+"|"+variant)
	if err := f.docErr[path]; err != nil {
		return nil, false, err
	}
	doc, ok := f.docs[path]
	return doc, ok, nil
}

func (f *fakeUpstream) catalogRequests() int {
	n := 0
	for _, g := range f.gets {
		if strings.Contains(g, "/catalog/") {
			n++
		}
	}
	return n
}

// catalogItem is one item of a fake catalog page.
type catalogItem struct {
	id, version, stamp string
	del                bool
}

// setCatalog installs the catalog: pages in the order given (the index lists them in that order too).
func (f *fakeUpstream) setCatalog(pages ...[]catalogItem) {
	var idx []map[string]string
	for i, items := range pages {
		pageURL := fmt.Sprintf("%s/catalog/page%d.json", fakeBase, i)
		var list []map[string]string
		last := ""
		for _, it := range items {
			kind := "nuget:PackageDetails"
			if it.del {
				kind = "nuget:PackageDelete"
			}
			list = append(list, map[string]string{
				"@id":   fmt.Sprintf("%s/catalog/data/%s/%s.%s.json", fakeBase, it.stamp, strings.ToLower(it.id), it.version),
				"@type": kind, "commitTimeStamp": it.stamp, "nuget:id": it.id, "nuget:version": it.version,
			})
			if it.stamp > last {
				last = it.stamp
			}
		}
		f.pages[pageURL], _ = json.Marshal(map[string]any{"items": list})
		idx = append(idx, map[string]string{"@id": pageURL, "commitTimeStamp": last})
	}
	f.pages[fakeCatLog], _ = json.Marshal(map[string]any{"items": idx})
}

func admit(prefixes ...string) discover.Scope {
	return discover.Scope{Admits: func(name string) bool {
		for _, p := range prefixes {
			if strings.HasPrefix(name, p) {
				return true
			}
		}
		return false
	}}
}

func lister(t testing.TB) discover.Lister {
	t.Helper()
	l, ok := New().Remote().(discover.Lister)
	if !ok {
		t.Fatal("the remote adapter of NuGet is no discover.Lister")
	}
	return l
}

// walk repeats MirrorNames with the cursor it gets until Done, as the engine does, and returns the pages.
func walk(t testing.TB, l discover.Lister, u discover.Upstream, rq discover.PageRequest) []discover.Page {
	t.Helper()
	var pages []discover.Page
	for range 100 {
		p, err := l.MirrorNames(context.Background(), u, rq)
		if err != nil {
			t.Fatal(err)
		}
		pages = append(pages, p)
		if p.Done {
			return pages
		}
		rq.Cursor = p.Cursor
	}
	t.Fatal("the pass does not end")
	return nil
}

func TestR_REPO_050_NuGetRemoteImplementsTheMirrorLister(t *testing.T) {
	if formats.CapabilitiesOf(New()).Remote.Mirror == nil {
		t.Fatal("the remote capabilities of NuGet hold no lister")
	}
	if !formats.CatalogEntry(New()).Mirror {
		t.Fatal("the catalog fact Mirror of NuGet is false")
	}
}

func TestR_REPO_051_MethodsAreWhatTheServiceIndexOffers(t *testing.T) {
	l := lister(t)
	for _, tc := range []struct {
		name            string
		catalog, search bool
		want            []discover.Method
	}{
		{"catalog and search", true, true, []discover.Method{discover.MethodFeed, discover.MethodListing, discover.MethodNames}},
		{"search only", false, true, []discover.Method{discover.MethodListing, discover.MethodNames}},
		{"neither", false, false, []discover.Method{discover.MethodNames}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := l.MirrorMethods(context.Background(), newFakeUpstream(tc.catalog, tc.search))
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("%v, %v; want %v", got, err, tc.want)
			}
		})
	}
	t.Run("no service index", func(t *testing.T) {
		f := newFakeUpstream(true, true)
		delete(f.docs, ".nuget/index.json")
		got, err := l.MirrorMethods(context.Background(), f)
		if err != nil || !slices.Equal(got, []discover.Method{discover.MethodNames}) {
			t.Fatalf("%v, %v", got, err)
		}
	})
	t.Run("a method the upstream does not offer", func(t *testing.T) {
		f := newFakeUpstream(false, false)
		for _, m := range []discover.Method{discover.MethodFeed, discover.MethodListing} {
			_, err := l.MirrorNames(context.Background(), f, discover.PageRequest{Method: m, Scope: admit("")})
			if !errors.Is(err, discover.ErrMethodUnavailable) {
				t.Errorf("%s: %v", m, err)
			}
		}
		if len(f.gets) != 0 {
			t.Errorf("requests %v", f.gets)
		}
	})
	if h := l.MirrorHosts(nil); len(h) != 0 {
		t.Errorf("hosts %v: the catalog and the search are the upstream's own", h)
	}
}

const (
	t1 = "2024-01-01T10:00:00.1234567Z"
	t2 = "2024-01-02T10:00:00.1234567Z"
	t3 = "2024-01-03T10:00:00.1234567Z"
	t4 = "2024-01-04T10:00:00.1234567Z"
)

// The criterion of nuget.md: three pages naming Acme.A (two versions), Acme.B and Other.C; the full pass names Acme.A and
// Acme.B and asks for no page of Other.C and no leaf.
func TestR_REPO_051_AFullCatalogPassReadsTheIndexAndEveryPageAndNoLeaf(t *testing.T) {
	f := newFakeUpstream(true, false)
	f.setCatalog(
		[]catalogItem{{"Acme.A", "1.0.0", t1, false}, {"Acme.A", "2.0.0", t1, false}},
		[]catalogItem{{"Acme.B", "1.0.0", t2, false}},
		[]catalogItem{{"Other.C", "1.0.0", t3, false}},
	)
	pages := walk(t, lister(t), f, discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassFull, Scope: admit("acme."), Limit: 100})
	if len(pages) != 1 {
		t.Fatalf("%d pages", len(pages))
	}
	p := pages[0]
	if !slices.Equal(p.Names, []string{"acme.a", "acme.b"}) || len(p.Removed) != 0 || !p.Done || p.Truncated {
		t.Fatalf("%+v", p)
	}
	if string(p.Cursor) != `{"commit":"`+t3+`"}` {
		t.Errorf("cursor %s: the last commit read, even one whose names are refused", p.Cursor)
	}
	if len(f.gets) != 4 || f.catalogRequests() != 4 {
		t.Errorf("requests %v: the index and three pages", f.gets)
	}
	for _, g := range f.gets {
		if strings.Contains(g, "/data/") {
			t.Errorf("a leaf was read: %s", g)
		}
	}
}

// The second criterion: a new page with Acme.A 3.0.0 and a PackageDelete of Acme.B 1.0.0; the incremental pass reads the
// index and the new page only.
func TestR_REPO_051_AnIncrementalCatalogPassReadsOnlyTheNewPages(t *testing.T) {
	f := newFakeUpstream(true, false)
	first := [][]catalogItem{
		{{"Acme.A", "1.0.0", t1, false}, {"Acme.A", "2.0.0", t1, false}},
		{{"Acme.B", "1.0.0", t2, false}},
	}
	f.setCatalog(first...)
	l := lister(t)
	rq := discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassFull, Scope: admit("acme."), Limit: 100}
	since := walk(t, l, f, rq)[0].Cursor

	f.gets = nil
	f.setCatalog(append(first, []catalogItem{{"Acme.A", "3.0.0", t3, false}, {"Acme.B", "1.0.0", t3, true}})...)
	rq.Kind, rq.Since = discover.PassIncremental, since
	pages := walk(t, l, f, rq)
	p := pages[len(pages)-1]
	if !slices.Equal(p.Names, []string{"acme.a"}) || !slices.Equal(p.Removed, []string{"acme.b"}) || !p.Done {
		t.Fatalf("%+v", p)
	}
	if string(p.Cursor) != `{"commit":"`+t3+`"}` {
		t.Errorf("cursor %s", p.Cursor)
	}
	if want := []string{fakeCatLog, fakeBase + "/catalog/page2.json"}; !slices.Equal(f.gets, want) {
		t.Errorf("requests %v, want %v", f.gets, want)
	}

	// Nothing new: the cursor stays and the pass is done after the index alone.
	f.gets = nil
	rq.Since = p.Cursor
	p = walk(t, l, f, rq)[0]
	if len(p.Names)+len(p.Removed) != 0 || !p.Done || string(p.Cursor) != `{"commit":"`+t3+`"}` || len(f.gets) != 1 {
		t.Errorf("%+v after %v", p, f.gets)
	}
}

func TestR_REPO_051_TheCatalogIsReadInCommitOrderWhateverTheOrderOfPagesAndItems(t *testing.T) {
	f := newFakeUpstream(true, false)
	f.setCatalog(
		[]catalogItem{{"Acme.D", "1.0.0", t3, false}, {"Acme.C", "1.0.0", t2, false}}, // a page whose items are out of order
		[]catalogItem{{"Acme.B", "1.0.0", t1, false}},                                 // an earlier page listed later
	)
	rq := discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassFull, Scope: admit("acme."), Limit: 1}
	var got []string
	var cursors []string
	for _, p := range walk(t, lister(t), f, rq) {
		got = append(got, p.Names...)
		cursors = append(cursors, string(p.Cursor))
	}
	if !slices.Equal(got, []string{"acme.b", "acme.c", "acme.d"}) {
		t.Fatalf("names %v", got)
	}
	// With a page of one name, the cursor is the commit of the last commit queued whole.
	if want := `{"commit":"` + t1 + `","page":"` + fakeBase + `/catalog/page0.json"}`; cursors[0] != want {
		t.Errorf("first cursor %s, want %s", cursors[0], want)
	}
}

// A page of the engine that is smaller than a commit: the commit is taken in parts, in the order of the name, and every
// name is taken once; repeating a call gives the same page (idempotence).
func TestR_REPO_051_ACommitLargerThanAPageIsTakenInParts(t *testing.T) {
	f := newFakeUpstream(true, false)
	f.setCatalog([]catalogItem{
		{"Acme.E", "1.0.0", t1, false},
		{"Acme.C", "1.0.0", t1, false},
		{"Acme.D", "1.0.0", t1, false},
		{"Acme.A", "1.0.0", t1, false},
		{"Acme.B", "1.0.0", t1, false},
		{"Acme.A", "2.0.0", t1, false},
		{"Acme.F", "1.0.0", t2, false},
	})
	l := lister(t)
	rq := discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassFull, Scope: admit("acme."), Limit: 2}
	pages := walk(t, l, f, rq)
	var got []string
	for _, p := range pages {
		if len(p.Names) > 2 {
			t.Errorf("page %+v exceeds the limit", p)
		}
		got = append(got, p.Names...)
	}
	if !slices.Equal(got, []string{"acme.a", "acme.b", "acme.c", "acme.d", "acme.e", "acme.f"}) {
		t.Fatalf("names %v", got)
	}
	if want := `{"page":"` + fakeBase + `/catalog/page0.json","skip":2}`; string(pages[0].Cursor) != want {
		t.Errorf("first cursor %s, want %s", pages[0].Cursor, want)
	}
	again, err := l.MirrorNames(context.Background(), f, discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassFull, Scope: rq.Scope, Limit: 2, Cursor: pages[1].Cursor})
	if err != nil || fmt.Sprint(again) != fmt.Sprint(pages[2]) {
		t.Errorf("a repeated call: %+v, %v; want %+v", again, err, pages[2])
	}
}

func TestR_REPO_051_APackageDeleteIsRemovedUnlessTheSameCallNamesItAgain(t *testing.T) {
	f := newFakeUpstream(true, false)
	f.setCatalog([]catalogItem{
		{"Acme.Gone", "1.0.0", t1, true},
		{"Acme.Back", "1.0.0", t1, true},
		{"Acme.Back", "2.0.0", t2, false}, // deleted, then published again
		{"Acme.Both", "1.0.0", t3, true},
		{"Acme.Both", "2.0.0", t3, false}, // one commit: a delete and a new version
	})
	p := walk(t, lister(t), f, discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassFull, Scope: admit("acme."), Limit: 100})[0]
	if !slices.Equal(p.Names, []string{"acme.back", "acme.both"}) || !slices.Equal(p.Removed, []string{"acme.gone"}) {
		t.Fatalf("%+v", p)
	}
}

func TestR_REPO_051_ARateLimitAnswerIsReturnedUnchanged(t *testing.T) {
	l := lister(t)
	rl := &rateLimit{}
	t.Run("the catalog", func(t *testing.T) {
		f := newFakeUpstream(true, true)
		f.setCatalog([]catalogItem{{"Acme.A", "1.0.0", t1, false}})
		f.getErr = fmt.Errorf("get: %w", rl)
		_, err := l.MirrorNames(context.Background(), f, discover.PageRequest{Method: discover.MethodFeed, Scope: admit("acme.")})
		if got, ok := discover.AsRateLimited(err); !ok || got != discover.RateLimited(rl) {
			t.Fatalf("%v", err)
		}
		if len(f.gets) != 1 {
			t.Errorf("requests after the answer: %v", f.gets)
		}
	})
	t.Run("the search", func(t *testing.T) {
		f := newFakeUpstream(true, true)
		f.getErr = rl
		_, err := l.MirrorNames(context.Background(), f, discover.PageRequest{Method: discover.MethodListing, Scope: admit("acme.")})
		if !errors.Is(err, rl) {
			t.Fatalf("%v", err)
		}
	})
	t.Run("a document of the plan", func(t *testing.T) {
		f := newFakeUpstream(true, true)
		f.docErr["acme.a/index.json"] = rl
		_, err := l.MirrorPlan(context.Background(), f, "acme.a")
		if _, ok := discover.AsRateLimited(err); !ok {
			t.Fatalf("%v", err)
		}
	})
}

type rateLimit struct{}

func (*rateLimit) Error() string                     { return "rate limited" }
func (*rateLimit) RetryAfter() (time.Duration, bool) { return 30 * time.Second, true }
func (*rateLimit) RateLimitReason() string           { return "too_many_requests" }

// setSearch installs a search resource with total hits whose rows are ids(i), refusing a skip of cap or more (cap 0: none).
func (f *fakeUpstream) setSearch(total, cap int, ids func(i int) string) {
	f.search = func(q url.Values) (int, []byte) {
		skip, _ := strconv.Atoi(q.Get("skip"))
		take, _ := strconv.Atoi(q.Get("take"))
		if q.Get("take") != "1000" || q.Get("prerelease") != "true" || q.Get("semVerLevel") != "2.0.0" || q.Has("q") && q.Get("q") != "" {
			return http.StatusBadRequest, nil
		}
		if cap > 0 && skip >= cap {
			return http.StatusBadRequest, []byte(`{"error":"skip"}`)
		}
		var data []map[string]string
		for i := skip; i < min(skip+take, total); i++ {
			data = append(data, map[string]string{"id": ids(i)})
		}
		body, _ := json.Marshal(map[string]any{"totalHits": total, "data": data})
		return http.StatusOK, body
	}
}

func TestR_REPO_051_AListingPagesBySkipUntilTheHitsAreRead(t *testing.T) {
	f := newFakeUpstream(false, true)
	f.setSearch(2300, 0, func(i int) string { return fmt.Sprintf("Acme.P%04d", i) })
	pages := walk(t, lister(t), f, discover.PageRequest{Method: discover.MethodListing, Kind: discover.PassFull, Scope: admit("acme."), Limit: 5000})
	var n int
	for _, p := range pages {
		n += len(p.Names)
	}
	last := pages[len(pages)-1]
	if n != 2300 || len(pages) != 3 || !last.Done || last.Truncated || last.Note != "" || last.Cursor != nil {
		t.Fatalf("%d names in %d pages, last %+v", n, len(pages), last)
	}
	if q, _ := url.Parse(f.gets[1]); q.Query().Get("skip") != "1000" || q.Query().Get("take") != "1000" {
		t.Errorf("second request %s", f.gets[1])
	}
}

// The third criterion: a search that refuses skip from 2,000 for 2,500 hits; the pass completes as truncated and the note
// names 2,000 of 2,500, without an address.
func TestR_REPO_051_ACappedListingCompletesTruncatedWithANote(t *testing.T) {
	f := newFakeUpstream(false, true)
	f.setSearch(2500, 2000, func(i int) string { return fmt.Sprintf("Acme.P%04d", i) })
	pages := walk(t, lister(t), f, discover.PageRequest{Method: discover.MethodListing, Kind: discover.PassFull, Scope: admit("acme."), Limit: 5000})
	var n int
	for _, p := range pages {
		n += len(p.Names)
	}
	last := pages[len(pages)-1]
	if n != 2000 || !last.Done || !last.Truncated || last.Note != "listing truncated at 2000 of 2500" || last.Cursor != nil {
		t.Fatalf("%d names, last %+v", n, last)
	}
	if strings.Contains(last.Note, "http") || len(last.Note) > 256 {
		t.Errorf("note %q", last.Note)
	}
}

func TestR_REPO_051_AListingThatReturnsNothingBeforeItsHitsIsTruncated(t *testing.T) {
	f := newFakeUpstream(false, true)
	f.search = func(q url.Values) (int, []byte) {
		if q.Get("skip") != "0" {
			return http.StatusOK, []byte(`{"totalHits":1500,"data":[]}`)
		}
		var data []map[string]string
		for i := 0; i < 1000; i++ {
			data = append(data, map[string]string{"id": fmt.Sprintf("Acme.P%04d", i)})
		}
		body, _ := json.Marshal(map[string]any{"totalHits": 1500, "data": data})
		return http.StatusOK, body
	}
	pages := walk(t, lister(t), f, discover.PageRequest{Method: discover.MethodListing, Scope: admit("acme."), Limit: 5000})
	if last := pages[len(pages)-1]; !last.Truncated || last.Note != "listing truncated at 1000 of 1500" {
		t.Fatalf("%+v", last)
	}
}

// A page of the engine smaller than the answer: the listing reads on from the row after the last one taken, and refuses
// every name the scope refuses.
func TestR_REPO_051_AListingReturnsAdmittedNormalizedNamesInPagesOfTheLimit(t *testing.T) {
	f := newFakeUpstream(false, true)
	ids := []string{"Acme.One", "Other.Two", "ACME.Three", "Acme.One", "Acme.Four", "bad id", "Acme.Five"}
	f.setSearch(len(ids), 0, func(i int) string { return ids[i] })
	pages := walk(t, lister(t), f, discover.PageRequest{Method: discover.MethodListing, Scope: admit("acme."), Limit: 2})
	var got []string
	for _, p := range pages {
		if len(p.Names) > 2 {
			t.Errorf("page %+v exceeds the limit", p)
		}
		got = append(got, p.Names...)
	}
	// A name may appear in several pages (discover.Page): the second page's row 3 is the first page's row 0 again.
	if want := []string{"acme.one", "acme.three", "acme.one", "acme.four", "acme.five"}; !slices.Equal(got, want) {
		t.Fatalf("names %v, want %v", got, want)
	}
}

func TestR_REPO_051_NamesAreTheIncludePatternsWithoutAWildcard(t *testing.T) {
	scope := admit("acme.", "newtonsoft.")
	scope.Include = []string{"Acme.Widgets", "Acme.*", "Newtonsoft.Json", "Other.Pkg", "acme.widgets", `Acme.Esc\*aped`, "", "Newtonsoft.Bad?"}
	f := newFakeUpstream(false, false)
	p := walk(t, lister(t), f, discover.PageRequest{Method: discover.MethodNames, Scope: scope, Limit: 10})[0]
	// The escaped asterisk is part of the name, and no ID has one: the name rule refuses it.
	if want := []string{"acme.widgets", "newtonsoft.json"}; !slices.Equal(p.Names, want) {
		t.Fatalf("%+v, want %v", p, want)
	}
	if !p.Done || p.Cursor != nil || len(f.gets)+len(f.docReqs) != 0 {
		t.Errorf("%+v, requests %v %v", p, f.gets, f.docReqs)
	}
	pages := walk(t, lister(t), f, discover.PageRequest{Method: discover.MethodNames, Scope: scope, Limit: 1})
	if len(pages) != 2 || !slices.Equal(pages[0].Names, []string{"acme.widgets"}) || !slices.Equal(pages[1].Names, []string{"newtonsoft.json"}) {
		t.Errorf("%+v", pages)
	}
}

func TestR_REPO_051_NoNameThatTheScopeRefusesIsReturned(t *testing.T) {
	f := newFakeUpstream(true, true)
	f.setCatalog([]catalogItem{{"Acme.A", "1.0.0", t1, false}, {"Acme.B", "1.0.0", t1, true}})
	f.setSearch(2, 0, func(i int) string { return []string{"Acme.A", "Acme.B"}[i] })
	none := discover.Scope{Include: []string{"Acme.A"}, Admits: func(string) bool { return false }}
	unchecked := discover.Scope{}
	for _, m := range []discover.Method{discover.MethodFeed, discover.MethodListing, discover.MethodNames} {
		for _, sc := range []discover.Scope{none, unchecked} {
			p, err := lister(t).MirrorNames(context.Background(), f, discover.PageRequest{Method: m, Scope: sc})
			if err != nil || len(p.Names)+len(p.Removed) != 0 {
				t.Errorf("%s: %+v, %v", m, p, err)
			}
		}
	}
}

// A plan lists the version list and the registration through the remote engine and stores the .nupkg of every version.
func TestR_REPO_051_ThePlanIsTheVersionListNewestFirstWithAPathAndAPrereleaseFlagPerVersion(t *testing.T) {
	f := newFakeUpstream(true, true)
	f.docs["acme.a/index.json"] = []byte(`{"versions":["1.0.0","2.0.0-beta.1","1.10.0","2.0.0","1.2.0+build5","1.0.0.0","not a version","1.9.0-rc.2","1.9.0-rc.10"]}`)
	f.docs["acme.a/.registration/index.json"] = []byte(`{"items":[
	 {"@id":"x#page/1.0.0/1.9.0","lower":"1.0.0","upper":"1.9.0","items":[
	   {"catalogEntry":{"version":"1.0.0","packageSize":1234,"listed":true}},
	   {"catalogEntry":{"version":"1.2.0","listed":false}}]},
	 {"@id":"https://feed.test/reg/acme.a/page/1.10.0/2.0.0-beta.1.json","lower":"1.10.0","upper":"2.0.0-beta.1"}]}`)
	f.docs["acme.a/.registration/page/1.10.0/2.0.0-beta.1.json"] = []byte(`{"items":[
	   {"catalogEntry":{"version":"2.0.0-beta.1","packageSize":99}},{"catalogEntry":{"version":"1.10.0","packageSize":-4}}]}`)
	plan, err := lister(t).MirrorPlan(context.Background(), f, "Acme.A")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Name != "acme.a" {
		t.Errorf("name %q", plan.Name)
	}
	type row struct {
		v    string
		pre  bool
		size int64
	}
	var got []row
	for _, it := range plan.Items {
		got = append(got, row{it.Version, it.Prerelease, it.Size})
		if want := "acme.a/" + it.Version + "/acme.a." + it.Version + ".nupkg"; it.Path != want || it.Pinned {
			t.Errorf("item %+v, want path %s unpinned", it, want)
		}
	}
	want := []row{{"2.0.0", false, 0}, {"2.0.0-beta.1", true, 99}, {"1.10.0", false, 0}, {"1.9.0-rc.10", true, 0}, {"1.9.0-rc.2", true, 0}, {"1.2.0", false, 0}, {"1.0.0", false, 1234}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("items\n%v\nwant\n%v", got, want)
	}
	if wantDocs := []string{"acme.a/index.json|doc", "acme.a/.registration/index.json|doc", "acme.a/.registration/page/1.10.0/2.0.0-beta.1.json|doc"}; !slices.Equal(f.docReqs, wantDocs) {
		t.Errorf("documents %v, want %v", f.docReqs, wantDocs)
	}
	if len(f.gets) != 0 {
		t.Errorf("the plan sent requests of its own: %v", f.gets)
	}
}

func TestR_REPO_051_AnUnlistedVersionIsPlannedLikeAListedOne(t *testing.T) {
	f := newFakeUpstream(true, true)
	f.docs["acme.a/index.json"] = []byte(`{"versions":["1.0.0","1.1.0"]}`)
	f.docs["acme.a/.registration/index.json"] = []byte(`{"items":[{"@id":"x#page","lower":"1.0.0","upper":"1.1.0","items":[
	  {"catalogEntry":{"version":"1.0.0","listed":false}},{"catalogEntry":{"version":"1.1.0","listed":true}}]}]}`)
	plan, err := lister(t).MirrorPlan(context.Background(), f, "acme.a")
	if err != nil || len(plan.Items) != 2 || plan.Items[1].Version != "1.0.0" {
		t.Fatalf("%+v, %v", plan, err)
	}
}

func TestR_REPO_051_APlanOfAnIDTheUpstreamDoesNotHaveIsEmptyAndNoError(t *testing.T) {
	l := lister(t)
	f := newFakeUpstream(true, true)
	for _, name := range []string{"acme.missing", "Acme.Missing"} {
		plan, err := l.MirrorPlan(context.Background(), f, name)
		if err != nil || len(plan.Items) != 0 || plan.Name != "acme.missing" {
			t.Errorf("%s: %+v, %v", name, plan, err)
		}
	}
	if len(f.docReqs) != 2 {
		t.Errorf("documents %v: the version list only", f.docReqs)
	}
	// A registration that is missing leaves the plan standing on the version list.
	f.docs["acme.nop/index.json"] = []byte(`{"versions":["1.0.0"]}`)
	if plan, err := l.MirrorPlan(context.Background(), f, "acme.nop"); err != nil || len(plan.Items) != 1 || plan.Items[0].Size != 0 {
		t.Errorf("%+v, %v", plan, err)
	}
	// A name that is no ID gets no request.
	f.docReqs = nil
	for _, name := range []string{"", "a/b", "bad id", "..", "ünïKode"} {
		plan, err := l.MirrorPlan(context.Background(), f, name)
		if err != nil || len(plan.Items) != 0 {
			t.Errorf("%q: %+v, %v", name, plan, err)
		}
	}
	if len(f.docReqs) != 0 {
		t.Errorf("documents requested for names that are no IDs: %v", f.docReqs)
	}
}

func TestR_REPO_051_APlanFailsOnADocumentThatIsNoVersionList(t *testing.T) {
	f := newFakeUpstream(true, true)
	f.docs["acme.bad/index.json"] = []byte(`<html>`)
	if _, err := lister(t).MirrorPlan(context.Background(), f, "acme.bad"); err == nil {
		t.Fatal("a plan of a broken version list")
	}
}

// The catalog resource is read for a mirror only: a bad address leaves it out and never fails the service index that clients
// restore through.
func TestR_REPO_051_ABadCatalogAddressNeverFailsTheServiceIndex(t *testing.T) {
	const head = `{"resources":[{"@id":"https://x.test/reg/","@type":"RegistrationsBaseUrl/3.6.0"},{"@id":"https://x.test/flat/","@type":"PackageBaseAddress/3.0.0"},`
	d, err := remote.ParseDiscovery([]byte(head + `{"@id":"ftp://x.test/catalog/index.json","@type":"Catalog/3.0.0"}]}`))
	if err != nil || d.Catalog != nil || d.CatalogURL() != nil {
		t.Fatalf("%+v, %v", d, err)
	}
	d, err = remote.ParseDiscovery([]byte(head + `{"@id":"https://x.test/catalog/index.json?x=1","@type":["Catalog/3.0.0"]}]}`))
	if err != nil || d.CatalogURL() == nil || d.CatalogURL().String() != "https://x.test/catalog/index.json" {
		t.Fatalf("%+v, %v", d, err)
	}
}
