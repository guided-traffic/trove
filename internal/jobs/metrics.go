package jobs

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

// Outcomes of an attempt, the values of the outcome label of
// trove_jobs_completed_total (R-OPS-008).
const (
	OutcomeSucceeded = "succeeded"
	OutcomeFailed    = "failed"
	OutcomeRetried   = "retried"
	OutcomeCancelled = "cancelled"
	OutcomeReleased  = "released"
	OutcomeLost      = "lost"
	OutcomeReaped    = "reaped"
)

type metrics struct {
	completed *prometheus.CounterVec
	claimed   *prometheus.CounterVec
	running   *prometheus.GaugeVec
	duration  *prometheus.HistogramVec
	claimErrs prometheus.Counter
}

func newMetrics(reg prometheus.Registerer) (*metrics, error) {
	m := &metrics{
		completed: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "trove_jobs_completed_total", Help: "Job attempts that ended, by kind and outcome (succeeded, failed, retried, cancelled, released, lost, reaped)."}, []string{"kind", "outcome"}),
		claimed:   prometheus.NewCounterVec(prometheus.CounterOpts{Name: "trove_jobs_claimed_total", Help: "Jobs claimed by this replica, by kind."}, []string{"kind"}),
		running:   prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "trove_jobs_running", Help: "Jobs running on this replica, by kind."}, []string{"kind"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "trove_jobs_duration_seconds", Help: "Duration of a job attempt by kind.",
			Buckets: []float64{0.01, 0.1, 1, 10, 60, 600, 3600},
		}, []string{"kind"}),
		claimErrs: prometheus.NewCounter(prometheus.CounterOpts{Name: "trove_jobs_claim_errors_total", Help: "Claim attempts that failed."}),
	}
	if reg == nil {
		return m, nil
	}
	for _, c := range []prometheus.Collector{m.completed, m.claimed, m.running, m.duration, m.claimErrs} {
		if err := reg.Register(c); err != nil {
			var are prometheus.AlreadyRegisteredError
			if errors.As(err, &are) {
				return nil, errors.New("jobs: metrics are already registered on this registerer")
			}
			return nil, err
		}
	}
	return m, nil
}
