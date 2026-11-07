package npm

import (
	"testing"

	"github.com/guided-traffic/trove/internal/observability"
)

// repoValues returns the values of the label repo over every family of the registry.
func repoValues(t *testing.T, m *observability.Metrics) map[string]bool {
	t.Helper()
	fams, err := m.Registry().Gather()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, f := range fams {
		for _, s := range f.GetMetric() {
			for _, l := range s.GetLabel() {
				if l.GetName() == "repo" {
					out[l.GetValue()] = true
				}
			}
		}
	}
	return out
}

// R-OPS-007: the series of a repository that leaves the snapshot (deleted, or moved under another name) are deleted with
// its name; those of the repositories that stay remain.
func TestR_OPS_007_NPMSeriesLeaveWithTheRepository(t *testing.T) {
	m := observability.NewMetrics("web")
	mm := newMetrics(m)
	m.Repos().UpdateRepositories([]observability.RepoLabel{{Name: "p/old"}, {Name: "p/stay"}})
	for _, name := range []string{"p/old", "p/stay"} {
		mm.retryAcknowledged(name)
		mm.versionConflicts(name, 1)
		mm.removedVersions(name, 1)
		mm.staleDocument(name)
		mm.revalidation(name, "revalidated")
		mm.auditWithheld(name, map[string]int{withheldReasons()[0]: 1})
	}
	if got := repoValues(t, m); !got["p/old"] || !got["p/stay"] {
		t.Fatalf("the series before the move: %v", got)
	}
	m.Repos().UpdateRepositories([]observability.RepoLabel{{Name: "q/new"}, {Name: "p/stay"}})
	if got := repoValues(t, m); got["p/old"] || !got["p/stay"] {
		t.Errorf("the repo values after the repository left the snapshot: %v", got)
	}
}
