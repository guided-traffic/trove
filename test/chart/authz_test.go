//go:build chart

package chart_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestR_AUTHZ_010_DeclaredRoleBindingsReachTheMigrationJob: authz.role_bindings under
// config is rendered into the file of the migration Job, which applies it (trove
// migrate, R-AUTHZ-010, Q-AUTHZ-170), and into the file of the replicas unchanged.
func TestR_AUTHZ_010_DeclaredRoleBindingsReachTheMigrationJob(t *testing.T) {
	_, docs := render(t, []string{filepath.Join(chartDir, "ci", "ha-ingress.yaml")})
	bindings := func(t *testing.T, configMap string) any {
		t.Helper()
		cm := find(docs, "ConfigMap", configMap)
		if cm == nil {
			t.Fatalf("no ConfigMap %s", configMap)
		}
		var cfg map[string]any
		if err := yaml.Unmarshal([]byte(str(cm, "data", "trove.yaml")), &cfg); err != nil {
			t.Fatal(err)
		}
		return at(cfg, "authz", "role_bindings")
	}
	want := []any{
		map[string]any{"role": "admin", "group": "gh-wds18-k8s-main-admins"},
		map[string]any{"role": "auditor", "group": "gh-wds18-k8s-main-auditors"},
	}
	t.Run("R-AUTHZ-010 the migration Job's file carries the declaration", func(t *testing.T) {
		if got := bindings(t, "trove-migrate"); !reflect.DeepEqual(got, want) {
			t.Fatalf("authz.role_bindings = %#v", got)
		}
		job := find(docs, "Job", "trove-migrate")
		pod := at(job, "spec", "template", "spec")
		mounted := false
		for _, m := range list(pod, "containers", "0", "volumeMounts") {
			mounted = mounted || str(m, "mountPath") == "/etc/trove/trove.yaml" && str(m, "name") == "config"
		}
		if !mounted || str(volumeNamed(pod, "config"), "configMap", "name") != "trove-migrate" {
			t.Fatal("the migration Job does not read its ConfigMap as /etc/trove/trove.yaml")
		}
	})
	t.Run("R-AUTHZ-010 the replicas' file carries the same declaration", func(t *testing.T) {
		if got := bindings(t, "trove-config"); !reflect.DeepEqual(got, want) {
			t.Fatalf("authz.role_bindings = %#v", got)
		}
	})
}
