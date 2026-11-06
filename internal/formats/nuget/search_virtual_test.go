package nuget

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"testing"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/nuget/nugettest"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/repository"
)

// perRepoSearch is the search port of several local repositories: a fixed list of hits per
// repository id, recorded per call.
type perRepoSearch struct {
	*nugettest.Store
	repos map[int64]*searchFake
}

func (s *perRepoSearch) Search(ctx context.Context, q rows.SearchQuery) ([]rows.SearchHit, error) {
	return s.repos[q.RepoID].Search(ctx, q)
}

func (s *perRepoSearch) SearchCount(ctx context.Context, q rows.SearchQuery) (int, error) {
	return s.repos[q.RepoID].SearchCount(ctx, q)
}

// memberAt is a planned member of a virtual repository.
func memberAt(id int64, key string, tier formats.Tier, position int) formats.PlannedMember {
	class := formats.Local
	if tier != formats.TierLocal {
		class = formats.Remote
	}
	return formats.PlannedMember{Member: formats.RepoRef{ID: id, Name: key, Class: class, Format: ID}, Tier: tier, Position: position}
}

// virtualSearchHost is a virtual repository over the members given, each with its own list.
func virtualSearchHost(t *testing.T, window int, members []formats.PlannedMember, lists map[int64][]rows.SearchHit, res *mergeResolver) (*formattest.Host, *perRepoSearch) {
	t.Helper()
	st := &perRepoSearch{Store: nugettest.New(), repos: map[int64]*searchFake{}}
	for id, hits := range lists {
		st.repos[id] = listOf(hits...)
	}
	res.plan = &formats.VirtualPlan{Admitted: true, Members: members}
	if res.scope == nil {
		res.scope = allScope()
	}
	h := virtualHost(t, res, st)
	withMembers(h, members)
	if window > 0 {
		res.f.Bind(h, &config.Config{NuGet: config.NuGet{Search: config.NuGetSearch{DefaultTake: 20, MaxTake: 1000, MaxSkip: 3000, VirtualMergeWindow: window}}}, nil)
	}
	return h, st
}

// withMembers makes the virtual repository of the host hold the planned members, as the
// snapshot links them: the search reads their patterns there.
func withMembers(h *formattest.Host, members []formats.PlannedMember) {
	var repos []repository.Repository
	for _, m := range members {
		repos = append(repos, repository.Repository{Name: strconv.FormatInt(m.Member.ID, 10), Key: m.Member.Name, Class: m.Member.Class, Format: string(ID)})
	}
	*h.Repo() = repository.Assemble(*h.Repo(), repos, nil, 60)
}

func idsOf(d searchDoc) []string {
	var out []string
	for _, r := range d.Data {
		out = append(out, r.Name)
	}
	return out
}

// TestR_NUGET_018_ThePackagesOfTheMembersAreMergedInTheOrderOfTheRequirement: a package whose
// ID is exactly a term of the query comes first, whichever member has it; the others follow
// member by member, each in its own order; a package of two members is one result with the
// union of the versions, the package-level fields of the member that has the highest one, and
// a total that does not count it twice.
func TestR_NUGET_018_ThePackagesOfTheMembersAreMergedInTheOrderOfTheRequirement(t *testing.T) {
	res := &mergeResolver{}
	h, _ := virtualSearchHost(t, 0, []formats.PlannedMember{
		memberAt(11, "first", formats.TierLocal, 0), memberAt(12, "second", formats.TierLocal, 1),
	}, map[int64][]rows.SearchHit{
		11: {pkg("Acme.Alpha", "1.0.0"), pkg("Acme.Widgets", "1.0.0", "2.0.0"), pkg("Acme.Beta", "1.0.0")},
		12: {pkg("Acme.Gamma", "1.0.0"), pkg("Acme.Widgets", "2.0.0", "3.0.0"), pkg("Acme.Delta", "1.0.0")},
	}, res)
	d := decodeSearch(t, doSearch(t, h, "q=acme+packageid:acme.widgets"))
	if want := []string{"Acme.Widgets", "Acme.Alpha", "Acme.Beta", "Acme.Gamma", "Acme.Delta"}; !slices.Equal(idsOf(d), want) {
		t.Fatalf("order = %v, want %v", idsOf(d), want)
	}
	w := d.Data[0]
	if w.Version != "3.0.0" || len(w.Versions) != 3 || w.Versions[0].Version != "1.0.0" || w.Versions[2].Version != "3.0.0" {
		t.Errorf("Acme.Widgets = %s %+v", w.Version, w.Versions)
	}
	if want := feedOfAll + "registration/acme.widgets/index.json"; w.Registration != want {
		t.Errorf("registration = %q, want %q (every URL points at the virtual repository)", w.Registration, want)
	}
	if d.TotalHits != 5 {
		t.Errorf("totalHits = %d, want 5 (6 results of the members, one package twice)", d.TotalHits)
	}
}

// TestR_NUGET_018_ThePageIsCutFromTheMergeAndTheMembersAreAskedForTheWindow: every member is
// asked for the first min(skip + take, window) results, from skip 0; the page is cut from the
// merge; a page that starts at or beyond the window is empty and asks no member for results.
func TestR_NUGET_018_ThePageIsCutFromTheMergeAndTheMembersAreAskedForTheWindow(t *testing.T) {
	list := func(prefix string, n int) []rows.SearchHit {
		var out []rows.SearchHit
		for i := range n {
			out = append(out, pkg(prefix+string(rune('A'+i)), "1.0.0"))
		}
		return out
	}
	res := &mergeResolver{}
	h, st := virtualSearchHost(t, 6, []formats.PlannedMember{
		memberAt(11, "first", formats.TierLocal, 0), memberAt(12, "second", formats.TierLocal, 1),
	}, map[int64][]rows.SearchHit{11: list("One", 8), 12: list("Two", 8)}, res)

	d := decodeSearch(t, doSearch(t, h, "q=x&skip=2&take=3"))
	if want := []string{"OneC", "OneD", "OneE"}; !slices.Equal(idsOf(d), want) {
		t.Errorf("page = %v, want %v", idsOf(d), want)
	}
	if d.TotalHits != 16 {
		t.Errorf("totalHits = %d, want 16", d.TotalHits)
	}
	for id, f := range st.repos {
		for _, q := range f.calls() {
			if q.Offset != 0 || (q.Limit != 0 && q.Limit != 5) {
				t.Errorf("member %d was asked for offset %d limit %d, want 0 and 5", id, q.Offset, q.Limit)
			}
		}
	}

	// The window cuts the merge: 6 results of each member are merged, the page beyond them is
	// empty, and the total still counts what the members count.
	d = decodeSearch(t, doSearch(t, h, "q=x&skip=6&take=5"))
	if len(d.Data) != 0 || d.TotalHits != 16 {
		t.Errorf("a page at the window: %d results, totalHits %d", len(d.Data), d.TotalHits)
	}
	d = decodeSearch(t, doSearch(t, h, "q=x&skip=5&take=10"))
	if !slices.Equal(idsOf(d), []string{"OneF"}) {
		t.Errorf("the page across the window = %v, want the one result inside it", idsOf(d))
	}
}

// TestR_NUGET_018_AClaimOrAPinRuleKeepsAPackageAwayFromAMember: the results of a member for a
// package that the virtual repository's claims and pin rules keep away from it are removed,
// and the total is reduced by them; a package another member has stays.
func TestR_NUGET_018_AClaimOrAPinRuleKeepsAPackageAwayFromAMember(t *testing.T) {
	res := &mergeResolver{keeps: func(member, term string) bool { return member == "second" && term == "acme.secret" }}
	h, _ := virtualSearchHost(t, 0, []formats.PlannedMember{
		memberAt(11, "first", formats.TierLocal, 0), memberAt(12, "second", formats.TierLocal, 1),
	}, map[int64][]rows.SearchHit{
		11: {pkg("Acme.Widgets", "1.0.0")},
		12: {pkg("Acme.Secret", "9.0.0"), pkg("Acme.Widgets", "2.0.0")},
	}, res)
	d := decodeSearch(t, doSearch(t, h, "q=acme"))
	if want := []string{"Acme.Widgets"}; !slices.Equal(idsOf(d), want) {
		t.Fatalf("results = %v, want %v", idsOf(d), want)
	}
	if d.Data[0].Version != "2.0.0" || d.TotalHits != 1 {
		t.Errorf("version %s, totalHits %d", d.Data[0].Version, d.TotalHits)
	}
}

// TestR_AUTHZ_016_TheReadScopeOfTheCallerAppliesToEachMemberSeparately: a member the caller
// reads row by row shows the versions the caller may read, one the caller reads whole shows
// everything, and a member that is not in the plan (the caller may not read it) shows nothing.
func TestR_AUTHZ_016_TheReadScopeOfTheCallerAppliesToEachMemberSeparately(t *testing.T) {
	scope := memberScope{shapes: map[string]formats.ReadShape{"rows": formats.ReadRows, "whole": formats.ReadAll}, may: func(repo, pkg, v string) bool {
		return repo == "rows" && v == "1.0.0"
	}}
	res := &mergeResolver{scope: scope}
	h, _ := virtualSearchHost(t, 0, []formats.PlannedMember{
		memberAt(11, "rows", formats.TierLocal, 0), memberAt(12, "whole", formats.TierLocal, 1),
	}, map[int64][]rows.SearchHit{
		11: {pkg("Acme.Rows", "1.0.0", "2.0.0")},
		12: {pkg("Acme.Whole", "5.0.0")},
		13: {pkg("Acme.Hidden", "1.0.0")},
	}, res)
	d := decodeSearch(t, doSearch(t, h, "q=acme"))
	if want := []string{"Acme.Rows", "Acme.Whole"}; !slices.Equal(idsOf(d), want) {
		t.Fatalf("results = %v, want %v", idsOf(d), want)
	}
	if rows := d.Data[0]; rows.Version != "1.0.0" || len(rows.Versions) != 1 {
		t.Errorf("the row-level member shows %s %+v, want 1.0.0 alone", rows.Version, rows.Versions)
	}
}

// memberScope answers by repository key.
type memberScope struct {
	shapes map[string]formats.ReadShape
	may    func(repo, pkg, version string) bool
}

func (s memberScope) Shape(repo string) formats.ReadShape { return s.shapes[repo] }
func (s memberScope) MayRead(repo, pkg, version string) bool {
	return s.may == nil || s.may(repo, pkg, version)
}

// TestR_NUGET_018_APlanThatAdmitsNothingIsAnEmptyResult: a plan that admits nothing is an empty
// result and not an error, and so is a plan without members. A remote member takes part through
// its forwarded search (search_remote_test.go, test/integration/nuget).
func TestR_NUGET_018_APlanThatAdmitsNothingIsAnEmptyResult(t *testing.T) {
	res := &mergeResolver{}
	h, _ := virtualSearchHost(t, 0, []formats.PlannedMember{
		memberAt(11, "local", formats.TierLocal, 0),
	}, map[int64][]rows.SearchHit{11: {pkg("Acme.Widgets", "1.0.0")}}, res)
	d := decodeSearch(t, doSearch(t, h, "q=acme"))
	if !slices.Equal(idsOf(d), []string{"Acme.Widgets"}) || d.TotalHits != 1 {
		t.Errorf("results = %v, totalHits %d", idsOf(d), d.TotalHits)
	}
	res.plan = &formats.VirtualPlan{Admitted: false}
	if d := decodeSearch(t, doSearch(t, h, "q=acme")); len(d.Data) != 0 || d.TotalHits != 0 {
		t.Errorf("a plan that admits nothing: %+v", d)
	}
	res.plan = &formats.VirtualPlan{Admitted: true}
	if r := doSearch(t, h, "q=acme"); r.Code != http.StatusOK {
		t.Errorf("a plan without members: %d", r.Code)
	}
}
