package journal

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/observability"
)

// MetricPending is the revocation-journal backlog gauge (R-HA-054, R-OPS-008): the rows of revocation_outbox not yet
// shipped. The alert TroveRevocationJournalBacklog reads it. Every web and worker replica reads it from PostgreSQL, so
// dashboards use max (R-OPS-007).
const MetricPending = "trove_revocation_journal_pending"

// MetricQuarantined counts outbox rows that could not be encoded as journal objects and were set aside (audit finding L4).
// Any increase means a revocation is missing from the journal; the alert TroveRevocationJournalQuarantined reads it.
const MetricQuarantined = "trove_revocation_journal_quarantined_total"

// Metrics holds the families of the journal.
type Metrics struct {
	Pending     prometheus.Gauge
	Quarantined prometheus.Counter
}

// NewMetrics registers the families on m; it panics when called twice for one registry.
func NewMetrics(m *observability.Metrics) *Metrics {
	return &Metrics{
		Pending:     m.NewGaugeVec(MetricPending, "Rows of the revocation outbox that are not yet shipped to object storage.").WithLabelValues(),
		Quarantined: m.NewCounterVec(MetricQuarantined, "Outbox rows that could not be encoded as journal objects and were set aside.").WithLabelValues(),
	}
}
