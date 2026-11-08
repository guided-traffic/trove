package registrytoken

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

// The outcomes of trove_oci_registry_tokens_total (R-OCI-034).
const (
	OutcomeIssued    = "issued"
	OutcomeAnonymous = "anonymous"
	OutcomeDenied    = "denied"
)

// Metrics counts the answers of the token service (R-OCI-034): trove_oci_registry_tokens_total by outcome, issued for a
// token of a principal, anonymous for an anonymous token, denied for a token request the authentication stage refused. A
// nil *Metrics counts nothing.
type Metrics struct {
	tokens *prometheus.CounterVec
}

// NewMetrics creates the metrics and registers them on reg (nil: unregistered). Every outcome starts at zero, so a
// dashboard sees the series before the first token.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{tokens: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "trove_oci_registry_tokens_total",
		Help: "Answers of the OCI registry's token service, by outcome: issued, anonymous, denied (R-OCI-034).",
	}, []string{"outcome"})}
	for _, o := range []string{OutcomeIssued, OutcomeAnonymous, OutcomeDenied} {
		m.tokens.WithLabelValues(o)
	}
	if reg == nil {
		return m, nil
	}
	if err := reg.Register(m.tokens); err != nil {
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			return nil, errors.New("registrytoken: the token service metrics are already registered on this registerer")
		}
		return nil, err
	}
	return m, nil
}

// Count counts one answer of the token service.
func (m *Metrics) Count(outcome string) {
	if m != nil {
		m.tokens.WithLabelValues(outcome).Inc()
	}
}
