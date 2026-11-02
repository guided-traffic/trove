//go:build chart

package chart_test

import (
	"path/filepath"
	"reflect"
	"testing"

	"go.yaml.in/yaml/v3"
)

// TestR_CI_001_DeclaredCIIssuersReachTheMigrationJob: ci.issuers and the other ci.* keys under
// config are rendered into the file of the migration Job, which applies the issuers (trove migrate,
// R-CI-001, Q-CI-16), and into the file of the replicas unchanged.
func TestR_CI_001_DeclaredCIIssuersReachTheMigrationJob(t *testing.T) {
	_, docs := render(t, []string{filepath.Join(chartDir, "ci", "ha-ingress.yaml")})
	ciOf := func(t *testing.T, configMap string) map[string]any {
		t.Helper()
		cm := find(docs, "ConfigMap", configMap)
		if cm == nil {
			t.Fatalf("no ConfigMap %s", configMap)
		}
		var cfg map[string]any
		if err := yaml.Unmarshal([]byte(str(cm, "data", "trove.yaml")), &cfg); err != nil {
			t.Fatal(err)
		}
		got, _ := cfg["ci"].(map[string]any)
		if got == nil {
			t.Fatalf("%s has no ci section", configMap)
		}
		return got
	}
	wantIssuers := []any{map[string]any{
		"name": "gitlab-main", "kind": "gitlab", "issuer": "https://gitlab.example.com", "public_issuer": false,
		"allowed_audiences": []any{"https://packages.example.com"}, "ca_bundle_ref": "file:/var/run/secrets/gitlab-ca/ca.crt",
	}}
	for _, name := range []string{"trove-migrate", "trove-config"} {
		t.Run("R-CI-001 the file of "+name+" carries ci.issuers and the ci keys", func(t *testing.T) {
			ci := ciOf(t, name)
			if !reflect.DeepEqual(ci["issuers"], wantIssuers) {
				t.Fatalf("ci.issuers = %#v", ci["issuers"])
			}
			if at(ci, "token", "max_ttl") != "30m" || !reflect.DeepEqual(at(ci, "exchange", "rate_limit", "exempt_cidrs"), []any{"10.20.0.0/16"}) {
				t.Fatalf("ci = %#v", ci)
			}
		})
	}
}
