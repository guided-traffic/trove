//go:build chart

// The render assertions of the chart deploy/helm/trove-bastion (phase 5b, docs/spec/bastion.md R-BAS-002,
// R-BAS-025, R-BAS-030, R-BAS-033, R-BAS-038, R-BAS-040, R-BAS-041, R-BAS-043 to R-BAS-047). They share the
// helpers of chart_test.go and need helm at the pinned version, no cluster. The rule tests with promtool are in
// bastion_rules_test.go.
package chart_test

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

const bastionRelease = "trove-bastion"

func bastionDir() string { return filepath.Join(repoRoot, "deploy", "helm", "trove-bastion") }

func bastionSet(name string) string { return filepath.Join(bastionDir(), "ci", name+".yaml") }

func bastionArgs(release string, files []string, sets []string) []string {
	args := []string{"template", release, bastionDir(), "--namespace", "default"}
	for _, f := range files {
		args = append(args, "-f", f)
	}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	return args
}

// bstRender renders the chart trove-bastion as the release trove-bastion and parses the documents.
func bstRender(t *testing.T, files []string, sets ...string) (string, []doc) {
	t.Helper()
	out, stderr, err := helm(t, bastionArgs(bastionRelease, files, sets)...)
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, stderr)
	}
	return out, parse(t, out)
}

// bstFails asserts that the render fails and that the error names every wanted fragment.
func bstFails(t *testing.T, files []string, sets []string, want ...string) {
	t.Helper()
	out, stderr, err := helm(t, bastionArgs(bastionRelease, files, sets)...)
	if err == nil {
		t.Fatalf("helm template succeeded (%d bytes), want a failure naming %q", len(out), want)
	}
	for _, w := range want {
		if !strings.Contains(stderr, w) {
			t.Errorf("error does not name %q:\n%s", w, stderr)
		}
	}
}

func bstRequired() []string { return []string{bastionSet("required-only")} }

// bstConfig parses the configuration file the chart puts into the ConfigMap <fullname>-config.
func bstConfig(t *testing.T, docs []doc) map[string]any {
	t.Helper()
	cm := find(docs, "ConfigMap", bastionRelease+"-config")
	if cm == nil {
		t.Fatal("no ConfigMap trove-bastion-config")
	}
	var cfg map[string]any
	if err := yaml.Unmarshal([]byte(str(cm, "data", "trove.yaml")), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func bstWorkload(t *testing.T, docs []doc) doc {
	t.Helper()
	w := find(docs, "Deployment", bastionRelease)
	if w == nil {
		w = find(docs, "StatefulSet", bastionRelease)
	}
	if w == nil {
		t.Fatal("no Deployment or StatefulSet trove-bastion")
	}
	return w
}

func bstChecksum(t *testing.T, files []string, sets ...string) string {
	t.Helper()
	_, docs := bstRender(t, files, sets...)
	sum := str(bstWorkload(t, docs), "spec", "template", "metadata", "annotations", "checksum/config")
	if sum == "" {
		t.Fatal("the pod template has no checksum/config annotation")
	}
	return sum
}

func bstIngressAnnotations(t *testing.T, files []string, sets ...string) map[string]any {
	t.Helper()
	_, docs := bstRender(t, files, sets...)
	ing := find(docs, "Ingress", bastionRelease)
	if ing == nil {
		t.Fatal("no Ingress trove-bastion")
	}
	a, _ := at(ing, "metadata", "annotations").(map[string]any)
	return a
}

const hp = "haproxy-ingress.github.io/"

// leafPaths flattens a values tree to its leaf paths: a leaf is anything but a non-empty map.
func leafPaths(prefix string, v any, out *[]string) {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		*out = append(*out, prefix)
		return
	}
	for k, c := range m {
		p := k
		if prefix != "" {
			p = prefix + "." + k
		}
		leafPaths(p, c, out)
	}
}

func sortedLeaves(t *testing.T, raw string) []string {
	t.Helper()
	var v map[string]any
	if err := yaml.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("not YAML: %v", err)
	}
	var out []string
	leafPaths("", v, &out)
	sort.Strings(out)
	return out
}

// TestR_BAS_045_RequiredValuesRenderTheResources (R-BAS-045): the values a render needs give a Deployment
// with two replicas, a PodDisruptionBudget, the Ingress with the edge-limit annotations, a NetworkPolicy and
// no Secret, no Role, no RoleBinding and no PersistentVolumeClaim; the names are the table of the spec.
func TestR_BAS_045_RequiredValuesRenderTheResources(t *testing.T) {
	_, docs := bstRender(t, bstRequired())
	t.Run("a Deployment with two replicas, a PDB, the Ingress with the edge-limit annotations and a NetworkPolicy", func(t *testing.T) {
		dep := find(docs, "Deployment", bastionRelease)
		if dep == nil {
			t.Fatal("no Deployment trove-bastion")
		}
		if got := at(dep, "spec", "replicas"); got != 2 {
			t.Errorf("replicas = %v, want 2", got)
		}
		for _, k := range []string{"PodDisruptionBudget", "NetworkPolicy", "Ingress", "Service", "ServiceAccount", "ConfigMap"} {
			if len(ofKind(docs, k)) == 0 {
				t.Errorf("no %s rendered", k)
			}
		}
		a := bstIngressAnnotations(t, bstRequired())
		if a[hp+"limit-rps"] != "100" || a[hp+"limit-connections"] != "50" {
			t.Errorf("edge-limit annotations = %v", a)
		}
	})
	t.Run("no Secret, no Role, no RoleBinding, no PersistentVolumeClaim, in any value set", func(t *testing.T) {
		sets, _ := filepath.Glob(filepath.Join(bastionDir(), "ci", "*.yaml"))
		if len(sets) < 5 {
			t.Fatalf("found %d value sets under deploy/helm/trove-bastion/ci", len(sets))
		}
		for _, f := range sets {
			_, d := bstRender(t, []string{f})
			for _, k := range []string{"Secret", "Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding", "PersistentVolumeClaim"} {
				if n := len(ofKind(d, k)); n != 0 {
					t.Errorf("%s renders %d %s", base(f), n, k)
				}
			}
		}
	})
	t.Run("the ServiceAccount mounts no token", func(t *testing.T) {
		sa := find(docs, "ServiceAccount", bastionRelease)
		if sa == nil || at(sa, "automountServiceAccountToken") != false {
			t.Errorf("ServiceAccount = %v", sa)
		}
		pod := at(bstWorkload(t, docs), "spec", "template", "spec")
		if at(pod, "automountServiceAccountToken") != false {
			t.Errorf("the pod automounts the service account token")
		}
	})
	t.Run("ports https and ops, the role bastion, the one image at the appVersion", func(t *testing.T) {
		pod := at(bstWorkload(t, docs), "spec", "template", "spec")
		args := containerArgs(pod)
		if strings.Join(args, " ") != "serve --role bastion" {
			t.Errorf("args = %v", args)
		}
		ports := map[string]any{}
		for _, p := range list(pod, "containers", "0", "ports") {
			ports[str(p, "name")] = at(p, "containerPort")
		}
		if ports["https"] != 8443 || ports["ops"] != 8081 {
			t.Errorf("ports = %v", ports)
		}
		if got := str(pod, "containers", "0", "image"); got != "docker.io/guidedtraffic/trove:0.0.0-dev" {
			t.Errorf("image = %q", got)
		}
	})
	t.Run("the names of the table", func(t *testing.T) {
		_, all := bstRender(t, []string{bastionSet("bucket-valkey")})
		for kind, name := range map[string]string{
			"Deployment": "trove-bastion", "ConfigMap": "trove-bastion-config", "Service": "trove-bastion",
			"Ingress": "trove-bastion", "NetworkPolicy": "trove-bastion", "PodDisruptionBudget": "trove-bastion",
			"ServiceAccount": "trove-bastion", "ServiceMonitor": "trove-bastion", "PrometheusRule": "trove-bastion",
		} {
			if find(all, kind, name) == nil {
				t.Errorf("no %s %s", kind, name)
			}
		}
		if find(all, "Service", "trove-bastion-ops") == nil {
			t.Error("no Service trove-bastion-ops")
		}
		_, withCert := bstRender(t, bstRequired())
		if find(withCert, "Certificate", "trove-bastion-tls") == nil {
			t.Error("no Certificate trove-bastion-tls")
		}
		_, scaled := bstRender(t, []string{bastionSet("autoscaling")})
		if find(scaled, "HorizontalPodAutoscaler", "trove-bastion") == nil {
			t.Error("no HorizontalPodAutoscaler trove-bastion")
		}
	})
	t.Run("<fullname> is the release name, or <release>-trove-bastion", func(t *testing.T) {
		out, stderr, err := helm(t, bastionArgs("partners", bstRequired(), nil)...)
		if err != nil {
			t.Fatalf("%v\n%s", err, stderr)
		}
		d := parse(t, out)
		for _, n := range []string{"partners-trove-bastion", "partners-trove-bastion-config", "partners-trove-bastion-ops"} {
			found := false
			for _, o := range d {
				if str(o, "metadata", "name") == n {
					found = true
				}
			}
			if !found {
				t.Errorf("no object named %s", n)
			}
		}
	})
	t.Run("the Service has no session affinity", func(t *testing.T) {
		for _, n := range []string{"trove-bastion", "trove-bastion-ops"} {
			svc := find(docs, "Service", n)
			if svc == nil || str(svc, "spec", "sessionAffinity") != "None" {
				t.Errorf("Service %s = %v", n, svc)
			}
		}
	})
	t.Run("every validation fails the render and names the values", func(t *testing.T) {
		cases := []struct {
			name string
			sets []string
			want []string
		}{
			{"base_url missing", []string{"config.server.base_url="}, []string{"config.server.base_url"}},
			{"base_url http", []string{"config.server.base_url=http://packages.example.com"}, []string{"config.server.base_url", "https"}},
			{"main url missing", []string{"config.bastion.main.url="}, []string{"config.bastion.main.url"}},
			{"main url http", []string{"config.bastion.main.url=http://trove.example.com"}, []string{"config.bastion.main.url", "https"}},
			{"token Secret missing", []string{"secrets.mainToken.existingSecret="}, []string{"secrets.mainToken.existingSecret"}},
			{"bucket backend without a signing key", []string{"config.bastion.cache.backend=bucket", "config.bastion.cache.bucket.bucket=b", "secrets.cacheBucket.existingSecret=s"}, []string{"secrets.signingKey.existingSecret"}},
			{"Valkey without a signing key", []string{"config.valkey.mode=standalone", "config.valkey.address=v:6379", "highAvailability.enforce=false"}, []string{"secrets.signingKey.existingSecret"}},
			{"bucket backend without a bucket", []string{"config.bastion.cache.backend=bucket", "secrets.signingKey.existingSecret=k"}, []string{"config.bastion.cache.bucket.bucket"}},
			{"bucket credentials without a Secret", []string{"config.bastion.cache.backend=bucket", "config.bastion.cache.bucket.bucket=b", "secrets.signingKey.existingSecret=k"}, []string{"secrets.cacheBucket.existingSecret"}},
			{"persistent volume with the bucket backend", []string{"config.bastion.cache.backend=bucket", "config.bastion.cache.bucket.bucket=b", "secrets.signingKey.existingSecret=k", "secrets.cacheBucket.existingSecret=s", "cacheVolume.type=persistent"}, []string{"cacheVolume.type", "config.bastion.cache.backend"}},
			{"no certificate source", []string{"tls.certManager.enabled=false"}, []string{"tls.existingSecret", "tls.certManager.enabled"}},
			{"cert-manager without an issuer", []string{"tls.certManager.issuerRef.name="}, []string{"tls.certManager.issuerRef.name"}},
			{"two trust bundle sources", []string{"trustedCA.existingSecret=a", "trustedCA.existingConfigMap=b"}, []string{"trustedCA.existingSecret", "trustedCA.existingConfigMap"}},
			{"two trust bundle keys", []string{"trustedCA.existingSecret=a", "trustedCA.keys={a.crt,b.crt}"}, []string{"trustedCA.keys"}},
			{"no ingress peer for the https port", []string{"networkPolicy.ingress.https.from=null"}, []string{"networkPolicy.ingress.https.from"}},
			{"Sentinel without addresses", []string{"config.valkey.mode=sentinel"}, []string{"config.valkey.sentinel.addresses"}},
			{"a typo in disabledAlerts", []string{"metrics.prometheusRule.disabledAlerts={TroveBastionNope}"}, []string{"metrics.prometheusRule.disabledAlerts", "TroveBastionNope"}},
			{"autoscaling without a metric", []string{"autoscaling.enabled=true", "autoscaling.targetCPUUtilizationPercentage=0"}, []string{"autoscaling.targetCPUUtilizationPercentage"}},
			{"autoscaling max below min", []string{"autoscaling.enabled=true", "autoscaling.maxReplicas=1"}, []string{"autoscaling.maxReplicas", "autoscaling.minReplicas"}},
			{"a ServiceMonitor in another namespace with TLS", []string{"metrics.serviceMonitor.enabled=true", "metrics.serviceMonitor.namespace=monitoring"}, []string{"metrics.serviceMonitor.namespace"}},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) { bstFails(t, bstRequired(), c.sets, c.want...) })
		}
	})
}

// TestR_BAS_046_ValuesReferenceIsTheSpecBlock (R-BAS-046): helm show values has the key set of the values
// block of the spec page.
func TestR_BAS_046_ValuesReferenceIsTheSpecBlock(t *testing.T) {
	spec, err := os.ReadFile(filepath.Join(repoRoot, "docs", "spec", "bastion.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(spec), "\n")
	var block []string
	state := 0
	for _, l := range lines {
		switch {
		case state == 0 && strings.Contains(l, "<summary>deploy/helm/trove-bastion/values.yaml</summary>"):
			state = 1
		case state == 1 && strings.HasPrefix(l, "```yaml"):
			state = 2
		case state == 2 && strings.HasPrefix(l, "```"):
			state = 3
		case state == 2:
			block = append(block, l)
		}
	}
	if state != 3 || len(block) < 100 {
		t.Fatalf("the values block of docs/spec/bastion.md was not found (state %d, %d lines)", state, len(block))
	}
	t.Run("the key set of helm show values equals the spec block", func(t *testing.T) {
		out, stderr, err := helm(t, "show", "values", bastionDir())
		if err != nil {
			t.Fatalf("%v\n%s", err, stderr)
		}
		got, want := sortedLeaves(t, out), sortedLeaves(t, strings.Join(block, "\n"))
		if strings.Join(got, "\n") != strings.Join(want, "\n") {
			seen := map[string]int{}
			for _, g := range got {
				seen[g]++
			}
			for _, w := range want {
				seen[w]--
			}
			for k, n := range seen {
				if n > 0 {
					t.Errorf("values.yaml has %s, the spec block does not", k)
				}
				if n < 0 {
					t.Errorf("the spec block has %s, values.yaml does not", k)
				}
			}
		}
	})
}

// TestR_BAS_002_ConfigurationOnlyThroughTheChart (R-BAS-002): config is rendered to /etc/trove/trove.yaml in
// <fullname>-config with the chart-managed keys; a managed key is refused; the checksum annotation rolls the
// pods on a config change and not on a token Secret; the token is a mounted file.
func TestR_BAS_002_ConfigurationOnlyThroughTheChart(t *testing.T) {
	t.Run("the file is mounted at /etc/trove/trove.yaml from <fullname>-config", func(t *testing.T) {
		_, docs := bstRender(t, bstRequired())
		pod := at(bstWorkload(t, docs), "spec", "template", "spec")
		var found bool
		for _, m := range list(pod, "containers", "0", "volumeMounts") {
			if str(m, "mountPath") == "/etc/trove/trove.yaml" {
				found = str(m, "subPath") == "trove.yaml" && at(m, "readOnly") == true
			}
		}
		if !found {
			t.Error("trove.yaml is not mounted read-only at /etc/trove/trove.yaml")
		}
		if got := str(volumeNamed(pod, "config"), "configMap", "name"); got != "trove-bastion-config" {
			t.Errorf("config volume = %q", got)
		}
	})
	t.Run("every value under config is in the file, and the managed keys are rendered", func(t *testing.T) {
		_, docs := bstRender(t, []string{bastionSet("bucket-valkey")})
		cfg := bstConfig(t, docs)
		raw, err := os.ReadFile(filepath.Join(bastionDir(), "values.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var vals map[string]any
		if err := yaml.Unmarshal(raw, &vals); err != nil {
			t.Fatal(err)
		}
		var leaves []string
		leafPaths("", vals["config"], &leaves)
		for _, p := range leaves {
			var cur any = cfg
			for _, k := range strings.Split(p, ".") {
				m, ok := cur.(map[string]any)
				if !ok {
					t.Errorf("config.%s is not in the rendered file", p)
					break
				}
				cur, ok = m[k]
				if !ok {
					t.Errorf("config.%s is not in the rendered file", p)
					break
				}
			}
		}
		for p, want := range map[string]any{
			"bastion.main.token_file":             "/var/run/secrets/trove/main-token/token",
			"bastion.main.ca_file":                "/etc/trove/trusted-ca/ca-bundle.crt",
			"bastion.signing_key_file":            "/var/run/secrets/trove/signing-key/key",
			"bastion.cache.bucket.ca_file":        "/etc/trove/trusted-ca/ca-bundle.crt",
			"valkey.password_file":                "/var/run/secrets/trove/valkey/password",
			"valkey.username_file":                "/var/run/secrets/trove/valkey/username",
			"valkey.sentinel.password_file":       "/var/run/secrets/trove/valkey-sentinel/password",
			"valkey.sentinel.username_file":       "/var/run/secrets/trove/valkey-sentinel/username",
			"valkey.tls.ca_file":                  "/var/run/secrets/trove/valkey-ca/ca.crt",
			"bastion.replicas":                    2,
			"server.tls.certificates.0.cert_file": "/var/run/secrets/trove/tls/tls.crt",
			"server.tls.certificates.0.key_file":  "/var/run/secrets/trove/tls/tls.key",
		} {
			if got := at(cfg, strings.Split(p, ".")...); got != want {
				t.Errorf("%s = %v, want %v", p, got, want)
			}
		}
		if got := at(cfg, "bastion", "cache", "bucket", "credentials", "access_key_id_file"); got != "/var/run/secrets/trove/cache-bucket/access-key-id" {
			t.Errorf("bucket credentials file = %v", got)
		}
		if at(cfg, "bastion", "cache", "volume") != nil {
			t.Error("the bucket backend renders a cache volume directory")
		}
	})
	t.Run("the volume backend renders the fixed cache directory", func(t *testing.T) {
		_, docs := bstRender(t, bstRequired())
		if got := at(bstConfig(t, docs), "bastion", "cache", "volume", "dir"); got != "/var/cache/trove-bastion" {
			t.Errorf("bastion.cache.volume.dir = %v", got)
		}
	})
	t.Run("every Secret named by a value is mounted as a directory without subPath", func(t *testing.T) {
		_, docs := bstRender(t, []string{bastionSet("bucket-valkey")})
		pod := at(bstWorkload(t, docs), "spec", "template", "spec")
		secrets := map[string]bool{}
		for _, v := range list(pod, "volumes") {
			if s := str(v, "secret", "secretName"); s != "" {
				secrets[s] = true
			}
		}
		for _, s := range []string{"trove-bastion-token", "trove-bastion-signing", "trove-bastion-s3", "trove-bastion-valkey", "trove-bastion-valkey-sentinel", "trove-bastion-valkey-ca", "trove-bastion-listener"} {
			if !secrets[s] {
				t.Errorf("Secret %s is not mounted", s)
			}
		}
		if got := str(volumeNamed(pod, "secret-trusted-ca"), "configMap", "name"); got != "corp-ca-bundle" {
			t.Errorf("trust bundle ConfigMap = %q", got)
		}
		for _, m := range list(pod, "containers", "0", "volumeMounts") {
			if strings.HasPrefix(str(m, "name"), "secret-") && str(m, "subPath") != "" {
				t.Errorf("mount %s uses subPath; a replaced Secret would not reach the file", str(m, "name"))
			}
		}
	})
	t.Run("no Secret value reaches an environment variable", func(t *testing.T) {
		_, docs := bstRender(t, []string{bastionSet("bucket-valkey")})
		pod := at(bstWorkload(t, docs), "spec", "template", "spec")
		for _, e := range list(pod, "containers", "0", "env") {
			if at(e, "valueFrom", "secretKeyRef") != nil {
				t.Errorf("env %s reads a Secret", str(e, "name"))
			}
		}
	})
	t.Run("a chart-managed key under config is refused", func(t *testing.T) {
		for _, p := range []string{
			"bastion.main.token_file", "bastion.main.ca_file", "bastion.signing_key_file", "bastion.cache.volume.dir",
			"bastion.cache.bucket.ca_file", "bastion.cache.bucket.credentials.access_key_id_file",
			"bastion.cache.bucket.credentials.secret_access_key_file", "bastion.replicas",
			"valkey.username_file", "valkey.password_file", "valkey.tls.ca_file", "server.tls", "server.ops_tls",
		} {
			t.Run(p, func(t *testing.T) {
				value := "x"
				switch p {
				case "bastion.replicas":
					value = "9"
				case "server.tls", "server.ops_tls":
					// An object: the schema rejects a string, so the test sets a key inside it.
					p += ".min_version"
					value = "1.3"
				}
				bstFails(t, bstRequired(), []string{"config." + p + "=" + value}, "config."+p, "managed by the chart")
			})
		}
	})
	t.Run("the checksum annotation changes with a config value and not with the token Secret", func(t *testing.T) {
		base := bstChecksum(t, bstRequired())
		if changed := bstChecksum(t, bstRequired(), "config.bastion.answers.period=10m"); changed == base {
			t.Error("checksum/config does not change with config.bastion.answers.period")
		}
		if changed := bstChecksum(t, bstRequired(), "secrets.mainToken.existingSecret=another-token", "secrets.mainToken.key=other"); changed != base {
			t.Error("checksum/config changes with the token Secret; replacing it must roll nothing")
		}
		if changed := bstChecksum(t, bstRequired(), "config.bastion.main.url=https://other.example.com"); changed == base {
			t.Error("checksum/config does not change with config.bastion.main.url")
		}
	})
}

// TestR_BAS_025_ReplicasAreRenderedIntoTheConfiguration (R-BAS-025): bastion.replicas is replicaCount, or
// autoscaling.maxReplicas with autoscaling.
func TestR_BAS_025_ReplicasAreRenderedIntoTheConfiguration(t *testing.T) {
	_, docs := bstRender(t, bstRequired(), "replicaCount=3")
	if got := at(bstConfig(t, docs), "bastion", "replicas"); got != 3 {
		t.Errorf("bastion.replicas = %v, want replicaCount 3", got)
	}
	if got := at(bstWorkload(t, docs), "spec", "replicas"); got != 3 {
		t.Errorf("replicas = %v, want 3", got)
	}
	_, scaled := bstRender(t, []string{bastionSet("autoscaling")}, "replicaCount=3")
	if got := at(bstConfig(t, scaled), "bastion", "replicas"); got != 8 {
		t.Errorf("with autoscaling bastion.replicas = %v, want autoscaling.maxReplicas 8", got)
	}
	if at(bstWorkload(t, scaled), "spec", "replicas") != nil {
		t.Error("with autoscaling the workload carries a replicas field the autoscaler would fight")
	}
	hpa := find(scaled, "HorizontalPodAutoscaler", bastionRelease)
	if at(hpa, "spec", "minReplicas") != 2 || at(hpa, "spec", "maxReplicas") != 8 || str(hpa, "spec", "scaleTargetRef", "kind") != "Deployment" {
		t.Errorf("HPA = %v", at(hpa, "spec"))
	}
}

// TestR_BAS_030_CacheVolumeKinds (R-BAS-030): the cache volume of the volume backend is a generic ephemeral
// volume, an emptyDir, or a claim template per pod of a StatefulSet; the bucket backend keeps no volume.
func TestR_BAS_030_CacheVolumeKinds(t *testing.T) {
	t.Run("persistent: a StatefulSet with a claim template, no Deployment", func(t *testing.T) {
		_, docs := bstRender(t, bstRequired(), "cacheVolume.type=persistent", "cacheVolume.size=40Gi", "cacheVolume.storageClassName=fast")
		if find(docs, "Deployment", bastionRelease) != nil {
			t.Error("a Deployment is rendered next to the StatefulSet")
		}
		sts := find(docs, "StatefulSet", bastionRelease)
		if sts == nil {
			t.Fatal("no StatefulSet trove-bastion")
		}
		claim := list(sts, "spec", "volumeClaimTemplates")
		if len(claim) != 1 || str(claim[0], "metadata", "name") != "cache" ||
			str(claim[0], "spec", "resources", "requests", "storage") != "40Gi" || str(claim[0], "spec", "storageClassName") != "fast" {
			t.Errorf("volumeClaimTemplates = %v", claim)
		}
		pod := at(sts, "spec", "template", "spec")
		if volumeNamed(pod, "cache") != nil {
			t.Error("the pod also declares a cache volume of its own")
		}
		var mounted bool
		for _, m := range list(pod, "containers", "0", "volumeMounts") {
			mounted = mounted || str(m, "name") == "cache" && str(m, "mountPath") == "/var/cache/trove-bastion"
		}
		if !mounted {
			t.Error("the cache claim is not mounted at /var/cache/trove-bastion")
		}
		if len(ofKind(docs, "PersistentVolumeClaim")) != 0 {
			t.Error("the chart renders a PersistentVolumeClaim")
		}
	})
	t.Run("ephemeral (default): a generic ephemeral volume of the pod", func(t *testing.T) {
		_, docs := bstRender(t, bstRequired())
		pod := at(bstWorkload(t, docs), "spec", "template", "spec")
		v := volumeNamed(pod, "cache")
		if got := list(v, "ephemeral", "volumeClaimTemplate", "spec", "accessModes"); len(got) != 1 || got[0] != "ReadWriteOnce" {
			t.Errorf("cache volume = %v", v)
		}
	})
	t.Run("emptyDir", func(t *testing.T) {
		_, docs := bstRender(t, bstRequired(), "cacheVolume.type=emptyDir", "cacheVolume.size=30Gi")
		v := volumeNamed(at(bstWorkload(t, docs), "spec", "template", "spec"), "cache")
		if str(v, "emptyDir", "sizeLimit") != "30Gi" {
			t.Errorf("cache volume = %v", v)
		}
	})
	t.Run("the bucket backend mounts no cache volume", func(t *testing.T) {
		_, docs := bstRender(t, []string{bastionSet("bucket-valkey")})
		pod := at(bstWorkload(t, docs), "spec", "template", "spec")
		if volumeNamed(pod, "cache") != nil {
			t.Error("the bucket backend declares a cache volume")
		}
		if volumeNamed(pod, "tmp") == nil {
			t.Error("no tmp volume for the spool of fills")
		}
	})
}

// TestR_BAS_038_EdgeLimitIsWiderThanTheBastionsOwn (R-BAS-038): edgeLimit renders HAProxy Ingress's
// per-address limits on the Ingress and must be above the Bastion's own authenticated limit.
func TestR_BAS_038_EdgeLimitIsWiderThanTheBastionsOwn(t *testing.T) {
	t.Run("edgeLimit 50 with the default authenticated limit fails and names both values", func(t *testing.T) {
		bstFails(t, bstRequired(), []string{"edgeLimit.requestsPerSecond=50"},
			"edgeLimit.requestsPerSecond", "config.bastion.limits.authenticated.requests_per_second")
	})
	t.Run("the authenticated limit itself is refused, one above it passes", func(t *testing.T) {
		bstFails(t, bstRequired(), []string{"config.bastion.limits.authenticated.requests_per_second=100"}, "edgeLimit.requestsPerSecond")
		a := bstIngressAnnotations(t, bstRequired(), "config.bastion.limits.authenticated.requests_per_second=99")
		if a[hp+"limit-rps"] != "100" {
			t.Errorf("annotations = %v", a)
		}
	})
	t.Run("the limits are annotations, the whitelist is blocking.exempt_cidrs", func(t *testing.T) {
		a := bstIngressAnnotations(t, []string{bastionSet("persistent-routed")})
		if a[hp+"limit-rps"] != "100" || a[hp+"limit-connections"] != "50" || a[hp+"limit-whitelist"] != "198.51.100.0/24,203.0.113.7/32" {
			t.Errorf("annotations = %v", a)
		}
		none := bstIngressAnnotations(t, bstRequired())
		if _, ok := none[hp+"limit-whitelist"]; ok {
			t.Error("a whitelist is rendered without exempt_cidrs")
		}
	})
	t.Run("the values reach the annotations", func(t *testing.T) {
		a := bstIngressAnnotations(t, bstRequired(), "edgeLimit.requestsPerSecond=250", "edgeLimit.connections=80")
		if a[hp+"limit-rps"] != "250" || a[hp+"limit-connections"] != "80" {
			t.Errorf("annotations = %v", a)
		}
	})
	t.Run("switched off, no limit annotation is rendered and the comparison is not made", func(t *testing.T) {
		a := bstIngressAnnotations(t, bstRequired(), "edgeLimit.enabled=false", "edgeLimit.requestsPerSecond=1")
		for k := range a {
			if strings.HasPrefix(k, hp+"limit-") {
				t.Errorf("annotation %s with edgeLimit.enabled: false", k)
			}
		}
	})
	t.Run("a user annotation cannot override a key the chart renders", func(t *testing.T) {
		bstFails(t, bstRequired(), []string{`ingress.annotations.haproxy-ingress\.github\.io/limit-rps=5`}, "ingress.annotations", "limit-rps")
	})
}

// TestR_BAS_033_RouteByPath (R-BAS-033): URI hashing with consistent hashing and stable server IDs, only with
// the volume backend and an Ingress.
func TestR_BAS_033_RouteByPath(t *testing.T) {
	t.Run("routeByPath renders URI hashing, consistent hashing and server IDs", func(t *testing.T) {
		a := bstIngressAnnotations(t, bstRequired(), "cacheVolume.routeByPath=true")
		want := map[string]string{hp + "balance-algorithm": "uri", hp + "assign-backend-server-id": "true", hp + "config-backend": "hash-type consistent"}
		for k, v := range want {
			if a[k] != v {
				t.Errorf("annotation %s = %v, want %s", k, a[k], v)
			}
		}
	})
	t.Run("by default no balancing annotation is rendered", func(t *testing.T) {
		a := bstIngressAnnotations(t, bstRequired())
		for _, k := range []string{"balance-algorithm", "assign-backend-server-id", "config-backend"} {
			if _, ok := a[hp+k]; ok {
				t.Errorf("annotation %s without routeByPath", k)
			}
		}
	})
	t.Run("refused with the bucket backend, naming both values", func(t *testing.T) {
		bstFails(t, []string{bastionSet("bucket-valkey")}, []string{"cacheVolume.routeByPath=true"},
			"cacheVolume.routeByPath", "config.bastion.cache.backend")
	})
	t.Run("refused without the Ingress, naming both values", func(t *testing.T) {
		bstFails(t, bstRequired(), []string{"cacheVolume.routeByPath=true", "ingress.enabled=false"}, "cacheVolume.routeByPath", "ingress.enabled")
	})
}

// TestR_BAS_047_NetworkPolicy (R-BAS-047, ADR-154): ingress rules only, each with its port: https from the ingress controller's
// namespace, ops from monitoring and the probe CIDRs; no egress rule and no networkPolicy.egress value.
func TestR_BAS_047_NetworkPolicy(t *testing.T) {
	policy := func(t *testing.T, files []string, sets ...string) doc {
		t.Helper()
		_, docs := bstRender(t, files, sets...)
		np := find(docs, "NetworkPolicy", bastionRelease)
		if np == nil {
			t.Fatal("no NetworkPolicy trove-bastion")
		}
		return np
	}
	ports := func(rule any) []int {
		var out []int
		for _, p := range list(rule, "ports") {
			out = append(out, at(p, "port").(int))
		}
		return out
	}
	t.Run("ingress: https from the controller's namespace, ops from monitoring", func(t *testing.T) {
		np := policy(t, bstRequired())
		in := list(np, "spec", "ingress")
		if len(in) != 2 {
			t.Fatalf("ingress rules = %v", in)
		}
		if p := ports(in[0]); len(p) != 1 || p[0] != 8443 {
			t.Errorf("rule 0 ports = %v", p)
		}
		if str(in[0], "from", "0", "namespaceSelector", "matchLabels", "kubernetes.io/metadata.name") != "haproxy-ingress" {
			t.Errorf("https peers = %v", at(in[0], "from"))
		}
		if p := ports(in[1]); len(p) != 1 || p[0] != 8081 || str(in[1], "from", "0", "namespaceSelector", "matchLabels", "kubernetes.io/metadata.name") != "monitoring" {
			t.Errorf("ops rule = %v", in[1])
		}
	})
	t.Run("ingress only in every value set: policyTypes [Ingress], no egress key, one port per rule, no empty peer list", func(t *testing.T) {
		sets, _ := filepath.Glob(filepath.Join(bastionDir(), "ci", "*.yaml"))
		if len(sets) < 5 {
			t.Fatalf("found %d value sets under deploy/helm/trove-bastion/ci", len(sets))
		}
		for _, f := range sets {
			np := policy(t, []string{f})
			if types := list(np, "spec", "policyTypes"); len(types) != 1 || types[0] != "Ingress" {
				t.Errorf("%s: policyTypes = %v, want [Ingress] only", base(f), types)
			}
			if spec, _ := at(np, "spec").(map[string]any); spec != nil {
				if _, has := spec["egress"]; has {
					t.Errorf("%s: the policy has an egress key: %v", base(f), spec["egress"])
				}
			}
			for i, r := range list(np, "spec", "ingress") {
				if p := ports(r); len(p) != 1 {
					t.Errorf("%s: ingress rule %d ports = %v, want exactly one", base(f), i, p)
				}
				if len(list(r, "from")) == 0 {
					t.Errorf("%s: ingress rule %d has no peer, which means from anywhere", base(f), i)
				}
			}
		}
	})
	t.Run("networkPolicy.egress is not a value", func(t *testing.T) {
		bstFails(t, bstRequired(), []string{"networkPolicy.egress.enabled=true"}, "egress")
		raw, err := os.ReadFile(filepath.Join(bastionDir(), "values.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var vals map[string]any
		if err := yaml.Unmarshal(raw, &vals); err != nil {
			t.Fatal(err)
		}
		if np, _ := vals["networkPolicy"].(map[string]any); np == nil || np["egress"] != nil {
			t.Errorf("values.yaml networkPolicy = %v, want ingress values only", vals["networkPolicy"])
		}
	})
	t.Run("probe CIDRs join the ops rule", func(t *testing.T) {
		np := policy(t, []string{bastionSet("existing-tls")})
		in := list(np, "spec", "ingress")
		var found bool
		for _, f := range list(in[1], "from") {
			found = found || str(f, "ipBlock", "cidr") == "10.0.0.0/8"
		}
		if !found {
			t.Errorf("ops rule = %v", in[1])
		}
	})
	t.Run("no peer for the ops port leaves its rule out", func(t *testing.T) {
		in := list(policy(t, bstRequired(), "networkPolicy.ingress.ops.from=null"), "spec", "ingress")
		if len(in) != 1 || ports(in[0])[0] != 8443 {
			t.Errorf("ingress rules = %v, want the https rule only", in)
		}
	})
	t.Run("networkPolicy.enabled: false renders no policy", func(t *testing.T) {
		_, docs := bstRender(t, bstRequired(), "networkPolicy.enabled=false")
		if len(ofKind(docs, "NetworkPolicy")) != 0 {
			t.Error("a NetworkPolicy is rendered")
		}
	})
}

// TestR_BAS_043_PodDisruptionBudgetAndSpreading (R-BAS-043): at least two replicas spread across nodes behind
// a PodDisruptionBudget that evicts one pod at a time; highAvailability.enforce makes the chart refuse less.
func TestR_BAS_043_PodDisruptionBudgetAndSpreading(t *testing.T) {
	t.Run("the PDB evicts one pod at a time", func(t *testing.T) {
		_, docs := bstRender(t, bstRequired())
		pdb := find(docs, "PodDisruptionBudget", bastionRelease)
		if at(pdb, "spec", "maxUnavailable") != 1 || str(pdb, "spec", "unhealthyPodEvictionPolicy") != "AlwaysAllow" || at(pdb, "spec", "minAvailable") != nil {
			t.Errorf("PDB spec = %v", at(pdb, "spec"))
		}
		if str(pdb, "spec", "selector", "matchLabels", "app.kubernetes.io/component") != "bastion" {
			t.Errorf("PDB selector = %v", at(pdb, "spec", "selector"))
		}
	})
	t.Run("host spreading is DoNotSchedule, zone spreading ScheduleAnyway, both honour taints", func(t *testing.T) {
		_, docs := bstRender(t, bstRequired())
		pod := at(bstWorkload(t, docs), "spec", "template", "spec")
		got := map[string]string{}
		for _, c := range list(pod, "topologySpreadConstraints") {
			got[str(c, "topologyKey")] = str(c, "whenUnsatisfiable") + "/" + str(c, "nodeTaintsPolicy")
		}
		if got["kubernetes.io/hostname"] != "DoNotSchedule/Honor" || got["topology.kubernetes.io/zone"] != "ScheduleAnyway/Honor" {
			t.Errorf("constraints = %v", got)
		}
	})
	t.Run("a StatefulSet spreads per controller revision", func(t *testing.T) {
		_, docs := bstRender(t, bstRequired(), "cacheVolume.type=persistent")
		pod := at(find(docs, "StatefulSet", bastionRelease), "spec", "template", "spec")
		if list(pod, "topologySpreadConstraints", "0", "matchLabelKeys")[0] != "controller-revision-hash" {
			t.Errorf("constraints = %v", at(pod, "topologySpreadConstraints"))
		}
	})
	t.Run("minAvailable replaces maxUnavailable only when the other is nulled", func(t *testing.T) {
		bstFails(t, bstRequired(), []string{"podDisruptionBudget.minAvailable=1"}, "podDisruptionBudget.minAvailable", "podDisruptionBudget.maxUnavailable")
		_, docs := bstRender(t, bstRequired(), "podDisruptionBudget.minAvailable=1", "podDisruptionBudget.maxUnavailable=null")
		if at(find(docs, "PodDisruptionBudget", bastionRelease), "spec", "minAvailable") != 1 {
			t.Error("minAvailable is not rendered")
		}
	})
	t.Run("highAvailability.enforce refuses one replica", func(t *testing.T) {
		bstFails(t, bstRequired(), []string{"replicaCount=1"}, "highAvailability.enforce", "replicaCount")
		bstFails(t, []string{bastionSet("autoscaling")}, []string{"autoscaling.minReplicas=1"}, "highAvailability.enforce", "autoscaling.minReplicas")
	})
	t.Run("highAvailability.enforce refuses a missing PDB", func(t *testing.T) {
		bstFails(t, bstRequired(), []string{"podDisruptionBudget.enabled=false"}, "highAvailability.enforce", "podDisruptionBudget.enabled")
	})
	t.Run("highAvailability.enforce refuses host spreading that may be violated", func(t *testing.T) {
		bstFails(t, bstRequired(), []string{"topologySpread.host.whenUnsatisfiable=ScheduleAnyway"}, "highAvailability.enforce", "topologySpread.host.whenUnsatisfiable")
		bstFails(t, bstRequired(), []string{"topologySpread.enabled=false"}, "highAvailability.enforce", "topologySpread")
		bstFails(t, bstRequired(), []string{"topologySpread.nodeTaintsPolicy=Ignore"}, "highAvailability.enforce", "topologySpread.nodeTaintsPolicy")
	})
	t.Run("highAvailability.enforce: false allows one replica without a PDB", func(t *testing.T) {
		_, docs := bstRender(t, bstRequired(), "highAvailability.enforce=false", "replicaCount=1", "podDisruptionBudget.enabled=false", "topologySpread.enabled=false")
		if len(ofKind(docs, "PodDisruptionBudget")) != 0 {
			t.Error("a PDB is rendered with podDisruptionBudget.enabled: false")
		}
	})
	t.Run("Valkey in standalone mode is development only", func(t *testing.T) {
		bstFails(t, bstRequired(), []string{"config.valkey.mode=standalone", "config.valkey.address=v:6379", "secrets.signingKey.existingSecret=k"}, "highAvailability.enforce", "standalone")
	})
	t.Run("probes use the ops port, over HTTPS while the listeners run TLS", func(t *testing.T) {
		_, docs := bstRender(t, bstRequired())
		c := at(bstWorkload(t, docs), "spec", "template", "spec", "containers", "0")
		for _, p := range []string{"startupProbe", "livenessProbe", "readinessProbe"} {
			if str(c, p, "httpGet", "port") != "ops" || str(c, p, "httpGet", "scheme") != "HTTPS" {
				t.Errorf("%s = %v", p, at(c, p))
			}
		}
		if str(c, "readinessProbe", "httpGet", "path") != "/readyz" || str(c, "livenessProbe", "httpGet", "path") != "/healthz" {
			t.Error("probe paths are not /readyz and /healthz")
		}
	})
}

// TestR_BAS_044_TLS (R-BAS-044): the listener's key pair from tls.existingSecret or the cert-manager
// Certificate, trust bundles, https required for both URLs, the plaintext switch.
func TestR_BAS_044_TLS(t *testing.T) {
	t.Run("the Certificate is rendered for the Service names and mounted", func(t *testing.T) {
		_, docs := bstRender(t, bstRequired())
		cert := find(docs, "Certificate", "trove-bastion-tls")
		if cert == nil {
			t.Fatal("no Certificate")
		}
		if str(cert, "spec", "secretName") != "trove-bastion-tls" || str(cert, "spec", "issuerRef", "name") != "internal-ca" || str(cert, "spec", "issuerRef", "kind") != "ClusterIssuer" {
			t.Errorf("Certificate spec = %v", at(cert, "spec"))
		}
		names := map[string]bool{}
		for _, n := range list(cert, "spec", "dnsNames") {
			names[n.(string)] = true
		}
		for _, n := range []string{"trove-bastion.default.svc", "trove-bastion-ops.default.svc"} {
			if !names[n] {
				t.Errorf("dnsNames lack %s: %v", n, names)
			}
		}
		pod := at(bstWorkload(t, docs), "spec", "template", "spec")
		if got := str(volumeNamed(pod, "secret-tls"), "secret", "secretName"); got != "trove-bastion-tls" {
			t.Errorf("tls volume Secret = %q", got)
		}
	})
	t.Run("tls.existingSecret replaces the Certificate", func(t *testing.T) {
		_, docs := bstRender(t, []string{bastionSet("existing-tls")})
		if len(ofKind(docs, "Certificate")) != 0 {
			t.Error("a Certificate is rendered next to tls.existingSecret")
		}
		pod := at(bstWorkload(t, docs), "spec", "template", "spec")
		if got := str(volumeNamed(pod, "secret-tls"), "secret", "secretName"); got != "trove-bastion-listener" {
			t.Errorf("tls volume Secret = %q", got)
		}
	})
	t.Run("the Ingress verifies the pods against the listener's Secret", func(t *testing.T) {
		a := bstIngressAnnotations(t, []string{bastionSet("existing-tls")})
		if a[hp+"secure-backends"] != "true" || a[hp+"secure-verify-ca-secret"] != "trove-bastion-listener" || a[hp+"secure-sni"] != "trove-bastion.default.svc" {
			t.Errorf("annotations = %v", a)
		}
		a = bstIngressAnnotations(t, bstRequired(), "ingress.backendTLS.caSecret=edge-ca")
		if a[hp+"secure-verify-ca-secret"] != "edge-ca" {
			t.Errorf("annotations = %v", a)
		}
		a = bstIngressAnnotations(t, bstRequired(), "ingress.backendTLS.verify=false")
		if _, ok := a[hp+"secure-verify-ca-secret"]; ok || a[hp+"secure-backends"] != "true" {
			t.Errorf("annotations = %v", a)
		}
	})
	t.Run("trust bundles from a Secret or a ConfigMap give ca_file to the main installation and the bucket", func(t *testing.T) {
		_, docs := bstRender(t, []string{bastionSet("existing-tls")})
		cfg := bstConfig(t, docs)
		if got := at(cfg, "bastion", "main", "ca_file"); got != "/etc/trove/trusted-ca/ca-bundle.crt" {
			t.Errorf("bastion.main.ca_file = %v", got)
		}
		pod := at(bstWorkload(t, docs), "spec", "template", "spec")
		if got := str(volumeNamed(pod, "secret-trusted-ca"), "secret", "secretName"); got != "corp-ca-bundle" {
			t.Errorf("trust bundle Secret = %q", got)
		}
		_, bucket := bstRender(t, []string{bastionSet("bucket-valkey")})
		bcfg := bstConfig(t, bucket)
		if at(bcfg, "bastion", "cache", "bucket", "ca_file") != "/etc/trove/trusted-ca/ca-bundle.crt" {
			t.Errorf("bastion.cache.bucket.ca_file = %v", at(bcfg, "bastion", "cache", "bucket", "ca_file"))
		}
		_, plain := bstRender(t, bstRequired())
		if at(bstConfig(t, plain), "bastion", "main", "ca_file") != nil {
			t.Error("ca_file is rendered without a trust bundle")
		}
	})
	t.Run("Valkey takes its own CA, else the trust bundle", func(t *testing.T) {
		_, docs := bstRender(t, []string{bastionSet("bucket-valkey")}, "secrets.valkeyCA.existingSecret=")
		if got := at(bstConfig(t, docs), "valkey", "tls", "ca_file"); got != "/etc/trove/trusted-ca/ca-bundle.crt" {
			t.Errorf("valkey.tls.ca_file = %v", got)
		}
	})
	t.Run("tls.enabled: false is the explicit plaintext switch", func(t *testing.T) {
		_, docs := bstRender(t, []string{bastionSet("plaintext")})
		cfg := bstConfig(t, docs)
		if at(cfg, "server", "tls") != nil {
			t.Errorf("server.tls = %v", at(cfg, "server", "tls"))
		}
		if len(ofKind(docs, "Certificate")) != 0 {
			t.Error("a Certificate is rendered")
		}
		c := at(bstWorkload(t, docs), "spec", "template", "spec", "containers", "0")
		if at(c, "readinessProbe", "httpGet", "scheme") != nil {
			t.Errorf("readinessProbe = %v", at(c, "readinessProbe"))
		}
		if at(find(docs, "Service", bastionRelease), "spec", "ports", "0", "appProtocol") != nil {
			t.Error("appProtocol https on a plaintext Service")
		}
	})
	t.Run("the ServiceMonitor scrapes over https and verifies the pods", func(t *testing.T) {
		_, docs := bstRender(t, []string{bastionSet("bucket-valkey")})
		ep := at(find(docs, "ServiceMonitor", bastionRelease), "spec", "endpoints", "0")
		if str(ep, "scheme") != "https" || str(ep, "tlsConfig", "serverName") != "trove-bastion-ops.default.svc" ||
			str(ep, "tlsConfig", "ca", "secret", "name") != "trove-bastion-listener" {
			t.Errorf("endpoint = %v", ep)
		}
	})
}

// TestR_BAS_040_ServiceMonitor (R-BAS-040): the ops port of <fullname>-ops, the fixed role relabeling first.
func TestR_BAS_040_ServiceMonitor(t *testing.T) {
	_, docs := bstRender(t, bstRequired(), "metrics.serviceMonitor.enabled=true",
		"metrics.serviceMonitor.relabelings[0].targetLabel=extra", "metrics.serviceMonitor.relabelings[0].replacement=x",
		"metrics.serviceMonitor.labels.release=kube-prometheus-stack")
	sm := find(docs, "ServiceMonitor", bastionRelease)
	if sm == nil {
		t.Fatal("no ServiceMonitor")
	}
	ep := at(sm, "spec", "endpoints", "0")
	if str(ep, "port") != "ops" || str(ep, "path") != "/metrics" || str(ep, "interval") != "30s" {
		t.Errorf("endpoint = %v", ep)
	}
	rel := list(ep, "relabelings")
	if len(rel) != 2 || str(rel[0], "targetLabel") != "role" || str(rel[0], "replacement") != "bastion" || str(rel[1], "targetLabel") != "extra" {
		t.Errorf("relabelings = %v", rel)
	}
	if str(sm, "metadata", "labels", "release") != "kube-prometheus-stack" {
		t.Errorf("labels = %v", at(sm, "metadata", "labels"))
	}
	if str(sm, "spec", "selector", "matchLabels", "app.kubernetes.io/component") != "ops" {
		t.Errorf("selector = %v", at(sm, "spec", "selector"))
	}
	_, off := bstRender(t, bstRequired())
	if len(ofKind(off, "ServiceMonitor")) != 0 || len(ofKind(off, "PrometheusRule")) != 0 {
		t.Error("monitoring objects are rendered by default")
	}
}

// ruleNames renders the PrometheusRule of the chart and returns the names of its alerts in order.
func ruleNames(t *testing.T, files []string, sets ...string) []string {
	t.Helper()
	_, docs := bstRender(t, files, sets...)
	pr := find(docs, "PrometheusRule", bastionRelease)
	if pr == nil {
		t.Fatal("no PrometheusRule")
	}
	var out []string
	for _, r := range list(pr, "spec", "groups", "0", "rules") {
		out = append(out, str(r, "alert"))
	}
	return out
}

// TestR_BAS_041_PrometheusRuleAlerts (R-BAS-041): every alert of the spec by its stable name, disabledAlerts,
// the Valkey alert only with Valkey, the replica alert for the workload kind, extraRules appended. The
// expressions are tested with promtool in TestR_BAS_041_PromtoolRules.
func TestR_BAS_041_PrometheusRuleAlerts(t *testing.T) {
	all := []string{
		"TroveBastionInterfaceMismatch", "TroveBastionMainUnreachable", "TroveBastionMainUnreachableSingleReplica",
		"TroveBastionGraceExhausted", "TroveBastionBaseURLMismatch", "TroveBastionTokenExpiresSoon", "TroveBastionTokenRefused",
		"TroveBastionFeedStale", "TroveBastionMainBudgetSaturated", "TroveBastionAuditEventsDropped", "TroveBastionCacheCorrupt",
		"TroveBastionValkeyUnavailable", "TroveBastionReplicasNotReady",
	}
	t.Run("every alert is rendered with Valkey", func(t *testing.T) {
		got := ruleNames(t, []string{bastionSet("bucket-valkey")})
		if strings.Join(got, ",") != strings.Join(all, ",") {
			t.Errorf("alerts = %v, want %v", got, all)
		}
	})
	t.Run("without Valkey the Valkey alert is not rendered", func(t *testing.T) {
		got := ruleNames(t, bstRequired(), "metrics.prometheusRule.enabled=true")
		if len(got) != len(all)-1 {
			t.Errorf("alerts = %v", got)
		}
		for _, n := range got {
			if n == "TroveBastionValkeyUnavailable" {
				t.Error("the Valkey alert is rendered without Valkey")
			}
		}
	})
	t.Run("disabledAlerts removes an alert", func(t *testing.T) {
		got := ruleNames(t, []string{bastionSet("bucket-valkey")}, "metrics.prometheusRule.disabledAlerts={TroveBastionCacheCorrupt,TroveBastionFeedStale}")
		if len(got) != len(all)-2 {
			t.Errorf("alerts = %v", got)
		}
		for _, n := range got {
			if n == "TroveBastionCacheCorrupt" || n == "TroveBastionFeedStale" {
				t.Errorf("%s is rendered although disabled", n)
			}
		}
	})
	t.Run("extraRules are appended", func(t *testing.T) {
		got := ruleNames(t, bstRequired(), "metrics.prometheusRule.enabled=true", "metrics.prometheusRule.extraRules[0].alert=Mine", "metrics.prometheusRule.extraRules[0].expr=vector(1)")
		if got[len(got)-1] != "Mine" {
			t.Errorf("alerts = %v", got)
		}
	})
	t.Run("no threshold is a chart value: the thresholds are gauges of the Bastion", func(t *testing.T) {
		_, docs := bstRender(t, []string{bastionSet("bucket-valkey")})
		exprs := map[string]string{}
		for _, r := range list(find(docs, "PrometheusRule", bastionRelease), "spec", "groups", "0", "rules") {
			exprs[str(r, "alert")] = str(r, "expr")
		}
		if !strings.Contains(exprs["TroveBastionTokenExpiresSoon"], "trove_bastion_token_warn_before_seconds") {
			t.Errorf("expr = %s", exprs["TroveBastionTokenExpiresSoon"])
		}
		g := exprs["TroveBastionGraceExhausted"]
		if !strings.Contains(g, "trove_bastion_answer_period_seconds") || !strings.Contains(g, "trove_bastion_grace_period_seconds") {
			t.Errorf("expr = %s", g)
		}
		for n, e := range exprs {
			if !strings.Contains(e, `namespace="default"`) {
				t.Errorf("%s is not scoped to the release's namespace: %s", n, e)
			}
		}
	})
	t.Run("the replica alert follows the workload kind", func(t *testing.T) {
		_, docs := bstRender(t, bstRequired(), "metrics.prometheusRule.enabled=true", "cacheVolume.type=persistent")
		var expr string
		for _, r := range list(find(docs, "PrometheusRule", bastionRelease), "spec", "groups", "0", "rules") {
			if str(r, "alert") == "TroveBastionReplicasNotReady" {
				expr = str(r, "expr")
			}
		}
		if !strings.Contains(expr, "kube_statefulset_status_replicas_ready") {
			t.Errorf("expr = %s", expr)
		}
	})
}
