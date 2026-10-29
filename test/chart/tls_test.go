//go:build chart

package chart_test

import (
	"path/filepath"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// tlsMountPath is where the chart mounts the listener certificate (R-HA-063).
const tlsMountPath = "/var/run/secrets/trove/tls"

func haIngress() []string { return []string{filepath.Join(chartDir, "ci", "ha-ingress.yaml")} }

// secretVolume returns the volume of pod that projects the Secret secretName, or nil.
func secretVolume(pod any, secretName string) any {
	for _, v := range list(pod, "volumes") {
		if str(v, "secret", "secretName") == secretName {
			return v
		}
	}
	return nil
}

// mountOf returns the container volumeMount that uses the volume.
func mountOf(pod any, volume any) any {
	for _, m := range list(pod, "containers", "0", "volumeMounts") {
		if volume != nil && str(m, "name") == str(volume, "name") {
			return m
		}
	}
	return nil
}

func configOf(t *testing.T, docs []doc) doc {
	t.Helper()
	var cfg doc
	if err := yaml.Unmarshal([]byte(str(find(docs, "ConfigMap", "trove-config"), "data", "trove.yaml")), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestTLS(t *testing.T) {
	t.Run("R-HA-063 the default render serves HTTPS: probes, mount without subPath, config paths, appProtocol", func(t *testing.T) {
		_, docs := render(t, required())
		for _, role := range []string{"web", "worker"} {
			pod := at(find(docs, "Deployment", "trove-"+role), "spec", "template", "spec")
			c := at(pod, "containers", "0")
			for _, probe := range []string{"startupProbe", "livenessProbe", "readinessProbe"} {
				if got := str(c, probe, "httpGet", "scheme"); got != "HTTPS" {
					t.Errorf("trove-%s %s scheme = %q, want HTTPS", role, probe, got)
				}
				if got := str(c, probe, "httpGet", "port"); got != "ops" {
					t.Errorf("trove-%s %s port = %q, want ops", role, probe, got)
				}
			}
			vol := secretVolume(pod, "trove-tls")
			if vol == nil {
				t.Fatalf("trove-%s does not project the Secret trove-tls", role)
			}
			m := mountOf(pod, vol)
			if m == nil || str(m, "mountPath") != tlsMountPath+"" || at(m, "readOnly") != true {
				t.Errorf("trove-%s tls mount = %v, want a read-only mount at %s", role, m, tlsMountPath)
			}
			if at(m, "subPath") != nil {
				t.Errorf("trove-%s tls mount uses subPath, the kubelet's symlink swap would not reach the pod on rotation", role)
			}
			var keys []string
			for _, it := range list(vol, "secret", "items") {
				keys = append(keys, str(it, "key")+"->"+str(it, "path"))
			}
			if strings.Join(keys, ",") != "tls.crt->tls.crt,tls.key->tls.key" {
				t.Errorf("trove-%s projects %v, want exactly the certificate and the key (ca.crt is for the edge, not for this process)", role, keys)
			}
		}

		cfg := configOf(t, docs)
		want := map[string]string{
			"cert_file": tlsMountPath + "/tls.crt",
			"key_file":  tlsMountPath + "/tls.key",
		}
		for k, v := range want {
			if got := str(cfg, "server", "tls", "certificates", "0", k); got != v {
				t.Errorf("config server.tls.certificates[0].%s = %q, want %q", k, got, v)
			}
		}
		if got := str(cfg, "server", "tls", "min_version"); got != "1.2" {
			t.Errorf("server.tls.min_version = %q, want 1.2", got)
		}
		if got := str(cfg, "server", "tls", "reload_interval"); got != "10s" {
			t.Errorf("server.tls.reload_interval = %q, want 10s", got)
		}
		for _, absent := range [][]string{{"server", "tls", "client_ca_file"}, {"server", "tls", "client_auth"}, {"server", "ops_tls"}} {
			if at(cfg, absent...) != nil {
				t.Errorf("config %s is set without tls.clientCA or tls.ops", strings.Join(absent, "."))
			}
		}

		for _, name := range []string{"trove", "trove-ops"} {
			svc := find(docs, "Service", name)
			for _, p := range list(svc, "spec", "ports") {
				if got := str(p, "appProtocol"); got != "https" {
					t.Errorf("Service %s port %s appProtocol = %q, want https", name, str(p, "name"), got)
				}
			}
		}
	})

	t.Run("R-HA-063 tls.enabled without tls.secretName fails, names the key and says how to opt out", func(t *testing.T) {
		renderFails(t, []string{filepath.Join(chartDir, "ci", "required-only.yaml")}, []string{"tls.secretName="}, "tls.secretName", "tls.enabled: false")
	})

	t.Run("R-HA-063 tls.enabled=false renders HTTP probes, plain Service ports and no TLS wiring", func(t *testing.T) {
		out, docs := render(t, required(), "tls.enabled=false", "tls.secretName=")
		for _, role := range []string{"web", "worker"} {
			pod := at(find(docs, "Deployment", "trove-"+role), "spec", "template", "spec")
			c := at(pod, "containers", "0")
			for _, probe := range []string{"startupProbe", "livenessProbe", "readinessProbe"} {
				if got := at(c, probe, "httpGet", "scheme"); got != nil {
					t.Errorf("trove-%s %s has scheme %v with tls.enabled false", role, probe, got)
				}
			}
			for _, v := range list(pod, "volumes") {
				if strings.Contains(str(v, "name"), "tls") {
					t.Errorf("trove-%s mounts %s with tls.enabled false", role, str(v, "name"))
				}
			}
		}
		cfg := configOf(t, docs)
		if at(cfg, "server", "tls") != nil || at(cfg, "server", "ops_tls") != nil {
			t.Errorf("config carries server.tls or ops_tls with tls.enabled false")
		}
		if strings.Contains(out, "appProtocol") || strings.Contains(out, "scheme: HTTPS") || strings.Contains(out, "BackendTLSPolicy") {
			t.Error("TLS wiring rendered with tls.enabled false")
		}
		t.Run("TLS options without tls.enabled fail", func(t *testing.T) {
			renderFails(t, required(), []string{"tls.enabled=false", "tls.clientCA.secretName=edge"}, "tls.enabled")
		})
	})

	t.Run("R-HA-030 the migration Job serves no listener and mounts no TLS material", func(t *testing.T) {
		_, docs := render(t, haIngress())
		job := at(find(docs, "Job", "trove-migrate"), "spec", "template", "spec")
		if job == nil {
			t.Fatal("no migration Job rendered")
		}
		for _, v := range list(job, "volumes") {
			if strings.Contains(str(v, "name"), "tls") || strings.Contains(str(v, "secret", "secretName"), "tls") {
				t.Errorf("the migration Job mounts %s", str(v, "name"))
			}
		}
		for _, m := range list(job, "containers", "0", "volumeMounts") {
			if strings.Contains(str(m, "mountPath"), "/tls") {
				t.Errorf("the migration Job mounts %s", str(m, "mountPath"))
			}
		}
		// The web pods hold every listener Secret. The worker opens the operations listener only, which has its own pair here:
		// the listener private key and the client CA of :8080 stay out of it (SA5, H-57).
		web := at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec")
		for _, s := range []string{"trove-tls", "trove-edge-ca", "trove-ops-tls"} {
			if secretVolume(web, s) == nil {
				t.Errorf("trove-web does not mount %s", s)
			}
		}
		worker := at(find(docs, "Deployment", "trove-worker"), "spec", "template", "spec")
		if secretVolume(worker, "trove-ops-tls") == nil {
			t.Error("trove-worker does not mount trove-ops-tls")
		}
		for _, s := range []string{"trove-tls", "trove-edge-ca"} {
			if secretVolume(worker, s) != nil {
				t.Errorf("trove-worker mounts %s although it serves no listener that loads it", s)
			}
		}
	})

	t.Run("R-HA-063 a client CA renders client_ca_file and client_auth and keeps the operations listener free of client certificates", func(t *testing.T) {
		_, docs := render(t, required(), "tls.clientCA.secretName=trove-edge-ca")
		cfg := configOf(t, docs)
		if got := str(cfg, "server", "tls", "client_ca_file"); got != "/var/run/secrets/trove/tls-client-ca/ca.crt" {
			t.Errorf("client_ca_file = %q", got)
		}
		if got := str(cfg, "server", "tls", "client_auth"); got != "require" {
			t.Errorf("client_auth = %q, want require", got)
		}
		// Probes and scrapers present no client certificate: :8081 must not inherit the requirement.
		if at(cfg, "server", "ops_tls", "inherit") != false {
			t.Errorf("ops_tls.inherit = %v, want false so the client CA does not reach :8081", at(cfg, "server", "ops_tls", "inherit"))
		}
		if got := str(cfg, "server", "ops_tls", "certificates", "0", "cert_file"); got != tlsMountPath+"/tls.crt" {
			t.Errorf("ops_tls certificate = %q, want the same pair as :8080", got)
		}
		if at(cfg, "server", "ops_tls", "client_ca_file") != nil || at(cfg, "server", "ops_tls", "client_auth") != nil {
			t.Error("ops_tls carries a client CA")
		}
		pod := at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec")
		vol := secretVolume(pod, "trove-edge-ca")
		if vol == nil || str(mountOf(pod, vol), "mountPath") != "/var/run/secrets/trove/tls-client-ca" {
			t.Errorf("client CA volume = %v", vol)
		}
		if at(mountOf(pod, vol), "subPath") != nil {
			t.Error("client CA mount uses subPath")
		}
		t.Run("verify_if_given", func(t *testing.T) {
			_, docs := render(t, required(), "tls.clientCA.secretName=trove-edge-ca", "tls.clientCA.auth=verify_if_given")
			if got := str(configOf(t, docs), "server", "tls", "client_auth"); got != "verify_if_given" {
				t.Errorf("client_auth = %q", got)
			}
		})
		t.Run("an unknown mode fails schema validation", func(t *testing.T) {
			renderFails(t, required(), []string{"tls.clientCA.secretName=x", "tls.clientCA.auth=maybe"}, "auth")
		})
	})

	t.Run("R-HA-063 tls.ops separates the operations listener certificate", func(t *testing.T) {
		_, docs := render(t, required(), "tls.ops.secretName=trove-ops-tls")
		cfg := configOf(t, docs)
		if at(cfg, "server", "ops_tls", "inherit") != false {
			t.Errorf("ops_tls.inherit = %v, want false", at(cfg, "server", "ops_tls", "inherit"))
		}
		if got := str(cfg, "server", "ops_tls", "certificates", "0", "cert_file"); got != "/var/run/secrets/trove/tls-ops/tls.crt" {
			t.Errorf("ops_tls cert_file = %q", got)
		}
		if got := str(cfg, "server", "ops_tls", "certificates", "0", "key_file"); got != "/var/run/secrets/trove/tls-ops/tls.key" {
			t.Errorf("ops_tls key_file = %q", got)
		}
		if got := str(cfg, "server", "tls", "certificates", "0", "cert_file"); got != tlsMountPath+"/tls.crt" {
			t.Errorf("server.tls cert_file = %q, the client listener keeps its own pair", got)
		}
		pod := at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec")
		if secretVolume(pod, "trove-tls") == nil || secretVolume(pod, "trove-ops-tls") == nil {
			t.Error("web must mount both the client and the operations Secret")
		}
	})

	t.Run("R-HA-063 min version and reload interval reach the config", func(t *testing.T) {
		_, docs := render(t, required(), "tls.minVersion=1.3", "tls.reloadInterval=30s")
		cfg := configOf(t, docs)
		if str(cfg, "server", "tls", "min_version") != "1.3" || str(cfg, "server", "tls", "reload_interval") != "30s" {
			t.Errorf("server.tls = %v", at(cfg, "server", "tls"))
		}
		renderFails(t, required(), []string{"tls.minVersion=1.1"}, "minVersion")
	})

	t.Run("R-HA-063 the chart owns server.tls and server.ops_tls while TLS is on", func(t *testing.T) {
		renderFails(t, required(), []string{"config.server.tls.min_version=1.3"}, "config.server.tls")
		renderFails(t, required(), []string{"config.server.ops_tls.inherit=false"}, "config.server.ops_tls")
		// Opted out, the operator may wire their own material (extraVolumes) through config.
		render(t, required(), "tls.enabled=false", "tls.secretName=", "config.server.tls.min_version=1.3")
	})

	t.Run("R-HA-063 rotating the Secret does not roll the pods, a changed setting does", func(t *testing.T) {
		_, a := render(t, required())
		_, b := render(t, required(), "tls.minVersion=1.3")
		sum := func(docs []doc) string {
			return str(find(docs, "Deployment", "trove-web"), "spec", "template", "metadata", "annotations", "checksum/config")
		}
		if sum(a) == sum(b) {
			t.Error("checksum/config does not follow server.tls")
		}
	})

	t.Run("R-HA-063 the Ingress reaches trove over HTTPS: the backend setting of the controller named", func(t *testing.T) {
		t.Run("HAProxy Ingress is the default and renders the backend-scope keys with CA verification and SNI", func(t *testing.T) {
			_, docs := render(t, haIngress())
			ann := at(ofKind(docs, "Ingress")[0], "metadata", "annotations")
			want := map[string]string{
				"haproxy-ingress.github.io/secure-backends":         "true",
				"haproxy-ingress.github.io/secure-verify-ca-secret": "trove-tls",
				"haproxy-ingress.github.io/secure-sni":              "trove.default.svc",
				"haproxy-ingress.github.io/secure-crt-secret":       "trove-edge-client",
			}
			for k, v := range want {
				if got := str(ann, k); got != v {
					t.Errorf("annotation %s = %q, want %q", k, got, v)
				}
			}
			if s := toStr(ann); strings.Contains(s, "nginx.ingress") || strings.Contains(s, "traefik") {
				t.Errorf("another controller's setting rendered: %s", s)
			}
		})
		t.Run("verification needs the CA, tls.clientCA needs a client certificate", func(t *testing.T) {
			renderFails(t, required(), []string{"ingress.enabled=true"}, "ingress.backendTLS.caSecret", "ingress.backendTLS.verify: false")
			renderFails(t, required(), []string{"ingress.enabled=true", "ingress.backendTLS.caSecret=trove-tls", "tls.clientCA.secretName=edge"}, "ingress.backendTLS.clientCertSecret")
			renderFails(t, required(), []string{"ingress.enabled=true", "ingress.backendTLS.controller=traefik"}, "ingress.backendTLS.serversTransport")
		})
		t.Run("verify=false renders an encrypted but unverified connection", func(t *testing.T) {
			_, docs := render(t, required(), "ingress.enabled=true", "ingress.backendTLS.verify=false")
			ann := at(ofKind(docs, "Ingress")[0], "metadata", "annotations")
			if str(ann, "haproxy-ingress.github.io/secure-backends") != "true" || at(ann, "haproxy-ingress.github.io/secure-verify-ca-secret") != nil {
				t.Errorf("annotations = %v", ann)
			}
		})
		t.Run("tls.enabled=false needs no controller", func(t *testing.T) {
			_, docs := render(t, required(), "tls.enabled=false", "tls.secretName=", "ingress.enabled=true")
			ing := ofKind(docs, "Ingress")[0]
			if at(ing, "metadata", "annotations") != nil {
				t.Errorf("annotations = %v", at(ing, "metadata", "annotations"))
			}
		})
		t.Run("ingress-nginx", func(t *testing.T) {
			_, docs := render(t, required(), "ingress.enabled=true", "ingress.backendTLS.controller=ingress-nginx", "ingress.backendTLS.caSecret=trove-tls")
			ann := at(ofKind(docs, "Ingress")[0], "metadata", "annotations")
			want := map[string]string{
				"nginx.ingress.kubernetes.io/backend-protocol":      "HTTPS",
				"nginx.ingress.kubernetes.io/proxy-ssl-verify":      "on",
				"nginx.ingress.kubernetes.io/proxy-ssl-secret":      "default/trove-tls",
				"nginx.ingress.kubernetes.io/proxy-ssl-name":        "trove.default.svc",
				"nginx.ingress.kubernetes.io/proxy-ssl-server-name": "on",
			}
			for k, v := range want {
				if got := str(ann, k); got != v {
					t.Errorf("annotation %s = %q, want %q", k, got, v)
				}
			}
			if strings.Contains(strings.ToLower(toStr(ann)), "traefik") {
				t.Error("a Traefik setting rendered for ingress-nginx")
			}
		})
		t.Run("traefik sets the scheme and transport on the Service", func(t *testing.T) {
			_, docs := render(t, required(), "ingress.enabled=true", "ingress.backendTLS.controller=traefik",
				"ingress.backendTLS.serversTransport=trove-trove-tls@kubernetescrd")
			ann := at(find(docs, "Service", "trove"), "metadata", "annotations")
			if str(ann, "traefik.ingress.kubernetes.io/service.serversscheme") != "https" ||
				str(ann, "traefik.ingress.kubernetes.io/service.serverstransport") != "trove-trove-tls@kubernetescrd" {
				t.Errorf("Service annotations = %v", ann)
			}
			if a := at(ofKind(docs, "Ingress")[0], "metadata", "annotations"); a != nil && strings.Contains(toStr(a), "nginx") {
				t.Errorf("an ingress-nginx setting rendered for Traefik: %v", a)
			}
		})
		t.Run("manual renders no vendor setting", func(t *testing.T) {
			out, docs := render(t, required(), "ingress.enabled=true", "ingress.backendTLS.controller=manual")
			if strings.Contains(out, "nginx.ingress") || strings.Contains(out, "traefik.ingress") {
				t.Error("a vendor annotation rendered for manual")
			}
			_ = docs
		})
	})

	t.Run("R-HA-063 the BackendTLSPolicy for Gateway API is optional and verifies against a CA ConfigMap", func(t *testing.T) {
		_, docs := render(t, required())
		if len(ofKind(docs, "BackendTLSPolicy")) != 0 {
			t.Error("a BackendTLSPolicy rendered by default")
		}
		renderFails(t, required(), []string{"tls.backendTLSPolicy.enabled=true"}, "tls.backendTLSPolicy.caConfigMapName")
		_, docs = render(t, required(), "tls.backendTLSPolicy.enabled=true", "tls.backendTLSPolicy.caConfigMapName=trove-tls-ca")
		p := ofKind(docs, "BackendTLSPolicy")
		if len(p) != 1 {
			t.Fatalf("%d BackendTLSPolicy objects", len(p))
		}
		if str(p[0], "spec", "targetRefs", "0", "name") != "trove" || str(p[0], "spec", "targetRefs", "0", "kind") != "Service" {
			t.Errorf("targetRefs = %v", at(p[0], "spec", "targetRefs"))
		}
		if str(p[0], "spec", "validation", "hostname") != "trove.default.svc" ||
			str(p[0], "spec", "validation", "caCertificateRefs", "0", "name") != "trove-tls-ca" ||
			str(p[0], "spec", "validation", "caCertificateRefs", "0", "kind") != "ConfigMap" {
			t.Errorf("validation = %v", at(p[0], "spec", "validation"))
		}
	})

	t.Run("R-HA-063 every value set either names its TLS Secret or opts out explicitly", func(t *testing.T) {
		eachSet(t, func(t *testing.T, _ string, _ string, docs []doc) {
			cfg := configOf(t, docs)
			on := at(cfg, "server", "tls") != nil
			pod := at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec")
			probe := str(pod, "containers", "0", "readinessProbe", "httpGet", "scheme")
			if on != (probe == "HTTPS") {
				t.Errorf("server.tls set = %v but the readiness probe scheme is %q", on, probe)
			}
		})
	})
}

// toStr renders a decoded document for substring checks.
func toStr(v any) string {
	b, _ := yaml.Marshal(v)
	return string(b)
}

func TestTLSMountsFollowTheListeners(t *testing.T) {
	t.Run("R-HA-030 without tls.ops every runtime pod mounts the listener pair, the operations listener inherits it", func(t *testing.T) {
		_, docs := render(t, required())
		for _, role := range []string{"web", "worker"} {
			if secretVolume(at(find(docs, "Deployment", "trove-"+role), "spec", "template", "spec"), "trove-tls") == nil {
				t.Errorf("trove-%s does not mount trove-tls", role)
			}
		}
	})

	t.Run("R-HA-030 with tls.ops the worker does not mount the private key of :8080 and the web pods do", func(t *testing.T) {
		_, docs := render(t, required(), "tls.ops.secretName=trove-ops-tls")
		if secretVolume(at(find(docs, "Deployment", "trove-worker"), "spec", "template", "spec"), "trove-tls") != nil {
			t.Error("trove-worker mounts trove-tls")
		}
		if secretVolume(at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec"), "trove-tls") == nil {
			t.Error("trove-web does not mount trove-tls")
		}
	})

	t.Run("R-HA-030 with tls.ops an endpoint pool mounts the pair only when an endpoint it serves has none of its own", func(t *testing.T) {
		pool := []string{"tls.ops.secretName=trove-ops-tls", "endpoints.c.port=8443", "endpoints.c.baseUrl=https://p.example.com", "endpoints.c.pool.enabled=true"}
		_, docs := render(t, required(), pool...)
		podOf := func(docs []doc) any {
			return at(find(docs, "Deployment", "trove-web-ep-c"), "spec", "template", "spec")
		}
		if secretVolume(podOf(docs), "trove-tls") == nil {
			t.Error("the pool serves an endpoint on the listener pair and does not mount it")
		}
		_, docs = render(t, required(), append(pool, "endpoints.c.tls.existingSecret=c-tls")...)
		if secretVolume(podOf(docs), "trove-tls") != nil {
			t.Error("the pool mounts trove-tls although its endpoint has a pair of its own and the operations listener has one")
		}
		if secretVolume(podOf(docs), "c-tls") == nil {
			t.Error("the pool does not mount the pair of its endpoint")
		}
		if secretVolume(podOf(docs), "trove-ops-tls") == nil {
			t.Error("the pool does not mount trove-ops-tls")
		}
		// The main web pods keep the default endpoint's pair.
		if secretVolume(at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec"), "trove-tls") == nil {
			t.Error("trove-web does not mount trove-tls")
		}
	})

	t.Run("R-HA-030 the client CA of :8080 reaches the main web pods only", func(t *testing.T) {
		_, docs := render(t, required(), "tls.clientCA.secretName=trove-edge-ca", "endpoints.c.port=8443", "endpoints.c.baseUrl=https://p.example.com", "endpoints.c.pool.enabled=true")
		for name, want := range map[string]bool{"trove-web": true, "trove-worker": false, "trove-web-ep-c": false} {
			if got := secretVolume(at(find(docs, "Deployment", name), "spec", "template", "spec"), "trove-edge-ca") != nil; got != want {
				t.Errorf("%s mounts the client CA: %v, want %v", name, got, want)
			}
		}
	})
}

func TestS3CA(t *testing.T) {
	t.Run("R-HA-030 secrets.s3CA mounts the CA read-only without subPath and renders storage.s3.ca_file", func(t *testing.T) {
		_, docs := render(t, required(), "secrets.s3CA.existingSecret=trove-s3-tls")
		if got := str(configOf(t, docs), "storage", "s3", "ca_file"); got != "/var/run/secrets/trove/s3-ca/ca.crt" {
			t.Errorf("storage.s3.ca_file = %q", got)
		}
		for _, role := range []string{"web", "worker"} {
			pod := at(find(docs, "Deployment", "trove-"+role), "spec", "template", "spec")
			vol := secretVolume(pod, "trove-s3-tls")
			if vol == nil {
				t.Fatalf("trove-%s does not mount trove-s3-tls", role)
			}
			m := mountOf(pod, vol)
			if str(m, "mountPath") != "/var/run/secrets/trove/s3-ca" || at(m, "readOnly") != true || at(m, "subPath") != nil {
				t.Errorf("trove-%s mount = %v", role, m)
			}
			items := list(vol, "secret", "items")
			if len(items) != 1 || str(items[0], "key") != "ca.crt" || str(items[0], "path") != "ca.crt" {
				t.Errorf("trove-%s items = %v", role, items)
			}
		}
		job := at(find(docs, "Job", "trove-migrate"), "spec", "template", "spec")
		if secretVolume(job, "trove-s3-tls") != nil {
			t.Error("the migration Job mounts the object-store CA")
		}
	})

	t.Run("R-HA-030 a different key is projected as ca.crt", func(t *testing.T) {
		_, docs := render(t, required(), "secrets.s3CA.existingSecret=s3-ca", "secrets.s3CA.key=root.pem")
		vol := secretVolume(at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec"), "s3-ca")
		if got := str(list(vol, "secret", "items")[0], "key"); got != "root.pem" {
			t.Errorf("key = %q", got)
		}
	})

	t.Run("R-HA-030 without secrets.s3CA no ca_file is rendered and none may be set in config", func(t *testing.T) {
		_, docs := render(t, required())
		if at(configOf(t, docs), "storage", "s3", "ca_file") != nil {
			t.Error("storage.s3.ca_file rendered without secrets.s3CA")
		}
		renderFails(t, required(), []string{"config.storage.s3.ca_file=/x"}, "config.storage.s3.ca_file", "managed")
	})
}
