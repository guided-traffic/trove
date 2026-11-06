package proxy

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// TestR_REPO_029_TheUpstreamGaugeCarriesTheQualifiedName: trove_remote_upstream_up is labelled with the
// repository's qualified name <projectKey>/<repoKey> (R-REPO-002, R-OPS-007), so the same key in two
// projects has two series, and publishing it for a repository the engine remembered by name never panics.
func TestR_REPO_029_TheUpstreamGaugeCarriesTheQualifiedName(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	e := &Engine{m: newMetrics(prometheus.NewRegistry()), o: Options{Now: func() time.Time { return now }}}
	e.keys.Store(int64(1), qnameRepo("web", "npm-remote").String())
	e.keys.Store(int64(2), qnameRepo("mobile", "npm-remote").String())

	e.publishUp(1, &repoState{hasOpen: true, openUntil: now.Add(time.Minute)})
	e.publishUp(2, &repoState{})
	e.publishUp(3, &repoState{}) // a repository nothing remembered publishes nothing

	read := func(repo string) float64 {
		var m dto.Metric
		if err := e.m.upstreamUp.WithLabelValues(repo).Write(&m); err != nil {
			t.Fatal(err)
		}
		return m.GetGauge().GetValue()
	}
	if got := read("web/npm-remote"); got != 0 {
		t.Errorf("web/npm-remote = %v, want 0 (circuit open)", got)
	}
	if got := read("mobile/npm-remote"); got != 1 {
		t.Errorf("mobile/npm-remote = %v, want 1", got)
	}
}
