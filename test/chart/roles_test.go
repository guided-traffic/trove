//go:build chart

package chart_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The cut into the roles api, packages and worker (R-HA-002, R-HA-028, R-HA-032, R-HA-037, R-HA-042, R-HA-062,
// R-HA-064, R-EP-008, ADR-154, ADR-155): every rendered object per role, kept apart by name, label and
// selector, the edge's path rules, the pools and the upgrade bridge.

func rolesSet() []string  { return []string{filepath.Join(chartDir, "ci", "roles-pools.yaml")} }
func bridgeSet() []string { return []string{filepath.Join(chartDir, "ci", "bridge.yaml")} }

// matches reports whether the labels satisfy a matchLabels selector.
func matches(selector, labels any) bool {
	sel, _ := selector.(map[string]any)
	lab, _ := labels.(map[string]any)
	if len(sel) == 0 {
		return false
	}
	for k, v := range sel {
		if lab[k] != v {
			return false
		}
	}
	return true
}

// podsSelectedBy names the Deployments whose pods carry the labels of the selector.
func podsSelectedBy(docs []doc, selector any) []string {
	var out []string
	for _, d := range ofKind(docs, "Deployment") {
		if matches(selector, at(d, "spec", "template", "metadata", "labels")) {
			out = append(out, str(d, "metadata", "name"))
		}
	}
	sort.Strings(out)
	return out
}

func deploymentNames(docs []doc) []string { return names(docs, "Deployment") }

// names lists the names of the rendered objects of a kind, sorted.
func names(docs []doc, kind string) []string {
	var out []string
	for _, d := range ofKind(docs, kind) {
		out = append(out, str(d, "metadata", "name"))
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRolesDeployments(t *testing.T) {
	t.Run("R-HA-002 R-HA-062 a Deployment, PodDisruptionBudget and NetworkPolicy per role, pool and endpoint pool, named alike", func(t *testing.T) {
		_, docs := render(t, rolesSet())
		want := []string{
			"trove-api", "trove-api-admin", "trove-api-ep-customers", "trove-packages", "trove-packages-ep-customers",
			"trove-packages-npm", "trove-worker", "trove-worker-bulk", "trove-worker-vuln",
		}
		if got := deploymentNames(docs); !equalStrings(got, want) {
			t.Fatalf("Deployments = %v, want %v", got, want)
		}
		for _, kind := range []string{"PodDisruptionBudget", "NetworkPolicy"} {
			for _, n := range want {
				if find(docs, kind, n) == nil {
					t.Errorf("%s %s not rendered", kind, n)
				}
			}
		}
		// the autoscaler of the packages Deployment (autoscaling.enabled) is inherited by its pools
		for _, n := range []string{"trove-packages", "trove-packages-npm", "trove-packages-ep-customers"} {
			if find(docs, "HorizontalPodAutoscaler", n) == nil {
				t.Errorf("HorizontalPodAutoscaler %s not rendered", n)
			}
			if at(find(docs, "Deployment", n), "spec", "replicas") != nil {
				t.Errorf("Deployment %s keeps spec.replicas next to its autoscaler", n)
			}
		}
		for _, n := range []string{"trove-api", "trove-worker"} {
			if find(docs, "HorizontalPodAutoscaler", n) != nil {
				t.Errorf("HorizontalPodAutoscaler %s rendered without autoscaling.enabled", n)
			}
			if at(find(docs, "Deployment", n), "spec", "replicas") == nil {
				t.Errorf("Deployment %s has no replicas without an autoscaler", n)
			}
		}
	})

	t.Run("R-HA-028 the component label of every pod names its role and the pool label its pool, selectors never overlap", func(t *testing.T) {
		_, docs := render(t, rolesSet())
		wantPool := map[string]string{
			"trove-api": "", "trove-packages": "", "trove-worker": "",
			"trove-api-admin": "admin", "trove-packages-npm": "npm", "trove-worker-vuln": "vuln", "trove-worker-bulk": "bulk",
			"trove-api-ep-customers": "ep-customers", "trove-packages-ep-customers": "ep-customers",
		}
		for _, d := range ofKind(docs, "Deployment") {
			name := str(d, "metadata", "name")
			role, _, _ := strings.Cut(strings.TrimPrefix(name, "trove-"), "-")
			labels := at(d, "spec", "template", "metadata", "labels")
			if got := str(labels, "app.kubernetes.io/component"); got != role {
				t.Errorf("%s component = %q, want %q", name, got, role)
			}
			if v, ok := at(labels, "trove/pool").(string); !ok || v != wantPool[name] {
				t.Errorf("%s trove/pool = %v, want %q", name, at(labels, "trove/pool"), wantPool[name])
			}
			if str(labels, "trove/serve") != "true" {
				t.Errorf("%s pods are not labelled trove/serve", name)
			}
			// the selector selects the pods of this Deployment and of no other
			if got := podsSelectedBy(docs, at(d, "spec", "selector", "matchLabels")); !equalStrings(got, []string{name}) {
				t.Errorf("the selector of %s selects the pods of %v", name, got)
			}
			if pdb := find(docs, "PodDisruptionBudget", name); !equalMaps(at(pdb, "spec", "selector", "matchLabels"), at(d, "spec", "selector", "matchLabels")) {
				t.Errorf("the PodDisruptionBudget of %s selects other pods than the Deployment", name)
			}
			if np := find(docs, "NetworkPolicy", name); !equalMaps(at(np, "spec", "podSelector", "matchLabels"), at(d, "spec", "selector", "matchLabels")) {
				t.Errorf("the NetworkPolicy of %s selects other pods than the Deployment", name)
			}
		}
	})

	t.Run("R-HA-002 R-HA-062 each Deployment runs the --role of its name; a worker claims queues by environment", func(t *testing.T) {
		_, docs := render(t, rolesSet())
		for _, d := range ofKind(docs, "Deployment") {
			name := str(d, "metadata", "name")
			role, _, _ := strings.Cut(strings.TrimPrefix(name, "trove-"), "-")
			pod := at(d, "spec", "template", "spec")
			if got := strings.Join(containerArgs(pod), " "); got != "serve --role "+role {
				t.Errorf("%s args = %q, want serve --role %s", name, got, role)
			}
			env := envByName(pod)
			want := map[string]string{
				"trove-worker":      "TROVE_JOBS_EXCLUDE_QUEUES=" + `["index","mirror","vuln"]`,
				"trove-worker-vuln": "TROVE_JOBS_QUEUES=" + `["vuln"]`,
				"trove-worker-bulk": "TROVE_JOBS_QUEUES=" + `["index","mirror"]`,
			}[name]
			for _, v := range []string{"TROVE_JOBS_QUEUES", "TROVE_JOBS_EXCLUDE_QUEUES"} {
				got := ""
				if env[v] != nil {
					got = v + "=" + str(env[v], "value")
				}
				if strings.HasPrefix(want, v+"=") {
					if got != want {
						t.Errorf("%s: %s, want %s", name, got, want)
					}
				} else if got != "" {
					t.Errorf("%s sets %s, want it unset", name, got)
				}
			}
		}
	})

	t.Run("R-HA-062 the queue lists travel as JSON lists, which the loader reads as one key", func(t *testing.T) {
		_, docs := render(t, rolesSet())
		for name, want := range map[string]string{"trove-worker-vuln": "TROVE_JOBS_QUEUES", "trove-worker": "TROVE_JOBS_EXCLUDE_QUEUES"} {
			var l []string
			if err := json.Unmarshal([]byte(str(envByName(at(find(docs, "Deployment", name), "spec", "template", "spec"))[want], "value")), &l); err != nil || len(l) == 0 {
				t.Errorf("%s: %s is not a JSON list: %v", name, want, err)
			}
		}
		// without a worker pool the main worker excludes nothing and claims every queue
		_, docs = render(t, required())
		env := envByName(at(find(docs, "Deployment", "trove-worker"), "spec", "template", "spec"))
		if env["TROVE_JOBS_QUEUES"] != nil || env["TROVE_JOBS_EXCLUDE_QUEUES"] != nil {
			t.Error("the main worker restricts its queues although no worker pool exists")
		}
	})

	t.Run("R-HA-002 a pool inherits the role's values and overrides them key by key", func(t *testing.T) {
		_, docs := render(t, rolesSet())
		if at(find(docs, "Deployment", "trove-worker-vuln"), "spec", "replicas") != 2 {
			t.Errorf("the vuln pool replicas = %v, want its own 2", at(find(docs, "Deployment", "trove-worker-vuln"), "spec", "replicas"))
		}
		if at(find(docs, "Deployment", "trove-worker-bulk"), "spec", "replicas") != 2 {
			t.Errorf("the bulk pool replicas = %v, want the worker value 2", at(find(docs, "Deployment", "trove-worker-bulk"), "spec", "replicas"))
		}
		res := at(find(docs, "Deployment", "trove-api-admin"), "spec", "template", "spec", "containers", "0", "resources")
		if str(res, "limits", "memory") != "1Gi" || str(res, "requests", "cpu") != "500m" {
			t.Errorf("the api pool resources = %v, want the api values", res)
		}
	})

	t.Run("R-HA-016 R-HA-032 the migration Job counts the replicas at their maximum with the surge", func(t *testing.T) {
		_, docs := render(t, rolesSet())
		env := envByName(at(find(docs, "Job", "trove-migrate"), "spec", "template", "spec"))
		// api 2+1, api pool admin 2+1, packages 8+1 (autoscaler), pool npm 8+1 and the endpoint pools packages 8+1 and api 2+1
		if got := str(env["TROVE_DATABASE_PREFLIGHT_EXPECTED_SERVING_REPLICAS"], "value"); got != "36" {
			t.Errorf("serving replicas = %q, want 36", got)
		}
		// worker 2+1, vuln pool 2+1, bulk pool 2+1
		if got := str(env["TROVE_DATABASE_PREFLIGHT_EXPECTED_WORKER_REPLICAS"], "value"); got != "9" {
			t.Errorf("worker replicas = %q, want 9", got)
		}
		_, docs = render(t, bridgeSet())
		env = envByName(at(find(docs, "Job", "trove-migrate"), "spec", "template", "spec"))
		// the bridge: api 3, packages 4, web 4, web endpoint pool 4 and the packages endpoint pool 4: 19; the budget of the first step counts all of them
		if got := str(env["TROVE_DATABASE_PREFLIGHT_EXPECTED_SERVING_REPLICAS"], "value"); got != "19" {
			t.Errorf("serving replicas in the bridge = %q, want 19", got)
		}
	})

	t.Run("R-HA-016 the notes print the connection budget of the defaults (231) and of the bridge (311 with the defaults)", func(t *testing.T) {
		got := notesOf(t, required())
		if !strings.Contains(got, "max_connections of at least 231") {
			t.Errorf("notes lack the budget 231:\n%s", got)
		}
		// the bridge adds the web Deployment (3 + 1): serving_max 11, (11 + 3) x 20 + 12 + 19
		if got := notesOf(t, required(), "web.enabled=true"); !strings.Contains(got, "max_connections of at least 311") {
			t.Errorf("notes lack the budget 311 of the bridge:\n%s", got)
		}
	})
}

func TestRolesServices(t *testing.T) {
	t.Run("R-HA-002 R-HA-062 each Service selects the pods of one Deployment; the ops Service every serving pod", func(t *testing.T) {
		_, docs := render(t, rolesSet())
		want := map[string][]string{
			"trove-api":              {"trove-api"},
			"trove-packages":         {"trove-packages"},
			"trove-api-admin":        {"trove-api-admin"},
			"trove-packages-npm":     {"trove-packages-npm"},
			"trove-ep-customers":     {"trove-packages-ep-customers"},
			"trove-api-ep-customers": {"trove-api-ep-customers"},
			"trove-ops":              deploymentNames(docs),
		}
		for _, s := range ofKind(docs, "Service") {
			name := str(s, "metadata", "name")
			exp, ok := want[name]
			if !ok {
				t.Errorf("unexpected Service %s", name)
				continue
			}
			if got := podsSelectedBy(docs, at(s, "spec", "selector")); !equalStrings(got, exp) {
				t.Errorf("Service %s selects the pods of %v, want %v", name, got, exp)
			}
			if str(s, "spec", "sessionAffinity") != "None" {
				t.Errorf("Service %s sessionAffinity = %q", name, str(s, "spec", "sessionAffinity"))
			}
			delete(want, name)
		}
		for name := range want {
			t.Errorf("Service %s not rendered", name)
		}
	})

	t.Run("R-HA-002 the role Services and the pool Services open port http 8080, the endpoint Services their port", func(t *testing.T) {
		_, docs := render(t, rolesSet())
		for _, n := range []string{"trove-api", "trove-packages", "trove-api-admin", "trove-packages-npm"} {
			s := find(docs, "Service", n)
			if str(s, "spec", "ports", "0", "name") != "http" || at(s, "spec", "ports", "0", "port") != 8080 || str(s, "spec", "ports", "0", "targetPort") != "http" {
				t.Errorf("Service %s ports = %v", n, at(s, "spec", "ports"))
			}
		}
		for _, n := range []string{"trove-ep-customers", "trove-api-ep-customers"} {
			s := find(docs, "Service", n)
			if at(s, "spec", "ports", "0", "port") != 8443 || str(s, "spec", "ports", "0", "name") != "ep-customers" {
				t.Errorf("Service %s ports = %v", n, at(s, "spec", "ports"))
			}
		}
	})

	t.Run("R-HA-028 containers open exactly the ports of their role, pool and endpoints", func(t *testing.T) {
		_, docs := render(t, rolesSet())
		want := map[string][]int{
			"trove-api": {8080, 8081}, "trove-packages": {8080, 8081}, "trove-api-admin": {8080, 8081}, "trove-packages-npm": {8080, 8081},
			"trove-worker": {8081}, "trove-worker-vuln": {8081}, "trove-worker-bulk": {8081},
			"trove-api-ep-customers": {8443, 8081}, "trove-packages-ep-customers": {8443, 8081},
		}
		for n, ports := range want {
			got := containerPorts(at(find(docs, "Deployment", n), "spec", "template", "spec"))
			if len(got) != len(ports) {
				t.Errorf("%s ports = %v, want %v", n, got, ports)
				continue
			}
			for _, p := range ports {
				if !hasInt(got, p) {
					t.Errorf("%s ports = %v, want %v", n, got, ports)
				}
			}
		}
	})
}

// edgeRules lists path -> Service of the first rule of every host of an Ingress.
func edgeRules(ing any) map[string]string {
	out := map[string]string{}
	for _, p := range list(ing, "spec", "rules", "0", "http", "paths") {
		if str(p, "pathType") != "Prefix" {
			out["!pathType:"+str(p, "path")] = str(p, "pathType")
		}
		out[str(p, "path")] = str(p, "backend", "service", "name")
	}
	return out
}

func TestRolesEdge(t *testing.T) {
	t.Run("R-HA-062 Q-HA-90 a pool with routed false renders without its route; the route follows when the key goes", func(t *testing.T) {
		sets := []string{"ingress.enabled=true", "ingress.backendTLS.caSecret=trove-tls", "packages.pools[0].name=npm", "packages.pools[0].paths={/api/npm}"}
		_, docs := render(t, required(), append(sets, "packages.pools[0].routed=false")...)
		if find(docs, "Deployment", "trove-packages-npm") == nil {
			t.Error("the pool Deployment is not rendered with routed: false")
		}
		if got := edgeRules(find(docs, "Ingress", "trove")); got["/api/npm"] != "" || len(got) != 6 {
			t.Errorf("rules with routed: false = %v, want the six fixed rules", got)
		}
		_, docs = render(t, required(), sets...)
		if got := edgeRules(find(docs, "Ingress", "trove")); got["/api/npm"] != "trove-packages-npm" {
			t.Errorf("rules without routed = %v, want /api/npm to trove-packages-npm", got)
		}
	})

	t.Run("R-EP-008 an endpoint named cust over both roles and an endpoint named cust-api name different Services", func(t *testing.T) {
		_, docs := render(t, required(),
			"endpoints.cust.port=8443", "endpoints.cust.baseUrl=https://a.example.com", "endpoints.cust.surface={packages,api}",
			"endpoints.cust-api.port=8444", "endpoints.cust-api.baseUrl=https://b.example.com", "endpoints.cust-api.surface={packages}")
		seen := map[string]bool{}
		for _, s := range ofKind(docs, "Service") {
			n := str(s, "metadata", "name")
			if seen[n] {
				t.Errorf("two Services are named %s", n)
			}
			seen[n] = true
		}
		for n, want := range map[string][]string{
			"trove-ep-cust": {"trove-packages"}, "trove-api-ep-cust": {"trove-api"}, "trove-ep-cust-api": {"trove-packages"},
		} {
			s := find(docs, "Service", n)
			if s == nil {
				t.Errorf("Service %s not rendered", n)
				continue
			}
			if got := podsSelectedBy(docs, at(s, "spec", "selector")); !equalStrings(got, want) {
				t.Errorf("Service %s selects %v, want %v", n, got, want)
			}
		}
	})

	t.Run("R-HA-037 each host is split by the six fixed Prefix rules between the role Services", func(t *testing.T) {
		_, docs := render(t, required(), "ingress.enabled=true", "ingress.backendTLS.caSecret=trove-tls", "ingress.hosts={trove.example.com,trove.internal.example.com}")
		ing := find(docs, "Ingress", "trove")
		want := map[string]string{
			"/": "trove-api", "/api/v1": "trove-api",
			"/api": "trove-packages", "/files": "trove-packages", "/v2": "trove-packages", "/.well-known": "trove-packages",
		}
		if len(list(ing, "spec", "rules")) != 2 {
			t.Fatalf("%d host rules, want 2", len(list(ing, "spec", "rules")))
		}
		for i := range list(ing, "spec", "rules") {
			got := map[string]string{}
			for _, p := range list(ing, "spec", "rules", string(rune('0'+i)), "http", "paths") {
				got[str(p, "path")] = str(p, "backend", "service", "name")
				if str(p, "pathType") != "Prefix" || str(p, "backend", "service", "port", "name") != "http" {
					t.Errorf("rule %v is not a Prefix rule to port http", p)
				}
			}
			if len(got) != len(want) {
				t.Errorf("host %d rules = %v, want %v", i, got, want)
			}
			for p, s := range want {
				if got[p] != s {
					t.Errorf("host %d: %s -> %q, want %q", i, p, got[p], s)
				}
			}
		}
	})

	t.Run("R-HA-062 the paths of the path pools route to their own Services, before the role's prefixes", func(t *testing.T) {
		_, docs := render(t, rolesSet())
		got := edgeRules(find(docs, "Ingress", "trove"))
		for p, s := range map[string]string{
			"/api/v1/repositories": "trove-api-admin", "/api/npm": "trove-packages-npm",
			"/": "trove-api", "/api/v1": "trove-api", "/api": "trove-packages", "/files": "trove-packages", "/v2": "trove-packages", "/.well-known": "trove-packages",
		} {
			if got[p] != s {
				t.Errorf("%s -> %q, want %q", p, got[p], s)
			}
		}
		if len(got) != 8 {
			t.Errorf("rules = %v, want the six fixed rows and two pool paths", got)
		}
		paths := list(find(docs, "Ingress", "trove"), "spec", "rules", "0", "http", "paths")
		if str(paths[0], "path") != "/api/v1/repositories" || str(paths[1], "path") != "/api/npm" {
			t.Errorf("the pool paths do not come first: %v", paths)
		}
	})

	t.Run("R-EP-008 a further endpoint within one role is routed whole, one over both roles by the rows of its families", func(t *testing.T) {
		_, docs := render(t, rolesSet())
		got := edgeRules(find(docs, "Ingress", "trove-ep-customers"))
		want := map[string]string{
			"/api/v1": "trove-api-ep-customers", "/": "trove-api-ep-customers",
			"/api": "trove-ep-customers", "/files": "trove-ep-customers", "/v2": "trove-ep-customers", "/.well-known": "trove-ep-customers",
		}
		if len(got) != len(want) {
			t.Errorf("customers rules = %v, want %v", got, want)
		}
		for p, s := range want {
			if got[p] != s {
				t.Errorf("customers %s -> %q, want %q", p, got[p], s)
			}
		}
		for _, p := range list(find(docs, "Ingress", "trove-ep-customers"), "spec", "rules", "0", "http", "paths") {
			if at(p, "backend", "service", "port", "number") != 8443 {
				t.Errorf("customers rule %v does not name the endpoint's port", p)
			}
		}
		// packages only: the whole host to the one Service
		_, docs = render(t, poolSet(), "endpoints.customers.ingress.backendTLS.caSecret=x")
		if got := edgeRules(find(docs, "Ingress", "trove-ep-customers")); len(got) != 1 || got["/"] != "trove-ep-customers" {
			t.Errorf("a packages-only endpoint rules = %v, want / to trove-ep-customers", got)
		}
		// an api-only surface is served by api pods
		_, docs = render(t, required(), "endpoints.c.port=8443", "endpoints.c.baseUrl=https://p.example.com", "endpoints.c.surface={api}")
		if got := podsSelectedBy(docs, at(find(docs, "Service", "trove-ep-c"), "spec", "selector")); !equalStrings(got, []string{"trove-api"}) {
			t.Errorf("an api-only endpoint is served by %v, want trove-api", got)
		}
		if find(docs, "Service", "trove-api-ep-c") != nil {
			t.Error("a second Service is rendered for a surface within one role")
		}
	})

	t.Run("R-EP-008 a surface over both roles opens its port on the main Deployment of each", func(t *testing.T) {
		_, docs := render(t, required(), "endpoints.c.port=8443", "endpoints.c.baseUrl=https://p.example.com", "endpoints.c.surface={packages,files,api}")
		for _, n := range []string{"trove-api", "trove-packages"} {
			if got := containerPorts(at(find(docs, "Deployment", n), "spec", "template", "spec")); !hasInt(got, 8443) {
				t.Errorf("%s ports = %v, want 8443", n, got)
			}
			found := false
			for _, r := range list(find(docs, "NetworkPolicy", n), "spec", "ingress") {
				if p, _, _ := portsOf(r); hasInt(p, 8443) {
					found = true
				}
			}
			if !found {
				t.Errorf("the policy of %s has no rule for 8443", n)
			}
		}
	})
}

func TestRolesNetworkPolicy(t *testing.T) {
	t.Run("R-HA-042 every policy of every value set is ingress only, every rule names one port and never an egress key", func(t *testing.T) {
		eachSet(t, func(t *testing.T, _ string, out string, docs []doc) {
			for _, np := range ofKind(docs, "NetworkPolicy") {
				name := str(np, "metadata", "name")
				if pt := list(np, "spec", "policyTypes"); len(pt) != 1 || pt[0] != "Ingress" {
					t.Errorf("%s policyTypes = %v", name, pt)
				}
				if at(np, "spec", "egress") != nil {
					t.Errorf("%s has an egress key", name)
				}
				for i, r := range list(np, "spec", "ingress") {
					if p, _, _ := portsOf(r); len(p) != 1 {
						t.Errorf("%s rule %d names ports %v, want exactly one", name, i, p)
					}
					if l, ok := at(r, "from").([]any); ok && len(l) == 0 {
						t.Errorf("%s rule %d has an empty from, which admits every peer", name, i)
					}
				}
				// the policies of the chart select its own pods only
				if sel, _ := at(np, "spec", "podSelector", "matchLabels").(map[string]any); sel["app.kubernetes.io/instance"] == nil || sel["app.kubernetes.io/component"] == nil {
					t.Errorf("%s selects pods by %v", name, sel)
				}
			}
			if strings.Contains(out, "Egress") || strings.Contains(out, "egress:") {
				t.Error("the rendered output mentions egress")
			}
		})
	})

	t.Run("R-HA-042 each policy admits the ports its pods open and no other, a pool's own 8080 and an endpoint pool's none", func(t *testing.T) {
		_, docs := render(t, rolesSet())
		want := map[string][]int{
			"trove-api": {8080, 8081}, "trove-packages": {8080, 8081}, "trove-api-admin": {8080, 8081}, "trove-packages-npm": {8080, 8081},
			"trove-worker": {8081}, "trove-worker-vuln": {8081}, "trove-worker-bulk": {8081},
			"trove-api-ep-customers": {8443, 8081}, "trove-packages-ep-customers": {8443, 8081},
			"trove-migrate": nil,
		}
		for n, ports := range want {
			var got []int
			for _, r := range list(find(docs, "NetworkPolicy", n), "spec", "ingress") {
				p, from, has := portsOf(r)
				got = append(got, p...)
				switch {
				case len(p) == 1 && p[0] == 8081:
					if !has || len(from) == 0 {
						t.Errorf("%s: 8081 without named peers", n)
					}
				case len(p) == 1 && p[0] == 8443:
					if !has || str(from[0], "namespaceSelector", "matchLabels", "kubernetes.io/metadata.name") != "ingress" {
						t.Errorf("%s: 8443 from %v, want the endpoint's networkPolicy.from", n, from)
					}
				case len(p) == 1 && p[0] == 8080:
					if has {
						t.Errorf("%s: 8080 has a from, want from anywhere by default", n)
					}
				}
			}
			if len(got) != len(ports) {
				t.Errorf("%s admits %v, want %v", n, got, ports)
				continue
			}
			for _, p := range ports {
				if !hasInt(got, p) {
					t.Errorf("%s admits %v, want %v", n, got, ports)
				}
			}
		}
	})

	t.Run("R-HA-042 the policies of the default render are api, packages, worker and migrate, and backup with the backup on", func(t *testing.T) {
		_, docs := render(t, required())
		var got []string
		for _, np := range ofKind(docs, "NetworkPolicy") {
			got = append(got, str(np, "metadata", "name"))
		}
		sort.Strings(got)
		if want := []string{"trove-api", "trove-migrate", "trove-packages", "trove-worker"}; !equalStrings(got, want) {
			t.Errorf("policies = %v, want %v", got, want)
		}
		if _, d := render(t, required(), backupSets()...); find(d, "NetworkPolicy", "trove-backup") == nil {
			t.Error("no policy trove-backup with backup.enabled: true")
		}
		if r := list(find(docs, "NetworkPolicy", "trove-migrate"), "spec", "ingress"); len(r) != 0 || at(find(docs, "NetworkPolicy", "trove-migrate"), "spec", "ingress") == nil {
			t.Errorf("trove-migrate ingress = %v, want an explicit empty list", r)
		}
	})

	t.Run("R-HA-042 networkPolicy.egress is not a value", func(t *testing.T) {
		renderFails(t, required(), []string{"networkPolicy.egress.enabled=true"}, "egress")
	})
}

func TestRolesValidation(t *testing.T) {
	pools := func(extra ...string) []string { return extra }
	for _, tc := range []struct {
		name string
		sets []string
		want []string
	}{
		{"two pools with the same name", pools("packages.pools[0].name=a", "packages.pools[0].paths={/api/npm}", "packages.pools[1].name=a", "packages.pools[1].paths={/api/nuget}"), []string{"packages.pools", "two pools named a"}},
		{"a pool name that starts with ep-", pools("packages.pools[0].name=ep-x", "packages.pools[0].paths={/api/npm}"), []string{"packages.pools[ep-x].name", "ep-"}},
		{"two pools with the same path prefix", pools("packages.pools[0].name=a", "packages.pools[0].paths={/api/npm}", "packages.pools[1].name=b", "packages.pools[1].paths={/api/npm}"), []string{"packages.pools[b].paths", "/api/npm", "packages.pools[a]"}},
		{"a packages pool path in the api families", pools("packages.pools[0].name=a", "packages.pools[0].paths={/api/v1/system}"), []string{"packages.pools[a].paths", "/api/v1/system"}},
		{"a packages pool path equal to the role's prefix", pools("packages.pools[0].name=a", "packages.pools[0].paths={/api}"), []string{"packages.pools[a].paths", "/api"}},
		{"a packages pool path outside every family", pools("packages.pools[0].name=a", "packages.pools[0].paths={/ui/x}"), []string{"packages.pools[a].paths", "/ui/x"}},
		{"an api pool path in the packages families", pools("api.pools[0].name=a", "api.pools[0].paths={/api/npm}"), []string{"api.pools[a].paths", "/api/npm"}},
		{"an api pool path equal to the role's prefix", pools("api.pools[0].name=a", "api.pools[0].paths={/api/v1}"), []string{"api.pools[a].paths", "/api/v1"}},
		{"a worker pool queue that does not exist", pools("worker.pools[0].name=a", "worker.pools[0].queues={nope}"), []string{"worker.pools[a].queues", "nope"}},
		{"two worker pools with the same queue", pools("worker.pools[0].name=a", "worker.pools[0].queues={vuln}", "worker.pools[1].name=b", "worker.pools[1].queues={vuln}"), []string{"worker.pools[b].queues", "vuln", "pool a"}},
		{"a pool name too long for a Deployment name", pools("fullnameOverride="+strings.Repeat("a", 45), "packages.pools[0].name=abcdefghijklmnopqrst", "packages.pools[0].paths={/api/npm}"), []string{"packages.pools[abcdefghijklmnopqrst].name", "63"}},
		{"a pool below two replicas", pools("worker.pools[0].name=a", "worker.pools[0].queues={vuln}", "worker.pools[0].replicaCount=1"), []string{"worker.pools[a].replicaCount", "trove-worker-a"}},
		{"a pool without a disruption budget", pools("api.pools[0].name=a", "api.pools[0].paths={/ui/x}", "api.pools[0].podDisruptionBudget.enabled=false"), []string{"api.pools[a].podDisruptionBudget.enabled"}},
		{"a pool without a memory limit", pools("packages.pools[0].name=a", "packages.pools[0].paths={/api/npm}", "packages.pools[0].resources.limits.memory="), []string{"packages.pools[a].resources.limits.memory is required"}},
		{"a worker pool of the vuln queue whose spool cannot hold the archive", pools("worker.pools[0].name=v", "worker.pools[0].queues={vuln}", "worker.pools[0].spool.sizeLimit=3Gi", "config.vulnerabilities.sources.osv.max_archive_size=2GiB"), []string{"max_archive_size", "trove-worker-v", "queue vuln"}},
		{"an autoscaler without a metric", pools("api.autoscaling.enabled=true", "api.autoscaling.targetCPUUtilizationPercentage=0"), []string{"api.autoscaling"}},
		{"an autoscaler whose maximum is below its minimum", pools("packages.autoscaling.enabled=true", "packages.autoscaling.maxReplicas=2"), []string{"packages.autoscaling.maxReplicas", "minReplicas"}},
		{"an autoscaler below two replicas with highAvailability.enforce", pools("api.autoscaling.enabled=true", "api.autoscaling.minReplicas=1"), []string{"api.autoscaling.minReplicas"}},
		{"the upload of the UI larger than the api spool", pools("config.ui.deploy.max_upload_bytes=9663676416"), []string{"config.ui.deploy.max_upload_bytes", "api.spool.sizeLimit"}},
		{"a UI upload limit of 0 falls back to the upload limit, larger than the api spool", pools("config.ui.deploy.max_upload_bytes=0", "spool.sizeLimit=20Gi", "api.spool.sizeLimit=5Gi"), []string{"config.storage.upload.max_size", "api.spool.sizeLimit"}},
		{"networkPolicy egress", pools("networkPolicy.egress.enabled=true"), []string{"egress"}},
		{"worker pools that claim every queue", pools("worker.pools[0].name=a", "worker.pools[0].queues={default,maintenance,index}", "worker.pools[1].name=b", "worker.pools[1].queues={delivery,mirror,vuln}"), []string{"worker.pools", "every queue", "main worker"}},
		{"a worker pool with no replica", pools("highAvailability.enforce=false", "worker.pools[0].name=a", "worker.pools[0].queues={vuln}", "worker.pools[0].replicaCount=0"), []string{"worker.pools[a].replicaCount", "claimed by nobody"}},
		{"a default surface without a family of the role packages and no endpoint served by it", pools("endpoints.default.surface={api,ui}"), []string{"endpoints.default.surface", "role packages"}},
		{"a default surface without a family of the role api and no endpoint served by it", pools("endpoints.default.surface={packages}"), []string{"endpoints.default.surface", "role api"}},
	} {
		t.Run("R-HA-029 R-HA-062 "+tc.name, func(t *testing.T) { renderFails(t, required(), tc.sets, tc.want...) })
	}

	t.Run("R-HA-029 a worker pool below two replicas renders without highAvailability.enforce, and an autoscaler counts by its minimum", func(t *testing.T) {
		render(t, required(), "highAvailability.enforce=false", "worker.pools[0].name=a", "worker.pools[0].queues={vuln}", "worker.pools[0].replicaCount=1")
		render(t, required(), "api.autoscaling.enabled=true", "api.autoscaling.minReplicas=2", "api.replicaCount=1")
	})

	t.Run("R-HA-062 a default surface that leaves a role to a further endpoint, and a worker pool kept by its autoscaler, render", func(t *testing.T) {
		render(t, required(), "endpoints.default.surface={api,ui}", "endpoints.c.port=8443", "endpoints.c.baseUrl=https://p.example.com", "endpoints.c.surface={packages}")
		render(t, required(), "worker.pools[0].name=a", "worker.pools[0].queues={vuln}", "worker.pools[0].replicaCount=0",
			"worker.pools[0].autoscaling.enabled=true", "worker.pools[0].autoscaling.minReplicas=2", "worker.pools[0].autoscaling.targetCPUUtilizationPercentage=70")
	})

	t.Run("R-HA-029 the UI upload limit is checked against the api spool, the package upload limit against the packages spool", func(t *testing.T) {
		render(t, required(), "config.ui.deploy.max_upload_bytes=1073741824")
		render(t, required(), "config.storage.upload.max_size=1GiB", "packages.spool.sizeLimit=4Gi", "api.spool.sizeLimit=4Gi")
		renderFails(t, required(), []string{"packages.spool.sizeLimit=5Gi"}, "config.storage.upload.max_size", "Deployment trove-packages", "packages.spool.sizeLimit")
	})
}

func TestRolesBridge(t *testing.T) {
	t.Run("R-HA-064 web.enabled renders trove-web with --role web, the Service trove and an edge that routes every host whole to it", func(t *testing.T) {
		_, docs := render(t, bridgeSet())
		web := find(docs, "Deployment", "trove-web")
		if web == nil {
			t.Fatal("Deployment trove-web not rendered")
		}
		if got := strings.Join(containerArgs(at(web, "spec", "template", "spec")), " "); got != "serve --role web" {
			t.Errorf("trove-web args = %q", got)
		}
		svc := find(docs, "Service", "trove")
		if svc == nil {
			t.Fatal("Service trove not rendered")
		}
		if got := podsSelectedBy(docs, at(svc, "spec", "selector")); !equalStrings(got, []string{"trove-web"}) {
			t.Errorf("Service trove selects the pods of %v, want trove-web", got)
		}
		got := edgeRules(find(docs, "Ingress", "trove"))
		if len(got) != 1 || got["/"] != "trove" {
			t.Errorf("Ingress rules = %v, want / to trove", got)
		}
		// the Deployments of the cut run beside it
		for _, n := range []string{"trove-api", "trove-packages", "trove-worker"} {
			if find(docs, "Deployment", n) == nil {
				t.Errorf("Deployment %s not rendered next to the bridge", n)
			}
		}
		for _, n := range []string{"trove-api", "trove-packages"} {
			if find(docs, "Service", n) == nil {
				t.Errorf("Service %s not rendered next to the bridge", n)
			}
		}
		for _, n := range []string{"trove-web", "trove-api", "trove-packages", "trove-worker", "trove-web-ep-customers"} {
			if find(docs, "NetworkPolicy", n) == nil || find(docs, "PodDisruptionBudget", n) == nil {
				t.Errorf("%s has no NetworkPolicy or PodDisruptionBudget", n)
			}
		}
	})

	t.Run("R-HA-064 in the bridge an endpoint pool is a pool of web, and its Service selects it", func(t *testing.T) {
		_, docs := render(t, bridgeSet())
		pool := find(docs, "Deployment", "trove-web-ep-customers")
		if pool == nil {
			t.Fatal("Deployment trove-web-ep-customers not rendered")
		}
		pod := at(pool, "spec", "template", "spec")
		if got := strings.Join(containerArgs(pod), " "); got != "serve --role web" || str(envByName(pod)["TROVE_POOL_NAME"], "value") != "ep-customers" {
			t.Errorf("pool args = %q, TROVE_POOL_NAME = %v", got, envByName(pod)["TROVE_POOL_NAME"])
		}
		// the pool of the role is rendered beside it, so step 2 switches the Service onto pods that are ready since step 1;
		// no Service selects it yet (R-HA-064, Q-HA-90)
		if find(docs, "Deployment", "trove-packages-ep-customers") == nil {
			t.Error("the endpoint pool of the role packages is not rendered next to the web pool")
		}
		for _, s := range ofKind(docs, "Service") {
			if str(s, "metadata", "name") == "trove-ops" {
				continue
			}
			for _, n := range podsSelectedBy(docs, at(s, "spec", "selector")) {
				if n == "trove-packages-ep-customers" || n == "trove-api-ep-customers" {
					t.Errorf("Service %s selects the pool %s during the bridge", str(s, "metadata", "name"), n)
				}
			}
		}
		if got := podsSelectedBy(docs, at(find(docs, "Service", "trove-ep-customers"), "spec", "selector")); !equalStrings(got, []string{"trove-web-ep-customers"}) {
			t.Errorf("Service trove-ep-customers selects the pods of %v", got)
		}
		if got := edgeRules(find(docs, "Ingress", "trove-ep-customers")); len(got) != 1 || got["/"] != "trove-ep-customers" {
			t.Errorf("the endpoint Ingress rules = %v", got)
		}
		// the web pods keep the policy rules of both roles: 8080, and the policy of the pool 8443
		for n, ports := range map[string][]int{"trove-web": {8080, 8081}, "trove-web-ep-customers": {8443, 8081}} {
			var got []int
			for _, r := range list(find(docs, "NetworkPolicy", n), "spec", "ingress") {
				p, _, _ := portsOf(r)
				got = append(got, p...)
			}
			if len(got) != len(ports) || !hasInt(got, ports[0]) || !hasInt(got, ports[1]) {
				t.Errorf("%s admits %v, want %v", n, got, ports)
			}
		}
	})

	t.Run("R-HA-064 without web.enabled nothing of the old shape renders", func(t *testing.T) {
		_, docs := render(t, rolesSet())
		for _, k := range []string{"Deployment", "Service", "NetworkPolicy", "PodDisruptionBudget"} {
			for _, d := range ofKind(docs, k) {
				if n := str(d, "metadata", "name"); n == "trove" || strings.Contains(n, "-web") {
					t.Errorf("%s %s rendered without web.enabled", k, n)
				}
			}
		}
	})

	t.Run("R-HA-064 Q-HA-130 each step of the cut changes either the edge or the backends, never both", func(t *testing.T) {
		// step 1 (web.enabled), step 2 (web.routed false), step 3 (the target shape): an edge applies the removal of a
		// backend before a new route (F-64), so the step that moves the edge keeps every backend, and the step that
		// removes the bridge moves no route.
		_, s1 := render(t, bridgeSet())
		_, s2 := render(t, bridgeSet(), "web.routed=false")
		_, s3 := render(t, bridgeSet(), "web.enabled=false")
		for _, k := range []string{"Deployment", "Service"} {
			if a, b := names(s1, k), names(s2, k); !equalStrings(a, b) {
				t.Errorf("step 2 changes the %ss: %v -> %v", k, a, b)
			}
		}
		for _, n := range []string{"trove", "trove-ep-customers"} {
			if a, b := edgeRules(find(s2, "Ingress", n)), edgeRules(find(s3, "Ingress", n)); fmt.Sprint(a) != fmt.Sprint(b) {
				t.Errorf("step 3 moves the routes of Ingress %s: %v -> %v", n, a, b)
			}
		}
		for _, r := range edgeRules(find(s2, "Ingress", "trove")) {
			if r == "trove" {
				t.Errorf("step 2 still routes to the Service trove: %v", edgeRules(find(s2, "Ingress", "trove")))
			}
		}
		// every Service the edge names in step 3 already selects the same pods in step 2
		for _, ing := range ofKind(s3, "Ingress") {
			for _, n := range edgeRules(ing) {
				if got, want := podsSelectedBy(s2, at(find(s2, "Service", n), "spec", "selector")), podsSelectedBy(s3, at(find(s3, "Service", n), "spec", "selector")); len(want) == 0 || !equalStrings(got, want) {
					t.Errorf("Service %s selects %v in step 2 and %v in step 3", n, got, want)
				}
			}
		}
		// step 2 keeps the bridge running behind its own Service, and the endpoint's Service on the pool of the role
		if got := podsSelectedBy(s2, at(find(s2, "Service", "trove"), "spec", "selector")); !equalStrings(got, []string{"trove-web"}) {
			t.Errorf("Service trove selects %v in step 2, want trove-web", got)
		}
		if got := podsSelectedBy(s2, at(find(s2, "Service", "trove-ep-customers"), "spec", "selector")); !equalStrings(got, []string{"trove-packages-ep-customers"}) {
			t.Errorf("Service trove-ep-customers selects %v in step 2", got)
		}
		gone := []string{}
		for _, n := range names(s2, "Deployment") {
			if find(s3, "Deployment", n) == nil {
				gone = append(gone, n)
			}
		}
		if !equalStrings(gone, []string{"trove-web", "trove-web-ep-customers"}) {
			t.Errorf("step 3 removes the Deployments %v, want the bridge's", gone)
		}
		// web.routed means nothing without the bridge
		a, _ := render(t, required())
		b, _ := render(t, required(), "web.routed=false")
		if a != b {
			t.Error("web.routed=false changes the rendering without web.enabled")
		}
	})

	t.Run("R-HA-064 Q-HA-130 a Service that step 2 routes to is rendered in step 1, without a route", func(t *testing.T) {
		// an endpoint whose surface holds families of both roles: step 2 splits its host between trove-ep-customers and
		// trove-api-ep-customers, so the second exists since step 1 and has its endpoints when the route comes
		both := "endpoints.customers.surface={packages,files,api}"
		_, s1 := render(t, bridgeSet(), both)
		_, s2 := render(t, bridgeSet(), both, "web.routed=false")
		svc := find(s1, "Service", "trove-api-ep-customers")
		if svc == nil {
			t.Fatal("Service trove-api-ep-customers is not rendered in step 1")
		}
		if got := podsSelectedBy(s1, at(svc, "spec", "selector")); !equalStrings(got, []string{"trove-api-ep-customers"}) {
			t.Errorf("Service trove-api-ep-customers selects %v in step 1", got)
		}
		if got := edgeRules(find(s1, "Ingress", "trove-ep-customers")); len(got) != 1 || got["/"] != "trove-ep-customers" {
			t.Errorf("the endpoint Ingress of step 1 = %v, want / to trove-ep-customers", got)
		}
		if got := edgeRules(find(s2, "Ingress", "trove-ep-customers")); got["/api/v1"] != "trove-api-ep-customers" || got["/api"] != "trove-ep-customers" {
			t.Errorf("the endpoint Ingress of step 2 = %v", got)
		}
	})

	t.Run("R-HA-063 R-HA-064 the BackendTLSPolicies cover the Service trove and the role Services for the whole bridge", func(t *testing.T) {
		// a route outside the chart (a Gateway API HTTPRoute) moves at a time the chart does not know
		policy := []string{"tls.backendTLSPolicy.enabled=true", "tls.backendTLSPolicy.caConfigMapName=trove-tls-ca"}
		for _, c := range []struct {
			name string
			sets []string
			want []string
		}{
			{"step 1", nil, []string{"trove", "trove-api", "trove-packages"}},
			{"step 2", []string{"web.routed=false"}, []string{"trove", "trove-api", "trove-packages"}},
			{"step 3", []string{"web.enabled=false"}, []string{"trove-api", "trove-packages"}},
		} {
			_, docs := render(t, bridgeSet(), append(append([]string{}, policy...), c.sets...)...)
			if got := names(docs, "BackendTLSPolicy"); !equalStrings(got, c.want) {
				t.Errorf("%s: BackendTLSPolicies %v, want %v", c.name, got, c.want)
			}
		}
	})

	t.Run("R-HA-064 the notes name the step and the next one", func(t *testing.T) {
		if got := notesOf(t, bridgeSet()); !strings.Contains(got, "step 1 of 3") || !strings.Contains(got, "--set web.enabled=true --set web.routed=false") {
			t.Errorf("notes of step 1:\n%s", got)
		}
		if got := notesOf(t, bridgeSet(), "web.routed=false"); !strings.Contains(got, "step 2 of 3") || !strings.Contains(got, "web.enabled: false") {
			t.Errorf("notes of step 2:\n%s", got)
		}
		if got := notesOf(t, bridgeSet(), "web.enabled=false"); strings.Contains(got, "not complete") {
			t.Errorf("notes of step 3 still name the cut:\n%s", got)
		}
	})

	t.Run("R-HA-064 Q-HA-132 the guard refuses only the upgrade that moves the edge onto a target that is not ready, and step 3 before step 2", func(t *testing.T) {
		// The decision is a function of what trove.bridgeFacts reads with lookup; here the facts are given. The units of
		// ci/bridge.yaml: trove-api, trove-packages and the endpoint pool trove-packages-ep-customers.
		const ready = "deployments: {trove-api: {replicas: 3, ready: 3}, trove-packages: {replicas: 4, ready: 4}, trove-packages-ep-customers: {replicas: 3, ready: 3}}\n"
		const apiDown = "deployments: {trove-api: {replicas: 3, ready: 1}, trove-packages: {replicas: 4, ready: 4}, trove-packages-ep-customers: {replicas: 3, ready: 3}}\n"
		const noAPI = "deployments: {trove-packages: {replicas: 4, ready: 4}, trove-packages-ep-customers: {replicas: 3, ready: 3}}\n"
		const onBridge = "web: true\ningress: true\ningressToBridge: [/]\nendpointServices: {customers: web}\n"
		const moved = "web: true\ningress: true\ningressToBridge: []\nendpointServices: {customers: packages}\n"
		const unread = "web: true\ningress: false\ningressToBridge: []\nendpointServices: {}\n"
		step2 := []string{"web.routed=false"}
		step3 := []string{"web.enabled=false"}
		for _, c := range []struct {
			name  string
			sets  []string
			facts string
			want  []string // empty: the guard lets the upgrade through
		}{
			{"step 2 at step 2 with api crash-looping (the edge has moved)", step2, moved + apiDown, nil},
			{"step 3 at step 2 with api crash-looping (the edge has moved)", step3, moved + apiDown, nil},
			{"step 2 from step 1 with every target ready", step2, onBridge + ready, nil},
			{
				"step 2 from step 1 with api not ready", step2, onBridge + apiDown,
				[]string{"moves the edge", "the Ingress trove routes / to the Service trove", "the Service trove-ep-customers selects the web pods", "trove-api is missing or not fully ready", "three upgrades"},
			},
			{"step 2 from the old shape (no trove-api)", step2, onBridge + noAPI, []string{"trove-api is missing or not fully ready", "--set web.enabled=true"}},
			{
				"step 3 from step 1 (step 2 skipped)", step3, onBridge + ready,
				[]string{"web.enabled is false while the edge still routes to the bridge", "Run step 2 first", "--set web.enabled=true --set web.routed=false", "three upgrades"},
			},
			{
				"step 3 from step 1 with only the endpoint Service on the bridge", step3, "web: true\ningress: true\ningressToBridge: []\nendpointServices: {customers: web}\n" + ready,
				[]string{"still routes to the bridge (the Service trove-ep-customers selects the web pods)"},
			},
			{"no readable edge and no trove-api: step 1 has not run", step3, unread + noAPI, []string{"trove-api does not exist, so step 1 has not run", "three upgrades"}},
			{"no readable edge and trove-api not ready", step2, unread + apiDown, nil},
			{"no trove-web", step3, "web: false\n" + noAPI, nil},
			{"step 1 is never refused", nil, onBridge + noAPI, nil},
		} {
			got := bridgeGuardOf(t, bridgeSet(), c.sets, c.facts)
			if len(c.want) == 0 && got != "" {
				t.Errorf("%s: refused: %s", c.name, got)
			}
			if len(c.want) > 0 && got == "" {
				t.Errorf("%s: let through, want a refusal naming %q", c.name, c.want)
			}
			for _, w := range c.want {
				if got != "" && !strings.Contains(got, w) {
					t.Errorf("%s: the refusal does not name %q:\n%s", c.name, w, got)
				}
			}
		}
	})

	t.Run("R-HA-064 the guard reads the cluster only: helm template, which has none, renders web.enabled=false next to a trove-web", func(t *testing.T) {
		// lookup returns nothing without a cluster, so the two steps are enforced under helm upgrade only
		// (helm upgrade --dry-run=server, the upgrade e2e test); the rendering must not depend on it.
		a, _ := render(t, required())
		b, _ := render(t, required(), "web.enabled=false")
		if a != b {
			t.Error("the rendering differs between the default and web.enabled=false")
		}
	})

	t.Run("R-HA-064 the web values keep their keys: replicas, a pool and the schema", func(t *testing.T) {
		_, docs := render(t, bridgeSet(), "web.replicaCount=5", "web.resources.limits.memory=4Gi")
		if at(find(docs, "Deployment", "trove-web"), "spec", "replicas") != 5 {
			t.Errorf("trove-web replicas = %v", at(find(docs, "Deployment", "trove-web"), "spec", "replicas"))
		}
		renderFails(t, bridgeSet(), []string{"web.replicas=5"}, "replicas")
	})

	t.Run("R-HA-064 R-HA-032 the bridge's Deployment follows the rules of every role", func(t *testing.T) {
		renderFails(t, bridgeSet(), []string{"web.replicaCount=1"}, "web.replicaCount", "trove-web")
		renderFails(t, bridgeSet(), []string{"web.resources.limits=null"}, "web.resources.limits.memory is required")
	})
}

// bridgeGuardOf renders the decision of the bridge guard (trove.bridgeGuard) in a copy of the chart for the given facts
// of the cluster (the YAML trove.bridgeFacts reads with lookup) and returns its refusal, empty when it lets the upgrade
// through.
func bridgeGuardOf(t *testing.T, files, sets []string, facts string) string {
	t.Helper()
	dir := t.TempDir()
	copyDir(t, chartDir, filepath.Join(dir, "trove"))
	probe := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: guard\ndata:\n  message: {{ include \"trove.bridgeGuard\" (dict \"root\" . \"facts\" (`" +
		facts + "` | fromYaml)) | quote }}\n"
	if err := os.WriteFile(filepath.Join(dir, "trove", "templates", "guard-probe.yaml"), []byte(probe), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"template", "trove", filepath.Join(dir, "trove"), "--show-only", "templates/guard-probe.yaml", "--namespace", "default"}
	for _, f := range files {
		args = append(args, "-f", f)
	}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	out, stderr, err := helm(t, args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	docs := parse(t, out)
	if len(docs) != 1 {
		t.Fatalf("the probe rendered %d documents", len(docs))
	}
	return str(docs[0], "data", "message")
}

// notesOf renders NOTES.txt through the template trove.notes in a copy of the chart.
func notesOf(t *testing.T, files []string, sets ...string) string {
	t.Helper()
	dir := t.TempDir()
	copyDir(t, chartDir, filepath.Join(dir, "trove"))
	probe := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: notes\ndata:\n  notes: |\n{{ include \"trove.notes\" . | indent 4 }}\n"
	if err := os.WriteFile(filepath.Join(dir, "trove", "templates", "notes-probe.yaml"), []byte(probe), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"template", "trove", filepath.Join(dir, "trove"), "--show-only", "templates/notes-probe.yaml", "--namespace", "default"}
	for _, f := range files {
		args = append(args, "-f", f)
	}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	out, stderr, err := helm(t, args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, stderr)
	}
	return out
}
