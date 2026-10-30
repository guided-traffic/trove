package virtual

import (
	"errors"
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// The sources of trove_virtual_resolutions_total (R-REPO-047).
const (
	sourceLocal    = "local"
	sourceCache    = "cache"
	sourceUpstream = "upstream"
	sourceNone     = "none"
)

// The outcomes of trove_shadow_checks_total (R-REPO-048).
const (
	OutcomeNoExposure          = "no-exposure"
	OutcomeClear               = "clear"
	OutcomeShadowingApproved   = "shadowing-approved"
	OutcomeRefused             = "refused"
	OutcomeUndeterminedAllowed = "undetermined-approved"
	OutcomeUnavailable         = "unavailable"
)

type metrics struct {
	resolutions *prometheus.CounterVec
	shadow      *prometheus.CounterVec
}

func newMetrics(reg prometheus.Registerer) (*metrics, error) {
	m := &metrics{
		resolutions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_virtual_resolutions_total",
			Help: "Resolutions of artifacts and metadata documents through a virtual repository, by the tier that served them (local, cache, upstream) or none (R-REPO-047).",
		}, []string{"repo", "source"}),
		shadow: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_shadow_checks_total",
			Help: "Shadowing checks of first publishes by the local repository and their outcome (R-REPO-048).",
		}, []string{"repo", "outcome"}),
	}
	if reg == nil {
		return m, nil
	}
	for _, c := range []prometheus.Collector{m.resolutions, m.shadow} {
		if err := reg.Register(c); err != nil {
			var are prometheus.AlreadyRegisteredError
			if errors.As(err, &are) {
				return nil, fmt.Errorf("virtual: metrics are already registered on this registerer: %w", err)
			}
			return nil, err
		}
	}
	return m, nil
}

func (m *metrics) resolved(repo, source string) { m.resolutions.WithLabelValues(repo, source).Inc() }

func (m *metrics) checked(repo, outcome string) { m.shadow.WithLabelValues(repo, outcome).Inc() }
