package view

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/qname"
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

// testIDs are the database IDs the unit tests give their repositories.
var testIDs = map[string]string{
	"npm-a": "1", "npm-b": "2", "npm-remote": "3", "npm-inner": "4", "npm-outer": "5", "npm-empty": "6", "npm-all": "7", "npm-secret": "8",
}

func repoOf(key string, class repository.Class) repository.Repository {
	return repository.Repository{ID: testIDs[key], Name: qr(key), Class: class, Format: "npm"}
}

// idOf is the database ID of a test repository.
func idOf(topo Topology, key string) int64 { return topo.ID(qr(key)) }

func idsOf(topo Topology, keys ...string) []int64 {
	var out []int64
	for _, k := range keys {
		out = append(out, idOf(topo, k))
	}
	return out
}

func TestR_API_029_TopologyExpandsVirtualRepositories(t *testing.T) {
	a, b, c := repoOf("npm-a", repository.Local), repoOf("npm-b", repository.Local), repoOf("npm-remote", repository.Remote)
	inner := repository.Assemble(repoOf("npm-inner", repository.Virtual), []repository.Repository{a, c}, nil, 0)
	outer := repository.Assemble(repoOf("npm-outer", repository.Virtual), []repository.Repository{inner, b}, nil, 0)
	empty := repository.Assemble(repoOf("npm-empty", repository.Virtual), nil, nil, 0)
	topo := NewTopology([]repository.Repository{a, b, c, inner, outer, empty})

	if got, want := topo.Physical(), idsOf(topo, "npm-a", "npm-b", "npm-remote"); !slices.Equal(got, want) {
		t.Errorf("Physical %v", got)
	}
	if got, want := topo.Virtual(), idsOf(topo, "npm-empty", "npm-inner", "npm-outer"); !slices.Equal(got, want) {
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
		want := idsOf(topo, tc.want...)
		slices.Sort(want)
		if got := topo.Expand(topo.IDs(namesOf(tc.in...))); !slices.Equal(got, want) {
			t.Errorf("Expand(%v) = %v, want %v", tc.in, got, want)
		}
	}
	if got := topo.Containers(idOf(topo, "npm-a")); !slices.Equal(got, idsOf(topo, "npm-inner", "npm-outer")) {
		t.Errorf("Containers(npm-a) = %v", got)
	}
	if got := topo.Containers(idOf(topo, "npm-b")); !slices.Equal(got, idsOf(topo, "npm-outer")) {
		t.Errorf("Containers(npm-b) = %v", got)
	}
	if c, ok := topo.Class(idOf(topo, "npm-remote")); !ok || c != repository.Remote {
		t.Errorf("Class %v %v", c, ok)
	}
	if topo.Name(idOf(topo, "npm-b")) != qr("npm-b") || topo.Name(999) != (qname.Repo{}) {
		t.Error("Name must give the name of a known ID and the zero name for an unknown one")
	}
}

func namesOf(keys ...string) []qname.Repo {
	var out []qname.Repo
	for _, k := range keys {
		out = append(out, qr(k))
	}
	return out
}

// TestR_API_029_HoldersShowOnlyReadableVirtualRepositories: a version shows in the
// virtual repositories that contain its repository and that the caller may read, local
// repositories first.
func TestR_API_029_HoldersShowOnlyReadableVirtualRepositories(t *testing.T) {
	a, c := repoOf("npm-a", repository.Local), repoOf("npm-remote", repository.Remote)
	inner := repository.Assemble(repoOf("npm-all", repository.Virtual), []repository.Repository{a, c}, nil, 0)
	hidden := repository.Assemble(repoOf("npm-secret", repository.Virtual), []repository.Repository{a}, nil, 0)
	topo := NewTopology([]repository.Repository{a, c, inner, hidden})
	sc := Scope{topo: topo, virtualReadable: map[int64]bool{idOf(topo, "npm-all"): true}}
	if got := sc.holders(idsOf(topo, "npm-remote", "npm-a")); !slices.Equal(got, idsOf(topo, "npm-a", "npm-remote", "npm-all")) {
		t.Fatalf("%v", got)
	}
	if got := sc.holders(idsOf(topo, "npm-a")); slices.Contains(got, idOf(topo, "npm-secret")) {
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

// digestAdapter is a format whose version metadata has a `digest` member (an OCI tag), for all versions but the one named none.
type digestAdapter struct {
	fakeAdapter
	asked *[]Fields
}

func (d digestAdapter) Describe(_ context.Context, vs []Version, want Fields, _ func(string) bool) (map[int64]Data, error) {
	*d.asked = append(*d.asked, want)
	out := map[int64]Data{}
	for _, v := range vs {
		if v.Version != "none" {
			out[v.ID] = Data{Metadata: map[string]any{"digest": "sha256:" + v.Version}}
		}
	}
	return out, nil
}

// TestR_UI_052_DigestsComeFromTheFormatsMetadataInOneRead: the list of an OCI tag's versions shows the digest of the format's
// metadata, read once for the page; a format without the member has none, and the core never parses a format's metadata beyond it.
func TestR_UI_052_DigestsComeFromTheFormatsMetadataInOneRead(t *testing.T) {
	var asked []Fields
	a := digestAdapter{asked: &asked}
	repo := repoOf("npm-a", repository.Local)
	s := &Service{}
	sc := Scope{topo: NewTopology([]repository.Repository{repo})}
	rows := rowsOf("1.0.0", "none")
	groupOf := map[string]versionGroup{
		"1.0.0": {version: "1.0.0", rows: rows[:1]},
		"none":  {version: "none", rows: rows[1:2]},
	}
	page := []VersionSummary{{Version: "1.0.0"}, {Version: "none"}}
	if err := s.fillDigests(context.Background(), sc, a, "oci", groupOf, page); err != nil {
		t.Fatal(err)
	}
	if page[0].Digest != "sha256:1.0.0" || page[1].Digest != "" {
		t.Errorf("digests = %+v", page)
	}
	if len(asked) != 1 || asked[0] != FieldDetails {
		t.Errorf("the adapter was asked %v, want the details once", asked)
	}
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

// TestR_API_029_ProjectNarrowsToThePhysicalRepositoriesOfTheProject: the project filter of the list (Q-UI-129) becomes
// a repository filter; it intersects with the asked repositories, and a virtual repository stands for its members.
func TestR_API_029_ProjectNarrowsToThePhysicalRepositoriesOfTheProject(t *testing.T) {
	a := repository.Repository{ID: "1", Name: qname.Repo{Project: "it", Key: "npm-a"}, Class: repository.Local, Format: "npm"}
	b := repository.Repository{ID: "2", Name: qname.Repo{Project: "other", Key: "npm-b"}, Class: repository.Local, Format: "npm"}
	v := repository.Assemble(repository.Repository{ID: "3", Name: qname.Repo{Project: "it", Key: "npm-all"}, Class: repository.Virtual, Format: "npm"},
		[]repository.Repository{a, b}, nil, 0)
	topo := NewTopology([]repository.Repository{a, b, v})
	for _, tc := range []struct {
		name string
		in   Filter
		want []qname.Repo
	}{
		{"project only", Filter{Project: "it"}, []qname.Repo{a.Name}},
		{"another project", Filter{Project: "other"}, []qname.Repo{b.Name}},
		{"unknown project lists nothing", Filter{Project: "ghost"}, nil},
		{"a virtual repository stands for its members, cut to the project", Filter{Project: "it", HasRepositories: true, Repositories: []qname.Repo{v.Name}}, []qname.Repo{a.Name}},
		{"excluded by the repository filter", Filter{Project: "it", HasRepositories: true, Repositories: []qname.Repo{b.Name}}, nil},
	} {
		got := topo.InProject(tc.in)
		if !got.HasRepositories || got.Project != "" || !slices.Equal(got.Repositories, tc.want) {
			t.Errorf("%s: %+v, want %v", tc.name, got, tc.want)
		}
	}
	if got := topo.InProject(Filter{Q: "x"}); got.HasRepositories || got.Q != "x" {
		t.Errorf("no project must leave the filter alone: %+v", got)
	}
}

// TestR_VULN_028_TopologyKeysRepositoriesByIDNotByParsedNames: a repository is found by its database ID whatever its
// qualified text looks like, so that no row is parsed back into a name (R-VULN-028). Two repositories whose texts differ
// only at the separator, and a repository that moved to another project, keep their own IDs and names.
func TestR_VULN_028_TopologyKeysRepositoriesByIDNotByParsedNames(t *testing.T) {
	dash := repository.Repository{ID: "10", Name: qname.Repo{Project: "a-b", Key: "x1"}, Class: repository.Local, Format: "npm"}
	slash := repository.Repository{ID: "11", Name: qname.Repo{Project: "a", Key: "x1"}, Class: repository.Local, Format: "npm"}
	moved := repository.Repository{ID: "12", Name: qname.Repo{Project: "zeta", Key: "x1"}, Class: repository.Remote, Format: "npm"}
	topo := NewTopology([]repository.Repository{moved, slash, dash})

	for _, r := range []repository.Repository{dash, slash, moved} {
		id := r.NumericID()
		if topo.Name(id) != r.Name || topo.ID(r.Name) != id {
			t.Errorf("%v: ID %d, name %v", r.Name, topo.ID(r.Name), topo.Name(id))
		}
	}
	// "a-b/x1" sorts before "a/x1" as text ('-' is below '/'), whatever the tuple (project, key) would say.
	if got, want := topo.Physical(), []int64{10, 11, 12}; !slices.Equal(got, want) {
		t.Errorf("Physical %v, want the order of the qualified texts %v", got, want)
	}
	if !topo.Less(10, 11) || topo.Less(11, 10) {
		t.Error("Less must follow the qualified texts")
	}
	sc := Scope{topo: topo, whole: []int64{12}, rowRepos: []int64{10, 12}}
	if got := sc.Repos(); !slices.Equal(got, []int64{10, 12}) {
		t.Errorf("Repos %v", got)
	}
}
