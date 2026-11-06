package nuget

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/nuget/nugettest"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/formats/nuget/version"
	"github.com/guided-traffic/trove/internal/qname"
)

// The scope of a search: the terms meet only the versions the caller may read (Q-NUGET-74), the
// merged search filters every member by the view of the virtual repository the client addressed
// (Q-NUGET-75), and the fan-out over the members is bounded in time and in work (Q-NUGET-76). The
// patterns of R-REPO-005 (Q-NUGET-77) need repositories as the snapshot compiles them; they are
// tested against PostgreSQL in test/integration/nuget/search_scope_test.go.

// scopeSwitch is a resolver whose read scope a test changes between two requests.
type scopeSwitch struct {
	formats.Resolver
	scope formats.ReadScope
}

func (s *scopeSwitch) ReadScope(context.Context, *formats.Request, []qname.Repo) (formats.ReadScope, error) {
	return s.scope, nil
}

// seedSdk stores the versions of Acme.Sdk on the twin, each with its description.
func seedSdk(t *testing.T, twin *nugettest.Store, repo int64, versions ...[2]string) {
	t.Helper()
	for i, vd := range versions {
		v := version.MustParse(vd[0])
		twin.Seed(rows.VersionInput{
			RepoID: repo, VersionID: int64(i + 1), LowerID: "acme.sdk", DeclaredID: "Acme.Sdk",
			OriginalVersion: vd[0], LowerVersion: v.URLForm(), FullVersion: v.FullNormalized(), SortKey: v.SortKey(),
			IsPrerelease: v.IsPrerelease(), IsSemver2: v.IsSemver2(), PublishedAt: time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC),
			IDTokens: "Acme Sdk", Description: vd[1], PackageTypes: []string{"dependency"},
			Metadata: []byte(`{"description":"` + vd[1] + `"}`), Origin: rows.OriginPush,
		})
	}
}

// TestR_NUGET_034_SearchTermsMeetOnlyTheVersionsTheCallerMayRead: the E3.7 rule exposes Acme.Sdk
// from 2.0.0 without pre-releases. A word that only a hidden version's description holds finds
// nothing, a word of the latest exposed version finds the package although a hidden pre-release
// is newer, and a version: term finds the package only by a version the caller may read, so
// totalHits tells nothing of a hidden version (Q-NUGET-74). A caller who reads every version
// keeps the store's count over the latest visible version.
func TestR_NUGET_034_SearchTermsMeetOnlyTheVersionsTheCallerMayRead(t *testing.T) {
	twin := nugettest.New()
	exposed := scopeFake{shape: formats.ReadRows, may: func(p, v string) bool {
		return p == "acme.sdk" && (v == "2.0.0" || v == "2.1.0")
	}}
	res := &scopeSwitch{scope: exposed}
	h := newHost(t, formattest.HostOptions{Data: twin, Resolver: res})
	seedSdk(t, twin, h.Ref().ID,
		[2]string{"1.9.0", "Legacy widgets"}, [2]string{"2.0.0", "Widgets"}, [2]string{"2.1.0", "Widgets"}, [2]string{"3.0.0-rc.1", "Zebra preview"})
	const all = "&prerelease=true&semVerLevel=2.0.0"
	for _, c := range []struct {
		name, query string
		total       int
	}{
		{"a word of a hidden pre-release only", "q=description:zebra" + all, 0},
		{"a free-text word of a hidden pre-release only", "q=zebra" + all, 0},
		{"a word of a hidden older version only", "q=legacy" + all, 0},
		{"a word of the latest exposed version under a newer hidden one", "q=description:widgets" + all, 1},
		{"a hidden older version", "q=version:1.9.0", 0},
		{"a hidden pre-release", "q=packageid:acme.sdk+version:3.0.0-rc.1" + all, 0},
		{"a version that does not exist", "q=packageid:acme.sdk+version:9.9.9" + all, 0},
		{"an exposed version", "q=version:2.0.0", 1},
		{"an exposed version and a word of the latest exposed one", "q=packageid:acme.sdk+version:2.0.0+description:widgets" + all, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := doSearch(t, h, c.query)
			d := decodeSearch(t, r)
			if d.TotalHits != c.total || len(d.Data) != c.total {
				t.Fatalf("totalHits %d, %d results, want %d: %s", d.TotalHits, len(d.Data), c.total, r.Body)
			}
			if c.total == 1 && (d.Data[0].Version != "2.1.0" || len(d.Data[0].Versions) != 2) {
				t.Errorf("the result shows %s %+v, want 2.1.0 of 2.0.0 and 2.1.0", d.Data[0].Version, d.Data[0].Versions)
			}
			for _, hidden := range []string{"1.9.0", "3.0.0-rc.1", "Zebra", "Legacy"} {
				if strings.Contains(r.Body.String(), hidden) {
					t.Errorf("the result names %q: %s", hidden, r.Body)
				}
			}
		})
	}

	res.scope = allScope()
	if d := decodeSearch(t, doSearch(t, h, "q=description:zebra"+all)); d.TotalHits != 1 || d.Data[0].Version != "3.0.0-rc.1" {
		t.Errorf("a caller who reads every version: %+v", d)
	}
	if d := decodeSearch(t, doSearch(t, h, "q=version:1.9.0")); d.TotalHits != 1 || d.Data[0].Version != "2.1.0" {
		t.Errorf("a version: term of a caller who reads every version: %+v", d)
	}
}

// TestR_NUGET_034_TheScanDecidesOnTheFlagsOfTheReadableVersions: the format applies the match
// flags of the scan to the latest readable version, and a version the store gave no flag
// matches nothing.
func TestR_NUGET_034_TheScanDecidesOnTheFlagsOfTheReadableVersions(t *testing.T) {
	hidden := pkg("Acme.Sdk", "2.0.0", "3.0.0")
	hidden.ListedMatch = []bool{false, true} // only the newer, unreadable version matches
	open := pkg("Acme.Open", "1.0.0", "2.0.0")
	open.ListedMatch = []bool{false, true}
	unflagged := pkg("Acme.Unflagged", "1.0.0")
	unflagged.ListedMatch = []bool{}
	scope := scopeFake{shape: formats.ReadRows, may: func(p, v string) bool { return p != "acme.sdk" || v == "2.0.0" }}
	d := decodeSearch(t, doSearch(t, searchHost(t, listOf(hidden, open, unflagged), scope), "q=acme"))
	if !slices.Equal(idsOf(d), []string{"Acme.Open"}) || d.TotalHits != 1 {
		t.Errorf("results %v, totalHits %d, want Acme.Open alone", idsOf(d), d.TotalHits)
	}
	if matchedAt(rows.SearchHit{Listed: make([]rows.VersionEntry, 2)}, 1) {
		t.Error("a hit without flags matched")
	}
	if namesReadable([]string{""}, []string{""}) || !namesReadable(nil, nil) || namesReadable([]string{"1.0.0"}, []string{"2.0.0"}) {
		t.Error("namesReadable")
	}
}

// serveSearchOn serves a search on an endpoint.
func serveSearchOn(t *testing.T, h *formattest.Host, ep *formats.EndpointFacts, query string) *formattest.Response {
	t.Helper()
	rq, err := h.NewRequest(context.Background(), http.MethodGet, "v3/search", nil)
	if err != nil {
		t.Fatal(err)
	}
	rq.URL.RawQuery = query
	rq.Endpoint = *ep
	return h.ServeRequest(rq, false)
}

// TestR_NUGET_034_TheMergedSearchFiltersEveryMemberByTheViewOfTheAddressedRepository: what a
// member contributes is narrowed to what the endpoint exposes of the virtual repository the
// client addressed, whatever decided the member's rows (Q-NUGET-75). The members here are read
// whole, as a remote member's answer is decided by no read scope: the versions outside the
// view are dropped, a package outside it is dropped, and a hit whose latest version is outside
// it is dropped whole and counted as removed.
func TestR_NUGET_034_TheMergedSearchFiltersEveryMemberByTheViewOfTheAddressedRepository(t *testing.T) {
	res := &mergeResolver{scope: allScope()}
	h, _ := virtualSearchHost(t, 0, []formats.PlannedMember{
		memberAt(11, "first", formats.TierLocal, 0), memberAt(12, "second", formats.TierLocal, 1),
	}, map[int64][]rows.SearchHit{
		11: {pkg("Acme.Sdk", "1.9.0", "2.0.0", "2.1.0"), pkg("Acme.Internal", "1.0.0")},
		12: {pkg("Acme.Sdk", "2.1.0", "3.0.0")},
	}, res)
	view := rulesView{x: newExposure(), packages: map[string]bool{"acme.sdk": true}, rng: "[2.0,3.0)"}
	r := serveSearchOn(t, h, view.endpoint("customers", "fp"), "q=acme")
	d := decodeSearch(t, r)
	if !slices.Equal(idsOf(d), []string{"Acme.Sdk"}) || d.TotalHits != 1 {
		t.Fatalf("results %v, totalHits %d: %s", idsOf(d), d.TotalHits, r.Body)
	}
	var got []string
	for _, v := range d.Data[0].Versions {
		got = append(got, v.Version)
	}
	if d.Data[0].Version != "2.1.0" || !slices.Equal(got, []string{"2.0.0", "2.1.0"}) {
		t.Errorf("Acme.Sdk %s %v, want 2.1.0 of 2.0.0 and 2.1.0", d.Data[0].Version, got)
	}
	for _, hidden := range []string{"1.9.0", "3.0.0", "Internal"} {
		if strings.Contains(r.Body.String(), hidden) {
			t.Errorf("the result names %q: %s", hidden, r.Body)
		}
	}
	// The default endpoint does not restrict.
	if d := decodeSearch(t, doSearch(t, h, "q=acme")); d.TotalHits != 2 || len(d.Data[0].Versions) != 4 {
		t.Errorf("default endpoint: %+v", d)
	}
}

// gateStore answers every search with one package of the repository asked, after the test
// opens the gate, and records how many calls were open at once.
type gateStore struct {
	*nugettest.Store
	mu             sync.Mutex
	inflight, peak int
	release        chan struct{}
}

func (g *gateStore) enter() {
	g.mu.Lock()
	g.inflight++
	g.peak = max(g.peak, g.inflight)
	g.mu.Unlock()
	<-g.release
	g.mu.Lock()
	g.inflight--
	g.mu.Unlock()
}

func (g *gateStore) open() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inflight
}

func (g *gateStore) SearchCount(_ context.Context, _ rows.SearchQuery) (int, error) {
	g.enter()
	return 1, nil
}

func (g *gateStore) Search(_ context.Context, q rows.SearchQuery) ([]rows.SearchHit, error) {
	return []rows.SearchHit{pkg(fmt.Sprintf("Member%02d", q.RepoID), "1.0.0")}, nil
}

// TestR_NUGET_018_AVirtualSearchAsksAFewMembersAtATime: a virtual repository of ten local
// members is searched with at most maxMemberSearches members at once, and every member still
// answers (Q-NUGET-76). The test waits until the bound is reached and then gives a member beyond
// it time to start; it never waits for an amount of time to pass to succeed.
func TestR_NUGET_018_AVirtualSearchAsksAFewMembersAtATime(t *testing.T) {
	var members []formats.PlannedMember
	for i := range 10 {
		members = append(members, memberAt(int64(i+1), fmt.Sprintf("m%02d", i+1), formats.TierLocal, i))
	}
	st := &gateStore{Store: nugettest.New(), release: make(chan struct{})}
	res := &mergeResolver{scope: allScope(), plan: &formats.VirtualPlan{Admitted: true, Members: members}}
	h := virtualHost(t, res, st)
	withMembers(h, members)
	done := make(chan *formattest.Response, 1)
	go func() { done <- doSearch(t, h, "q=member") }()
	deadline := time.Now().Add(30 * time.Second) // a failure guard, not a budget
	for st.open() < maxMemberSearches {
		if time.Now().After(deadline) {
			close(st.release)
			t.Fatalf("%d members started, want %d", st.open(), maxMemberSearches)
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // a member beyond the bound would start now
	close(st.release)
	d := decodeSearch(t, <-done)
	if st.peak != maxMemberSearches {
		t.Errorf("%d members were asked at once, want %d", st.peak, maxMemberSearches)
	}
	if len(d.Data) != 10 || d.TotalHits != 10 {
		t.Errorf("%d results, totalHits %d, want every member's", len(d.Data), d.TotalHits)
	}
}

// TestR_NUGET_018_TheMembersOfAVirtualSearchShareOneScanBudget: the scans of the local members
// take their batches from one budget of maxSearchScan packages, so three members of 70,000
// matches each answer 400 where each alone stays inside the bound, and two of them do not
// (Q-NUGET-76).
func TestR_NUGET_018_TheMembersOfAVirtualSearchShareOneScanBudget(t *testing.T) {
	const each = 70_000
	bulk := func(prefix string) *searchFake {
		return &searchFake{Store: nugettest.New(), n: each, at: func(i int) rows.SearchHit {
			return rows.SearchHit{LowerID: fmt.Sprintf("%s%d", prefix, i), Listed: []rows.VersionEntry{ver("1.0.0", false)}, ListedTypes: [][]string{{"dependency"}}}
		}}
	}
	search := func(n int) *formattest.Response {
		var members []formats.PlannedMember
		st := &perRepoSearch{Store: nugettest.New(), repos: map[int64]*searchFake{}}
		for i := range n {
			id := int64(i + 1)
			members = append(members, memberAt(id, fmt.Sprintf("m%d", id), formats.TierLocal, i))
			st.repos[id] = bulk(fmt.Sprintf("m%d.p", id))
		}
		res := &mergeResolver{scope: scopeFake{shape: formats.ReadRows}, plan: &formats.VirtualPlan{Admitted: true, Members: members}}
		h := virtualHost(t, res, st)
		withMembers(h, members)
		return doSearch(t, h, "q=p&take=1")
	}
	if r := search(3); r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), "Narrow the query") {
		t.Errorf("three members of %d matches: %d %s", each, r.Code, r.Body.String())
	}
	if d := decodeSearch(t, search(2)); d.TotalHits != 2*each {
		t.Errorf("two members: totalHits %d", d.TotalHits)
	}
}

// TestR_NUGET_018_ACanceledSearchStopsAskingMembers: a request whose context ends while it
// waits for a member slot answers with the context's error instead of a partial merge.
func TestR_NUGET_018_ACanceledSearchStopsAskingMembers(t *testing.T) {
	var members []formats.PlannedMember
	for i := range maxMemberSearches + 2 {
		members = append(members, memberAt(int64(i+1), fmt.Sprintf("m%02d", i+1), formats.TierLocal, i))
	}
	st := &gateStore{Store: nugettest.New(), release: make(chan struct{})}
	res := &mergeResolver{scope: allScope(), plan: &formats.VirtualPlan{Admitted: true, Members: members}}
	h := virtualHost(t, res, st)
	withMembers(h, members)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan *formattest.Response, 1)
	go func() {
		rq, err := h.NewRequest(ctx, http.MethodGet, "v3/search", nil)
		if err != nil {
			done <- nil
			return
		}
		rq.URL.RawQuery = "q=member"
		done <- h.ServeRequest(rq, false)
	}()
	deadline := time.Now().Add(30 * time.Second) // a failure guard, not a budget
	for st.open() < maxMemberSearches {
		if time.Now().After(deadline) {
			close(st.release)
			t.Fatalf("%d members started", st.open())
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	close(st.release)
	r := <-done
	if r == nil || r.Code == http.StatusOK {
		t.Fatalf("a canceled search answered %v", r)
	}
}
