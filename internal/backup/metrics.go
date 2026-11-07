package backup

import (
	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/observability"
)

// The metric families of the backup lane (R-BAK-014, operations-and-observability.md R-OPS-008). The gauges are read from
// PostgreSQL by every web and worker replica with one query on backup_run, reused for 30 seconds, so every replica reports
// the same value and dashboards use max; the counters are exported by the backup pod for the duration of its run. The names
// are the contract of the alert rules in the chart (R-HA-045). The families that carry a timestamp of an event that may never
// have happened (the last success, the last failure) have no series until it did, so a rule can tell "never" from a value.
const (
	MetricLastSuccess      = "trove_backup_last_success_timestamp_seconds"
	MetricLastFailure      = "trove_backup_last_failure_timestamp_seconds"
	MetricExpectedInterval = "trove_backup_expected_interval_seconds"
	MetricLastDuration     = "trove_backup_last_duration_seconds"
	MetricLastDatabase     = "trove_backup_last_database_bytes"
	MetricLastBlob         = "trove_backup_last_blob_bytes"
	MetricLastCopied       = "trove_backup_last_copied_bytes"
	MetricRunning          = "trove_backup_running"
	MetricRunningSeconds   = "trove_backup_running_seconds"
	MetricGCHold           = "trove_backup_gc_hold"
	MetricPinnedBlobs      = "trove_backup_pinned_blobs"
	MetricSnapshotOpen     = "trove_backup_snapshot_open_seconds"
	MetricSourceDamaged    = "trove_backup_source_blobs_damaged"
	MetricLastVerify       = "trove_backup_last_verify_timestamp_seconds"
	MetricLastVerifyFinds  = "trove_backup_last_verify_findings"
	MetricCopiedObjects    = "trove_backup_copied_objects_total"
	MetricCopiedBytes      = "trove_backup_copied_bytes_total"
)

// Metrics holds the families. Build it once per process with NewMetrics; a nil *Metrics records nothing.
type Metrics struct {
	// LastSuccess and LastFailure have no series until a run succeeded or failed (Q-OPS-180); the alert expressions
	// handle the absence.
	LastSuccess, LastFailure                 *prometheus.GaugeVec
	ExpectedInterval, LastDuration           prometheus.Gauge
	LastDatabase, LastBlob, LastCopied       prometheus.Gauge
	Running                                  *prometheus.GaugeVec // operation, state
	RunningSeconds                           prometheus.Gauge
	GCHold                                   *prometheus.GaugeVec // mode
	PinnedBlobs, SnapshotOpen, SourceDamaged prometheus.Gauge
	LastVerify, LastVerifyFindings           prometheus.Gauge
	CopiedObjects, CopiedBytes               *prometheus.CounterVec // mode
}

// NewMetrics registers the families on m. It panics when called twice for one registry, like every registration in
// internal/observability.
func NewMetrics(m *observability.Metrics) *Metrics {
	g := func(name, help string) prometheus.Gauge { return m.NewGaugeVec(name, help).WithLabelValues() }
	return &Metrics{
		LastSuccess:        m.NewGaugeVec(MetricLastSuccess, "finished_at of the newest completed or completed_with_warnings create run; absent until one exists."),
		LastFailure:        m.NewGaugeVec(MetricLastFailure, "The newest failed or abandoned create run, or a create run in an active state whose lease expired; interrupted runs do not count; absent until one exists."),
		ExpectedInterval:   g(MetricExpectedInterval, "backup.expected_interval of this replica's configuration: the threshold of TroveBackupStale."),
		LastDuration:       g(MetricLastDuration, "Duration of the newest successful create run."),
		LastDatabase:       g(MetricLastDatabase, "Database bytes of the newest successful create run."),
		LastBlob:           g(MetricLastBlob, "Referenced blob bytes of the newest successful create run."),
		LastCopied:         g(MetricLastCopied, "Bytes copied by the newest successful create run."),
		Running:            m.NewGaugeVec(MetricRunning, "1 for the operation and state of the active run with a valid lease, if any.", "operation", "state"),
		RunningSeconds:     g(MetricRunningSeconds, "Age of the active create or delete run with a valid lease, else 0."),
		GCHold:             m.NewGaugeVec(MetricGCHold, "1 while a valid garbage-collection hold of that mode exists.", "mode"),
		PinnedBlobs:        g(MetricPinnedBlobs, "Pins left, from the active run's progress."),
		SnapshotOpen:       g(MetricSnapshotOpen, "Age of the active run's open snapshot, else 0."),
		SourceDamaged:      g(MetricSourceDamaged, "Damaged blobs recorded by the newest successful create run."),
		LastVerify:         g(MetricLastVerify, "Finish time of the newest verify run."),
		LastVerifyFindings: g(MetricLastVerifyFinds, "Findings of the newest verify run."),
		CopiedObjects:      m.NewCounterVec(MetricCopiedObjects, "Pool objects copied by a backup run, by mode (server or stream). Backup pod only.", "mode"),
		CopiedBytes:        m.NewCounterVec(MetricCopiedBytes, "Bytes copied by a backup run, by mode (server or stream). Backup pod only.", "mode"),
	}
}

// Copied counts one pool object copied in the given mode.
func (m *Metrics) Copied(mode string, bytes int64) {
	if m == nil {
		return
	}
	m.CopiedObjects.WithLabelValues(mode).Inc()
	m.CopiedBytes.WithLabelValues(mode).Add(float64(bytes))
}
