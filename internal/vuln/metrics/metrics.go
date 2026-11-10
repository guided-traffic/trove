// Package metrics defines every metric of the vulnerability scanning in one place (R-VULN-024, R-OPS-007, INV-16): the names,
// the label values and the collectors. The packages of the scanning reference the fields of [Metrics] and add no metric of
// their own; a new one is a new line here and a new row in R-VULN-024. No label carries a package name, an advisory
// identifier or a repository key.
//
// It is a leaf (it imports the Prometheus client only) so that every work package, the composition root and the format host
// share it. The counters and the histogram are the replica's own: [New] creates and registers them. The five gauges are read
// from PostgreSQL once per metrics.db_gauge_interval (Q-OPS-18) and aggregated with max across replicas; the composition
// root registers them as a database gauge source with [GaugeDescs] (work package G).
package metrics

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"
)

// The names of R-VULN-024.
const (
	SourceLastSuccess  = "trove_vuln_source_last_success_timestamp_seconds"
	SourceRecords      = "trove_vuln_source_records"
	SourceFailures     = "trove_vuln_source_consecutive_failures"
	SyncRuns           = "trove_vuln_sync_runs_total"
	SyncRecords        = "trove_vuln_sync_records_total"
	UnevaluableRanges  = "trove_vuln_unevaluable_ranges_total"
	Evaluations        = "trove_vuln_evaluations_total"
	EvaluationDuration = "trove_vuln_evaluation_duration_seconds"
	Findings           = "trove_vuln_findings"
	DownloadsBlocked   = "trove_vuln_downloads_blocked_total"
	AuditRequests      = "trove_vuln_audit_requests_total"
	ImageScans         = "trove_vuln_image_scans_total"
	ImageScanDuration  = "trove_vuln_image_scan_duration_seconds"
	ClaimedNameMatches = "trove_vuln_claimed_name_matches"
)

// Label values. The sets are closed: a value outside them is a defect, never a new series.
const (
	// kind of trove_vuln_sync_runs_total.
	RunFull        = "full"
	RunIncremental = "incremental"

	// outcome of trove_vuln_sync_runs_total. A run whose job an operator cancelled is cancelled, not failed (Q-VULN-80).
	RunChanged   = "changed"
	RunUnchanged = "unchanged"
	RunFailed    = "failed"
	RunCancelled = "cancelled"

	// result of trove_vuln_sync_records_total.
	RecordWritten  = "written"
	RecordSkipped  = "skipped"
	RecordRejected = "rejected"
	// RecordInvalid counts the records that failed a rule of R-VULN-006 (and the rows of the change file that do not parse); the
	// bounds of one entry are RecordRejected (Q-VULN-122).
	RecordInvalid = "invalid"
	// RecordRejectedMalicious counts the records named like a malicious-package record (MAL-) that were rejected, by a bound of
	// one entry or by validation; they are counted in RecordRejected or RecordInvalid too. The record's identifier is never a label (Q-VULN-122).
	RecordRejectedMalicious = "rejected_malicious"
	RecordDeleted           = "deleted"

	// mode of trove_vuln_downloads_blocked_total.
	ModeEnforce = "enforce"
	ModeReport  = "report"

	// source of the source gauges and the sync counters.
	SourceOSV = "osv"
	SourceKEV = "kev"
)

// The label sets, by metric, in the order the collectors take them. Functions, not package-level slices (R-ARCH-032).
func sourceLabels() []string    { return []string{"source", "ecosystem"} }
func runLabels() []string       { return []string{"source", "ecosystem", "kind", "outcome"} }
func recordLabels() []string    { return []string{"source", "ecosystem", "result"} }
func ecosystemLabels() []string { return []string{"ecosystem"} }
func triggerLabels() []string   { return []string{"trigger"} }
func findingLabels() []string   { return []string{"severity", "status", "kind"} }
func blockedLabels() []string   { return []string{"format", "reason", "mode"} }
func auditLabels() []string     { return []string{"format", "mode", "outcome"} }
func claimedLabels() []string   { return []string{"ecosystem", "kind"} }

// Metrics are the collectors of the replica: the counters and the histogram of R-VULN-024. A nil *Metrics is a replica that
// did not build them; the producers check for it.
type Metrics struct {
	// SyncRuns counts sync runs by source, ecosystem, kind (full, incremental) and outcome (changed, unchanged, failed, cancelled).
	SyncRuns *prometheus.CounterVec
	// SyncRecords counts records by source, ecosystem and result (written, skipped, rejected, deleted).
	SyncRecords *prometheus.CounterVec
	// UnevaluableRanges counts the ranges the format's version order cannot evaluate, by ecosystem (R-VULN-009).
	UnevaluableRanges *prometheus.CounterVec
	// ImageScans counts finished attempts to scan an image, by result: scanned, or the code that ended it (R-VULN-034).
	ImageScans *prometheus.CounterVec
	// ImageScanDuration is the duration of the scan of one image.
	ImageScanDuration prometheus.Histogram
	// Evaluations counts evaluations by trigger (publish, fill, miss, advisory, full, rescan).
	Evaluations *prometheus.CounterVec
	// EvaluationDuration is the duration of an evaluation by trigger.
	EvaluationDuration *prometheus.HistogramVec
	// DownloadsBlocked counts refused downloads (and, in the mode report, would-be refusals) by format, reason (malicious,
	// triage, missing_data) and mode (enforce, report).
	DownloadsBlocked *prometheus.CounterVec
	// AuditRequests counts the client audit requests by format, mode and outcome: npm's bulk audits in every mode, and
	// NuGet's vulnerability page, the data of its audit.
	AuditRequests *prometheus.CounterVec
}

// New creates the collectors and registers them on reg; a nil reg creates them unregistered.
func New(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		SyncRuns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: SyncRuns, Help: "Advisory sync runs by source, ecosystem, kind (full, incremental) and outcome (changed, unchanged, failed, cancelled) (R-VULN-004).",
		}, runLabels()),
		SyncRecords: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: SyncRecords, Help: "Advisory records by source, ecosystem and result (written, skipped, rejected, invalid, rejected_malicious, deleted) (R-VULN-004).",
		}, recordLabels()),
		UnevaluableRanges: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: UnevaluableRanges, Help: "Advisory ranges the format's version order cannot evaluate, by ecosystem (R-VULN-009).",
		}, ecosystemLabels()),
		Evaluations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: Evaluations, Help: "Evaluations of stored versions by trigger (publish, fill, miss, advisory, full, rescan) (R-VULN-011).",
		}, triggerLabels()),
		EvaluationDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: EvaluationDuration, Help: "Duration of an evaluation by trigger (R-VULN-011).",
			Buckets: []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300},
		}, triggerLabels()),
		DownloadsBlocked: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: DownloadsBlocked, Help: "Downloads refused by a finding or by missing advisory data, by format, reason (malicious, triage, missing_data) and mode (enforce, report) (R-VULN-015).",
		}, blockedLabels()),
		ImageScans: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: ImageScans, Help: "Attempts to scan a container image, by result: scanned, or the code that ended it (R-VULN-034).",
		}, []string{"result"}),
		ImageScanDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name: ImageScanDuration, Help: "Duration of the scan of one container image (R-VULN-034).",
			Buckets: []float64{0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1200},
		}),
		AuditRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: AuditRequests, Help: "Client audit requests (npm bulk audits in every mode, the NuGet vulnerability page), by format, mode and outcome (R-VULN-017, R-VULN-018).",
		}, auditLabels()),
	}
	if reg == nil {
		return m, nil
	}
	var errs []error
	for _, c := range []prometheus.Collector{m.SyncRuns, m.SyncRecords, m.UnevaluableRanges, m.Evaluations, m.EvaluationDuration, m.DownloadsBlocked, m.AuditRequests, m.ImageScans, m.ImageScanDuration} {
		errs = append(errs, reg.Register(c))
	}
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return m, nil
}

// GaugeDescs returns the descriptors of the five gauges read from PostgreSQL (R-VULN-024): the last successful run of a
// source, its record count and consecutive failures, the findings by severity, status and kind, and the public advisories for
// claimed names. The composition root registers them as one database gauge source (work package G) and aggregates with max.
func GaugeDescs() []*prometheus.Desc {
	return []*prometheus.Desc{
		prometheus.NewDesc(SourceLastSuccess, "Last sync run of the advisory source that ended changed or unchanged, from PostgreSQL (aggregate with max, not sum, across replicas) (R-VULN-024).", sourceLabels(), nil),
		prometheus.NewDesc(SourceRecords, "Stored advisory records, from PostgreSQL (aggregate with max, not sum, across replicas) (R-VULN-024).", sourceLabels(), nil),
		prometheus.NewDesc(SourceFailures, "Consecutive failed sync runs (vuln_source_state.failures), from PostgreSQL (aggregate with max, not sum, across replicas) (R-VULN-024).", sourceLabels(), nil),
		prometheus.NewDesc(Findings, "Findings by severity, status and kind, from PostgreSQL (aggregate with max, not sum, across replicas) (R-VULN-012).", findingLabels(), nil),
		prometheus.NewDesc(ClaimedNameMatches, "Public advisories for claimed names by ecosystem and kind, from PostgreSQL (aggregate with max, not sum, across replicas) (R-VULN-010).", claimedLabels(), nil),
	}
}

// Names lists every metric name of the scanning, for the tests that hold the registry and the specification together.
func Names() []string {
	return []string{
		SourceLastSuccess, SourceRecords, SourceFailures, SyncRuns, SyncRecords, UnevaluableRanges, Evaluations, ImageScans, ImageScanDuration,
		EvaluationDuration, Findings, DownloadsBlocked, AuditRequests, ClaimedNameMatches,
	}
}
