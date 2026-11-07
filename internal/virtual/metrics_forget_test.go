package virtual

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func repoSeries(t *testing.T, reg *prometheus.Registry) map[string]int {
	t.Helper()
	fams, err := reg.Gather()
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

// R-OPS-007: the series of a virtual repository that leaves the snapshot are deleted with its name (Forget is the function
// that the composition root registers with CappedLabels.OnForget).
func TestR_OPS_007_VirtualSeriesLeaveWithTheRepository(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := newMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	r := &Resolver{m: m}
	for _, name := range []string{"p/old", "p/stay"} {
		m.resolved(name, sourceLocal)
		m.checked(name, OutcomeClear)
	}
	if got := repoSeries(t, reg); got["p/old"] != 2 || got["p/stay"] != 2 {
		t.Fatalf("the series before the move: %v", got)
	}
	r.Forget("p/old")
	if got := repoSeries(t, reg); got["p/old"] != 0 || got["p/stay"] != 2 {
		t.Errorf("the series after the repository left the snapshot: %v", got)
	}
}
