//go:build chart

package chart_test

import (
	"encoding/json"
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

func deploymentNames(docs []doc) []string {
	var out []string
	for _, d := range ofKind(docs, "Deployment") {
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
		want := []string{"trove-api", "trove-api-admin", "trove-api-ep-customers", "trove-packages", "trove-packages-ep-customers",
			"trove-packages-npm", "trove-worker", "trove-worker-bulk", "trove-worker-vuln"}
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
		// the bridge: api 3, packages 4, web 4, web endpoint pool 4: 15; the budget of the first step counts all of them
		if got := str(env["TROVE_DATABASE_PREFLIGHT_EXPECTED_SERVING_REPLICAS"], "value"); got != "15" {
			t.Errorf("serving replicas in the bridge = %q, want 15", got)
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
			"trove-ep-customers-api": {"trove-api-ep-customers"},
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
		for _, n := range []string{"trove-ep-customers", "trove-ep-customers-api"} {
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
			"/api/v1": "trove-ep-customers-api", "/": "trove-ep-customers-api",
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
		if find(docs, "Service", "trove-ep-c-api") != nil {
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

	t.Run("R-HA-042 the policies of the default render are api, packages, worker and migrate", func(t *testing.T) {
		_, docs := render(t, required())
		var got []string
		for _, np := range ofKind(docs, "NetworkPolicy") {
			got = append(got, str(np, "metadata", "name"))
		}
		sort.Strings(got)
		if want := []string{"trove-api", "trove-migrate", "trove-packages", "trove-worker"}; !equalStrings(got, want) {
			t.Errorf("policies = %v, want %v", got, want)
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
	} {
		t.Run("R-HA-029 R-HA-062 "+tc.name, func(t *testing.T) { renderFails(t, required(), tc.sets, tc.want...) })
	}

	t.Run("R-HA-029 a worker pool below two replicas renders without highAvailability.enforce, and an autoscaler counts by its minimum", func(t *testing.T) {
		render(t, required(), "highAvailability.enforce=false", "worker.pools[0].name=a", "worker.pools[0].queues={vuln}", "worker.pools[0].replicaCount=1")
		render(t, required(), "api.autoscaling.enabled=true", "api.autoscaling.minReplicas=2", "api.replicaCount=1")
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
		if find(docs, "Deployment", "trove-packages-ep-customers") != nil || find(docs, "Deployment", "trove-api-ep-customers") != nil {
			t.Error("endpoint pools of the roles are rendered next to the web pool")
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
