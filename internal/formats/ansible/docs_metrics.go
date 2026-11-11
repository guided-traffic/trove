package ansible

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/observability"
)

// The metric families of the documentation (R-ANS-045). No label holds the name of a repository or a collection: the outcomes are a closed
// set.
const (
	metricDocsGenerated = "trove_ansible_docs_generated_total"
	metricDocsFill      = "trove_ansible_docs_fill_total"
	metricDocsRead      = "trove_ansible_docs_read_total"
)

// The outcomes of a generation, of a fill and of a read.
const (
	docsCompleted = "completed"
	docsTruncated = "truncated"
	docsRefused   = "refused"
	fillAvailable = "available"
	fillNone      = "none"
	readOK        = "ok"
	readNotFound  = "not_found"
	readDenied    = "denied"
)

// docsMetrics are the families of the documentation.
type docsMetrics struct {
	generated, fill, read *prometheus.CounterVec
}

type docsKey struct{}

func init() {
	registerBinder(func(r *replica, m *observability.Metrics) {
		r.ext[docsKey{}] = &docsMetrics{
			generated: m.NewCounterVec(metricDocsGenerated, "Documentations generated from the archive of a published collection by outcome: completed, truncated or refused (R-ANS-045).", "outcome"),
			fill:      m.NewCounterVec(metricDocsFill, "Documentations of a remote repository's versions kept from the upstream by outcome: available, none, upstream_error, too_large or invalid_document (R-ANS-045).", "outcome"),
			read:      m.NewCounterVec(metricDocsRead, "Reads of the docs-blob route by outcome: ok, not_found or denied (R-ANS-045).", "operation", "outcome"),
		}
	})
}

func (f *Format) docsMetricsOf(r *replica) *docsMetrics {
	if r == nil {
		return nil
	}
	m, _ := r.ext[docsKey{}].(*docsMetrics)
	return m
}

// countGenerated counts the generation of a publish.
func (f *Format) countGenerated(port any, truncated bool) {
	m := f.docsMetricsOf(f.replicaOfPort(port))
	if m == nil {
		return
	}
	if truncated {
		m.generated.WithLabelValues(docsTruncated).Inc()
		return
	}
	m.generated.WithLabelValues(docsCompleted).Inc()
}

// countFill counts the outcome of the fill of a remote's documentation.
func (f *Format) countFill(port any, outcome string) {
	if m := f.docsMetricsOf(f.replicaOfPort(port)); m != nil {
		m.fill.WithLabelValues(outcome).Inc()
	}
}

// countRead counts a read of the route by the outcome of its handler.
func (f *Format) countRead(h formats.Host, err error) {
	m := f.docsMetricsOf(f.replicaOf(h))
	if m == nil {
		return
	}
	outcome := readOK
	if err != nil {
		switch errs.KindOf(err) {
		case errs.NotFound:
			outcome = readNotFound
		case errs.Forbidden, errs.Unauthenticated:
			outcome = readDenied
		default:
			return
		}
	}
	m.read.WithLabelValues("docs_blob", outcome).Inc()
}
