//go:build chart

package chart_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func endpointsSet() []string { return []string{filepath.Join(chartDir, "ci", "endpoints.yaml")} }
func poolSet() []string      { return []string{filepath.Join(chartDir, "ci", "endpoints-pool.yaml")} }

// containerPorts returns the containerPort numbers of the pod's container.
func containerPorts(pod any) []int {
	var out []int
	for _, p := range list(pod, "containers", "0", "ports") {
		out = append(out, at(p, "containerPort").(int))
	}
	return out
}

func hasInt(l []int, n int) bool {
	for _, v := range l {
		if v == n {
			return true
		}
	}
	return false
}

func ingressHosts(ing any) []string {
	var out []string
	for _, r := range list(ing, "spec", "rules") {
		out = append(out, str(r, "host"))
	}
	return out
}

func TestEndpoints(t *testing.T) {
	t.Run("R-EP-008 two endpoints render two Services, two Ingresses, two NetworkPolicies and the configuration block", func(t *testing.T) {
		_, docs := render(t, endpointsSet())

		// Service per endpoint with its own port, selecting the main web pods.
		for name, port := range map[string]int{"customers": 8443, "partners": 8444} {
			svc := find(docs, "Service", "trove-ep-"+name)
			if svc == nil {
				t.Fatalf("Service trove-ep-%s not rendered", name)
			}
			if at(svc, "spec", "ports", "0", "port") != port || at(svc, "spec", "ports", "0", "targetPort") != port ||
				str(svc, "spec", "ports", "0", "name") != "ep-"+name || str(svc, "spec", "ports", "0", "appProtocol") != "https" {
				t.Errorf("Service %s ports = %v", name, at(svc, "spec", "ports"))
			}
			if str(svc, "spec", "sessionAffinity") != "None" {
				t.Errorf("Service %s sessionAffinity = %q", name, str(svc, "spec", "sessionAffinity"))
			}
			sel, _ := at(svc, "spec", "selector").(map[string]any)
			if sel["app.kubernetes.io/component"] != "web" || sel["trove/pool"] != "" {
				t.Errorf("Service %s selector = %v, want the main web pods", name, sel)
			}
		}
		web := at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec")
		if got := containerPorts(web); !hasInt(got, 8080) || !hasInt(got, 8443) || !hasInt(got, 8444) || !hasInt(got, 8081) {
			t.Errorf("web container ports = %v, want 8080, 8081 and both endpoint ports", got)
		}
		if got := containerPorts(at(find(docs, "Deployment", "trove-worker"), "spec", "template", "spec")); len(got) != 1 || got[0] != 8081 {
			t.Errorf("worker container ports = %v, want 8081 only", got)
		}

		// Ingress per endpoint: class, hosts incl. the wildcard, TLS entry, backend.
		cust := find(docs, "Ingress", "trove-ep-customers")
		if cust == nil {
			t.Fatal("Ingress trove-ep-customers not rendered")
		}
		if str(cust, "spec", "ingressClassName") != "traefik-external" {
			t.Errorf("customers class = %q", str(cust, "spec", "ingressClassName"))
		}
		if got := strings.Join(ingressHosts(cust), ","); got != "packages.example.com,pkgs.example.org,*.registry.example.com" {
			t.Errorf("customers hosts = %s", got)
		}
		if str(cust, "spec", "tls", "0", "secretName") != "packages-tls" || len(list(cust, "spec", "tls", "0", "hosts")) != 2 {
			t.Errorf("customers tls = %v", at(cust, "spec", "tls"))
		}
		for _, r := range list(cust, "spec", "rules") {
			if str(r, "http", "paths", "0", "path") != "/" || str(r, "http", "paths", "0", "pathType") != "Prefix" ||
				str(r, "http", "paths", "0", "backend", "service", "name") != "trove-ep-customers" ||
				at(r, "http", "paths", "0", "backend", "service", "port", "number") != 8443 {
				t.Errorf("customers rule = %v", r)
			}
		}
		if str(cust, "metadata", "annotations", "cert-manager.io/cluster-issuer") != "letsencrypt" {
			t.Errorf("customers annotations = %v", at(cust, "metadata", "annotations"))
		}
		part := find(docs, "Ingress", "trove-ep-partners")
		if part == nil || str(part, "spec", "ingressClassName") != "" || str(part, "spec", "tls", "0", "secretName") != "partners-tls" {
			t.Errorf("partners Ingress = %v", part)
		}
		// the default Ingress is untouched
		def := find(docs, "Ingress", "trove")
		if str(def, "spec", "ingressClassName") != "haproxy" || strings.Join(ingressHosts(def), ",") != "trove.example.com" {
			t.Errorf("default Ingress = %v", def)
		}

		// NetworkPolicy per endpoint port, from networkPolicy.from only.
		np := find(docs, "NetworkPolicy", "trove-ep-customers")
		rules := list(np, "spec", "ingress")
		if len(rules) != 1 {
			t.Fatalf("customers policy has %d rules, want 1", len(rules))
		}
		ports, from, has := portsOf(rules[0])
		if len(ports) != 1 || ports[0] != 8443 || !has || len(from) != 1 ||
			str(from[0], "namespaceSelector", "matchLabels", "kubernetes.io/metadata.name") != "ingress-external" {
			t.Errorf("customers rule = %v from %v", ports, from)
		}
		ports, _, has = portsOf(list(find(docs, "NetworkPolicy", "trove-ep-partners"), "spec", "ingress")[0])
		if len(ports) != 1 || ports[0] != 8444 || has {
			t.Errorf("partners rule = ports %v (has from %v), want 8444 from anywhere", ports, has)
		}
		// the web policy still admits the default ports only
		if p, _, _ := portsOf(list(find(docs, "NetworkPolicy", "trove-web"), "spec", "ingress")[0]); len(p) != 1 || p[0] != 8080 {
			t.Errorf("web policy rule 1 = %v", p)
		}

		// trove.yaml: the endpoints block and the default endpoint's hosts.
		cfg := configOf(t, docs)
		eps, _ := at(cfg, "server", "endpoints").(map[string]any)
		if len(eps) != 3 {
			t.Fatalf("server.endpoints = %v", eps)
		}
		c := eps["customers"]
		if str(c, "listen") != ":8443" || str(c, "base_url") != "https://packages.example.com" || str(c, "wildcard_host") != "*.registry.example.com" ||
			str(c, "wildcard_root") != "label" || str(c, "exposure") != "rules" || str(c, "replica_header") != "never" ||
			at(c, "max_in_flight") != 1024 || str(c, "rate_limit_class") != "external" || str(c, "host_roots", "pkgs.example.org") != "docker-hub" {
			t.Errorf("customers config = %v", c)
		}
		if at(c, "auth", "bound_tokens_only") != true || at(c, "auth", "anonymous") != false || at(c, "cache_fill") != false {
			t.Errorf("customers defaults = %v", c)
		}
		if got := list(c, "surface"); len(got) != 2 || got[0] != "packages" || got[1] != "files" {
			t.Errorf("customers surface = %v", got)
		}
		if at(eps["partners"], "exposure") != "all" {
			t.Errorf("partners exposure = %v", at(eps["partners"], "exposure"))
		}
		hosts := list(eps["default"], "hosts")
		if len(hosts) != 2 || hosts[0] != "trove.example.com" || hosts[1] != "trove.trove.svc" {
			t.Errorf("default hosts = %v, want the explicit endpoints.default.hosts", hosts)
		}
	})

	t.Run("R-EP-008 the default endpoint's hosts come from the base URLs once a further endpoint exists", func(t *testing.T) {
		_, docs := render(t, poolSet(), "config.server.additional_base_urls={https://trove.internal.example.com:8443}")
		hosts := list(at(configOf(t, docs), "server", "endpoints", "default"), "hosts")
		if len(hosts) != 2 || hosts[0] != "trove.example.com" || hosts[1] != "trove.internal.example.com" {
			t.Errorf("default hosts = %v", hosts)
		}
		_, docs = render(t, required())
		if at(configOf(t, docs), "server", "endpoints") != nil {
			t.Error("a chart without endpoints renders server.endpoints")
		}
		_, docs = render(t, required(), "endpoints.default.surface={packages,api}", "endpoints.default.replicaHeader=always")
		d := at(configOf(t, docs), "server", "endpoints", "default")
		if len(list(d, "surface")) != 2 || str(d, "replica_header") != "always" || at(d, "hosts") != nil {
			t.Errorf("a tuned default endpoint alone = %v", d)
		}
	})

	t.Run("R-EP-008 the endpoint's pairs are mounted as directories and rendered as tls.certificates", func(t *testing.T) {
		_, docs := render(t, endpointsSet())
		web := at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec")
		for secret, path := range map[string]string{
			"trove-packages-tls":          "/var/run/secrets/trove/endpoints/customers/0",
			"trove-packages-wildcard-tls": "/var/run/secrets/trove/endpoints/customers/1",
			"trove-customer-ca":           "/var/run/secrets/trove/endpoints/customers/client-ca",
		} {
			v := secretVolume(web, secret)
			if v == nil {
				t.Errorf("web does not mount Secret %s", secret)
				continue
			}
			m := mountOf(web, v)
			if str(m, "mountPath") != path || at(m, "subPath") != nil || at(m, "readOnly") != true {
				t.Errorf("mount of %s = %v, want %s read-only without subPath", secret, m, path)
			}
			if at(v, "secret", "defaultMode") != 288 || len(list(v, "secret", "items")) == 0 {
				t.Errorf("volume of %s = %v", secret, v)
			}
		}
		if secretVolume(at(find(docs, "Deployment", "trove-worker"), "spec", "template", "spec"), "trove-packages-tls") != nil {
			t.Error("a worker mounts an endpoint's certificate")
		}
		if secretVolume(at(find(docs, "Job", "trove-migrate"), "spec", "template", "spec"), "trove-packages-tls") != nil {
			t.Error("the migration Job mounts an endpoint's certificate")
		}
		c := at(configOf(t, docs), "server", "endpoints", "customers", "tls")
		certs := list(c, "certificates")
		if len(certs) != 2 || str(certs[0], "cert_file") != "/var/run/secrets/trove/endpoints/customers/0/tls.crt" ||
			str(certs[1], "key_file") != "/var/run/secrets/trove/endpoints/customers/1/tls.key" ||
			str(c, "client_ca_file") != "/var/run/secrets/trove/endpoints/customers/client-ca/ca.crt" {
			t.Errorf("customers tls = %v", c)
		}
		// partners names no Secret: it serves the pair of tls.secretName (server.tls) and renders none.
		if at(configOf(t, docs), "server", "endpoints", "partners", "tls") != nil {
			t.Error("an endpoint without its own Secret renders a tls block")
		}
		// extraSecrets alone: position 0 is the listener pair of tls.secretName.
		_, docs = render(t, required(), "endpoints.c.port=8443", "endpoints.c.baseUrl=https://p.example.com", "endpoints.c.tls.extraSecrets={wild}")
		certs = list(at(configOf(t, docs), "server", "endpoints", "c", "tls"), "certificates")
		if len(certs) != 2 || str(certs[0], "cert_file") != tlsMountPath+"/tls.crt" || str(certs[1], "cert_file") != "/var/run/secrets/trove/endpoints/c/1/tls.crt" {
			t.Errorf("certificates with extraSecrets only = %v", certs)
		}
	})

	t.Run("R-EP-008 backend TLS annotations follow the endpoint's controller, with the Service name as the verified name", func(t *testing.T) {
		_, docs := render(t, endpointsSet())
		part := find(docs, "Ingress", "trove-ep-partners")
		if str(part, "metadata", "annotations", "haproxy-ingress.github.io/secure-backends") != "true" ||
			str(part, "metadata", "annotations", "haproxy-ingress.github.io/secure-verify-ca-secret") != "trove-tls" ||
			!strings.HasPrefix(str(part, "metadata", "annotations", "haproxy-ingress.github.io/secure-sni"), "trove-ep-partners.") {
			t.Errorf("partners annotations = %v", at(part, "metadata", "annotations"))
		}
		// customers uses traefik with its own backendTLS: the scheme goes on its Service, no HAProxy keys on its Ingress.
		svc := find(docs, "Service", "trove-ep-customers")
		if str(svc, "metadata", "annotations", "traefik.ingress.kubernetes.io/service.serversscheme") != "https" {
			t.Errorf("customers Service annotations = %v", at(svc, "metadata", "annotations"))
		}
		for k := range at(find(docs, "Ingress", "trove-ep-customers"), "metadata", "annotations").(map[string]any) {
			if strings.HasPrefix(k, "haproxy-ingress") {
				t.Errorf("customers Ingress carries %s although its controller is traefik", k)
			}
		}
		if find(docs, "Service", "trove-ep-partners") == nil || at(find(docs, "Service", "trove-ep-partners"), "metadata", "annotations") != nil {
			t.Error("partners Service carries annotations although its controller is haproxy")
		}
	})

	t.Run("R-EP-008 an endpoint pool renders its Deployment, disruption budget and a Service that selects only its pods", func(t *testing.T) {
		_, docs := render(t, poolSet())
		d := find(docs, "Deployment", "trove-web-ep-customers")
		if d == nil {
			t.Fatal("Deployment trove-web-ep-customers not rendered")
		}
		pod := at(d, "spec", "template", "spec")
		if got := strings.Join(containerArgs(pod), " "); got != "serve --role web" {
			t.Errorf("pool args = %q", got)
		}
		if str(envByName(pod)["TROVE_POOL_NAME"], "value") != "ep-customers" {
			t.Errorf("TROVE_POOL_NAME = %v", envByName(pod)["TROVE_POOL_NAME"])
		}
		if at(d, "spec", "replicas") != 3 {
			t.Errorf("pool replicas = %v, want the pool's replicaCount", at(d, "spec", "replicas"))
		}
		if str(at(pod, "containers", "0", "resources", "limits"), "memory") != "1Gi" {
			t.Errorf("pool limits = %v, want the override", at(pod, "containers", "0", "resources", "limits"))
		}
		if got := str(at(pod, "containers", "0", "resources", "requests"), "cpu"); got != "1" {
			t.Errorf("pool requests.cpu = %q, want the web value", got)
		}
		if str(d, "spec", "selector", "matchLabels", "trove/pool") != "ep-customers" || str(d, "spec", "template", "metadata", "labels", "trove/pool") != "ep-customers" {
			t.Errorf("pool labels: selector %v", at(d, "spec", "selector"))
		}
		if got := containerPorts(pod); len(got) != 2 || !hasInt(got, 8443) || !hasInt(got, 8081) || hasInt(got, 8080) {
			t.Errorf("pool container ports = %v, want 8443 and 8081 and no 8080", got)
		}
		// the spread constraints select the pool's own pods
		for _, c := range list(pod, "topologySpreadConstraints") {
			if str(c, "labelSelector", "matchLabels", "trove/pool") != "ep-customers" {
				t.Errorf("pool spread selector = %v", at(c, "labelSelector"))
			}
		}
		if pdb := find(docs, "PodDisruptionBudget", "trove-web-ep-customers"); pdb == nil || str(pdb, "spec", "selector", "matchLabels", "trove/pool") != "ep-customers" {
			t.Errorf("pool PDB = %v", pdb)
		}
		svc := find(docs, "Service", "trove-ep-customers")
		if str(svc, "spec", "selector", "trove/pool") != "ep-customers" || str(svc, "spec", "selector", "app.kubernetes.io/component") != "web" {
			t.Errorf("Service selector = %v", at(svc, "spec", "selector"))
		}
		// the main web pods carry no container port 8443 and no TROVE_POOL_NAME, and mount no endpoint Secret
		web := at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec")
		if hasInt(containerPorts(web), 8443) || envByName(web)["TROVE_POOL_NAME"] != nil || secretVolume(web, "trove-packages-tls") != nil {
			t.Errorf("main web pods serve the pooled endpoint: ports %v", containerPorts(web))
		}
		if secretVolume(pod, "trove-packages-tls") == nil || secretVolume(pod, "trove-tls") == nil {
			t.Error("the pool does not mount its endpoint's certificate and the listener pair")
		}
		// the pool's pods are selected by no other policy, so its policy admits the operations port too
		np := find(docs, "NetworkPolicy", "trove-ep-customers")
		var all []int
		for _, r := range list(np, "spec", "ingress") {
			p, _, _ := portsOf(r)
			all = append(all, p...)
		}
		if len(all) != 2 || !hasInt(all, 8443) || !hasInt(all, 8081) {
			t.Errorf("pool policy ports = %v, want 8443 and 8081", all)
		}
		if str(np, "spec", "podSelector", "matchLabels", "trove/pool") != "ep-customers" {
			t.Errorf("pool policy selector = %v", at(np, "spec", "podSelector"))
		}
	})

	t.Run("R-EP-008 an endpoint pool can switch off what web has on", func(t *testing.T) {
		_, docs := render(t, poolSet(), "highAvailability.enforce=false", "endpoints.customers.pool.podDisruptionBudget.enabled=false", "endpoints.customers.pool.replicaCount=1")
		if find(docs, "PodDisruptionBudget", "trove-web-ep-customers") != nil {
			t.Error("the pool's disruption budget is rendered although the pool turned it off")
		}
		if find(docs, "PodDisruptionBudget", "trove-web") == nil {
			t.Error("the web disruption budget disappeared")
		}
	})

	t.Run("R-EP-008 the notes list each endpoint with its Service and hosts and warn about shared pods and open policies", func(t *testing.T) {
		// helm template does not print NOTES.txt; the text is the template trove.notes, rendered here through a ConfigMap in a copy of the chart.
		dir, err := os.MkdirTemp("", "notes-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		copyDir(t, chartDir, filepath.Join(dir, "trove"))
		probe := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: notes\ndata:\n  notes: |\n{{ include \"trove.notes\" . | indent 4 }}\n"
		if err := os.WriteFile(filepath.Join(dir, "trove", "templates", "notes-probe.yaml"), []byte(probe), 0o600); err != nil {
			t.Fatal(err)
		}
		notes := func(set []string) string {
			args := []string{"template", "trove", filepath.Join(dir, "trove"), "--show-only", "templates/notes-probe.yaml"}
			for _, f := range set {
				args = append(args, "-f", f)
			}
			out, stderr, err := helm(t, args...)
			if err != nil {
				t.Fatalf("%v\n%s", err, stderr)
			}
			return out
		}
		got := notes(endpointsSet())
		for _, want := range []string{
			"customers   Service trove-ep-customers:8443", "packages.example.com, pkgs.example.org, *.registry.example.com",
			"partners   Service trove-ep-partners:8444", "endpoint customers has exposure: rules and shares the pods of the default endpoint",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("notes lack %q:\n%s", want, got)
			}
		}
		if strings.Contains(got, "empty networkPolicy.from") && strings.Contains(got, "endpoint customers has exposure: rules and an empty") {
			t.Error("customers lists networkPolicy.from but is warned about an empty one")
		}
		got = notes(poolSet())
		if !strings.Contains(got, "pool trove-web-ep-customers") || !strings.Contains(got, "endpoint customers has exposure: rules and an empty networkPolicy.from") {
			t.Errorf("pool notes:\n%s", got)
		}
	})

	t.Run("R-EP-008 rendering fails and names the value", func(t *testing.T) {
		base := func(extra ...string) []string {
			return append([]string{"endpoints.c.port=8443", "endpoints.c.baseUrl=https://p.example.com"}, extra...)
		}
		for _, tc := range []struct {
			name string
			sets []string
			want []string
		}{
			{"port 8080", []string{"endpoints.c.port=8080", "endpoints.c.baseUrl=https://p.example.com"}, []string{"endpoints.c.port", "8080"}},
			{"port 8081", []string{"endpoints.c.port=8081", "endpoints.c.baseUrl=https://p.example.com"}, []string{"endpoints.c.port", "8081"}},
			{"a port shared with another endpoint", base("endpoints.d.port=8443", "endpoints.d.baseUrl=https://q.example.com"), []string{"endpoints.d.port", "8443", "endpoint c"}},
			{"no port", []string{"endpoints.c.baseUrl=https://p.example.com"}, []string{"endpoints.c.port"}},
			{"no base URL", []string{"endpoints.c.port=8443"}, []string{"endpoints.c.baseUrl"}},
			{"a base URL with a path", []string{"endpoints.c.port=8443", "endpoints.c.baseUrl=https://p.example.com/x"}, []string{"endpoints.c.baseUrl"}},
			{"an invalid name", []string{"endpoints.Cust.port=8443", "endpoints.Cust.baseUrl=https://p.example.com"}, []string{"endpoints"}},
			{"a name that is too long", []string{"endpoints.abcdefghijklmnopqrstu.port=8443", "endpoints.abcdefghijklmnopqrstu.baseUrl=https://p.example.com"}, []string{"endpoints"}},
			{"a Service name over 63 characters", []string{"fullnameOverride=" + strings.Repeat("a", 50), "endpoints.abcdefghijklmnopqrs.port=8443", "endpoints.abcdefghijklmnopqrs.baseUrl=https://p.example.com"}, []string{"endpoints.abcdefghijklmnopqrs", "63"}},
			{"a host in two endpoints", base("endpoints.d.port=8444", "endpoints.d.baseUrl=https://q.example.com", "endpoints.d.hosts={p.example.com}"), []string{"endpoints.d", "p.example.com", "endpoints.c"}},
			{"a host of the default endpoint", base("endpoints.c.hosts={trove.example.com}"), []string{"endpoints.c", "trove.example.com"}},
			{"a wildcard host that covers another endpoint's host", base("endpoints.d.port=8444", "endpoints.d.baseUrl=https://q.example.com", "endpoints.d.wildcardHost=*.example.com"), []string{"endpoints.d.wildcardHost", "p.example.com"}},
			{"an invalid wildcard host", base("endpoints.c.wildcardHost=*.com"), []string{"endpoints.c.wildcardHost"}},
			{"ui on a further endpoint", base("endpoints.c.surface={ui,packages}"), []string{"endpoints.c.surface", "ui"}},
			{"endpoints set inside config", []string{"config.server.endpoints.x.listen=:9000"}, []string{"config.server.endpoints"}},
			{"a pool below two replicas", base("endpoints.c.pool.enabled=true", "endpoints.c.pool.replicaCount=1"), []string{"endpoints.c.pool.replicaCount"}},
			{"extra pairs without TLS", base("tls.enabled=false", "endpoints.c.tls.extraSecrets={wild}"), []string{"endpoints.c.tls", "tls.enabled"}},
			{"a client CA without TLS", base("tls.enabled=false", "endpoints.c.tls.clientCaSecret=ca"), []string{"endpoints.c.tls", "tls.enabled"}},
			{"label roots without a wildcard host", base("endpoints.c.wildcardRoot=label"), []string{"endpoints.c.wildcardRoot"}},
			{"an endpoint Ingress that verifies with haproxy and names no CA (the default controller)", base("endpoints.c.ingress.enabled=true"), []string{"endpoints.c.ingress.backendTLS.caSecret", "haproxy", "verify"}},
			{"an endpoint Ingress that verifies with ingress-nginx and names no CA", base("endpoints.c.ingress.enabled=true", "endpoints.c.ingress.backendTLS.controller=ingress-nginx"), []string{"endpoints.c.ingress.backendTLS.caSecret", "ingress-nginx"}},
			{"an endpoint Ingress that verifies with traefik and names no ServersTransport", base("endpoints.c.ingress.enabled=true", "endpoints.c.ingress.backendTLS.controller=traefik"), []string{"endpoints.c.ingress.backendTLS.serversTransport", "traefik"}},
			{"an endpoint with a client CA behind haproxy and no client certificate Secret", base("endpoints.c.ingress.enabled=true", "endpoints.c.ingress.backendTLS.caSecret=ca", "endpoints.c.tls.existingSecret=c-tls", "endpoints.c.tls.clientCaSecret=c-ca"), []string{"endpoints.c.ingress.backendTLS.clientCertSecret", "haproxy"}},
			{"a global CA does not help an endpoint that sets verify itself under another controller", base("ingress.backendTLS.caSecret=ca", "endpoints.c.ingress.enabled=true", "endpoints.c.ingress.backendTLS.controller=traefik"), []string{"endpoints.c.ingress.backendTLS.serversTransport"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				renderFails(t, required(), tc.sets, tc.want...)
			})
		}
	})

	t.Run("R-EP-008 the schema refuses keys the default endpoint may not set and unknown keys", func(t *testing.T) {
		renderFails(t, required(), []string{"endpoints.default.port=9000"}, "endpoints")
		renderFails(t, required(), []string{"endpoints.default.baseUrl=https://x.example.com"}, "endpoints")
		renderFails(t, required(), []string{"endpoints.c.port=8443", "endpoints.c.baseUrl=https://p.example.com", "endpoints.c.surfaces={packages}"}, "endpoints")
		renderFails(t, required(), []string{"endpoints.c.port=8443", "endpoints.c.baseUrl=https://p.example.com", "endpoints.c.exposure=some"}, "exposure")
	})

	t.Run("R-EP-008 a sixteen-endpoint set renders and an endpoint can switch the Service type", func(t *testing.T) {
		_, docs := render(t, required(), "endpoints.c.port=8443", "endpoints.c.baseUrl=https://p.example.com",
			"endpoints.c.service.type=LoadBalancer", "endpoints.c.service.loadBalancerSourceRanges={10.0.0.0/8}")
		svc := find(docs, "Service", "trove-ep-c")
		if str(svc, "spec", "type") != "LoadBalancer" || str(svc, "spec", "loadBalancerSourceRanges", "0") != "10.0.0.0/8" {
			t.Errorf("Service = %v", svc)
		}
		var sets []string
		for i := 0; i < 16; i++ {
			n := "e" + strconv.Itoa(i)
			sets = append(sets, "endpoints."+n+".port="+strconv.Itoa(9000+i), "endpoints."+n+".baseUrl=https://"+n+".example.com")
		}
		_, docs = render(t, required(), sets...)
		if n := len(ofKind(docs, "Ingress")); n != 0 {
			t.Errorf("%d Ingresses rendered without ingress.enabled", n)
		}
		if n := len(ofKind(docs, "Service")); n != 2+16 {
			t.Errorf("%d Services", n)
		}
	})
}

func TestS3ProvisionerSecret(t *testing.T) {
	set := []string{filepath.Join(chartDir, "ci", "s3-provisioner.yaml")}

	t.Run("R-HA-030 bucket, region and endpoint are read from the provisioner's Secret and the credentials are mounted as files", func(t *testing.T) {
		_, docs := render(t, set)
		for _, name := range []string{"trove-web", "trove-worker"} {
			pod := at(find(docs, "Deployment", name), "spec", "template", "spec")
			env := envByName(pod)
			for v, key := range map[string]string{"TROVE_STORAGE_S3_BUCKET": "S3_BUCKET", "TROVE_STORAGE_S3_REGION": "S3_REGION", "TROVE_STORAGE_S3_ENDPOINT": "S3_ENDPOINT"} {
				if str(env[v], "valueFrom", "secretKeyRef", "name") != "trove-s3" || str(env[v], "valueFrom", "secretKeyRef", "key") != key {
					t.Errorf("%s: %s = %v, want key %s of trove-s3", name, v, env[v], key)
				}
			}
			vol := secretVolume(pod, "trove-s3")
			if vol == nil {
				t.Fatalf("%s: the credentials are not mounted", name)
			}
			keys := map[string]string{}
			for _, it := range list(vol, "secret", "items") {
				keys[str(it, "key")] = str(it, "path")
			}
			if len(keys) != 2 || keys["AWS_ACCESS_KEY_ID"] != "access-key-id" || keys["AWS_SECRET_ACCESS_KEY"] != "secret-access-key" {
				t.Errorf("%s: mounted keys = %v, want only the two credentials", name, keys)
			}
		}
		if envByName(at(find(docs, "Job", "trove-migrate"), "spec", "template", "spec"))["TROVE_STORAGE_S3_BUCKET"] != nil {
			t.Error("the migration Job reads the S3 settings")
		}
		s3 := at(configOf(t, docs), "storage", "s3").(map[string]any)
		if s3["bucket"] != "" || s3["region"] != "" || s3["endpoint"] != "" {
			t.Errorf("config.storage.s3 = %v, want empty bucket, region and endpoint (the environment carries them)", s3)
		}
	})

	t.Run("R-HA-030 without the *Key values the environment carries no S3 setting", func(t *testing.T) {
		_, docs := render(t, required())
		for _, v := range []string{"TROVE_STORAGE_S3_BUCKET", "TROVE_STORAGE_S3_REGION", "TROVE_STORAGE_S3_ENDPOINT"} {
			if envByName(at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec"))[v] != nil {
				t.Errorf("%s is set without secrets.s3", v)
			}
		}
	})

	t.Run("R-HA-029 the bucket is required unless the Secret carries it, and the keys need the Secret", func(t *testing.T) {
		renderFails(t, set, []string{"secrets.s3.bucketKey="}, "config.storage.s3.bucket", "bucketKey")
		renderFails(t, set, []string{"secrets.s3.existingSecret="}, "secrets.s3")
		renderFails(t, required(), []string{"secrets.s3.regionKey=S3_REGION"}, "secrets.s3.existingSecret")
	})
}

// copyDir copies a directory tree.
func copyDir(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		b, err := os.ReadFile(p) //nolint:gosec // the chart's own files
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}
