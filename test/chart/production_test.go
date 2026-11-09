//go:build chart

package chart_test

import (
	"path/filepath"
	"strings"
	"testing"
)

// productionSet is the example values file of a production installation (deploy/helm/trove/examples/production.yaml).
func productionSet() string { return filepath.Join(chartDir, "examples", "production.yaml") }

// productionDeployments are the Deployments the example renders: the three roles and the pool vuln of the worker.
var productionDeployments = []string{"trove-api", "trove-packages", "trove-worker", "trove-worker-vuln"}

// TestR_HA_032_ProductionExample (R-HA-032, exit criterion E5.5): the production example values render, for every Deployment, a
// PodDisruptionBudget, the two topology spread constraints and a HorizontalPodAutoscaler that owns the replica count.
func TestR_HA_032_ProductionExample(t *testing.T) {
	_, docs := render(t, []string{productionSet()})
	for _, name := range productionDeployments {
		dep := find(docs, "Deployment", name)
		if dep == nil {
			t.Fatalf("no Deployment %s", name)
		}
		t.Run("PodDisruptionBudget of "+name, func(t *testing.T) {
			pdb := find(docs, "PodDisruptionBudget", name)
			if pdb == nil {
				t.Fatalf("no PodDisruptionBudget %s", name)
			}
			if at(pdb, "spec", "maxUnavailable") != 1 && str(pdb, "spec", "maxUnavailable") != "1" {
				t.Errorf("maxUnavailable = %v, want 1", at(pdb, "spec", "maxUnavailable"))
			}
			if str(pdb, "spec", "unhealthyPodEvictionPolicy") != "AlwaysAllow" {
				t.Errorf("unhealthyPodEvictionPolicy = %q", str(pdb, "spec", "unhealthyPodEvictionPolicy"))
			}
			if !equalMaps(at(pdb, "spec", "selector", "matchLabels"), at(dep, "spec", "selector", "matchLabels")) {
				t.Errorf("the selector differs from the Deployment's")
			}
		})
		t.Run("topology spreading of "+name, func(t *testing.T) {
			keys := map[string]any{}
			for _, c := range list(dep, "spec", "template", "spec", "topologySpreadConstraints") {
				keys[str(c, "topologyKey")] = c
				if str(c, "nodeTaintsPolicy") != "Honor" {
					t.Errorf("%s nodeTaintsPolicy = %q, want Honor", str(c, "topologyKey"), str(c, "nodeTaintsPolicy"))
				}
			}
			if zone, host := keys["topology.kubernetes.io/zone"], keys["kubernetes.io/hostname"]; zone == nil || host == nil || len(keys) != 2 {
				t.Fatalf("constraints = %v, want zone and hostname", keys)
			} else if str(zone, "whenUnsatisfiable") != "ScheduleAnyway" || str(host, "whenUnsatisfiable") != "DoNotSchedule" {
				t.Errorf("zone %q, hostname %q", str(zone, "whenUnsatisfiable"), str(host, "whenUnsatisfiable"))
			}
		})
		t.Run("HorizontalPodAutoscaler of "+name, func(t *testing.T) {
			hpa := find(docs, "HorizontalPodAutoscaler", name)
			if hpa == nil {
				t.Fatalf("no HorizontalPodAutoscaler %s", name)
			}
			if str(hpa, "apiVersion") != "autoscaling/v2" || str(hpa, "spec", "scaleTargetRef", "name") != name {
				t.Errorf("apiVersion %q, target %q", str(hpa, "apiVersion"), str(hpa, "spec", "scaleTargetRef", "name"))
			}
			if at(dep, "spec", "replicas") != nil {
				t.Errorf("the Deployment sets replicas %v, which would reset the autoscaler's choice on every upgrade", at(dep, "spec", "replicas"))
			}
			min, max := at(hpa, "spec", "minReplicas"), at(hpa, "spec", "maxReplicas")
			if min == nil || max == nil || min.(int) < 2 || max.(int) <= min.(int) {
				t.Errorf("minReplicas %v, maxReplicas %v", min, max)
			}
		})
	}
}

// TestR_HA_042_ProductionExample (R-HA-042, exit criterion E5.5): the production example renders a NetworkPolicy per Deployment
// that admits the client port from the named edge namespaces and the operations port from the monitoring namespace, and no egress.
func TestR_HA_042_ProductionExample(t *testing.T) {
	_, docs := render(t, []string{productionSet()})
	for _, name := range productionDeployments {
		pol := find(docs, "NetworkPolicy", name)
		if pol == nil {
			t.Fatalf("no NetworkPolicy %s", name)
		}
		if got := list(pol, "spec", "policyTypes"); len(got) != 1 || got[0] != "Ingress" {
			t.Errorf("%s policyTypes = %v, want [Ingress] only", name, got)
		}
		var ops []any
		var http []any
		for _, r := range list(pol, "spec", "ingress") {
			p, from, _ := portsOf(r)
			if len(p) == 1 && p[0] == 8080 {
				http = from
			}
			if len(p) == 1 && p[0] == 8081 {
				ops = from
			}
		}
		if len(ops) != 1 || str(ops[0], "namespaceSelector", "matchLabels", "kubernetes.io/metadata.name") != "monitoring" {
			t.Errorf("%s admits 8081 from %v, want the monitoring namespace", name, ops)
		}
		if strings.HasPrefix(name, "trove-worker") {
			if http != nil {
				t.Errorf("%s admits 8080 from %v, a worker opens no client port", name, http)
			}
			continue
		}
		var ns []string
		for _, f := range http {
			ns = append(ns, str(f, "namespaceSelector", "matchLabels", "kubernetes.io/metadata.name"))
		}
		if strings.Join(ns, ",") != "ingress-controller,envoy-gateway-system" {
			t.Errorf("%s admits 8080 from %v, want the Ingress controller's and the Gateway's namespaces", name, ns)
		}
	}
}

// TestR_HA_045_ProductionExample (R-HA-045, exit criterion E5.5): the production example renders the ServiceMonitor and the
// PrometheusRule, with the labels the Prometheus selects on and every alert of the requirement, the backup's among them.
func TestR_HA_045_ProductionExample(t *testing.T) {
	_, docs := render(t, []string{productionSet()})
	sm := find(docs, "ServiceMonitor", "trove")
	if sm == nil {
		t.Fatal("no ServiceMonitor trove")
	}
	pr := find(docs, "PrometheusRule", "trove")
	if pr == nil {
		t.Fatal("no PrometheusRule trove")
	}
	for _, o := range []doc{sm, pr} {
		if str(o, "metadata", "labels", "release") != "kube-prometheus-stack" {
			t.Errorf("%s carries the labels %v", str(o, "kind"), at(o, "metadata", "labels"))
		}
	}
	if ep := at(sm, "spec", "endpoints", "0"); str(ep, "port") != "ops" || str(ep, "scheme") != "https" {
		t.Errorf("endpoint = %v, want the ops port over https", ep)
	}
	have := map[string]bool{}
	for _, r := range list(pr, "spec", "groups", "0", "rules") {
		have[str(r, "alert")] = true
	}
	for _, a := range alertSeverities() {
		if !have[a.name] {
			t.Errorf("alert %s is not rendered with the production example", a.name)
		}
	}
}

// TestR_HA_038_ProductionExample (R-HA-038, exit criterion E5.5): the production example renders the Ingress and the HTTPRoute
// side by side, both to the same Services, and a BackendTLSPolicy for every Service the route names.
func TestR_HA_038_ProductionExample(t *testing.T) {
	_, docs := render(t, []string{productionSet()})
	route := find(docs, "HTTPRoute", "trove")
	if route == nil || find(docs, "Ingress", "trove") == nil {
		t.Fatalf("route %v, ingress %v", route != nil, find(docs, "Ingress", "trove") != nil)
	}
	if strings.Join(routeTable(t, route), " ") != strings.Join(edgeTable(docs), " ") {
		t.Errorf("the route sends %v, the Ingress %v", routeTable(t, route), edgeTable(docs))
	}
	if len(ofKind(docs, "BackendTLSPolicy")) == 0 {
		t.Error("no BackendTLSPolicy")
	}
}
