package session

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

// metrics are the session families of R-AUTH-041. The cache-bypass counter
// belongs to the identity area ([principal.Metrics]).
type metrics struct {
	touchFailures prometheus.Counter
	csrfFailed    prometheus.Counter
}

func newMetrics(reg prometheus.Registerer) (*metrics, error) {
	var errs []error
	m := &metrics{}
	m.touchFailures = counter(reg, &errs, prometheus.NewCounter(prometheus.CounterOpts{
		Name: "trove_auth_session_touch_failures_total", Help: "Session touches (last_seen_at) that failed; the request was served (R-AUTH-010).",
	}))
	m.csrfFailed = counter(reg, &errs, prometheus.NewCounter(prometheus.CounterOpts{
		Name: "trove_auth_csrf_failures_total", Help: "Cookie-authenticated requests refused by the CSRF checks (R-AUTH-012).",
	}))
	return m, errors.Join(errs...)
}

// counter registers c, or returns the collector already registered under its
// name. reg nil registers nothing.
func counter[C prometheus.Collector](reg prometheus.Registerer, errs *[]error, c C) C {
	if reg == nil {
		return c
	}
	if err := reg.Register(c); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			if existing, ok := are.ExistingCollector.(C); ok {
				return existing
			}
		}
		*errs = append(*errs, err)
	}
	return c
}
