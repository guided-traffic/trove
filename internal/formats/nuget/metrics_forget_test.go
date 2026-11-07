package nuget

import (
	"testing"

	"github.com/guided-traffic/trove/internal/observability"
)

// R-OPS-007: the series of a repository that leaves the snapshot (deleted, or moved under another name) are deleted with
// its name; those of the repositories that stay remain.
func TestR_OPS_007_NuGetSeriesLeaveWithTheRepository(t *testing.T) {
	m := observability.NewMetrics("web")
	writes, remote, virt, search := newWriteMetrics(m), newRemoteMetrics(m), newVirtualMetrics(m), newSearchMetrics(m)
	m.Repos().UpdateRepositories([]observability.RepoLabel{{Name: "p/old"}, {Name: "p/stay"}})
	for _, name := range []string{"p/old", "p/stay"} {
		writes.replayed(name)
		remote.unparseable(name)
		virt.skipped(name, reasonUnavailable)
		search.withheldQuery(name)
		search.forwardFailed(name)
	}
	seen := func() map[string]int {
		fams, err := m.Registry().Gather()
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int{}
		for _, f := range fams {
			for _, s := range f.GetMetric() {
				for _, l := range s.GetLabel() {
					if l.GetName() == "repo" {
						out[l.GetValue()]++
					}
				}
			}
		}
		return out
	}
	if got := seen(); got["p/old"] != 5 || got["p/stay"] != 5 {
		t.Fatalf("the series before the move: %v", got)
	}
	m.Repos().UpdateRepositories([]observability.RepoLabel{{Name: "q/new"}, {Name: "p/stay"}})
	if got := seen(); got["p/old"] != 0 || got["p/stay"] != 5 {
		t.Errorf("the series after the repository left the snapshot: %v", got)
	}
}
