//go:build chart

package chart_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type alertSeverity struct{ name, severity string }

// alertSeverities are the alerts the chart renders, in the order of templates/_rules.tpl, with their severity. The
// seven TroveBackup* alerts and TroveRevocationJournalBacklog of R-HA-045's table are not among them: the metrics they
// read (trove_backup_*, trove_revocation_journal_pending) are not exported until the backup and restore jobs and the
// revocation journal exist (phase 7).
func alertSeverities() []alertSeverity {
	return []alertSeverity{
		{"TroveReplicasNotReady", "critical"},
		{"TroveDatabaseUnreachable", "critical"},
		{"TroveObjectStoreUnreachable", "critical"},
		{"TroveObjectStoreUnreachableSingleReplica", "warning"},
		{"TroveDatabasePoolSaturated", "warning"},
		{"TroveControlConnectionWaits", "warning"},
		{"TroveJobsStuck", "warning"},
		{"TroveLeaseLost", "warning"},
		{"TroveMixedVersions", "warning"},
		{"TroveSpoolNearlyFull", "warning"},
		{"TroveVulnerabilityDataStale", "warning"},
		{"TroveVulnerabilitySyncFailing", "warning"},
		{"TroveVulnerabilityClaimedNameMalicious", "critical"},
		{"TroveMirrorFailing", "warning"},
		{"TroveMirrorStale", "warning"},
		{"TroveBridgeLeftOn", "warning"},
		{"TroveBlobMissing", "critical"},
		{"TroveGCStale", "warning"},
		{"TroveValkeyUnavailable", "critical"},
		{"TroveValkeyUnavailableSingleReplica", "warning"},
		{"TroveValkeySentinelsDegraded", "warning"},
		{"TroveValkeyFailoverFlapping", "warning"},
		{"TroveValkeyPrimaryDisagreement", "critical"},
	}
}

func renderedRules(t *testing.T, sets ...string) []any {
	t.Helper()
	_, docs := render(t, []string{monitoringSet()}, sets...)
	pr := find(docs, "PrometheusRule", "trove")
	if pr == nil {
		t.Fatal("no PrometheusRule")
	}
	return list(pr, "spec", "groups", "0", "rules")
}

// TestPrometheusRule is the check of the PrometheusRule of the chart for what is built of the monitoring integration. It
// does not carry the ID R-HA-045: the requirement is not complete, the TroveBackup* alerts wait for the backup and
// restore jobs and TroveRevocationJournalBacklog for the revocation journal (R-HA-045, status), and its acceptance
// criteria with a Prometheus scraping a running profile are not covered here.
func TestPrometheusRule(t *testing.T) {
	t.Run("it is rendered when metrics.prometheusRule.enabled is true and carries the configured labels", func(t *testing.T) {
		_, docs := render(t, []string{monitoringSet()})
		pr := find(docs, "PrometheusRule", "trove")
		if pr == nil {
			t.Fatal("no PrometheusRule with metrics.prometheusRule.enabled: true")
		}
		if str(pr, "apiVersion") != "monitoring.coreos.com/v1" {
			t.Errorf("apiVersion = %q", str(pr, "apiVersion"))
		}
		if str(pr, "metadata", "labels", "release") != "kube-prometheus-stack" {
			t.Errorf("labels = %v, want release: kube-prometheus-stack from metrics.prometheusRule.labels", at(pr, "metadata", "labels"))
		}
		if str(pr, "metadata", "labels", "app.kubernetes.io/name") == "" {
			t.Errorf("the chart's own labels are missing: %v", at(pr, "metadata", "labels"))
		}
		if got := str(pr, "spec", "groups", "0", "name"); got != "trove" {
			t.Errorf("group name = %q, want the full name of the release", got)
		}
	})

	t.Run("it is not rendered when metrics.prometheusRule.enabled is false, whatever else is set", func(t *testing.T) {
		out, _ := render(t, []string{monitoringSet()}, "metrics.prometheusRule.enabled=false")
		if strings.Contains(out, "kind: PrometheusRule") {
			t.Error("a PrometheusRule is rendered with metrics.prometheusRule.enabled: false")
		}
		out, _ = render(t, required())
		if strings.Contains(out, "kind: PrometheusRule") {
			t.Error("a PrometheusRule is rendered by the defaults")
		}
	})

	t.Run("the chart renders exactly these alerts with these severities", func(t *testing.T) {
		want := alertSeverities()
		got := renderedRules(t)
		if len(got) != len(want) {
			t.Errorf("%d alerts rendered, want %d", len(got), len(want))
		}
		for i, w := range want {
			if i >= len(got) {
				t.Errorf("alert %s is not rendered", w.name)
				continue
			}
			if str(got[i], "alert") != w.name {
				t.Errorf("alert %d = %s, want %s", i, str(got[i], "alert"), w.name)
				continue
			}
			if sev := str(got[i], "labels", "severity"); sev != w.severity {
				t.Errorf("%s severity = %q, want %q", w.name, sev, w.severity)
			}
		}
	})

	t.Run("every alert has an expression, a summary and a description, and a duration only where the table has one", func(t *testing.T) {
		forOf := map[string]string{
			"TroveReplicasNotReady": "5m", "TroveDatabaseUnreachable": "2m", "TroveObjectStoreUnreachable": "2m",
			"TroveObjectStoreUnreachableSingleReplica": "5m", "TroveDatabasePoolSaturated": "10m", "TroveControlConnectionWaits": "5m",
			"TroveJobsStuck": "5m", "TroveLeaseLost": "", "TroveMixedVersions": "2h", "TroveSpoolNearlyFull": "10m",
			"TroveBlobMissing": "", "TroveGCStale": "1h", "TroveValkeyUnavailable": "2m", "TroveValkeyUnavailableSingleReplica": "5m",
			"TroveValkeySentinelsDegraded": "5m", "TroveValkeyFailoverFlapping": "", "TroveValkeyPrimaryDisagreement": "5m",
		}
		for _, r := range renderedRules(t) {
			name := str(r, "alert")
			if str(r, "expr") == "" || str(r, "annotations", "summary") == "" || str(r, "annotations", "description") == "" {
				t.Errorf("%s: expr, summary or description is empty", name)
			}
			if want, ok := forOf[name]; ok && str(r, "for") != want {
				t.Errorf("%s: for = %q, want %q (R-HA-045's table)", name, str(r, "for"), want)
			}
		}
	})

	t.Run("the role regex of TroveReplicasNotReady follows the full name and covers every role and pool Deployment", func(t *testing.T) {
		_, docs := renderRelease(t, "acme", []string{monitoringSet()})
		pr := find(docs, "PrometheusRule", "acme-trove")
		if pr == nil {
			t.Fatal("no PrometheusRule for release acme (full name acme-trove)")
		}
		found := false
		for _, r := range list(pr, "spec", "groups", "0", "rules") {
			if str(r, "alert") != "TroveReplicasNotReady" {
				continue
			}
			found = true
			want := `deployment=~"acme-trove-(api|packages|worker|web)(-.*)?"`
			if !strings.Contains(str(r, "expr"), want) {
				t.Errorf("expr = %q, want it to contain %s", str(r, "expr"), want)
			}
			if !strings.Contains(str(r, "expr"), `min by (deployment)`) {
				t.Errorf("expr = %q is not per Deployment", str(r, "expr"))
			}
		}
		if !found {
			t.Error("TroveReplicasNotReady is not rendered")
		}
	})

	t.Run("no alert of the backup or the revocation journal is rendered before their metrics exist", func(t *testing.T) {
		for _, r := range renderedRules(t) {
			name := str(r, "alert")
			if strings.HasPrefix(name, "TroveBackup") || name == "TroveRevocationJournalBacklog" {
				t.Errorf("%s is rendered, but the binary exports no metric for it", name)
			}
			if strings.Contains(str(r, "expr"), "trove_backup_") || strings.Contains(str(r, "expr"), "trove_revocation_journal_pending") {
				t.Errorf("%s reads a metric the binary does not export: %s", name, str(r, "expr"))
			}
		}
	})

	t.Run("every metric an expression reads is a metric of the catalogue the binary exports or of kube-state-metrics", func(t *testing.T) {
		// The families the binary registers, found by name in the Go sources: a rule that reads a name no code registers fails here.
		var src strings.Builder
		for _, dir := range []string{"internal"} {
			_ = filepath.WalkDir(filepath.Join(repoRoot, dir), func(path string, d os.DirEntry, err error) error {
				if err == nil && !d.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
					if b, rerr := os.ReadFile(path); rerr == nil {
						src.Write(b)
					}
				}
				return nil
			})
		}
		for _, r := range renderedRules(t) {
			for _, m := range metricNames(str(r, "expr")) {
				if strings.HasPrefix(m, "kube_") {
					continue
				}
				base := strings.TrimSuffix(m, "_bucket")
				if !strings.Contains(src.String(), `"`+base+`"`) {
					t.Errorf("%s reads %s, which no code of internal/ registers", str(r, "alert"), base)
				}
			}
		}
	})

	t.Run("disabledAlerts accepts the names of the table", func(t *testing.T) {
		got := renderedRules(t, "metrics.prometheusRule.disabledAlerts={TroveJobsStuck,TroveGCStale,TroveReplicasNotReady}")
		for _, r := range got {
			switch str(r, "alert") {
			case "TroveJobsStuck", "TroveGCStale", "TroveReplicasNotReady":
				t.Errorf("%s is rendered although disabled", str(r, "alert"))
			}
		}
		if len(got) != len(alertSeverities())-3 {
			t.Errorf("%d alerts, want %d", len(got), len(alertSeverities())-3)
		}
	})

	t.Run("a name of an alert that waits for the backup is not yet an alert", func(t *testing.T) {
		renderFails(t, []string{monitoringSet()}, []string{"metrics.prometheusRule.disabledAlerts={TroveBackupStale}"}, "disabledAlerts", "TroveBackupStale")
	})
}

// metricNames returns the identifiers of an expression that look like a metric: they start with trove_ or kube_.
func metricNames(expr string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(expr, func(r rune) bool {
		return r != '_' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
	}) {
		if strings.HasPrefix(f, "trove_") || strings.HasPrefix(f, "kube_") {
			out = append(out, f)
		}
	}
	return out
}
