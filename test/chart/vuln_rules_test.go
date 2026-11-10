//go:build chart

package chart_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

func monitoringSet() string { return filepath.Join(chartDir, "ci", "monitoring.yaml") }

// TestR_VULN_024_PromtoolRules (R-VULN-024): the rendered PrometheusRule passes `promtool check rules` and
// `promtool test rules`, with one test per alert that fires on its failure input and one in which it stays silent
// (deploy/helm/trove/tests/rules_test.yaml). It runs over every alert of the chart, those of R-HA-045 included; the
// requirement's own checks are in TestR_HA_045_PrometheusRule.
func TestR_VULN_024_PromtoolRules(t *testing.T) {
	promtool := promtoolPath(t)
	_, docs := render(t, []string{monitoringSet()})
	pr := find(docs, "PrometheusRule", "trove")
	if pr == nil {
		t.Fatal("no PrometheusRule")
	}
	dir := t.TempDir()
	rules, err := yaml.Marshal(at(pr, "spec"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rules.yaml"), rules, 0o600); err != nil {
		t.Fatal(err)
	}
	testFile, err := os.ReadFile(filepath.Join(chartDir, "tests", "rules_test.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rules_test.yaml"), testFile, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("promtool check rules", func(t *testing.T) { runPromtool(t, dir, promtool, "check", "rules", "rules.yaml") })
	t.Run("R-OPS-046 promtool test rules", func(t *testing.T) { runPromtool(t, dir, promtool, "test", "rules", "rules_test.yaml") })

	t.Run("R-OPS-046 every alert has a test that fires and a test that stays silent", func(t *testing.T) {
		var parsed struct {
			Tests []struct {
				AlertRuleTest []struct {
					Alertname string `yaml:"alertname"`
					ExpAlerts []any  `yaml:"exp_alerts"`
				} `yaml:"alert_rule_test"`
			} `yaml:"tests"`
		}
		if err := yaml.Unmarshal(testFile, &parsed); err != nil {
			t.Fatal(err)
		}
		fires, silent := map[string]bool{}, map[string]bool{}
		for _, tc := range parsed.Tests {
			for _, a := range tc.AlertRuleTest {
				if len(a.ExpAlerts) > 0 {
					fires[a.Alertname] = true
				} else {
					silent[a.Alertname] = true
				}
			}
		}
		for _, r := range list(pr, "spec", "groups", "0", "rules") {
			name := str(r, "alert")
			if !fires[name] {
				t.Errorf("no test in which %s fires", name)
			}
			if !silent[name] {
				t.Errorf("no test in which %s stays silent", name)
			}
		}
	})
}

// TestR_VULN_024_PrometheusRuleValues (R-VULN-024, R-HA-045): the values of metrics.prometheusRule.
func TestR_VULN_024_PrometheusRuleValues(t *testing.T) {
	t.Run("no PrometheusRule by default", func(t *testing.T) {
		out, _ := render(t, required())
		if strings.Contains(out, "kind: PrometheusRule") {
			t.Error("a PrometheusRule is rendered with metrics.prometheusRule.enabled false")
		}
	})

	t.Run("the alerts, with the labels of the values and their severity", func(t *testing.T) {
		_, docs := render(t, []string{monitoringSet()})
		pr := find(docs, "PrometheusRule", "trove")
		if pr == nil {
			t.Fatal("no PrometheusRule")
		}
		if str(pr, "metadata", "labels", "release") != "kube-prometheus-stack" {
			t.Errorf("labels = %v, want the release label of metrics.prometheusRule.labels", at(pr, "metadata", "labels"))
		}
		want := map[string]string{
			"TroveVulnerabilityDataStale":            "warning",
			"TroveVulnerabilitySyncFailing":          "warning",
			"TroveVulnerabilityClaimedNameMalicious": "critical",
			"TroveVulnerabilityImageScansFailing":    "warning",
			"TroveMirrorFailing":                     "warning",
			"TroveMirrorStale":                       "warning",
			"TroveBridgeLeftOn":                      "warning",
			"TroveBlobMissing":                       "critical",
		}
		for _, r := range list(pr, "spec", "groups", "0", "rules") {
			name := str(r, "alert")
			sev, ok := want[name]
			if !ok {
				continue // the other alerts of the rule are checked by TestR_HA_045_PrometheusRule
			}
			if got := str(r, "labels", "severity"); got != sev {
				t.Errorf("%s severity = %q, want %q", name, got, sev)
			}
			delete(want, name)
		}
		for name := range want {
			t.Errorf("alert %s is not rendered", name)
		}
	})

	t.Run("the stale threshold follows config.vulnerabilities.stale_after", func(t *testing.T) {
		_, docs := render(t, []string{monitoringSet()}, "config.vulnerabilities.stale_after=48h")
		for _, r := range list(find(docs, "PrometheusRule", "trove"), "spec", "groups", "0", "rules") {
			if str(r, "alert") == "TroveVulnerabilityDataStale" && !strings.HasSuffix(strings.TrimSpace(str(r, "expr")), "> 172800") {
				t.Errorf("expr = %q, want a threshold of 172800 s", str(r, "expr"))
			}
		}
	})

	t.Run("every selector is scoped to the release namespace", func(t *testing.T) {
		_, docs := renderRelease(t, "trove", []string{monitoringSet()})
		for _, r := range list(find(docs, "PrometheusRule", "trove"), "spec", "groups", "0", "rules") {
			if !strings.Contains(str(r, "expr"), `namespace="default"`) {
				t.Errorf("%s: expr %q is not scoped to the namespace", str(r, "alert"), str(r, "expr"))
			}
		}
	})

	t.Run("disabledAlerts removes an alert and extraRules adds one", func(t *testing.T) {
		_, docs := render(t, []string{monitoringSet()},
			"metrics.prometheusRule.disabledAlerts={TroveVulnerabilitySyncFailing}",
			"metrics.prometheusRule.extraRules[0].alert=Mine", "metrics.prometheusRule.extraRules[0].expr=vector(1)")
		var names []string
		for _, r := range list(find(docs, "PrometheusRule", "trove"), "spec", "groups", "0", "rules") {
			names = append(names, str(r, "alert"))
		}
		want := []string{}
		for _, a := range alertSeverities() {
			if a.name != "TroveVulnerabilitySyncFailing" {
				want = append(want, a.name)
			}
		}
		want = append(want, "Mine")
		if got := strings.Join(names, ","); got != strings.Join(want, ",") {
			t.Errorf("alerts = %s, want %s", got, strings.Join(want, ","))
		}
	})

	t.Run("a name in disabledAlerts that is no alert fails", func(t *testing.T) {
		renderFails(t, []string{monitoringSet()}, []string{"metrics.prometheusRule.disabledAlerts={TroveVulnerabilitySynkFailing}"},
			"disabledAlerts", "TroveVulnerabilitySynkFailing", "TroveVulnerabilitySyncFailing")
	})

	t.Run("every alert disabled renders no PrometheusRule", func(t *testing.T) {
		var all []string
		for _, a := range alertSeverities() {
			all = append(all, a.name)
		}
		out, _ := render(t, []string{monitoringSet()}, "metrics.prometheusRule.disabledAlerts={"+strings.Join(all, ",")+"}")
		if strings.Contains(out, "kind: PrometheusRule") {
			t.Error("an empty PrometheusRule is rendered")
		}
	})
}

// TestR_VULN_025_SpoolHoldsOneArchive (R-VULN-025): the worker spool budget (spool size minus headroom) must hold
// config.vulnerabilities.sources.osv.max_archive_size while the OSV source is online or repository.
func TestR_VULN_025_SpoolHoldsOneArchive(t *testing.T) {
	t.Run("the defaults hold: 5Gi worker spool, 2GiB headroom, 1GiB archive", func(t *testing.T) {
		render(t, required())
	})
	t.Run("an archive larger than the worker budget fails and names the values", func(t *testing.T) {
		renderFails(t, required(), []string{"config.vulnerabilities.sources.osv.max_archive_size=4GiB"},
			"config.vulnerabilities.sources.osv.max_archive_size", "worker.spool.sizeLimit")
	})
	t.Run("the check holds in mode repository too", func(t *testing.T) {
		renderFails(t, required(), []string{"config.vulnerabilities.sources.osv.mode=repository", "config.vulnerabilities.sources.osv.max_archive_size=4GiB"},
			"config.vulnerabilities.sources.osv.max_archive_size")
	})
	t.Run("a worker spool that holds the archive plus headroom passes", func(t *testing.T) {
		render(t, required(), "config.vulnerabilities.sources.osv.max_archive_size=4GiB", "worker.spool.sizeLimit=6Gi")
	})
	t.Run("the headroom counts", func(t *testing.T) {
		renderFails(t, required(), []string{"config.vulnerabilities.sources.osv.max_archive_size=4GiB", "worker.spool.sizeLimit=6Gi", "spool.headroom=3GiB"},
			"max_archive_size")
	})
	t.Run("mode off, or the scanning disabled, is not checked", func(t *testing.T) {
		render(t, required(), "config.vulnerabilities.sources.osv.max_archive_size=4GiB", "config.vulnerabilities.sources.osv.mode=off")
		render(t, required(), "config.vulnerabilities.sources.osv.max_archive_size=4GiB", "config.vulnerabilities.enabled=false")
	})
	t.Run("the worker pods carry a spool limit of the value, not the web's", func(t *testing.T) {
		_, docs := render(t, required(), "worker.spool.sizeLimit=6Gi")
		env := envByName(at(find(docs, "Deployment", "trove-worker"), "spec", "template", "spec"))
		if got := str(env["TROVE_STORAGE_SPOOL_MAX_BYTES"], "value"); got != "4294967296" {
			t.Errorf("TROVE_STORAGE_SPOOL_MAX_BYTES of the worker = %q, want 4294967296 (6Gi - 2GiB)", got)
		}
	})
}

// TestR_VULN_025_ConfigurationPassesThrough (R-VULN-025): the vulnerabilities section is rendered into trove.yaml as set,
// and the binary's validator accepts it (the value set ci/monitoring.yaml is part of TestRenderedConfigurationPassesTheBinary).
func TestR_VULN_025_ConfigurationPassesThrough(t *testing.T) {
	_, docs := render(t, []string{monitoringSet()}, "config.vulnerabilities.enforcement=report")
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(str(find(docs, "ConfigMap", "trove-config"), "data", "trove.yaml")), &cfg); err != nil {
		t.Fatal(err)
	}
	if got := str(cfg, "vulnerabilities", "enforcement"); got != "report" {
		t.Errorf("vulnerabilities.enforcement = %q", got)
	}
	if got := str(cfg, "vulnerabilities", "sources", "osv", "max_archive_size"); got != "1GiB" {
		t.Errorf("vulnerabilities.sources.osv.max_archive_size = %q", got)
	}
}
