//go:build chart

package chart_test

import (
	"strings"
	"testing"
)

// TestR_HA_048_UninstallAndDataRemoval (R-HA-048): what helm uninstall leaves, and how the leftovers are found. The
// kubectl half of the acceptance criteria (nothing left after the uninstall and the documented delete, the database and
// bucket unchanged, the claims gone within two minutes) needs a cluster; this is what the rendered chart guarantees.
func TestR_HA_048_UninstallAndDataRemoval(t *testing.T) {
	t.Run("R-HA-048 no value set renders a StatefulSet, a claim or a resource that helm keeps on uninstall", func(t *testing.T) {
		eachSet(t, func(t *testing.T, file string, out string, docs []doc) {
			for _, k := range []string{"StatefulSet", "PersistentVolumeClaim"} {
				if n := len(ofKind(docs, k)); n != 0 {
					t.Errorf("%s: %d %s objects", file, n, k)
				}
			}
			if strings.Contains(out, "resource-policy") {
				t.Errorf("%s renders helm.sh/resource-policy, so uninstall would keep a resource", file)
			}
		})
	})

	t.Run("R-HA-048 spool.type emptyDir (the default) has no claim at all", func(t *testing.T) {
		_, docs := render(t, required())
		for name, pod := range podSpecs(docs) {
			for _, v := range list(pod, "volumes") {
				if str(v, "name") == "spool" && (at(v, "emptyDir") == nil || at(v, "ephemeral") != nil) {
					t.Errorf("%s: the spool volume is %v, want an emptyDir", name, v)
				}
			}
		}
	})

	t.Run("R-HA-048 spool.type ephemeral: the claim template carries the release's labels, so the claims are visible while the pods run", func(t *testing.T) {
		_, docs := render(t, required(), "spool.type=ephemeral")
		seen := 0
		for name, pod := range podSpecs(docs) {
			for _, v := range list(pod, "volumes") {
				tpl := at(v, "ephemeral", "volumeClaimTemplate")
				if str(v, "name") != "spool" || tpl == nil {
					continue
				}
				seen++
				labels, _ := at(tpl, "metadata", "labels").(map[string]any)
				if labels["app.kubernetes.io/instance"] != "trove" || labels["app.kubernetes.io/name"] == nil {
					t.Errorf("%s: claim template labels = %v, want the selector labels of the release", name, labels)
				}
			}
		}
		if seen == 0 {
			t.Fatal("no pod has a generic ephemeral spool")
		}
	})

	t.Run("R-HA-048 the migration hook: its Job removes itself, and its ConfigMap and ServiceAccount are found by the labels the notes print", func(t *testing.T) {
		_, docs := render(t, required())
		job := find(docs, "Job", "trove-migrate")
		if job == nil || at(job, "spec", "ttlSecondsAfterFinished") == nil {
			t.Fatalf("the migration Job has no ttlSecondsAfterFinished: %v", job)
		}
		notes := notesOf(t, required())
		cmd := "kubectl -n default delete configmap,serviceaccount,job -l app.kubernetes.io/instance=trove,app.kubernetes.io/component=migrate"
		if !strings.Contains(notes, cmd) {
			t.Fatalf("the notes do not print %q:\n%s", cmd, notes)
		}
		for _, kind := range []string{"ConfigMap", "ServiceAccount", "Job"} {
			d := find(docs, kind, "trove-migrate")
			if d == nil {
				t.Fatalf("no %s trove-migrate", kind)
			}
			labels, _ := at(d, "metadata", "labels").(map[string]any)
			if labels["app.kubernetes.io/instance"] != "trove" || labels["app.kubernetes.io/component"] != "migrate" {
				t.Errorf("%s trove-migrate labels = %v, the delete command would miss it", kind, labels)
			}
			if !strings.Contains(str(d, "metadata", "annotations", "helm.sh/hook"), "pre-install") {
				t.Errorf("%s trove-migrate is not a hook resource", kind)
			}
		}
	})

	t.Run("R-HA-048 the notes say that uninstall touches no data and that removing it is manual", func(t *testing.T) {
		notes := notesOf(t, required())
		if !strings.Contains(notes, "Uninstall touches neither the database, nor the buckets, nor Valkey, nor the Secrets") {
			t.Errorf("notes:\n%s", notes)
		}
	})
}

// TestR_HA_040_PublicBaseURLInTheChart (R-HA-040): the chart side of the public base URL. The acceptance criteria of
// R-OPS-005 through the chart's Ingress (a request through a real edge proxy) are the end-to-end half; the rules of
// R-OPS-005 themselves are tested in internal/httpserver.
func TestR_HA_040_PublicBaseURLInTheChart(t *testing.T) {
	t.Run("R-HA-040 config.server.base_url is required", func(t *testing.T) {
		renderFails(t, required(), []string{"config.server.base_url="}, "config.server.base_url")
	})

	t.Run("R-HA-040 the Ingress host and the HTTPRoute host name default to the host of the base URL", func(t *testing.T) {
		_, docs := render(t, []string{gatewaySet()}, "ingress.enabled=true", "ingress.backendTLS.caSecret=trove-tls",
			"config.server.base_url=https://packages.example.org")
		ing := ofKind(docs, "Ingress")
		if len(ing) != 1 || str(ing[0], "spec", "rules", "0", "host") != "packages.example.org" {
			t.Errorf("Ingress = %v", ing)
		}
		route := find(docs, "HTTPRoute", "trove")
		hn := list(route, "spec", "hostnames")
		if route == nil || len(hn) != 1 || hn[0] != "packages.example.org" {
			t.Errorf("HTTPRoute hostnames = %v", hn)
		}
	})

	t.Run("R-HA-040 config.server.trusted_proxies reaches the rendered configuration of every role, and nothing else is trusted by default", func(t *testing.T) {
		cfg := func(sets ...string) string {
			_, docs := render(t, required(), sets...)
			return str(find(docs, "ConfigMap", "trove-config"), "data", "trove.yaml")
		}
		if got := cfg(); strings.Contains(got, "trusted_proxies") && !strings.Contains(got, "trusted_proxies: []") {
			t.Errorf("the default configuration trusts proxies:\n%s", got)
		}
		got := cfg("config.server.trusted_proxies={10.42.0.0/24}")
		if !strings.Contains(got, "trusted_proxies") || !strings.Contains(got, "10.42.0.0/24") {
			t.Errorf("the configuration does not carry the trusted proxy:\n%s", got)
		}
	})

	t.Run("R-HA-040 the chart asks the edge proxy for no Host or X-Forwarded-Host handling: the URLs come from base_url", func(t *testing.T) {
		out, _ := render(t, []string{gatewaySet()}, "ingress.enabled=true", "ingress.backendTLS.caSecret=trove-tls")
		for _, banned := range []string{"X-Forwarded-Host", "use-forwarded-host", "preserve-host", "upstream-vhost", "x-forwarded-host"} {
			if strings.Contains(out, banned) {
				t.Errorf("the rendered chart mentions %q", banned)
			}
		}
	})
}
