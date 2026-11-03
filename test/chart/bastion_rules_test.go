//go:build chart

package chart_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// promtoolPath returns the promtool of the pin (tools/versions.env, PROMTOOL_VERSION). `make chart-test` fetches
// it into .bin and hands the path over in TROVE_PROMTOOL; a promtool of another version is refused, so the
// rules are never tested with two versions.
func promtoolPath(t *testing.T) string {
	t.Helper()
	path := os.Getenv("TROVE_PROMTOOL")
	if path == "" {
		t.Fatal("TROVE_PROMTOOL is not set: run `make chart-test`, which fetches promtool at the pin of tools/versions.env into .bin")
	}
	raw, err := os.ReadFile(filepath.Join(repoRoot, "tools", "versions.env"))
	if err != nil {
		t.Fatal(err)
	}
	want := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "PROMTOOL_VERSION="); ok {
			want = strings.TrimSpace(v)
		}
	}
	if want == "" {
		t.Fatal("PROMTOOL_VERSION is missing from tools/versions.env")
	}
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		t.Fatalf("%s --version: %v", path, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) < 3 || "v"+fields[2] != want {
		t.Fatalf("%s is %q, tools/versions.env pins promtool %s", path, strings.SplitN(string(out), "\n", 2)[0], want)
	}
	return path
}

func runPromtool(t *testing.T, dir, promtool string, args ...string) {
	t.Helper()
	cmd := exec.Command(promtool, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("promtool %s: %v\n%s", strings.Join(args, " "), err, out.String())
	}
}

// TestR_BAS_041_PromtoolRules (R-BAS-041): the rendered PrometheusRule passes `promtool check rules` and
// `promtool test rules`, with one test per alert that fires on its failure input and one on healthy input in
// which it stays silent (deploy/helm/trove-bastion/tests/rules_test.yaml).
func TestR_BAS_041_PromtoolRules(t *testing.T) {
	promtool := promtoolPath(t)
	_, docs := bstRender(t, []string{bastionSet("bucket-valkey")})
	pr := find(docs, "PrometheusRule", bastionRelease)
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
	testFile, err := os.ReadFile(filepath.Join(bastionDir(), "tests", "rules_test.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rules_test.yaml"), testFile, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("promtool check rules", func(t *testing.T) { runPromtool(t, dir, promtool, "check", "rules", "rules.yaml") })
	t.Run("promtool test rules", func(t *testing.T) { runPromtool(t, dir, promtool, "test", "rules", "rules_test.yaml") })

	t.Run("every alert has a test that fires and a test that stays silent", func(t *testing.T) {
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
