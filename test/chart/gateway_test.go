//go:build chart

package chart_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func gatewaySet() string { return filepath.Join(chartDir, "ci", "gateway.yaml") }

// edgeTable returns the (path, service) pairs the Ingress names for its first host, in order.
func edgeTable(docs []doc) []string {
	var out []string
	for _, p := range list(find(docs, "Ingress", "trove"), "spec", "rules", "0", "http", "paths") {
		out = append(out, fmt.Sprintf("%s>%s", str(p, "path"), str(p, "backend", "service", "name")))
	}
	return out
}

// routeTable returns the (path, service) pairs the HTTPRoute names, one per rule, in order.
func routeTable(t *testing.T, route doc) []string {
	t.Helper()
	var out []string
	for _, r := range list(route, "spec", "rules") {
		m := list(r, "matches")
		if len(m) != 1 || str(m[0], "path", "type") != "PathPrefix" {
			t.Errorf("rule matches = %v, want exactly one PathPrefix", m)
			continue
		}
		refs := list(r, "backendRefs")
		if len(refs) != 1 {
			t.Errorf("rule backendRefs = %v, want one", refs)
			continue
		}
		out = append(out, fmt.Sprintf("%s>%s", str(m[0], "path", "value"), str(refs[0], "name")))
	}
	return out
}

// TestR_HA_038_GatewayAPIExposure (R-HA-038): gateway.enabled renders an HTTPRoute on the parentRefs with one
// PathPrefix rule per row of the Ingress's table, timeouts and filters on every rule, BackendTLSPolicies for the
// Services it names; Ingress and HTTPRoute may run side by side; nothing renders when it is off; the Gateway API
// and the values are checked before anything is created.
func TestR_HA_038_GatewayAPIExposure(t *testing.T) {
	t.Run("the HTTPRoute is not rendered by default", func(t *testing.T) {
		_, docs := render(t, required())
		if got := ofKind(docs, "HTTPRoute"); len(got) != 0 {
			t.Errorf("%d HTTPRoutes rendered with gateway.enabled false", len(got))
		}
	})

	_, docs := render(t, []string{gatewaySet()})
	route := find(docs, "HTTPRoute", "trove")
	if route == nil {
		t.Fatal("no HTTPRoute trove with gateway.enabled")
	}

	t.Run("it attaches to gateway.parentRefs with the host of the base URL", func(t *testing.T) {
		if str(route, "apiVersion") != "gateway.networking.k8s.io/v1" {
			t.Errorf("apiVersion = %q", str(route, "apiVersion"))
		}
		pr := list(route, "spec", "parentRefs")
		if len(pr) != 1 || str(pr[0], "name") != "shared" || str(pr[0], "namespace") != "gateway-system" || str(pr[0], "sectionName") != "https" {
			t.Errorf("parentRefs = %v", pr)
		}
		hn := list(route, "spec", "hostnames")
		if len(hn) != 1 || hn[0] != "trove.example.com" {
			t.Errorf("hostnames = %v, want [trove.example.com]", hn)
		}
	})

	t.Run("one PathPrefix rule per row of the Ingress's table, most specific first, to the Service of its role", func(t *testing.T) {
		got, want := routeTable(t, route), edgeTable(docs)
		if len(want) < 7 {
			t.Fatalf("the Ingress table has %d rows: %v", len(want), want)
		}
		if strings.Join(got, " ") != strings.Join(want, " ") {
			t.Errorf("HTTPRoute rules = %v\nIngress rows   = %v", got, want)
		}
		for _, row := range []string{"/api/v1/exports>trove-api-exports", "/api/v1>trove-api", "/api>trove-packages", "/files>trove-packages", "/v2>trove-packages", "/.well-known>trove-packages", "/>trove-api"} {
			found := false
			for _, g := range got {
				found = found || g == row
			}
			if !found {
				t.Errorf("no rule %s in %v", row, got)
			}
		}
	})

	t.Run("every rule carries the timeouts, the filters and the Service port", func(t *testing.T) {
		for i, r := range list(route, "spec", "rules") {
			if str(r, "timeouts", "request") != "2h" || str(r, "timeouts", "backendRequest") != "90m" {
				t.Errorf("rule %d timeouts = %v", i, at(r, "timeouts"))
			}
			f := list(r, "filters")
			if len(f) != 1 || str(f[0], "type") != "RequestHeaderModifier" {
				t.Errorf("rule %d filters = %v", i, f)
			}
			if at(r, "backendRefs", "0", "port") != 8080 {
				t.Errorf("rule %d port = %v, want 8080", i, at(r, "backendRefs", "0", "port"))
			}
		}
	})

	t.Run("Ingress and HTTPRoute are rendered together", func(t *testing.T) {
		if find(docs, "Ingress", "trove") == nil {
			t.Error("no Ingress beside the HTTPRoute")
		}
	})

	t.Run("one BackendTLSPolicy per Service the HTTPRoute names", func(t *testing.T) {
		services := map[string]bool{}
		for _, row := range routeTable(t, route) {
			services[strings.SplitN(row, ">", 2)[1]] = true
		}
		for svc := range services {
			p := find(docs, "BackendTLSPolicy", svc)
			if p == nil {
				t.Errorf("no BackendTLSPolicy %s", svc)
				continue
			}
			if str(p, "spec", "targetRefs", "0", "name") != svc || str(p, "spec", "targetRefs", "0", "kind") != "Service" {
				t.Errorf("policy %s targetRefs = %v", svc, at(p, "spec", "targetRefs"))
			}
		}
		if got := len(ofKind(docs, "BackendTLSPolicy")); got != len(services) {
			t.Errorf("%d BackendTLSPolicies for %d Services", got, len(services))
		}
	})

	t.Run("hostnames, labels, annotations and extraRules come from the values", func(t *testing.T) {
		_, d := render(t, required(),
			"gateway.enabled=true", "gateway.parentRefs[0].name=shared",
			"gateway.hostnames[0]=a.example.com", "gateway.hostnames[1]=b.example.com",
			"gateway.labels.team=platform", "gateway.annotations.note=x",
			"gateway.extraRules[0].matches[0].path.type=PathPrefix", "gateway.extraRules[0].matches[0].path.value=/extra",
			"gateway.extraRules[0].backendRefs[0].name=other", "gateway.extraRules[0].backendRefs[0].port=80")
		r := find(d, "HTTPRoute", "trove")
		if r == nil {
			t.Fatal("no HTTPRoute")
		}
		if hn := list(r, "spec", "hostnames"); len(hn) != 2 || hn[0] != "a.example.com" || hn[1] != "b.example.com" {
			t.Errorf("hostnames = %v", hn)
		}
		if str(r, "metadata", "labels", "team") != "platform" || str(r, "metadata", "annotations", "note") != "x" {
			t.Errorf("metadata = %v", at(r, "metadata"))
		}
		rules := list(r, "spec", "rules")
		last := rules[len(rules)-1]
		if str(last, "matches", "0", "path", "value") != "/extra" || str(last, "backendRefs", "0", "name") != "other" {
			t.Errorf("last rule = %v, want the extra rule", last)
		}
		if str(rules[0], "timeouts", "request") != "3600s" || str(rules[0], "timeouts", "backendRequest") != "3600s" {
			t.Errorf("default timeouts = %v, want 3600s", at(rules[0], "timeouts"))
		}
		if len(list(rules[0], "filters")) != 0 {
			t.Errorf("filters = %v by default", at(rules[0], "filters"))
		}
	})

	t.Run("in the bridge the host goes whole to <fullname>", func(t *testing.T) {
		_, d := render(t, required(), "gateway.enabled=true", "gateway.parentRefs[0].name=shared", "web.enabled=true")
		r := find(d, "HTTPRoute", "trove")
		if r == nil {
			t.Fatal("no HTTPRoute")
		}
		if got := routeTable(t, r); strings.Join(got, " ") != "/>trove" {
			t.Errorf("rules = %v, want [/>trove]", got)
		}
	})

	t.Run("without the Gateway API in the cluster the render fails before anything is created", func(t *testing.T) {
		args := []string{"template", "trove", chartDir, "--namespace", "default", "-f", gatewaySet()}
		_, stderr, err := helm(t, args...)
		if err == nil {
			t.Fatal("helm template succeeded without the Gateway API capability")
		}
		if !strings.Contains(stderr, "gateway.enabled needs the Gateway API") {
			t.Errorf("error does not name the Gateway API:\n%s", stderr)
		}
	})

	t.Run("gateway.parentRefs is required", func(t *testing.T) {
		renderFails(t, required(), []string{"gateway.enabled=true"}, "gateway.parentRefs is required")
	})

	t.Run("backendRequest must not exceed request", func(t *testing.T) {
		renderFails(t, []string{gatewaySet()}, []string{"gateway.timeouts.backendRequest=3h"}, "gateway.timeouts.backendRequest (3h) must not exceed gateway.timeouts.request (2h)")
		renderFails(t, []string{gatewaySet()}, []string{"gateway.timeouts.request=30m", "gateway.timeouts.backendRequest=1h"}, "must not exceed")
		// A request timeout of 0s is "no timeout" in the Gateway API: nothing to exceed.
		render(t, []string{gatewaySet()}, "gateway.timeouts.request=0s", "gateway.timeouts.backendRequest=1h")
	})

	t.Run("a timeout that is no Gateway API duration is refused by the schema", func(t *testing.T) {
		renderFails(t, []string{gatewaySet()}, []string{"gateway.timeouts.request=forever"}, "/gateway/timeouts/request")
	})

	t.Run("an unknown gateway key is refused", func(t *testing.T) {
		renderFails(t, []string{gatewaySet()}, []string{"gateway.parentRef=x"}, "/gateway")
	})
}

// TestServiceMonitor (R-HA-045, the ServiceMonitor bullet only; the alerts of that requirement are not complete,
// see docs/developer/helm-chart.md): the ops port of <fullname>-ops, every pod, with the fixed role and pool
// relabelings first, the bearer token with config.metrics.auth static and the CA of the listener with TLS.
func TestServiceMonitor(t *testing.T) {
	t.Run("not rendered by default", func(t *testing.T) {
		_, docs := render(t, required())
		if got := ofKind(docs, "ServiceMonitor"); len(got) != 0 {
			t.Errorf("%d ServiceMonitors rendered with metrics.serviceMonitor.enabled false", len(got))
		}
	})

	_, docs := render(t, required(), "metrics.serviceMonitor.enabled=true",
		"metrics.serviceMonitor.labels.release=kube-prometheus-stack",
		"metrics.serviceMonitor.relabelings[0].targetLabel=extra", "metrics.serviceMonitor.relabelings[0].replacement=x",
		"metrics.serviceMonitor.metricRelabelings[0].action=drop", "metrics.serviceMonitor.metricRelabelings[0].sourceLabels[0]=__name__", "metrics.serviceMonitor.metricRelabelings[0].regex=go_.*")
	sm := find(docs, "ServiceMonitor", "trove")
	if sm == nil {
		t.Fatal("no ServiceMonitor trove")
	}
	ep := at(sm, "spec", "endpoints", "0")

	t.Run("it scrapes the ops port of <fullname>-ops at /metrics", func(t *testing.T) {
		if str(ep, "port") != "ops" || str(ep, "path") != "/metrics" || str(ep, "interval") != "30s" || str(ep, "scrapeTimeout") != "10s" {
			t.Errorf("endpoint = %v", ep)
		}
		sel := at(sm, "spec", "selector", "matchLabels")
		if str(sel, "app.kubernetes.io/component") != "ops" || str(sel, "app.kubernetes.io/instance") != "trove" {
			t.Errorf("selector = %v", sel)
		}
		ops := find(docs, "Service", "trove-ops")
		if ops == nil {
			t.Fatal("no Service trove-ops")
		}
		for k, v := range sel.(map[string]any) {
			if str(ops, "metadata", "labels", k) != v {
				t.Errorf("the selector %s=%v does not match the labels of trove-ops: %v", k, v, at(ops, "metadata", "labels"))
			}
		}
		if names := at(sm, "spec", "namespaceSelector", "matchNames"); fmt.Sprint(names) != "[default]" {
			t.Errorf("namespaceSelector = %v", names)
		}
	})

	t.Run("the role and pool relabelings come first, the values' after them", func(t *testing.T) {
		rel := list(ep, "relabelings")
		if len(rel) != 3 {
			t.Fatalf("relabelings = %v", rel)
		}
		if str(rel[0], "targetLabel") != "role" || fmt.Sprint(at(rel[0], "sourceLabels")) != "[__meta_kubernetes_pod_label_app_kubernetes_io_component]" {
			t.Errorf("relabeling 0 = %v", rel[0])
		}
		if str(rel[1], "targetLabel") != "pool" || fmt.Sprint(at(rel[1], "sourceLabels")) != "[__meta_kubernetes_pod_label_trove_pool]" {
			t.Errorf("relabeling 1 = %v", rel[1])
		}
		if str(rel[2], "targetLabel") != "extra" {
			t.Errorf("relabeling 2 = %v", rel[2])
		}
		if mr := list(ep, "metricRelabelings"); len(mr) != 1 || str(mr[0], "action") != "drop" {
			t.Errorf("metricRelabelings = %v", mr)
		}
		if str(sm, "metadata", "labels", "release") != "kube-prometheus-stack" {
			t.Errorf("labels = %v", at(sm, "metadata", "labels"))
		}
	})

	t.Run("the pod labels the relabelings read are on every serving pod", func(t *testing.T) {
		// component is the role (api, packages, worker), trove/pool the pool name, empty on the main Deployments.
		for _, name := range []string{"trove-api", "trove-packages", "trove-worker"} {
			dep := find(docs, "Deployment", name)
			if dep == nil {
				t.Fatalf("no Deployment %s", name)
			}
			labels := at(dep, "spec", "template", "metadata", "labels")
			if str(labels, "app.kubernetes.io/component") != strings.TrimPrefix(name, "trove-") {
				t.Errorf("%s component = %v", name, labels)
			}
			if _, ok := labels.(map[string]any)["trove/pool"]; !ok {
				t.Errorf("%s carries no trove/pool label: %v", name, labels)
			}
		}
	})

	t.Run("with TLS the endpoint verifies the pods against ca.crt of the listener's Secret", func(t *testing.T) {
		if str(ep, "scheme") != "https" || str(ep, "tlsConfig", "serverName") != "trove-ops.default.svc" ||
			str(ep, "tlsConfig", "ca", "secret", "name") != "trove-tls" || str(ep, "tlsConfig", "ca", "secret", "key") != "ca.crt" {
			t.Errorf("tlsConfig = %v scheme %q", at(ep, "tlsConfig"), str(ep, "scheme"))
		}
		_, d := render(t, required(), "metrics.serviceMonitor.enabled=true", "tls.ops.secretName=trove-ops-tls")
		if got := str(at(find(d, "ServiceMonitor", "trove"), "spec", "endpoints", "0"), "tlsConfig", "ca", "secret", "name"); got != "trove-ops-tls" {
			t.Errorf("CA Secret = %q, want the ops pair's trove-ops-tls", got)
		}
		_, plain := render(t, required(), "metrics.serviceMonitor.enabled=true", "tls.enabled=false")
		pep := at(find(plain, "ServiceMonitor", "trove"), "spec", "endpoints", "0")
		if str(pep, "scheme") != "" || at(pep, "tlsConfig") != nil {
			t.Errorf("plain-HTTP endpoint = %v", pep)
		}
	})

	t.Run("the CA Secret is read from the namespace of the ServiceMonitor, so it must be the release namespace with TLS", func(t *testing.T) {
		renderFails(t, required(), []string{"metrics.serviceMonitor.enabled=true", "metrics.serviceMonitor.namespace=monitoring"}, "metrics.serviceMonitor.namespace (monitoring) must be the release namespace")
		_, d := render(t, required(), "metrics.serviceMonitor.enabled=true", "metrics.serviceMonitor.namespace=monitoring", "tls.enabled=false")
		if str(find(d, "ServiceMonitor", "trove"), "metadata", "namespace") != "monitoring" {
			t.Error("the ServiceMonitor is not in the namespace set")
		}
	})

	t.Run("with config.metrics.auth static the bearer token comes from secrets.metricsToken", func(t *testing.T) {
		if at(ep, "authorization") != nil {
			t.Errorf("authorization = %v without static metrics auth", at(ep, "authorization"))
		}
		_, d := render(t, required(), "metrics.serviceMonitor.enabled=true", "config.metrics.auth=static",
			"secrets.metricsToken.existingSecret=trove-metrics", "secrets.metricsToken.key=bearer")
		a := at(find(d, "ServiceMonitor", "trove"), "spec", "endpoints", "0", "authorization")
		if str(a, "type") != "Bearer" || str(a, "credentials", "name") != "trove-metrics" || str(a, "credentials", "key") != "bearer" {
			t.Errorf("authorization = %v", a)
		}
	})
}
