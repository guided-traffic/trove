package nugetstub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/formats/nuget"
	"github.com/guided-traffic/trove/internal/formats/nuget/remote"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// The format's discovery against the stub over HTTP: the criteria of nuget.md "Mirror discovery" (R-REPO-051) with the NuGet
// stub of R-REPO-059, before the engine exists. The upstream adapter below is what the engine's one will do for these calls: Get
// is a GET, Document maps a canonical path of the repository to the resource the service index announces.

type httpUpstream struct {
	f     *fixture
	index *remote.Discovery
}

func (u *httpUpstream) Base() *url.URL { b, _ := url.Parse(u.f.api + "/v3/index.json"); return b }

func (u *httpUpstream) Settings() json.RawMessage { return nil }

func (u *httpUpstream) Get(_ context.Context, target *url.URL, accept string, _ int64) (*discover.Response, error) {
	r := u.f.do(http.MethodGet, target.String(), map[string]string{"Accept": accept}, nil)
	return &discover.Response{Status: r.status, Header: r.header, Body: r.body}, nil
}

func (u *httpUpstream) Document(_ context.Context, path, _ string) ([]byte, bool, error) {
	if path == ".nuget/index.json" {
		r := u.f.get(u.f.api + "/v3/index.json")
		return r.body, r.status == http.StatusOK, nil
	}
	if u.index == nil {
		d, err := remote.ParseDiscovery(u.f.get(u.f.api + "/v3/index.json").body)
		if err != nil {
			return nil, false, err
		}
		u.index = d
	}
	id, rest, _ := strings.Cut(path, "/")
	var target string
	switch {
	case rest == "index.json":
		target = u.index.Content.String() + id + "/index.json"
	case strings.HasPrefix(rest, ".registration/"):
		target = u.index.Registration.String() + id + "/" + strings.TrimPrefix(rest, ".registration/")
	default:
		return nil, false, fmt.Errorf("no document at %s", path)
	}
	r := u.f.get(target)
	return r.body, r.status == http.StatusOK, nil
}

func acmeScope(prefix string) discover.Scope {
	return discover.Scope{Admits: func(n string) bool { return strings.HasPrefix(n, prefix) }}
}

func nugetLister(t *testing.T) discover.Lister {
	t.Helper()
	l, ok := nuget.New().Remote().(discover.Lister)
	if !ok {
		t.Fatal("no lister")
	}
	return l
}

func drain(t *testing.T, l discover.Lister, u discover.Upstream, rq discover.PageRequest) (names, removed []string, last discover.Page) {
	t.Helper()
	for range 50 {
		p, err := l.MirrorNames(context.Background(), u, rq)
		if err != nil {
			t.Fatal(err)
		}
		names, removed, last = append(names, p.Names...), append(removed, p.Removed...), p
		if p.Done {
			return
		}
		rq.Cursor = p.Cursor
	}
	t.Fatal("the pass does not end")
	return
}

func TestR_REPO_051_TheCatalogOfTheStubIsDiscoveredFullAndThenIncrementally(t *testing.T) {
	f := newFixture(t, Config{Corpus: acmeCorpus(), CatalogPageSize: 1})
	f.setMode(Mode{Catalog: true})
	u, l := &httpUpstream{f: f}, nugetLister(t)
	methods, err := l.MirrorMethods(context.Background(), u)
	if err != nil || !slices.Equal(methods, []discover.Method{discover.MethodFeed, discover.MethodListing, discover.MethodNames}) {
		t.Fatalf("%v, %v", methods, err)
	}
	f.stub.Reset()

	rq := discover.PageRequest{Method: discover.MethodFeed, Kind: discover.PassFull, Scope: acmeScope("acme."), Limit: 100}
	names, removed, last := drain(t, l, u, rq)
	if !slices.Equal(names, []string{"acme.a", "acme.b"}) || len(removed) != 0 || last.Truncated {
		t.Fatalf("names %v removed %v", names, removed)
	}
	c := f.counts()
	if c.CatalogIndexes() != 1 || c.CatalogPages() != 3 || c.CatalogLeaves() != 0 {
		t.Errorf("requests: %d index, %d pages, %d leaves: the index and the three pages, no leaf", c.CatalogIndexes(), c.CatalogPages(), c.CatalogLeaves())
	}

	// A new page with Acme.A 3.0.0 and a delete of Acme.B 1.0.0: the next pass reads only what is new.
	if err := f.stub.Publish("Acme.A", "3.0.0", nil); err != nil {
		t.Fatal(err)
	}
	if err := f.stub.Drop("Acme.B", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	f.stub.Reset()
	rq.Kind, rq.Since = discover.PassIncremental, last.Cursor
	names, removed, last2 := drain(t, l, u, rq)
	if !slices.Equal(names, []string{"acme.a"}) || !slices.Equal(removed, []string{"acme.b"}) {
		t.Fatalf("names %v removed %v", names, removed)
	}
	c = f.counts()
	if c.CatalogIndexes() != 1 || c.CatalogPages() != 2 || c.CatalogLeaves() != 0 {
		t.Errorf("incremental requests: %d index, %d pages, %d leaves", c.CatalogIndexes(), c.CatalogPages(), c.CatalogLeaves())
	}
	if string(last2.Cursor) == string(last.Cursor) {
		t.Error("the cursor did not move")
	}

	// The plan of the ID that gained a version: the version list, the registration and one nupkg path per version.
	plan, err := l.MirrorPlan(context.Background(), u, "acme.a")
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, it := range plan.Items {
		paths = append(paths, it.Path)
		if it.Size <= 0 {
			t.Errorf("%s: the stub's registration announces a size", it.Path)
		}
	}
	if want := []string{"acme.a/3.0.0/acme.a.3.0.0.nupkg", "acme.a/2.0.0/acme.a.2.0.0.nupkg", "acme.a/1.0.0/acme.a.1.0.0.nupkg"}; !slices.Equal(paths, want) {
		t.Errorf("paths %v, want %v", paths, want)
	}
	// The ID that was withdrawn has an empty plan.
	if plan, err := l.MirrorPlan(context.Background(), u, "acme.b"); err != nil || len(plan.Items) != 0 {
		t.Errorf("%+v, %v", plan, err)
	}
	if n := f.counts().Searches(); n != 0 {
		t.Errorf("searches %d", n)
	}
}

func TestR_REPO_051_AStubWithoutACatalogAndACappedSearchGivesATruncatedListing(t *testing.T) {
	c := NewCorpus()
	for i := range 2500 {
		p := &Package{ID: fmt.Sprintf("Acme.P%04d", i)}
		p.AddVersion("1.0.0", catalogEpoch.Add(time.Duration(i)*time.Minute))
		c.Add(p)
	}
	f := newFixture(t, Config{Corpus: c})
	f.setMode(Mode{SearchSkipCap: 2000})
	u, l := &httpUpstream{f: f}, nugetLister(t)
	methods, err := l.MirrorMethods(context.Background(), u)
	if err != nil || !slices.Equal(methods, []discover.Method{discover.MethodListing, discover.MethodNames}) {
		t.Fatalf("%v, %v", methods, err)
	}
	if _, err := l.MirrorNames(context.Background(), u, discover.PageRequest{Method: discover.MethodFeed, Scope: acmeScope("acme.")}); err == nil {
		t.Fatal("the feed of a stub without a catalog")
	}
	names, _, last := drain(t, l, u, discover.PageRequest{Method: discover.MethodListing, Kind: discover.PassFull, Scope: acmeScope("acme."), Limit: 5000})
	if len(names) != 2000 || !last.Truncated || last.Note != "listing truncated at 2000 of 2500" {
		t.Fatalf("%d names, last %+v", len(names), last)
	}
	if n := f.counts().CatalogIndexes(); n != 0 {
		t.Errorf("catalog requests %d", n)
	}
}
