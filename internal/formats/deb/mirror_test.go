package deb

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/formats/deb/rows"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// fakeMirror is the mirror part of the data port over fixed rows.
type fakeMirror struct {
	Store
	names   []string
	entries map[string][]rows.Entry
	synced  int
	scope   rows.MirrorScope
}

func (f *fakeMirror) MirrorSync(_ context.Context, _ discover.Upstream, sc rows.MirrorScope) ([]string, []string, error) {
	f.synced++
	f.scope = sc
	return sc.Suites, []string{"suite_unavailable: testing"}, nil
}

func (f *fakeMirror) MirrorNames(_ context.Context, _ rows.MirrorScope, after string, limit int) ([]string, error) {
	var out []string
	for _, n := range f.names {
		if n > after && len(out) < limit {
			out = append(out, n)
		}
	}
	return out, nil
}

func (f *fakeMirror) MirrorEntries(_ context.Context, _ rows.MirrorScope, name string) ([]rows.Entry, error) {
	return f.entries[name], nil
}

type fakeUpstream struct{ settings string }

func (fakeUpstream) Base() *url.URL { u, _ := url.Parse("https://deb.example/debian/"); return u }
func (u fakeUpstream) Settings() json.RawMessage {
	return json.RawMessage(u.settings)
}
func (fakeUpstream) RepositoryID() int64 { return 7 }
func (fakeUpstream) Get(context.Context, *url.URL, string, int64) (*discover.Response, error) {
	return nil, nil
}

func (fakeUpstream) Document(context.Context, string, string) ([]byte, bool, error) {
	return nil, false, nil
}

func (fakeUpstream) Artifact(context.Context, string, int64) ([]byte, bool, error) {
	return nil, false, nil
}

func mirrorAdapter(t *testing.T, st Store) discover.Lister {
	t.Helper()
	f := New()
	f.port.Store(&portBox{st})
	l, ok := f.Remote().(discover.Lister)
	if !ok {
		t.Fatal("the remote adapter is no discover.Lister")
	}
	return l
}

func admitAll(string) bool { return true }

func TestTheListerOffersListingAndNamesAndReadsTheScopeFromTheSettings(t *testing.T) {
	st := &fakeMirror{names: []string{"alpha", "beta"}}
	l := mirrorAdapter(t, st)
	ms, err := l.MirrorMethods(context.Background(), fakeUpstream{})
	if err != nil || !slices.Equal(ms, []discover.Method{discover.MethodListing, discover.MethodNames}) {
		t.Fatalf("methods = %v, %v", ms, err)
	}
	up := fakeUpstream{`{"mirror":{"suites":["stable","testing"],"components":["main","contrib"],"architectures":["arm64"],"translations":["de"],"sources":true}}`}
	page, err := l.MirrorNames(context.Background(), up, discover.PageRequest{Method: discover.MethodListing, Limit: 10, Scope: discover.Scope{Admits: admitAll}})
	if err != nil || !page.Done || !slices.Equal(page.Names, []string{"alpha", "beta"}) {
		t.Fatalf("page = %+v, %v", page, err)
	}
	sc := st.scope
	if sc.RepoID != 7 || !slices.Equal(sc.Suites, []string{"stable", "testing"}) || !slices.Equal(sc.Components, []string{"main", "contrib"}) ||
		!slices.Equal(sc.Architectures, []string{"arm64"}) || !slices.Equal(sc.Translations, []string{"de"}) || !sc.Sources {
		t.Errorf("scope = %+v", sc)
	}
	if page.Note != "suite_unavailable: testing" {
		t.Errorf("note = %q", page.Note)
	}
}

func TestTheIndexStepRunsOnTheFirstPageOnly(t *testing.T) {
	st := &fakeMirror{names: []string{"a1", "a2", "a3"}}
	l := mirrorAdapter(t, st)
	up := fakeUpstream{`{}`}
	rq := discover.PageRequest{Method: discover.MethodListing, Limit: 2, Scope: discover.Scope{Admits: admitAll}}
	first, err := l.MirrorNames(context.Background(), up, rq)
	if err != nil || first.Done || !slices.Equal(first.Names, []string{"a1", "a2"}) || len(first.Cursor) == 0 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	rq.Cursor = first.Cursor
	second, err := l.MirrorNames(context.Background(), up, rq)
	if err != nil || !second.Done || !slices.Equal(second.Names, []string{"a3"}) {
		t.Fatalf("second = %+v, %v", second, err)
	}
	if st.synced != 1 {
		t.Errorf("the index step ran %d times", st.synced)
	}
	rq.Cursor = json.RawMessage(`{"last":`)
	if _, err := l.MirrorNames(context.Background(), up, rq); err == nil {
		t.Error("a cursor the format did not write was accepted")
	}
}

func TestTheListerReturnsOnlyNamesTheScopeAdmitsAndTheExactNamesOfTheIncludePatterns(t *testing.T) {
	st := &fakeMirror{names: []string{"alpha", "beta", "Bad_Name"}}
	l := mirrorAdapter(t, st)
	up := fakeUpstream{`{}`}
	only := func(n string) bool { return n == "beta" || n == "Bad_Name" }
	page, _ := l.MirrorNames(context.Background(), up, discover.PageRequest{Method: discover.MethodListing, Limit: 10, Scope: discover.Scope{Admits: only}})
	if !slices.Equal(page.Names, []string{"beta"}) {
		t.Errorf("listing = %v: a name outside the grammar or the scope was returned", page.Names)
	}
	page, _ = l.MirrorNames(context.Background(), up, discover.PageRequest{
		Method: discover.MethodNames, Limit: 10,
		Scope: discover.Scope{Include: []string{"zeta", "alpha", "al*", "zeta"}, Admits: admitAll},
	})
	if !slices.Equal(page.Names, []string{"alpha", "zeta"}) || !page.Done {
		t.Errorf("names = %+v", page)
	}
	if _, err := l.MirrorNames(context.Background(), up, discover.PageRequest{Method: discover.MethodFeed, Scope: discover.Scope{Admits: admitAll}}); err == nil {
		t.Error("the feed method was offered")
	}
}

func TestThePlanListsEachFileOnceNewestVersionFirst(t *testing.T) {
	e := func(v, p string, size int64) rows.Entry { return rows.Entry{Version: v, Path: p, Size: size} }
	st := &fakeMirror{entries: map[string][]rows.Entry{"hello": {
		e("1.0-1", "pool/main/h/hello/hello_1.0-1_amd64.deb", 10), e("1:0.9-1", "pool/main/h/hello/hello_0.9-1_amd64.deb", 11),
		e("1.10-1", "pool/main/h/hello/hello_1.10-1_amd64.deb", 12), e("1.2-1", "pool/main/h/hello/hello_1.2-1_all.deb", 13),
		e("1.10-1", "pool/main/h/hello/hello_1.10-1_amd64.deb", 12),
	}}}
	plan, err := mirrorAdapter(t, st).MirrorPlan(context.Background(), fakeUpstream{`{}`}, "hello")
	if err != nil || plan.Name != "hello" {
		t.Fatal(plan, err)
	}
	var got []string
	for _, it := range plan.Items {
		got = append(got, it.Version)
	}
	if !slices.Equal(got, []string{"1:0.9-1", "1.10-1", "1.2-1", "1.0-1"}) {
		t.Errorf("order = %v (the epoch comes first, then dpkg order, one entry per file)", got)
	}
}
