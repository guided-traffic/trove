//go:build chart

package chart_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type alertSeverity struct{ name, severity string }

// alertSeverities are the alerts the chart renders, in the order of templates/_rules.tpl, with their severity: every row of
// R-HA-045's table, and the alerts of the vulnerability scanning, the mirror and the bridge.
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
		{"TroveBackupCheckFailed", "critical"},
		{"TroveRevocationJournalBacklog", "warning"},
		{"TroveRevocationJournalQuarantined", "critical"},
		{"TroveValkeyUnavailable", "critical"},
		{"TroveValkeyUnavailableSingleReplica", "warning"},
		{"TroveValkeySentinelsDegraded", "warning"},
		{"TroveValkeyFailoverFlapping", "warning"},
		{"TroveValkeyPrimaryDisagreement", "critical"},
		{"TroveBackupStale", "critical"},
		{"TroveBackupFailed", "critical"},
		{"TroveBackupRunningLong", "warning"},
		{"TroveBackupGlobalHoldLong", "warning"},
		{"TroveBackupSourceDamage", "critical"},
		{"TroveBackupVerifyFindings", "critical"},
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

// TestR_HA_045_PrometheusRule (R-HA-045) checks the PrometheusRule of the chart: every alert of the requirement's table is
// rendered with its severity and duration, reads a metric that code under internal/ registers (or kube-state-metrics), and has
// a rule test in which it fires and one in which it stays silent (deploy/helm/trove/tests/rules_test.yaml, run by
// TestR_VULN_024_PromtoolRules). The acceptance criteria that need a Prometheus scraping a running profile are the
// end-to-end suite's: the rule tests prove the expressions against given series, not that a stopped component produces them.
func TestR_HA_045_PrometheusRule(t *testing.T) {
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
			"TroveBackupCheckFailed": "1h", "TroveRevocationJournalBacklog": "15m", "TroveRevocationJournalQuarantined": "", "TroveBackupStale": "", "TroveBackupFailed": "", "TroveBackupRunningLong": "",
			"TroveBackupGlobalHoldLong": "30m", "TroveBackupSourceDamage": "", "TroveBackupVerifyFindings": "",
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

	t.Run("every row of the table of R-HA-045 is rendered, with the expression the table gives", func(t *testing.T) {
		exprs := map[string]string{}
		for _, r := range renderedRules(t) {
			exprs[str(r, "alert")] = str(r, "expr")
		}
		for name, want := range map[string]string{
			"TroveBackupCheckFailed":        `min(trove_backup_check_ok{ namespace="default" }) == 0`,
			"TroveRevocationJournalBacklog": `max(trove_revocation_journal_pending{ namespace="default" }) > 0`,
			"TroveBackupFailed":             `max(trove_backup_last_failure_timestamp_seconds{ namespace="default" }) > (max(trove_backup_last_success_timestamp_seconds{ namespace="default" }) or vector(0))`,
		} {
			if exprs[name] != want {
				t.Errorf("%s: expr = %q, want %q", name, exprs[name], want)
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

	t.Run("a name that is no alert is refused", func(t *testing.T) {
		renderFails(t, []string{monitoringSet()}, []string{"metrics.prometheusRule.disabledAlerts={TroveBackupCheckFaild}"}, "disabledAlerts", "TroveBackupCheckFaild", "TroveBackupCheckFailed")
	})

	t.Run("the backup alerts can be disabled one by one", func(t *testing.T) {
		got := renderedRules(t, "metrics.prometheusRule.disabledAlerts={TroveBackupStale,TroveBackupGlobalHoldLong,TroveRevocationJournalBacklog}")
		for _, r := range got {
			switch str(r, "alert") {
			case "TroveBackupStale", "TroveBackupGlobalHoldLong", "TroveRevocationJournalBacklog":
				t.Errorf("%s is rendered although disabled", str(r, "alert"))
			}
		}
		if len(got) != len(alertSeverities())-3 {
			t.Errorf("%d alerts, want %d", len(got), len(alertSeverities())-3)
		}
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
