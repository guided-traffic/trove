package view

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/repository"
)

func TestR_API_029_PackageIDRoundTrips(t *testing.T) {
	for _, c := range []struct{ format, name string }{
		{"npm", "left-pad"},
		{"npm", "@team-a/ui"},
		{"nuget", "Contoso.Logging"},
		{"maven", "org.acme:lib"},
		{"npm", strings.Repeat("a", 214)},
		{"npm", "ünï/cödé"},
	} {
		id := ID(c.format, c.name)
		if !strings.HasPrefix(id, "pkg_") || strings.ToLower(id) != id {
			t.Errorf("%s: %q", c.name, id)
		}
		for _, r := range id[4:] {
			if (r < 'a' || r > 'z') && (r < '2' || r > '7') {
				t.Errorf("%s: %q holds %q, outside [0-9A-Za-z]", c.name, id, r)
			}
		}
		f, n, ok := ParseID(id)
		if !ok || f != c.format || n != c.name {
			t.Errorf("%q parses to %q %q %v", id, f, n, ok)
		}
	}
	if ID("npm", "a") == ID("nuget", "a") || ID("npm", "ab") == ID("npm", "a") {
		t.Error("two packages share an id")
	}
}

func TestR_API_029_ParseIDRefusesWhatIsNoID(t *testing.T) {
	for _, bad := range []string{"", "pkg_", "pkg_!!!", "pkg_a", "npm", "lookup", ID("npm", "x")[1:], "pkg_" + "mfzwk", "pkg_01JBQ6V7S3K9"} {
		if f, n, ok := ParseID(bad); ok {
			t.Errorf("%q parses to %q %q", bad, f, n)
		}
	}
}

func repoOf(key string, class repository.Class) repository.Repository {
	return repository.Repository{Key: key, Class: class, Format: "npm"}
}

func TestR_API_029_TopologyExpandsVirtualRepositories(t *testing.T) {
	a, b, c := repoOf("npm-a", repository.Local), repoOf("npm-b", repository.Local), repoOf("npm-remote", repository.Remote)
	inner := repository.Assemble(repoOf("npm-inner", repository.Virtual), []repository.Repository{a, c}, nil, 0)
	outer := repository.Assemble(repoOf("npm-outer", repository.Virtual), []repository.Repository{inner, b}, nil, 0)
	empty := repository.Assemble(repoOf("npm-empty", repository.Virtual), nil, nil, 0)
	topo := NewTopology([]repository.Repository{a, b, c, inner, outer, empty})

	if got, want := topo.Physical(), []string{"npm-a", "npm-b", "npm-remote"}; !slices.Equal(got, want) {
		t.Errorf("Physical %v", got)
	}
	if got, want := topo.Virtual(), []string{"npm-empty", "npm-inner", "npm-outer"}; !slices.Equal(got, want) {
		t.Errorf("Virtual %v", got)
	}
	for _, tc := range []struct {
		in   []string
		want []string
	}{
		{[]string{"npm-a"}, []string{"npm-a"}},
		{[]string{"npm-inner"}, []string{"npm-a", "npm-remote"}},
		{[]string{"npm-outer"}, []string{"npm-a", "npm-b", "npm-remote"}},
		{[]string{"npm-outer", "npm-a", "npm-inner"}, []string{"npm-a", "npm-b", "npm-remote"}},
		{[]string{"npm-empty"}, nil},
		{[]string{"ghost"}, nil},
		{nil, nil},
	} {
		if got := topo.Expand(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("Expand(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
	if got := topo.Containers("npm-a"); !slices.Equal(got, []string{"npm-inner", "npm-outer"}) {
		t.Errorf("Containers(npm-a) = %v", got)
	}
	if got := topo.Containers("npm-b"); !slices.Equal(got, []string{"npm-outer"}) {
		t.Errorf("Containers(npm-b) = %v", got)
	}
	if c, ok := topo.Class("npm-remote"); !ok || c != repository.Remote {
		t.Errorf("Class %v %v", c, ok)
	}
}

// TestR_API_029_HoldersShowOnlyReadableVirtualRepositories: a version shows in the
// virtual repositories that contain its repository and that the caller may read, local
// repositories first.
func TestR_API_029_HoldersShowOnlyReadableVirtualRepositories(t *testing.T) {
	a, c := repoOf("npm-a", repository.Local), repoOf("npm-remote", repository.Remote)
	inner := repository.Assemble(repoOf("npm-all", repository.Virtual), []repository.Repository{a, c}, nil, 0)
	hidden := repository.Assemble(repoOf("npm-secret", repository.Virtual), []repository.Repository{a}, nil, 0)
	topo := NewTopology([]repository.Repository{a, c, inner, hidden})
	sc := Scope{topo: topo, virtualReadable: map[string]bool{"npm-all": true}}
	if got := sc.holders([]string{"npm-remote", "npm-a"}); !slices.Equal(got, []string{"npm-a", "npm-remote", "npm-all"}) {
		t.Fatalf("%v", got)
	}
	if got := sc.holders([]string{"npm-a"}); slices.Contains(got, "npm-secret") {
		t.Fatalf("a virtual repository the caller may not read is shown: %v", got)
	}
}

// fakeAdapter orders versions as dotted integers and calls a "-" suffix a pre-release.
type fakeAdapter struct{}

func (fakeAdapter) Compare(a, b string) int {
	pa, pb := strings.TrimSuffix(a, "-pre"), strings.TrimSuffix(b, "-pre")
	return strings.Compare(pad(pa), pad(pb))
}

func pad(v string) string {
	parts := strings.Split(v, ".")
	for i, p := range parts {
		parts[i] = strings.Repeat("0", 4-len(p)) + p
	}
	return strings.Join(parts, ".")
}
func (fakeAdapter) Prerelease(v string) bool { return strings.HasSuffix(v, "-pre") }
func (fakeAdapter) Describe(context.Context, []Version, Fields, func(string) bool) (map[int64]Data, error) {
	return nil, nil
}
func (fakeAdapter) Find(context.Context, Find, int) ([]int64, error) { return nil, nil }
func (fakeAdapter) Withheld(context.Context, []string) ([]int64, error) {
	return nil, nil
}

func rowsOf(versions ...string) []q.PackageViewVersionsRow {
	out := make([]q.PackageViewVersionsRow, len(versions))
	for i, v := range versions {
		out[i] = q.PackageViewVersionsRow{VersionID: int64(i + 1), Version: v}
	}
	return out
}

func TestR_API_029_LatestIsTheHighestStableVersion(t *testing.T) {
	for _, tc := range []struct {
		name     string
		versions []string
		want     string
	}{
		{"the highest of several", []string{"1.2.0", "1.10.0", "1.9.0"}, "1.10.0"},
		{"a pre-release above the stable one does not count", []string{"1.0.0", "2.0.0-pre", "1.5.0"}, "1.5.0"},
		{"only pre-releases: the highest of them", []string{"1.0.0-pre", "3.0.0-pre", "2.0.0-pre"}, "3.0.0-pre"},
		{"a single version", []string{"0.1.0"}, "0.1.0"},
		{"the pre-release comes first in the rows", []string{"9.0.0-pre", "1.0.0"}, "1.0.0"},
	} {
		got, ok := latestOf(fakeAdapter{}, rowsOf(tc.versions...))
		if !ok || got.Version != tc.want {
			t.Errorf("%s: %q %v, want %q", tc.name, got.Version, ok, tc.want)
		}
	}
	if _, ok := latestOf(fakeAdapter{}, nil); ok {
		t.Error("a package without a visible version has a latest version")
	}
}

func TestR_API_029_CursorKeysFollowTheSort(t *testing.T) {
	at := time.Date(2026, 10, 20, 9, 12, 44, 512000000, time.UTC)
	p := Package{Name: "@Team-A/UI", Key: "@team-a/ui", Format: "npm", CreatedAt: at, UpdatedAt: at.Add(time.Hour)}
	for _, tc := range []struct {
		order Order
		want  []string
	}{
		{Order{Sort: "name"}, []string{"@team-a/ui", "npm"}},
		{Order{Sort: "name", Desc: true}, []string{"@team-a/ui", "npm"}},
		{Order{Sort: "createdAt"}, []string{"2026-10-20T09:12:44.512Z", "@team-a/ui", "npm"}},
		{Order{Sort: "updatedAt", Desc: true}, []string{"2026-10-20T10:12:44.512Z", "@team-a/ui", "npm"}},
	} {
		key := tc.order.Key(p)
		if !slices.Equal(key, tc.want) {
			t.Errorf("%v: %v, want %v", tc.order, key, tc.want)
		}
		after, err := tc.order.ParseAfter(key)
		if err != nil || after.Name != "@team-a/ui" || after.Format != "npm" {
			t.Errorf("%v: %+v %v", tc.order, after, err)
		}
		if tc.order.Sort != "name" && !after.Time.Equal(map[string]time.Time{"createdAt": p.CreatedAt, "updatedAt": p.UpdatedAt}[tc.order.Sort]) {
			t.Errorf("%v: time %v", tc.order, after.Time)
		}
	}
	for _, bad := range [][]string{nil, {"a"}, {"not a time", "n", "f"}, {"2026-10-20T09:12:44Z", "n"}} {
		if _, err := (Order{Sort: "createdAt"}).ParseAfter(bad); !IsBadCursor(err) {
			t.Errorf("createdAt accepts %v", bad)
		}
	}
	if _, err := (Order{Sort: "name"}).ParseAfter([]string{"a", "b", "c"}); !IsBadCursor(err) {
		t.Error("name accepts three values")
	}
}

func TestR_API_029_NilSlicesAreSentAsEmptyArrays(t *testing.T) {
	if keys(nil) == nil || ids(nil) == nil {
		t.Fatal("a nil slice reaches the driver as NULL, and NOT (x = ANY(NULL)) drops every row")
	}
}
