package store

import (
	"context"
	"sort"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/internal/vuln"
	"github.com/guided-traffic/trove/internal/vuln/advisory"
	vmetrics "github.com/guided-traffic/trove/internal/vuln/metrics"
)

// ExpectedSource is a source and ecosystem the configuration makes the installation sync (the KEV catalog has the
// ecosystem "").
type ExpectedSource struct{ Source, Ecosystem string }

// ExpectedSources lists the sources the configuration syncs: nothing while the scanning is disabled, the effective
// ecosystems of the OSV source unless its mode is off, and the KEV catalog unless its mode is off. A source whose mode is
// off keeps its stored data and its row, and is no longer measured: its data ages by decision, so no alert follows it
// (R-VULN-025).
func ExpectedSources(v config.Vulnerabilities) []ExpectedSource {
	if !v.Enabled {
		return nil
	}
	var out []ExpectedSource
	if v.Sources.OSV.Mode != "off" {
		for _, e := range v.Sources.OSV.EffectiveEcosystems() {
			out = append(out, ExpectedSource{vmetrics.SourceOSV, e})
		}
	}
	if v.Sources.KEV.Mode != "off" {
		out = append(out, ExpectedSource{vmetrics.SourceKEV, ""})
	}
	if v.Images.Enabled && v.Sources.Trivy.Mode != "off" {
		out = append(out, ExpectedSource{SourceTrivy, advisory.EcosystemOCI})
	}
	return out
}

// GaugeSource is the database gauge source of R-VULN-024: the five gauges read from PostgreSQL, evaluated in the
// background once per metrics.db_gauge_interval (Q-OPS-18) and aggregated with max across replicas. expected returns the
// sources the configuration syncs; it is a function so that the source reads the live configuration.
//
//   - The three source gauges carry a series for every expected source and ecosystem, a source without a row of
//     vuln_source_state included, with the value 0: a restore removes the rows (R-VULN-026) and a source that never
//     succeeded must keep TroveVulnerabilityDataStale firing instead of having no series (Q-VULN-56). A row of a source
//     that is not expected has no series.
//   - trove_vuln_findings carries every combination of severity, status and kind, 0 included, so that a dashboard sees a
//     count fall to zero instead of the series vanishing.
//   - trove_vuln_claimed_name_matches carries every effective ecosystem and both kinds, 0 included.
func GaugeSource(db Database, expected func() []ExpectedSource, ecosystems func() []string) observability.GaugeSource {
	descs := vmetrics.GaugeDescs()
	return observability.GaugeSource{
		Name: "vuln", Descs: descs,
		Collect: func(ctx context.Context) ([]prometheus.Metric, error) {
			states, err := SourceStates(ctx, db)
			if err != nil {
				return nil, err
			}
			findings, err := FindingCounts(ctx, db)
			if err != nil {
				return nil, err
			}
			claimed, err := ClaimedMatchCounts(ctx, db)
			if err != nil {
				return nil, err
			}
			return gaugeMetrics(descs, expected(), ecosystems(), states, findings, claimed), nil
		},
	}
}

// gaugeMetrics builds the constant metrics of the five gauges; descs is vmetrics.GaugeDescs() in its order.
func gaugeMetrics(descs []*prometheus.Desc, expected []ExpectedSource, ecosystems []string, states []SourceState, findings []FindingCount, claimed []ClaimedMatchCount) []prometheus.Metric {
	last, records, failures, findingDesc, claimedDesc := descs[0], descs[1], descs[2], descs[3], descs[4]
	var out []prometheus.Metric
	gauge := func(d *prometheus.Desc, v float64, labels ...string) {
		out = append(out, prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...))
	}

	byKey := map[ExpectedSource]SourceState{}
	for _, s := range states {
		byKey[ExpectedSource{s.Source, s.Ecosystem}] = s
	}
	for _, e := range expected {
		s := byKey[e] // the zero state for a source without a row
		var ts float64
		if !s.SyncedAt.IsZero() {
			ts = float64(s.SyncedAt.UnixMicro()) / 1e6
		}
		gauge(last, ts, e.Source, e.Ecosystem)
		gauge(records, float64(s.Records), e.Source, e.Ecosystem)
		gauge(failures, float64(s.Failures), e.Source, e.Ecosystem)
	}

	type fkey struct{ severity, status, kind string }
	counts := map[fkey]int64{}
	for _, sev := range advisory.Severities() {
		for _, st := range vuln.Statuses() {
			for _, k := range []advisory.Kind{advisory.KindVulnerability, advisory.KindMalicious} {
				counts[fkey{string(sev), string(st), string(k)}] = 0
			}
		}
	}
	for _, f := range findings {
		counts[fkey{f.Severity, f.Status, f.Kind}] = f.N
	}
	keys := make([]fkey, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := keys[i], keys[j]
		if a.severity != b.severity {
			return a.severity < b.severity
		}
		if a.status != b.status {
			return a.status < b.status
		}
		return a.kind < b.kind
	})
	for _, k := range keys {
		gauge(findingDesc, float64(counts[k]), k.severity, k.status, k.kind)
	}

	type ckey struct{ ecosystem, kind string }
	cc := map[ckey]int64{}
	for _, e := range ecosystems {
		for _, k := range []advisory.Kind{advisory.KindVulnerability, advisory.KindMalicious} {
			cc[ckey{e, string(k)}] = 0
		}
	}
	for _, c := range claimed {
		cc[ckey{c.Ecosystem, c.Kind}] = c.N
	}
	ck := make([]ckey, 0, len(cc))
	for k := range cc {
		ck = append(ck, k)
	}
	sort.Slice(ck, func(i, j int) bool {
		if ck[i].ecosystem != ck[j].ecosystem {
			return ck[i].ecosystem < ck[j].ecosystem
		}
		return ck[i].kind < ck[j].kind
	})
	for _, k := range ck {
		gauge(claimedDesc, float64(cc[k]), k.ecosystem, k.kind)
	}
	return out
}
