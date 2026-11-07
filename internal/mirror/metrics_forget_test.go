package mirror

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func repoSeries(t *testing.T, reg *prometheus.Registry, repo string) int {
	t.Helper()
	fams, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range fams {
		for _, m := range f.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "repo" && l.GetValue() == repo {
					n++
				}
			}
		}
	}
	return n
}

// R-OPS-007: a pass that still runs for a repository that left the snapshot under its name records nothing more under that
// name; a pass that begins under the name later (a new repository, or a move back) records again.
func TestR_OPS_007_APassInProgressDoesNotRestoreTheSeriesOfAForgottenName(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	running := m.of("p/old")
	running.touch()
	running.item(ResultStored)
	if repoSeries(t, reg, "p/old") == 0 {
		t.Fatal("the pass recorded no series before the forget")
	}
	m.Forget("p/old")
	if n := repoSeries(t, reg, "p/old"); n != 0 {
		t.Fatalf("%d series of the forgotten name left", n)
	}
	running.touch()
	running.pass("full", OutcomeComplete)
	running.item(ResultStored)
	running.bytes(10)
	running.rateLimited("slow_down")
	running.waited(1)
	running.window(4)
	running.preempted()
	running.close()
	if n := repoSeries(t, reg, "p/old"); n != 0 {
		t.Fatalf("the pass brought %d series of the forgotten name back", n)
	}

	next := m.of("p/old")
	next.item(ResultStored)
	if repoSeries(t, reg, "p/old") == 0 {
		t.Error("a pass that begins after the forget does not record")
	}
	next.close()
	if len(m.handles) != 0 {
		t.Errorf("handles left after close: %v", m.handles)
	}
}
