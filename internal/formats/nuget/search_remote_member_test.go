package nuget

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
	"github.com/guided-traffic/trove/internal/formats/nuget/nugettest"
	"github.com/guided-traffic/trove/internal/formats/nuget/rows"
	"github.com/guided-traffic/trove/internal/repository"
)

// A remote member of a virtual repository in the merged search (R-NUGET-018): its cache-only
// search takes its batches from the request's one scan budget (Q-NUGET-76), and a forwarded hit
// that the view of the addressed repository narrows counts the downloads of the versions it
// shows (Q-NUGET-78).

// withCacheOnlyRemote makes the virtual repository of the host hold the planned members, with
// forwardSearch off on every remote member, so a remote member answers from its cache.
func withCacheOnlyRemote(h *formattest.Host, members []formats.PlannedMember) {
	var repos []repository.Repository
	for _, m := range members {
		r := repository.Repository{ID: strconv.FormatInt(m.Member.ID, 10), Name: m.Member.Name, Class: m.Member.Class, Format: string(ID)}
		if m.Member.Class == formats.Remote {
			r.Config = []byte(`{"nuget":{"forwardSearch":false}}`)
		}
		repos = append(repos, r)
	}
	*h.Repo() = repository.Assemble(*h.Repo(), repos, nil, 60)
}

// TestR_NUGET_018_ARemoteMembersCacheSearchSharesTheScanBudget: the cache-only search of a remote
// member scans on the budget of the request, so a local and a remote member of 120,000 matches
// each answer 400 where either alone stays inside the bound; two of 90,000 each are counted
// (Q-NUGET-76).
func TestR_NUGET_018_ARemoteMembersCacheSearchSharesTheScanBudget(t *testing.T) {
	bulk := func(prefix string, n int) *searchFake {
		return &searchFake{Store: nugettest.New(), n: n, at: func(i int) rows.SearchHit {
			return rows.SearchHit{LowerID: fmt.Sprintf("%s%d", prefix, i), Listed: []rows.VersionEntry{ver("1.0.0", false)}, ListedTypes: [][]string{{"dependency"}}}
		}}
	}
	search := func(each int) *formattest.Response {
		members := []formats.PlannedMember{memberAt(11, "local", formats.TierLocal, 0), memberAt(20, "remote", formats.TierRemote, 1)}
		st := &perRepoSearch{Store: nugettest.New(), repos: map[int64]*searchFake{11: bulk("l.p", each), 20: bulk("r.p", each)}}
		res := &mergeResolver{scope: scopeFake{shape: formats.ReadRows}, plan: &formats.VirtualPlan{Admitted: true, Members: members}}
		h := virtualHost(t, res, st)
		withCacheOnlyRemote(h, members)
		return doSearch(t, h, "q=p&take=1")
	}
	if r := search(120_000); r.Code != http.StatusBadRequest || !strings.Contains(r.Body.String(), "Narrow the query") {
		t.Errorf("a local and a remote member of 120,000 matches each: %d %s", r.Code, r.Body.String())
	}
	if d := decodeSearch(t, search(90_000)); d.TotalHits != 2*90_000 {
		t.Errorf("a local and a remote member of 90,000 matches each: totalHits %d", d.TotalHits)
	}
}

// TestR_NUGET_034_AForwardedHitTheViewNarrowsCountsTheDownloadsItShows: exposedHit drops the
// versions the view does not expose from a forwarded hit, and its total downloads become the sum
// of the versions left, so the count of a hidden version does not reach the result.
func TestR_NUGET_034_AForwardedHitTheViewNarrowsCountsTheDownloadsItShows(t *testing.T) {
	h := pkg("Acme.Sdk", "1.9.0", "2.0.0", "2.1.0")
	for i := range h.Listed {
		h.Listed[i].Downloads = int64(i+1) * 100
	}
	h.Latest.VersionEntry = h.Listed[len(h.Listed)-1]
	h.Latest.Forwarded = &rows.Forwarded{TotalDownloads: 5_000, Owners: []string{"acme"}}
	view := rulesView{x: newExposure(), packages: map[string]bool{"acme.sdk": true}, rng: "[2.0,3.0)"}

	out, ok := exposedHit(view, h)
	if !ok || len(out.Listed) != 2 {
		t.Fatalf("exposedHit kept %v (%v)", out.Listed, ok)
	}
	if got := out.Latest.Forwarded.TotalDownloads; got != 200+300 {
		t.Errorf("total downloads %d, want the 500 of 2.0.0 and 2.1.0", got)
	}
	if h.Latest.Forwarded.TotalDownloads != 5_000 {
		t.Errorf("the member's hit was changed: %d", h.Latest.Forwarded.TotalDownloads)
	}
	// A hit the view leaves whole keeps the upstream's total, which counts unlisted versions too.
	whole, _ := exposedHit(rulesView{x: newExposure(), packages: map[string]bool{"acme.sdk": true}}, h)
	if whole.Latest.Forwarded.TotalDownloads != 5_000 {
		t.Errorf("an unnarrowed hit: total downloads %d", whole.Latest.Forwarded.TotalDownloads)
	}
}
