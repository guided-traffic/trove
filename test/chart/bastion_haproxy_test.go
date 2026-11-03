//go:build chart

package chart_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// haproxyVerifiedAt is the HAProxy Ingress version whose documentation, template and annotation parser the
// annotation names of the chart were read against (Q-BAS-56): limit-rps, limit-connections, limit-whitelist,
// balance-algorithm, assign-backend-server-id, config-backend, secure-backends, secure-sni and
// secure-verify-ca-secret.
const haproxyVerifiedAt = "v0.16.3"

// TestR_BAS_045_HAProxyAnnotationsArePinnedToTheLocalControllerVersion (R-BAS-045): the controller of the local
// profile is the one the annotation names were verified against. A new pin in tools/versions.env fails this
// test until the names are checked against the new version and haproxyVerifiedAt is moved.
func TestR_BAS_045_HAProxyAnnotationsArePinnedToTheLocalControllerVersion(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(repoRoot, "tools", "versions.env"))
	if err != nil {
		t.Fatal(err)
	}
	pins := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if k, v, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(k, "#") {
			pins[k] = strings.TrimSpace(v)
		}
	}
	image := pins["DEV_HAPROXY_INGRESS_IMAGE"]
	tag := strings.TrimPrefix(strings.SplitN(image, "@", 2)[0], "quay.io/jcmoraisjr/haproxy-ingress:")
	if tag != haproxyVerifiedAt {
		t.Errorf("DEV_HAPROXY_INGRESS_IMAGE is %s, the annotation names were verified against %s: verify limit-*, balance-algorithm, assign-backend-server-id, config-backend and secure-* in the controller's keys.md, haproxy.tmpl and annotations/backend.go at the new tag, then set haproxyVerifiedAt", tag, haproxyVerifiedAt)
	}
	if got := "v" + strings.TrimPrefix(pins["DEV_HAPROXY_INGRESS_CHART_VERSION"], "v"); got != haproxyVerifiedAt {
		t.Errorf("DEV_HAPROXY_INGRESS_CHART_VERSION is %s, verified against %s", got, haproxyVerifiedAt)
	}
	a := bstIngressAnnotations(t, bstRequired(), "cacheVolume.routeByPath=true")
	for _, k := range []string{"limit-rps", "limit-connections", "balance-algorithm", "assign-backend-server-id", "config-backend", "secure-backends", "secure-sni", "secure-verify-ca-secret"} {
		if _, ok := a[hp+k]; !ok {
			t.Errorf("annotation %s is not rendered", k)
		}
	}
	for k := range a {
		if !strings.HasPrefix(k, hp) {
			t.Errorf("annotation %s does not use the prefix %s", k, hp)
		}
	}
}
