package blob

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/observability"
)

// Metrics are the Prometheus families of the blob store (R-STOR-004,
// R-STOR-024, R-STOR-033). Build it once per process with NewMetrics and hand
// it to every Spool, Guard and Collector; a nil *Metrics records nothing, which
// is what unit tests use.
//
// The S3 request counter names its operation label "operation" because that is
// the label R-OPS-007 allows; R-STOR-033 writes "op".
type Metrics struct {
	rejections *prometheus.CounterVec
	spoolUsed  prometheus.Gauge
	spoolFree  prometheus.Gauge

	s3Requests    *prometheus.CounterVec
	s3Duration    *prometheus.HistogramVec
	s3Bytes       *prometheus.CounterVec
	s3CircuitOpen prometheus.Gauge

	gcDeleted   prometheus.Counter
	gcReclaimed prometheus.Counter
	gcKept      prometheus.Counter
	gcHeld      prometheus.Counter
	gcDeleting  prometheus.Gauge
	gcLastOK    *prometheus.GaugeVec // no series until a run succeeded

	damaged *prometheus.CounterVec
}

// NewMetrics registers the families on m. It panics, like every registration
// in internal/observability, when called twice for one registry.
func NewMetrics(m *observability.Metrics) *Metrics {
	return &Metrics{
		rejections: m.NewCounterVec("trove_storage_upload_rejections_total", "Uploads refused before or while reading the body, by reason.", "reason"),
		spoolUsed:  m.NewGaugeVec("trove_spool_used_bytes", "Spool budget held by running uploads: the bytes they have reserved on the spool volume.").WithLabelValues(),
		spoolFree:  m.NewGaugeVec("trove_spool_free_bytes", "Free bytes of the spool volume, read at the last reservation or release.").WithLabelValues(),

		s3Requests:    m.NewCounterVec("trove_storage_s3_requests_total", "Object-store requests by operation and outcome.", "operation", "outcome"),
		s3Duration:    m.NewHistogramVec("trove_storage_s3_request_duration_seconds", "Duration of object-store requests by operation.", observability.HTTPBuckets(), "operation"),
		s3Bytes:       m.NewCounterVec("trove_storage_s3_bytes_total", "Bytes sent to and received from the object store.", "direction"),
		s3CircuitOpen: m.NewGaugeVec("trove_storage_s3_circuit_open", "1 while the object-store circuit breaker is open.").WithLabelValues(),

		gcDeleted:   m.NewCounterVec("trove_gc_deleted_blobs_total", "Blobs whose object garbage collection deleted.").WithLabelValues(),
		gcReclaimed: m.NewCounterVec("trove_gc_reclaimed_bytes_total", "Bytes garbage collection reclaimed.").WithLabelValues(),
		gcKept:      m.NewCounterVec("trove_gc_candidates_kept_total", "Candidates the per-blob check found referenced or gone.").WithLabelValues(),
		gcHeld:      m.NewCounterVec("trove_gc_candidates_held_total", "Candidates kept by a backup hold.").WithLabelValues(),
		gcDeleting:  m.NewGaugeVec("trove_gc_deleting_rows", "Blob rows in state deleting at the end of the last run.").WithLabelValues(),
		gcLastOK:    m.NewGaugeVec("trove_gc_last_success_timestamp_seconds", "End of the last garbage-collection run of this replica that was neither a dry run, nor paused, nor failed; absent until one has ended."),

		damaged: m.NewCounterVec("trove_storage_blobs_damaged_total", "Blobs marked damaged by a detection, by new status.", "status"),
	}
}

func (m *Metrics) reject(reason string) {
	if m != nil {
		m.rejections.WithLabelValues(reason).Inc()
	}
}

func (m *Metrics) setSpool(used int64, free uint64) {
	if m != nil {
		m.spoolUsed.Set(float64(used))
		m.spoolFree.Set(float64(free))
	}
}

func (m *Metrics) request(op, outcome string, seconds float64) {
	if m != nil {
		m.s3Requests.WithLabelValues(op, outcome).Inc()
		m.s3Duration.WithLabelValues(op).Observe(seconds)
	}
}

func (m *Metrics) bytes(direction string, n int64) {
	if m != nil && n > 0 {
		m.s3Bytes.WithLabelValues(direction).Add(float64(n))
	}
}

func (m *Metrics) circuit(open bool) {
	if m == nil {
		return
	}
	v := 0.0
	if open {
		v = 1
	}
	m.s3CircuitOpen.Set(v)
}

func (m *Metrics) gc(deleted, kept, held int, reclaimed int64) {
	if m == nil {
		return
	}
	m.gcDeleted.Add(float64(deleted))
	m.gcKept.Add(float64(kept))
	m.gcHeld.Add(float64(held))
	m.gcReclaimed.Add(float64(reclaimed))
}

func (m *Metrics) setSpoolUsed(used int64) {
	if m != nil {
		m.spoolUsed.Set(float64(used))
	}
}

// gcSucceeded records the end of a run that did its work (R-OPS-007).
func (m *Metrics) gcSucceeded(at time.Time) {
	if m != nil {
		m.gcLastOK.WithLabelValues().Set(float64(at.UnixNano()) / 1e9)
	}
}

func (m *Metrics) setDeleting(n int) {
	if m != nil {
		m.gcDeleting.Set(float64(n))
	}
}

func (m *Metrics) damage(status string) {
	if m != nil {
		m.damaged.WithLabelValues(status).Inc()
	}
}
