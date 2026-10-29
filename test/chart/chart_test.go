//go:build chart

// Package chart_test renders deploy/helm/trove with the helm binary and asserts the chart
// requirements of docs/spec/ha-and-helm.md (R-HA-002, R-HA-011, R-HA-019, R-HA-027 to R-HA-037,
// R-HA-056, R-HA-061, R-HA-063) and the exit criterion E0.7 of the roadmap. It needs helm at the version
// pinned in tools/versions.env, and nothing else: no cluster.
//
// Run it with `make chart-test` or `go test -tags chart ./test/chart/...`.
package chart_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"go.yaml.in/yaml/v3"
)

type doc = map[string]any

var (
	repoRoot string
	chartDir string
	// valueSets are the value files every requirement is asserted against: the chart's own
	// ci/ sets and the dev profile's values (E0.7).
	valueSets []string
)

func TestMain(m *testing.M) {
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			println("chart tests: go.mod not found above", file)
			os.Exit(2)
		}
		dir = parent
	}
	repoRoot = dir
	chartDir = filepath.Join(repoRoot, "deploy", "helm", "trove")

	if err := checkHelmPin(); err != nil {
		println("chart tests: " + err.Error())
		os.Exit(2)
	}
	sets, err := filepath.Glob(filepath.Join(chartDir, "ci", "*.yaml"))
	if err != nil || len(sets) == 0 {
		println("chart tests: no value sets under deploy/helm/trove/ci")
		os.Exit(2)
	}
	valueSets = append(sets, filepath.Join(repoRoot, "deploy", "dev", "values-dev.yaml"))
	os.Exit(m.Run())
}

// checkHelmPin fails when the helm on PATH is not the version tools/versions.env pins.
func checkHelmPin() error {
	raw, err := os.ReadFile(filepath.Join(repoRoot, "tools", "versions.env"))
	if err != nil {
		return err
	}
	want := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if v, ok := strings.CutPrefix(line, "HELM_VERSION="); ok {
			want = strings.TrimSpace(v)
		}
	}
	if want == "" {
		return errors.New("HELM_VERSION missing from tools/versions.env")
	}
	out, err := exec.Command("helm", "version", "--template", "{{.Version}}").Output()
	if err != nil {
		return errors.New("helm not found on PATH; install helm " + want + " (https://helm.sh/docs/intro/install/)")
	}
	if got := strings.TrimSpace(string(out)); got != want {
		return errors.New("helm " + got + " is installed, tools/versions.env pins " + want +
			"; install helm " + want + " (https://helm.sh/docs/intro/install/)")
	}
	return nil
}

// helm runs the helm binary in the repository root.
func helm(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := exec.Command("helm", args...)
	cmd.Dir = repoRoot
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err = cmd.Run()
	return so.String(), se.String(), err
}

// templateArgs builds `helm template` arguments for value files and --set pairs.
func templateArgs(release string, files []string, sets []string) []string {
	args := []string{"template", release, chartDir}
	for _, f := range files {
		args = append(args, "-f", f)
	}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	return args
}

// render runs helm template and returns the raw output and the parsed documents.
func render(t *testing.T, files []string, sets ...string) (string, []doc) {
	t.Helper()
	return renderRelease(t, "trove", files, sets...)
}

func renderRelease(t *testing.T, release string, files []string, sets ...string) (string, []doc) {
	t.Helper()
	out, stderr, err := helm(t, templateArgs(release, files, sets)...)
	if err != nil {
		t.Fatalf("helm template failed: %v\n%s", err, stderr)
	}
	return out, parse(t, out)
}

// renderFails asserts that helm template fails and that its error names every wanted fragment.
func renderFails(t *testing.T, files []string, sets []string, want ...string) {
	t.Helper()
	out, stderr, err := helm(t, templateArgs("trove", files, sets)...)
	if err == nil {
		t.Fatalf("helm template succeeded (%d bytes), want a failure naming %q", len(out), want)
	}
	for _, w := range want {
		if !strings.Contains(stderr, w) {
			t.Errorf("error does not name %q:\n%s", w, stderr)
		}
	}
}

func parse(t *testing.T, out string) []doc {
	t.Helper()
	dec := yaml.NewDecoder(strings.NewReader(out))
	var docs []doc
	for {
		var d doc
		if err := dec.Decode(&d); err != nil {
			if errors.Is(err, io.EOF) {
				return docs
			}
			t.Fatalf("rendered output is not YAML: %v", err)
		}
		if d != nil {
			docs = append(docs, d)
		}
	}
}

// at walks maps by key and lists by decimal index; it returns nil for a missing path.
func at(v any, path ...string) any {
	for _, p := range path {
		switch c := v.(type) {
		case map[string]any:
			v = c[p]
		case []any:
			i, err := strconv.Atoi(p)
			if err != nil || i < 0 || i >= len(c) {
				return nil
			}
			v = c[i]
		default:
			return nil
		}
	}
	return v
}

func str(v any, path ...string) string {
	s, _ := at(v, path...).(string)
	return s
}

func list(v any, path ...string) []any {
	l, _ := at(v, path...).([]any)
	return l
}

func find(docs []doc, kind, name string) doc {
	for _, d := range docs {
		if str(d, "kind") == kind && str(d, "metadata", "name") == name {
			return d
		}
	}
	return nil
}

func ofKind(docs []doc, kind string) []doc {
	var out []doc
	for _, d := range docs {
		if str(d, "kind") == kind {
			out = append(out, d)
		}
	}
	return out
}

// podSpecs returns the pod spec of every Deployment and Job, keyed by object name.
func podSpecs(docs []doc) map[string]any {
	out := map[string]any{}
	for _, d := range docs {
		switch str(d, "kind") {
		case "Deployment", "Job":
			out[str(d, "metadata", "name")] = at(d, "spec", "template", "spec")
		}
	}
	return out
}

func containerArgs(pod any) []string {
	var out []string
	for _, a := range list(pod, "containers", "0", "args") {
		out = append(out, a.(string))
	}
	return out
}

func envByName(pod any) map[string]any {
	out := map[string]any{}
	for _, e := range list(pod, "containers", "0", "env") {
		out[str(e, "name")] = e
	}
	return out
}

// s3SettingEnv are the variables that read the non-secret S3 settings from the provisioner's
// Secret (secrets.s3.bucketKey, regionKey, endpointKey).
var s3SettingEnv = map[string]bool{
	"TROVE_STORAGE_S3_BUCKET": true, "TROVE_STORAGE_S3_REGION": true, "TROVE_STORAGE_S3_ENDPOINT": true,
}

func base(file string) string { return filepath.Base(file) }

// eachSet runs fn once per value set as a subtest.
func eachSet(t *testing.T, fn func(t *testing.T, file string, out string, docs []doc)) {
	t.Helper()
	for _, f := range valueSets {
		t.Run(base(f), func(t *testing.T) {
			out, docs := render(t, []string{f})
			fn(t, f, out, docs)
		})
	}
}

// required is the smallest set that renders; most failure cases start from it.
func required() []string { return []string{filepath.Join(chartDir, "ci", "required-only.yaml")} }

func TestChart(t *testing.T) {
	t.Run("R-HA-028 helm lint passes for every value set", func(t *testing.T) {
		for _, f := range valueSets {
			t.Run(base(f), func(t *testing.T) {
				if out, stderr, err := helm(t, "lint", "--strict", chartDir, "-f", f); err != nil {
					t.Fatalf("helm lint: %v\n%s%s", err, out, stderr)
				}
			})
		}
	})

	t.Run("R-HA-028 chart is apiVersion v2 with no CRDs, no subcharts and no Secret", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(chartDir, "Chart.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		var chart doc
		if err := yaml.Unmarshal(raw, &chart); err != nil {
			t.Fatal(err)
		}
		if str(chart, "apiVersion") != "v2" || str(chart, "name") != "trove" {
			t.Errorf("Chart.yaml apiVersion/name = %q/%q", str(chart, "apiVersion"), str(chart, "name"))
		}
		if chart["dependencies"] != nil {
			t.Error("Chart.yaml lists dependencies; the chart ships no subcharts")
		}
		for _, dir := range []string{"crds", "charts"} {
			if _, err := os.Stat(filepath.Join(chartDir, dir)); err == nil {
				t.Errorf("deploy/helm/trove/%s exists; the chart ships no CRDs and no subcharts", dir)
			}
		}
		eachSet(t, func(t *testing.T, _ string, out string, docs []doc) {
			if n := len(ofKind(docs, "Secret")); n != 0 {
				t.Errorf("%d Secret objects rendered, the chart never creates one", n)
			}
			for _, k := range []string{"Role", "RoleBinding", "ClusterRole", "ClusterRoleBinding"} {
				if n := len(ofKind(docs, k)); n != 0 {
					t.Errorf("%d %s objects rendered, the chart renders no RBAC (R-HA-033)", n, k)
				}
			}
		})
	})

	t.Run("R-HA-028 names follow the fullname rule", func(t *testing.T) {
		cases := []struct {
			release string
			sets    []string
			prefix  string
		}{
			{"trove", nil, "trove"},
			{"prod", nil, "prod-trove"},
			{"my-trove-eu", nil, "my-trove-eu"},
			{"prod", []string{"fullnameOverride=artifacts"}, "artifacts"},
		}
		for _, c := range cases {
			t.Run(c.release+"->"+c.prefix, func(t *testing.T) {
				_, docs := renderRelease(t, c.release, required(), c.sets...)
				for _, want := range []struct{ kind, name string }{
					{"Deployment", c.prefix + "-web"},
					{"Deployment", c.prefix + "-worker"},
					{"Service", c.prefix},
					{"Service", c.prefix + "-ops"},
					{"ConfigMap", c.prefix + "-config"},
					{"ServiceAccount", c.prefix},
					{"PodDisruptionBudget", c.prefix + "-web"},
					{"PodDisruptionBudget", c.prefix + "-worker"},
					{"Job", c.prefix + "-migrate"},
					{"ConfigMap", c.prefix + "-migrate"},
					{"ServiceAccount", c.prefix + "-migrate"},
				} {
					if find(docs, want.kind, want.name) == nil {
						t.Errorf("%s %s not rendered", want.kind, want.name)
					}
				}
			})
		}
	})

	t.Run("R-HA-028 every object carries the standard labels", func(t *testing.T) {
		eachSet(t, func(t *testing.T, _ string, _ string, docs []doc) {
			for _, d := range docs {
				name := str(d, "kind") + "/" + str(d, "metadata", "name")
				for _, l := range []string{
					"app.kubernetes.io/name", "app.kubernetes.io/instance",
					"app.kubernetes.io/version", "app.kubernetes.io/managed-by", "helm.sh/chart", "app.kubernetes.io/component",
				} {
					if str(d, "metadata", "labels", l) == "" {
						t.Errorf("%s lacks label %s", name, l)
					}
				}
			}
		})
	})

	t.Run("R-HA-002 web and worker Deployments run --role web and --role worker, never all", func(t *testing.T) {
		eachSet(t, func(t *testing.T, _ string, out string, docs []doc) {
			deploys := ofKind(docs, "Deployment")
			// The two role Deployments, plus one pool Deployment per endpoint with pool.enabled
			// (R-EP-008), which also runs --role web.
			pools := 0
			for _, d := range deploys {
				if name := str(d, "metadata", "name"); strings.HasPrefix(name, "trove-web-ep-") {
					pools++
					if got := strings.Join(containerArgs(at(d, "spec", "template", "spec")), " "); got != "serve --role web" {
						t.Errorf("%s args = %q, want serve --role web", name, got)
					}
				}
			}
			if len(deploys) != 2+pools {
				t.Fatalf("%d Deployments rendered, want <fullname>-web, <fullname>-worker and one <fullname>-web-ep-<name> per endpoint pool (%d)", len(deploys), pools)
			}
			for _, role := range []string{"web", "worker"} {
				d := find(docs, "Deployment", "trove-"+role)
				if d == nil {
					t.Fatalf("Deployment trove-%s missing", role)
				}
				args := containerArgs(at(d, "spec", "template", "spec"))
				if strings.Join(args, " ") != "serve --role "+role {
					t.Errorf("trove-%s args = %v, want [serve --role %s]", role, args, role)
				}
			}
			if strings.Contains(out, "--role\n            - all") || strings.Contains(out, "role=all") {
				t.Error("a rendered container runs --role all")
			}
			for name, pod := range podSpecs(docs) {
				for _, a := range containerArgs(pod) {
					if a == "all" {
						t.Errorf("%s passes the argument all", name)
					}
				}
			}
		})
	})

	t.Run("R-HA-002 worker.replicaCount=0 fails and names the key", func(t *testing.T) {
		renderFails(t, required(), []string{"worker.replicaCount=0"}, "worker.replicaCount")
		renderFails(t, required(), []string{"worker.replicaCount=0", "highAvailability.enforce=false"}, "worker.replicaCount")
	})

	t.Run("R-HA-002 the client Service selects only web pods", func(t *testing.T) {
		_, docs := render(t, required())
		svc := find(docs, "Service", "trove")
		if got := str(svc, "spec", "selector", "app.kubernetes.io/component"); got != "web" {
			t.Errorf("client Service selects component %q, want web", got)
		}
		if got := str(svc, "spec", "ports", "0", "name"); got != "http" {
			t.Errorf("client Service port name = %q, want http", got)
		}
		ops := find(docs, "Service", "trove-ops")
		if at(ops, "spec", "selector", "app.kubernetes.io/component") != nil {
			t.Error("the ops Service must select web and worker pods, not one component")
		}
		if got := at(ops, "spec", "ports", "0", "port"); got != 8081 {
			t.Errorf("ops Service port = %v, want 8081", got)
		}
	})

	t.Run("R-HA-002 web and worker pods carry the pool label for later selectors", func(t *testing.T) {
		_, docs := render(t, required())
		for _, role := range []string{"web", "worker"} {
			d := find(docs, "Deployment", "trove-"+role)
			sel, ok := at(d, "spec", "selector", "matchLabels").(map[string]any)
			if !ok {
				t.Fatalf("trove-%s has no selector", role)
			}
			if v, ok := sel["trove/pool"]; !ok || v != "" {
				t.Errorf("trove-%s selector trove/pool = %v (present %v), want the empty value", role, v, ok)
			}
		}
	})

	t.Run("R-HA-061 no ReadWriteMany, no PVC, no StatefulSet, no volumeClaimTemplates", func(t *testing.T) {
		check := func(t *testing.T, out string, docs []doc) {
			for _, bad := range []string{"ReadWriteMany", "ReadOnlyMany", "volumeClaimTemplates", "kind: StatefulSet", "kind: PersistentVolumeClaim"} {
				if strings.Contains(out, bad) {
					t.Errorf("rendered output contains %q", bad)
				}
			}
			for _, k := range []string{"StatefulSet", "PersistentVolumeClaim", "PersistentVolume"} {
				if n := len(ofKind(docs, k)); n != 0 {
					t.Errorf("%d %s objects rendered", n, k)
				}
			}
			for name, pod := range podSpecs(docs) {
				for _, v := range list(pod, "volumes") {
					for _, banned := range []string{"hostPath", "persistentVolumeClaim", "nfs", "iscsi"} {
						if at(v, banned) != nil {
							t.Errorf("%s: volume %s uses %s", name, str(v, "name"), banned)
						}
					}
				}
			}
		}
		eachSet(t, func(t *testing.T, _ string, out string, docs []doc) { check(t, out, docs) })
		t.Run("generic ephemeral spool", func(t *testing.T) {
			out, docs := render(t, required(), "spool.type=ephemeral", "spool.ephemeral.accessModes={ReadWriteOncePod}")
			check(t, out, docs)
			vol := list(at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec"), "volumes")
			found := false
			for _, v := range vol {
				if str(v, "name") == "spool" && at(v, "ephemeral", "volumeClaimTemplate") != nil {
					found = true
				}
			}
			if !found {
				t.Error("spool.type=ephemeral did not render a generic ephemeral volume")
			}
		})
	})

	t.Run("R-HA-061 a ReadWriteMany spool fails schema validation", func(t *testing.T) {
		for _, mode := range []string{"ReadWriteMany", "ReadOnlyMany"} {
			renderFails(t, required(), []string{"spool.type=ephemeral", "spool.ephemeral.accessModes={" + mode + "}"},
				"spool/ephemeral/accessModes")
		}
	})

	t.Run("R-HA-061 the only writable volumes are the spool and /tmp", func(t *testing.T) {
		_, docs := render(t, required())
		for name, pod := range podSpecs(docs) {
			mounts := map[string]bool{}
			for _, m := range list(pod, "containers", "0", "volumeMounts") {
				if at(m, "readOnly") != true {
					mounts[str(m, "mountPath")] = true
				}
			}
			want := map[string]bool{"/tmp": true}
			if strings.HasSuffix(name, "-web") || strings.HasSuffix(name, "-worker") {
				want["/var/lib/trove/spool"] = true
			}
			for p := range mounts {
				if !want[p] {
					t.Errorf("%s mounts %s writable", name, p)
				}
			}
			for p := range want {
				if !mounts[p] {
					t.Errorf("%s lacks the writable mount %s", name, p)
				}
			}
		}
	})

	t.Run("R-HA-029 rows that fail name the offending value", func(t *testing.T) {
		const enc = "secrets.encryption.existingSecret"
		cases := []struct {
			name string
			sets []string
			want []string
		}{
			{"base_url empty", []string{"config.server.base_url="}, []string{"config.server.base_url"}},
			{"database host empty", []string{"config.database.host="}, []string{"config.database.host"}},
			{"database secret empty", []string{"secrets.database.existingSecret="}, []string{"secrets.database.existingSecret"}},
			{"bucket empty", []string{"config.storage.s3.bucket="}, []string{"config.storage.s3.bucket"}},
			{"static s3 without secret", []string{"config.storage.s3.credentials.source=static"}, []string{"config.storage.s3.credentials.source", "secrets.s3.existingSecret"}},
			{"encryption secret empty", []string{enc + "="}, []string{enc, "secrets.encryption.keys"}},
			{"encryption keys empty", []string{"secrets.encryption.keys=null"}, []string{enc, "secrets.encryption.keys"}},
			{"web below two replicas", []string{"web.replicaCount=1"}, []string{"web.replicaCount", "trove-web"}},
			{"worker below two replicas", []string{"worker.replicaCount=1"}, []string{"worker.replicaCount", "trove-worker"}},
			{"web pdb disabled", []string{"web.podDisruptionBudget.enabled=false"}, []string{"web.podDisruptionBudget.enabled"}},
			{"worker pdb disabled", []string{"worker.podDisruptionBudget.enabled=false"}, []string{"worker.podDisruptionBudget.enabled"}},
			{"web host spread soft", []string{"web.topologySpread.host.whenUnsatisfiable=ScheduleAnyway"}, []string{"web.topologySpread.host.whenUnsatisfiable", "DoNotSchedule"}},
			{"worker host spread soft", []string{"worker.topologySpread.host.whenUnsatisfiable=ScheduleAnyway"}, []string{"worker.topologySpread.host.whenUnsatisfiable"}},
			{"web spread ignores taints", []string{"web.topologySpread.nodeTaintsPolicy=Ignore"}, []string{"web.topologySpread.nodeTaintsPolicy", "Honor"}},
			{"web spread disabled", []string{"web.topologySpread.enabled=false"}, []string{"web.topologySpread.host.whenUnsatisfiable"}},
			{"worker replicas zero", []string{"worker.replicaCount=0"}, []string{"worker.replicaCount"}},
			{"upload larger than the web spool", []string{"spool.sizeLimit=5Gi"}, []string{"config.storage.upload.max_size", "web spool budget"}},
			{"grace period below drain delay", []string{"web.terminationGracePeriodSeconds=10"}, []string{"web.terminationGracePeriodSeconds", "config.health.shutdown_drain_delay"}},
			{"worker grace period below drain delay", []string{"config.health.shutdown_drain_delay=30s", "worker.terminationGracePeriodSeconds=40"}, []string{"worker.terminationGracePeriodSeconds"}},
			{"sentinel addresses empty", []string{"config.valkey.sentinel.addresses=null", "highAvailability.enforce=false"}, []string{"config.valkey.sentinel.addresses"}},
			{"sentinel primary name empty", []string{"config.valkey.sentinel.primary_name="}, []string{"config.valkey.sentinel.primary_name"}},
			{"fewer than three sentinels", []string{"config.valkey.sentinel.addresses={a:26379,b:26379}"}, []string{"config.valkey.sentinel.addresses"}},
			{"standalone valkey with enforce", []string{"config.valkey.mode=standalone", "config.valkey.address=v:6379"}, []string{"config.valkey.mode"}},
			{"valkey secret empty", []string{"secrets.valkey.existingSecret="}, []string{"secrets.valkey.existingSecret"}},
			{"valkey tls disabled without allow_insecure", []string{"config.valkey.tls.mode=disable"}, []string{"config.valkey.tls.mode", "config.valkey.allow_insecure"}},
			{"pdb with both budgets", []string{"web.podDisruptionBudget.minAvailable=2"}, []string{"web.podDisruptionBudget.minAvailable", "web.podDisruptionBudget.maxUnavailable"}},
			{"metrics token without secret", []string{"config.metrics.auth=static"}, []string{"config.metrics.auth", "secrets.metricsToken.existingSecret"}},
			{"managed key: spool dir", []string{"config.storage.spool.dir=/data"}, []string{"config.storage.spool.dir"}},
			{"managed key: database password file", []string{"config.database.password_file=/x"}, []string{"config.database.password_file"}},
			{"managed key: valkey ca file", []string{"config.valkey.tls.ca_file=/x"}, []string{"config.valkey.tls.ca_file"}},
			{"managed key: encryption key files", []string{"config.secrets.encryption_key_files={/x}"}, []string{"config.secrets.encryption_key_files"}},
			{"managed key: active key id", []string{"config.secrets.active_key_id=old"}, []string{"config.secrets.active_key_id"}},
			{"spool headroom not smaller than spool", []string{"spool.headroom=40GiB"}, []string{"spool.headroom"}},
			{"spool size is not a quantity", []string{"spool.sizeLimit=lots"}, []string{"not a byte quantity"}},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) { renderFails(t, required(), c.sets, c.want...) })
		}
	})

	t.Run("R-HA-029 values.schema.json refuses unknown keys and wrong types", func(t *testing.T) {
		cases := []struct {
			name string
			sets []string
			want string
		}{
			{"typo at the top", []string{"webb.replicaCount=3"}, "webb"},
			{"typo in a role", []string{"web.replicas=3"}, "replicas"},
			{"type error", []string{"web.replicaCount=many"}, "replicaCount"},
			{"unknown service value", []string{"service.sessionAffinity=ClientIP"}, "sessionAffinity"},
			{"unknown image pull policy", []string{"image.pullPolicy=Sometimes"}, "pullPolicy"},
			{"digest not a digest", []string{"image.digest=latest"}, "digest"},
			{"unknown migration mode", []string{"migrations.mode=job"}, "mode"},
			{"unknown valkey mode", []string{"config.valkey.mode=endpoint"}, "mode"},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) { renderFails(t, required(), c.sets, c.want) })
		}
	})

	t.Run("R-HA-029 and R-HA-027 without the encryption-key Secret the error prints the creation command", func(t *testing.T) {
		out, stderr, err := helm(t, "template", "trove", chartDir, "--namespace", "artifacts",
			"--set", "config.server.base_url=https://trove.example.com",
			"--set", "config.database.host=db", "--set", "config.storage.s3.bucket=b",
			"--set", "config.valkey.sentinel.addresses={a:1,b:1,c:1}", "--set", "config.valkey.sentinel.primary_name=trove",
			"--set", "secrets.database.existingSecret=db", "--set", "secrets.valkey.existingSecret=vk")
		if err == nil {
			t.Fatalf("rendered without an encryption key Secret:\n%s", out)
		}
		for _, w := range []string{
			"secrets.encryption.existingSecret",
			"secrets.encryption.keys",
			"kubectl -n artifacts create secret generic trove-encryption-keys",
			"--from-file=2026-10.key=<(head -c 32 /dev/urandom | base64)",
		} {
			if !strings.Contains(stderr, w) {
				t.Errorf("error lacks %q:\n%s", w, stderr)
			}
		}
	})

	t.Run("R-HA-027 every pod that reads sealed secrets mounts the same key Secret", func(t *testing.T) {
		eachSet(t, func(t *testing.T, _ string, _ string, docs []doc) {
			secrets := map[string]string{}
			for name, pod := range podSpecs(docs) {
				for _, v := range list(pod, "volumes") {
					if str(v, "name") == "secret-encryption" {
						secrets[name] = str(v, "secret", "secretName")
					}
				}
			}
			if len(secrets) < 2 {
				t.Fatalf("encryption keys mounted by %v", secrets)
			}
			var first string
			for name, s := range secrets {
				if first == "" {
					first = s
				}
				if s != first {
					t.Errorf("%s mounts %s, others mount %s", name, s, first)
				}
			}
		})
		t.Run("keys are an ordered ring, first is active", func(t *testing.T) {
			_, docs := render(t, required(), "secrets.encryption.keys={2026-10.key,2026-04.key}")
			var cfg doc
			if err := yaml.Unmarshal([]byte(str(find(docs, "ConfigMap", "trove-config"), "data", "trove.yaml")), &cfg); err != nil {
				t.Fatal(err)
			}
			if got := str(cfg, "secrets", "active_key_id"); got != "2026-10" {
				t.Errorf("active_key_id = %q, want 2026-10", got)
			}
			files := list(cfg, "secrets", "encryption_key_files")
			if len(files) != 2 || files[0] != "/var/run/secrets/trove/encryption/2026-10.key" || files[1] != "/var/run/secrets/trove/encryption/2026-04.key" {
				t.Errorf("encryption_key_files = %v", files)
			}
		})
		t.Run("a change of the key list rolls the pods", func(t *testing.T) {
			_, a := render(t, required(), "secrets.encryption.keys={2026-10.key}")
			_, b := render(t, required(), "secrets.encryption.keys={2026-10.key,2026-04.key}")
			ca := str(find(a, "Deployment", "trove-web"), "spec", "template", "metadata", "annotations", "checksum/encryption-keys")
			cb := str(find(b, "Deployment", "trove-web"), "spec", "template", "metadata", "annotations", "checksum/encryption-keys")
			if ca == "" || ca == cb {
				t.Errorf("checksum/encryption-keys did not change: %q -> %q", ca, cb)
			}
		})
	})

	t.Run("R-HA-030 the configuration file is mounted at /etc/trove/trove.yaml and rolls the pods", func(t *testing.T) {
		eachSet(t, func(t *testing.T, _ string, _ string, docs []doc) {
			for _, role := range []string{"web", "worker"} {
				pod := at(find(docs, "Deployment", "trove-"+role), "spec", "template", "spec")
				ok := false
				for _, m := range list(pod, "containers", "0", "volumeMounts") {
					if str(m, "mountPath") == "/etc/trove/trove.yaml" && at(m, "readOnly") == true {
						ok = true
					}
				}
				if !ok {
					t.Errorf("trove-%s does not mount /etc/trove/trove.yaml read-only", role)
				}
				if str(find(docs, "Deployment", "trove-"+role), "spec", "template", "metadata", "annotations", "checksum/config") == "" {
					t.Errorf("trove-%s carries no checksum/config", role)
				}
			}
			if str(find(docs, "ConfigMap", "trove-config"), "data", "trove.yaml") == "" {
				t.Error("ConfigMap trove-config has no trove.yaml")
			}
		})
		_, a := render(t, required())
		_, b := render(t, required(), "config.log.level=debug")
		ca := str(find(a, "Deployment", "trove-worker"), "spec", "template", "metadata", "annotations", "checksum/config")
		cb := str(find(b, "Deployment", "trove-worker"), "spec", "template", "metadata", "annotations", "checksum/config")
		if ca == cb {
			t.Error("a configuration change did not change checksum/config")
		}
	})

	t.Run("R-HA-030 secrets mount read-only with items, mode 0440, no subPath and never as environment", func(t *testing.T) {
		eachSet(t, func(t *testing.T, _ string, out string, docs []doc) {
			for name, pod := range podSpecs(docs) {
				secretVols := map[string]bool{}
				for _, v := range list(pod, "volumes") {
					if at(v, "secret") == nil {
						continue
					}
					secretVols[str(v, "name")] = true
					if at(v, "secret", "defaultMode") != 288 { // 0440
						t.Errorf("%s: volume %s defaultMode = %v, want 0440", name, str(v, "name"), at(v, "secret", "defaultMode"))
					}
					if len(list(v, "secret", "items")) == 0 {
						t.Errorf("%s: volume %s mounts a whole Secret, want items", name, str(v, "name"))
					}
				}
				for _, m := range list(pod, "containers", "0", "volumeMounts") {
					if secretVols[str(m, "name")] {
						if at(m, "subPath") != nil {
							t.Errorf("%s: secret mount %s uses subPath, rotations would not arrive", name, str(m, "name"))
						}
						if at(m, "readOnly") != true {
							t.Errorf("%s: secret mount %s is writable", name, str(m, "name"))
						}
					}
				}
				for _, e := range list(pod, "containers", "0", "env") {
					// The one exception: the non-secret settings of an S3 provisioner's Secret
					// (bucket, region, endpoint) are read from it by secretKeyRef.
					if at(e, "valueFrom", "secretKeyRef") != nil && !s3SettingEnv[str(e, "name")] {
						t.Errorf("%s: env %s reads a Secret", name, str(e, "name"))
					}
					n := strings.ToLower(str(e, "name"))
					for _, w := range []string{"password", "secret", "token"} {
						if strings.Contains(n, w) {
							t.Errorf("%s: env %s looks like a credential", name, str(e, "name"))
						}
					}
				}
				if at(pod, "containers", "0", "envFrom") != nil && !strings.Contains(out, "envFrom") {
					t.Errorf("%s: unexpected envFrom", name)
				}
			}
		})
	})

	t.Run("R-HA-030 only the pods that need a Secret mount it", func(t *testing.T) {
		_, docs := render(t, []string{filepath.Join(chartDir, "ci", "ha-ingress.yaml")})
		mounted := func(name string) map[string]bool {
			out := map[string]bool{}
			for _, v := range list(podSpecs(docs)[name], "volumes") {
				if at(v, "secret") != nil {
					out[str(v, "secret", "secretName")] = true
				}
			}
			return out
		}
		job := mounted("trove-migrate")
		for _, s := range []string{"trove-valkey", "trove-valkey-sentinel", "trove-valkey-ca", "trove-valkey-client", "trove-s3", "trove-metrics"} {
			if job[s] {
				t.Errorf("the migration Job mounts %s", s)
			}
		}
		if !job["trove-db-owner"] || job["trove-db-app"] {
			t.Errorf("the migration Job must use the migration credentials, mounts %v", job)
		}
		for _, role := range []string{"web", "worker"} {
			m := mounted("trove-" + role)
			for _, s := range []string{"trove-db-app", "trove-db-ca", "trove-db-client", "trove-s3", "trove-valkey", "trove-valkey-sentinel", "trove-valkey-ca", "trove-valkey-client", "trove-metrics", "trove-encryption-keys"} {
				if !m[s] {
					t.Errorf("trove-%s does not mount %s", role, s)
				}
			}
			if m["trove-db-owner"] {
				t.Errorf("trove-%s mounts the migration credentials", role)
			}
		}
	})

	t.Run("R-HA-030 the chart sets the file paths of the Secrets it mounts", func(t *testing.T) {
		_, docs := render(t, []string{filepath.Join(chartDir, "ci", "ha-ingress.yaml")})
		var cfg doc
		if err := yaml.Unmarshal([]byte(str(find(docs, "ConfigMap", "trove-config"), "data", "trove.yaml")), &cfg); err != nil {
			t.Fatal(err)
		}
		want := map[string][]string{
			"/var/run/secrets/trove/database/username":        {"database", "username_file"},
			"/var/run/secrets/trove/database/password":        {"database", "password_file"},
			"/var/run/secrets/trove/database-ca/ca.crt":       {"database", "ssl_root_cert_file"},
			"/var/run/secrets/trove/database-client/tls.crt":  {"database", "ssl_cert_file"},
			"/var/run/secrets/trove/database-client/tls.key":  {"database", "ssl_key_file"},
			"/var/run/secrets/trove/valkey/username":          {"valkey", "username_file"},
			"/var/run/secrets/trove/valkey/password":          {"valkey", "password_file"},
			"/var/run/secrets/trove/valkey-sentinel/password": {"valkey", "sentinel", "password_file"},
			"/var/run/secrets/trove/valkey-ca/ca.crt":         {"valkey", "tls", "ca_file"},
			"/var/run/secrets/trove/valkey-client/tls.crt":    {"valkey", "tls", "cert_file"},
			"/var/run/secrets/trove/valkey-client/tls.key":    {"valkey", "tls", "key_file"},
			"/var/run/secrets/trove/s3/access-key-id":         {"storage", "s3", "credentials", "access_key_id_file"},
			"/var/run/secrets/trove/s3/secret-access-key":     {"storage", "s3", "credentials", "secret_access_key_file"},
			"/var/run/secrets/trove/metrics/token":            {"metrics", "bearer_token_file"},
			"/var/lib/trove/spool":                            {"storage", "spool", "dir"},
			"/var/run/secrets/trove/encryption/2026-10.key":   {"secrets", "encryption_key_files", "0"},
			"/var/run/secrets/trove/encryption/2026-04.key":   {"secrets", "encryption_key_files", "1"},
		}
		for value, path := range want {
			if got := str(cfg, path...); got != value {
				t.Errorf("config %s = %q, want %q", strings.Join(path, "."), got, value)
			}
		}
	})

	t.Run("R-HA-030 the chart sets the downward-API and runtime variables and disables service links", func(t *testing.T) {
		eachSet(t, func(t *testing.T, _ string, _ string, docs []doc) {
			for name, pod := range podSpecs(docs) {
				if at(pod, "enableServiceLinks") != false {
					t.Errorf("%s: enableServiceLinks is not false", name)
				}
				env := envByName(pod)
				for v, field := range map[string]string{"TROVE_POD_NAME": "metadata.name", "TROVE_POD_NAMESPACE": "metadata.namespace", "TROVE_NODE_NAME": "spec.nodeName"} {
					if got := str(env[v], "valueFrom", "fieldRef", "fieldPath"); got != field {
						t.Errorf("%s: %s fieldPath = %q, want %q", name, v, got, field)
					}
				}
				if strings.HasSuffix(name, "-web") || strings.HasSuffix(name, "-worker") || strings.Contains(name, "-web-ep-") {
					for _, v := range []string{"TROVE_RUNTIME_MEMORY_LIMIT_BYTES", "TROVE_RUNTIME_TERMINATION_GRACE_PERIOD", "TROVE_STORAGE_SPOOL_MAX_BYTES"} {
						if env[v] == nil {
							t.Errorf("%s lacks %s", name, v)
						}
					}
				}
				for v := range env {
					if strings.HasPrefix(v, "TROVE_") {
						switch v {
						case "TROVE_POD_NAME", "TROVE_POD_NAMESPACE", "TROVE_NODE_NAME", "TROVE_RUNTIME_MEMORY_LIMIT_BYTES",
							"TROVE_RUNTIME_TERMINATION_GRACE_PERIOD", "TROVE_STORAGE_SPOOL_MAX_BYTES",
							"TROVE_DATABASE_PREFLIGHT_EXPECTED_WEB_REPLICAS", "TROVE_DATABASE_PREFLIGHT_EXPECTED_WORKER_REPLICAS", "TROVE_LOG_LEVEL":
						case "TROVE_POOL_NAME": // the pods of an endpoint pool (R-EP-002, R-EP-008)
							if !strings.Contains(name, "-web-ep-") {
								t.Errorf("%s sets TROVE_POOL_NAME, which only an endpoint pool does", name)
							}
						case "TROVE_STORAGE_S3_BUCKET", "TROVE_STORAGE_S3_REGION", "TROVE_STORAGE_S3_ENDPOINT": // from the S3 Secret
						default:
							t.Errorf("%s sets %s, which the spec does not list (R-HA-030)", name, v)
						}
					}
				}
			}
		})
	})

	t.Run("R-HA-033 pods satisfy the restricted Pod Security Standard", func(t *testing.T) {
		eachSet(t, func(t *testing.T, _ string, _ string, docs []doc) {
			specs := podSpecs(docs)
			if len(specs) < 3 && !strings.Contains(t.Name(), "disabled") {
				t.Fatalf("expected the two Deployments and the migration Job, got %d pod specs", len(specs))
			}
			for name, pod := range specs {
				if at(pod, "securityContext", "runAsNonRoot") != true {
					t.Errorf("%s: pod runAsNonRoot is not true", name)
				}
				if got := str(pod, "securityContext", "seccompProfile", "type"); got != "RuntimeDefault" {
					t.Errorf("%s: pod seccompProfile = %q", name, got)
				}
				for _, bad := range []string{"hostNetwork", "hostPID", "hostIPC"} {
					if at(pod, bad) == true {
						t.Errorf("%s: %s is set", name, bad)
					}
				}
				if at(pod, "automountServiceAccountToken") != false {
					t.Errorf("%s: automountServiceAccountToken is not false", name)
				}
				for _, v := range list(pod, "volumes") {
					if at(v, "hostPath") != nil {
						t.Errorf("%s: hostPath volume", name)
					}
				}
				for _, key := range []string{"containers", "initContainers"} {
					for i, c := range list(pod, key) {
						sc := at(c, "securityContext")
						label := name + "/" + key + "/" + strconv.Itoa(i)
						if at(sc, "allowPrivilegeEscalation") != false {
							t.Errorf("%s: allowPrivilegeEscalation is not false", label)
						}
						if at(sc, "privileged") == true {
							t.Errorf("%s: privileged", label)
						}
						if at(sc, "readOnlyRootFilesystem") != true {
							t.Errorf("%s: readOnlyRootFilesystem is not true", label)
						}
						if at(sc, "runAsNonRoot") != true {
							t.Errorf("%s: runAsNonRoot is not true", label)
						}
						drop := list(sc, "capabilities", "drop")
						if len(drop) != 1 || drop[0] != "ALL" {
							t.Errorf("%s: capabilities.drop = %v, want [ALL]", label, drop)
						}
						if add := list(sc, "capabilities", "add"); len(add) != 0 {
							t.Errorf("%s: capabilities.add = %v", label, add)
						}
						if got := str(sc, "seccompProfile", "type"); got != "RuntimeDefault" {
							t.Errorf("%s: seccompProfile = %q", label, got)
						}
						if ports := list(c, "ports"); len(ports) > 0 {
							for _, p := range ports {
								if hp := at(p, "hostPort"); hp != nil {
									t.Errorf("%s: hostPort %v", label, hp)
								}
							}
						}
					}
				}
			}
		})
		t.Run("uid and group are 65532", func(t *testing.T) {
			_, docs := render(t, required())
			for name, pod := range podSpecs(docs) {
				for _, k := range []string{"runAsUser", "runAsGroup", "fsGroup"} {
					if at(pod, "securityContext", k) != 65532 {
						t.Errorf("%s: %s = %v, want 65532", name, k, at(pod, "securityContext", k))
					}
				}
			}
		})
	})

	t.Run("R-HA-034 probes render as specified", func(t *testing.T) {
		_, docs := render(t, required())
		for _, role := range []string{"web", "worker"} {
			c := at(find(docs, "Deployment", "trove-"+role), "spec", "template", "spec", "containers", "0")
			want := map[string][4]any{ // path, period, timeout, threshold
				"startupProbe":   {"/healthz", 2, 2, 60},
				"livenessProbe":  {"/healthz", 10, 3, 6},
				"readinessProbe": {"/readyz", 5, 3, 1},
			}
			for probe, w := range want {
				if str(c, probe, "httpGet", "path") != w[0] || str(c, probe, "httpGet", "port") != "ops" ||
					at(c, probe, "periodSeconds") != w[1] || at(c, probe, "timeoutSeconds") != w[2] || at(c, probe, "failureThreshold") != w[3] {
					t.Errorf("trove-%s %s = %v", role, probe, at(c, probe))
				}
			}
		}
		t.Run("a probe can be disabled or replaced", func(t *testing.T) {
			_, docs := render(t, required(), "probes.startup.enabled=false", "probes.liveness.custom.tcpSocket.port=ops")
			c := at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec", "containers", "0")
			if at(c, "startupProbe") != nil {
				t.Error("startupProbe rendered although disabled")
			}
			if at(c, "livenessProbe", "httpGet") != nil || at(c, "livenessProbe", "tcpSocket", "port") != "ops" {
				t.Errorf("livenessProbe = %v, want the custom probe only", at(c, "livenessProbe"))
			}
		})
	})

	t.Run("R-ARCH-024 probes use the operations port and no preStop hook is rendered", func(t *testing.T) {
		eachSet(t, func(t *testing.T, _ string, out string, docs []doc) {
			if strings.Contains(out, "preStop") {
				t.Error("a preStop hook is rendered; the drain delay lives in the process (R-HA-011)")
			}
			web := at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec", "containers", "0")
			var ports []string
			for _, p := range list(web, "ports") {
				if str(p, "name") == "" {
					continue // an endpoint's port (R-EP-008), asserted in TestEndpoints
				}
				ports = append(ports, str(p, "name")+"="+strconv.Itoa(at(p, "containerPort").(int)))
			}
			if strings.Join(ports, ",") != "http=8080,ops=8081" {
				t.Errorf("web ports = %v, want http=8080 and ops=8081 only", ports)
			}
			worker := at(find(docs, "Deployment", "trove-worker"), "spec", "template", "spec", "containers", "0")
			ports = nil
			for _, p := range list(worker, "ports") {
				ports = append(ports, str(p, "name"))
			}
			if strings.Join(ports, ",") != "ops" {
				t.Errorf("worker ports = %v, want ops only (no client listener)", ports)
			}
		})
	})

	t.Run("R-HA-011 the chart sets the grace period per role, passes it to the process and keeps it above the drain delay", func(t *testing.T) {
		_, docs := render(t, required())
		for role, want := range map[string]struct {
			seconds int
			env     string
		}{"web": {120, "120s"}, "worker": {300, "300s"}} {
			spec := at(find(docs, "Deployment", "trove-"+role), "spec", "template", "spec")
			if at(spec, "terminationGracePeriodSeconds") != want.seconds {
				t.Errorf("trove-%s terminationGracePeriodSeconds = %v, want %d", role, at(spec, "terminationGracePeriodSeconds"), want.seconds)
			}
			if got := str(envByName(spec)["TROVE_RUNTIME_TERMINATION_GRACE_PERIOD"], "value"); got != want.env {
				t.Errorf("trove-%s TROVE_RUNTIME_TERMINATION_GRACE_PERIOD = %q, want %q", role, got, want.env)
			}
		}
		_, docs = render(t, required(), "web.terminationGracePeriodSeconds=45", "config.health.shutdown_drain_delay=30s")
		if got := str(envByName(at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec"))["TROVE_RUNTIME_TERMINATION_GRACE_PERIOD"], "value"); got != "45s" {
			t.Errorf("grace period env = %q, want 45s (exactly the drain delay plus 15 s is allowed)", got)
		}
	})

	t.Run("R-HA-035 the spool is an emptyDir bounded by its size limit and the budget leaves headroom", func(t *testing.T) {
		_, docs := render(t, required(), "spool.sizeLimit=12Gi", "spool.headroom=2GiB", "worker.spool.sizeLimit=3Gi", "config.storage.upload.max_size=512MiB")
		for role, want := range map[string]struct{ limit, max string }{
			"web":    {"12Gi", "10737418240"},
			"worker": {"3Gi", "1073741824"},
		} {
			pod := at(find(docs, "Deployment", "trove-"+role), "spec", "template", "spec")
			found := false
			for _, v := range list(pod, "volumes") {
				if str(v, "name") == "spool" {
					found = true
					if got := str(v, "emptyDir", "sizeLimit"); got != want.limit {
						t.Errorf("trove-%s spool sizeLimit = %q, want %q", role, got, want.limit)
					}
				}
			}
			if !found {
				t.Errorf("trove-%s has no spool volume", role)
			}
			if got := str(envByName(pod)["TROVE_STORAGE_SPOOL_MAX_BYTES"], "value"); got != want.max {
				t.Errorf("trove-%s TROVE_STORAGE_SPOOL_MAX_BYTES = %q, want %q", role, got, want.max)
			}
		}
		t.Run("an explicit storage.spool.max_bytes wins", func(t *testing.T) {
			_, docs := render(t, required(), "config.storage.spool.max_bytes=1073741824")
			if envByName(at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec"))["TROVE_STORAGE_SPOOL_MAX_BYTES"] != nil {
				t.Error("the chart sets TROVE_STORAGE_SPOOL_MAX_BYTES although config.storage.spool.max_bytes is set")
			}
		})
		t.Run("a worker spool smaller than the largest upload renders: workers accept no uploads", func(t *testing.T) {
			_, docs := render(t, required(), "worker.spool.sizeLimit=5Gi", "config.storage.upload.max_size=10GiB")
			pod := at(find(docs, "Deployment", "trove-worker"), "spec", "template", "spec")
			if got := str(envByName(pod)["TROVE_STORAGE_SPOOL_MAX_BYTES"], "value"); got != "3221225472" {
				t.Errorf("trove-worker TROVE_STORAGE_SPOOL_MAX_BYTES = %q, want 3221225472", got)
			}
		})
		t.Run("spool.sizeLimit=5Gi with a 10GiB upload fails", func(t *testing.T) {
			renderFails(t, required(), []string{"spool.sizeLimit=5Gi", "config.storage.upload.max_size=10GiB"}, "config.storage.upload.max_size", "spool.sizeLimit")
		})
	})

	t.Run("R-HA-036 GOMAXPROCS follows the CPU request unless a CPU limit is set", func(t *testing.T) {
		_, docs := render(t, required())
		env := envByName(at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec"))
		if got := str(env["GOMAXPROCS"], "valueFrom", "resourceFieldRef", "resource"); got != "requests.cpu" {
			t.Errorf("GOMAXPROCS resource = %q, want requests.cpu", got)
		}
		if got := str(env["TROVE_RUNTIME_MEMORY_LIMIT_BYTES"], "valueFrom", "resourceFieldRef", "resource"); got != "limits.memory" {
			t.Errorf("TROVE_RUNTIME_MEMORY_LIMIT_BYTES resource = %q, want limits.memory", got)
		}
		_, docs = render(t, required(), "web.resources.limits.cpu=3")
		env = envByName(at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec"))
		if env["GOMAXPROCS"] != nil {
			t.Error("GOMAXPROCS is set although a CPU limit is set")
		}
		web := at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec", "containers", "0", "resources")
		worker := at(find(docs, "Deployment", "trove-worker"), "spec", "template", "spec", "containers", "0", "resources")
		if str(web, "requests", "cpu") != "1" || str(web, "limits", "memory") != "2Gi" ||
			str(worker, "requests", "cpu") != "500m" || str(worker, "limits", "memory") != "1Gi" {
			t.Errorf("default resources: web %v, worker %v", web, worker)
		}
	})

	t.Run("R-HA-032 each Deployment has a disruption budget and spreads over zones and hosts", func(t *testing.T) {
		eachSet(t, func(t *testing.T, f string, _ string, docs []doc) {
			for _, role := range []string{"web", "worker"} {
				pdb := find(docs, "PodDisruptionBudget", "trove-"+role)
				if pdb == nil {
					t.Fatalf("no PodDisruptionBudget trove-%s", role)
				}
				if got := str(pdb, "spec", "unhealthyPodEvictionPolicy"); got != "AlwaysAllow" {
					t.Errorf("trove-%s PDB unhealthyPodEvictionPolicy = %q", role, got)
				}
				if at(pdb, "spec", "maxUnavailable") == nil && at(pdb, "spec", "minAvailable") == nil {
					t.Errorf("trove-%s PDB has neither maxUnavailable nor minAvailable", role)
				}
				if at(pdb, "spec", "maxUnavailable") != nil && at(pdb, "spec", "minAvailable") != nil {
					t.Errorf("trove-%s PDB sets both budgets", role)
				}
				dep := find(docs, "Deployment", "trove-"+role)
				if got := at(pdb, "spec", "selector", "matchLabels"); !equalMaps(got, at(dep, "spec", "selector", "matchLabels")) {
					t.Errorf("trove-%s PDB selector %v differs from the Deployment's %v", role, got, at(dep, "spec", "selector", "matchLabels"))
				}
				cs := list(dep, "spec", "template", "spec", "topologySpreadConstraints")
				keys := map[string]any{}
				for _, c := range cs {
					keys[str(c, "topologyKey")] = c
					if str(c, "nodeTaintsPolicy") != "Honor" {
						t.Errorf("trove-%s constraint %s nodeTaintsPolicy = %q, want Honor", role, str(c, "topologyKey"), str(c, "nodeTaintsPolicy"))
					}
					if l := list(c, "matchLabelKeys"); len(l) != 1 || l[0] != "pod-template-hash" {
						t.Errorf("trove-%s constraint %s matchLabelKeys = %v", role, str(c, "topologyKey"), l)
					}
					if !equalMaps(at(c, "labelSelector", "matchLabels"), at(dep, "spec", "selector", "matchLabels")) {
						t.Errorf("trove-%s constraint %s selects other pods than the Deployment's", role, str(c, "topologyKey"))
					}
				}
				if len(cs) != 2 || keys["topology.kubernetes.io/zone"] == nil || keys["kubernetes.io/hostname"] == nil {
					t.Fatalf("trove-%s has constraints %v, want zone and hostname", role, keys)
				}
				if base(f) != "single-node.yaml" {
					if got := str(keys["kubernetes.io/hostname"], "whenUnsatisfiable"); got != "DoNotSchedule" {
						t.Errorf("trove-%s host spread = %q, want DoNotSchedule", role, got)
					}
				}
				if got := str(keys["topology.kubernetes.io/zone"], "whenUnsatisfiable"); got != "ScheduleAnyway" {
					t.Errorf("trove-%s zone spread = %q, want ScheduleAnyway", role, got)
				}
				tol := map[string]any{}
				for _, x := range list(dep, "spec", "template", "spec", "tolerations") {
					tol[str(x, "key")] = at(x, "tolerationSeconds")
				}
				if tol["node.kubernetes.io/not-ready"] != 60 || tol["node.kubernetes.io/unreachable"] != 60 {
					t.Errorf("trove-%s node-failure tolerations = %v, want 60 s", role, tol)
				}
			}
		})
	})

	t.Run("R-HA-032 defaults are three web and two worker replicas with a rolling update that never drops capacity", func(t *testing.T) {
		_, docs := render(t, required())
		for role, n := range map[string]int{"web": 3, "worker": 2} {
			d := find(docs, "Deployment", "trove-"+role)
			if at(d, "spec", "replicas") != n {
				t.Errorf("trove-%s replicas = %v, want %d", role, at(d, "spec", "replicas"), n)
			}
			if at(d, "spec", "strategy", "rollingUpdate", "maxUnavailable") != 0 || at(d, "spec", "strategy", "rollingUpdate", "maxSurge") != 1 {
				t.Errorf("trove-%s strategy = %v", role, at(d, "spec", "strategy"))
			}
			if at(d, "spec", "minReadySeconds") != 10 {
				t.Errorf("trove-%s minReadySeconds = %v", role, at(d, "spec", "minReadySeconds"))
			}
		}
	})

	t.Run("R-HA-037 no Service has session affinity and the Ingress routes the whole host", func(t *testing.T) {
		eachSet(t, func(t *testing.T, _ string, out string, docs []doc) {
			svcs := ofKind(docs, "Service")
			own := 0
			for _, s := range svcs {
				if str(s, "metadata", "labels", "trove/endpoint") == "" {
					own++
				}
			}
			if own != 2 {
				t.Errorf("%d Services rendered besides the endpoints', want the client and the ops Service", own)
			}
			for _, s := range svcs {
				if got := str(s, "spec", "sessionAffinity"); got != "None" {
					t.Errorf("Service %s sessionAffinity = %q, want None", str(s, "metadata", "name"), got)
				}
			}
			for _, bad := range []string{"ClientIP", "sessionAffinityConfig", "affinity-mode", "session-cookie", "affinity: cookie"} {
				if strings.Contains(out, bad) {
					t.Errorf("rendered output contains %q", bad)
				}
			}
			for _, ing := range ofKind(docs, "Ingress") {
				endpoint := str(ing, "metadata", "labels", "trove/endpoint") != ""
				for _, r := range list(ing, "spec", "rules") {
					for _, p := range list(r, "http", "paths") {
						if str(p, "path") != "/" || str(p, "pathType") != "Prefix" {
							t.Errorf("ingress rule %v is not path / with pathType Prefix", p)
						}
						if endpoint {
							continue // an endpoint's Ingress routes to its own Service (TestEndpoints)
						}
						if str(p, "backend", "service", "name") != "trove" || str(p, "backend", "service", "port", "name") != "http" {
							t.Errorf("ingress backend = %v, want Service trove port http", at(p, "backend"))
						}
					}
				}
				if strings.Contains(out, "8081") && strings.Contains(strings.SplitN(out, "kind: Ingress", 2)[1], "8081") {
					t.Error("the Ingress mentions the operations port")
				}
			}
		})
		t.Run("sessionAffinity is not a value", func(t *testing.T) {
			renderFails(t, required(), []string{"service.sessionAffinity=ClientIP"}, "sessionAffinity")
		})
	})

	t.Run("R-HA-037 ingress.path fails schema validation and names path", func(t *testing.T) {
		renderFails(t, required(), []string{"ingress.enabled=true", "ingress.path=/trove"}, "path")
		_, docs := render(t, required(), "ingress.enabled=true", "ingress.className=traefik", "ingress.backendTLS.caSecret=trove-tls")
		ing := ofKind(docs, "Ingress")
		if len(ing) != 1 {
			t.Fatalf("%d Ingress objects, want 1", len(ing))
		}
		if got := str(ing[0], "spec", "rules", "0", "host"); got != "trove.example.com" {
			t.Errorf("ingress host = %q, want the host of config.server.base_url", got)
		}
		if got := str(ing[0], "spec", "ingressClassName"); got != "traefik" {
			t.Errorf("ingressClassName = %q", got)
		}
		_, docs = render(t, required())
		if n := len(ofKind(docs, "Ingress")); n != 0 {
			t.Errorf("an Ingress is rendered by default")
		}
	})

	t.Run("R-ARCH-032 no state between requests: Services never pin a client to a pod", func(t *testing.T) {
		eachSet(t, func(t *testing.T, _ string, _ string, docs []doc) {
			for _, s := range ofKind(docs, "Service") {
				if at(s, "spec", "sessionAffinityConfig") != nil || str(s, "spec", "sessionAffinity") != "None" {
					t.Errorf("Service %s keeps a client affinity", str(s, "metadata", "name"))
				}
			}
		})
	})

	t.Run("R-HA-019 the migration Job is a pre-install and pre-upgrade hook with hook-scoped ConfigMap and ServiceAccount", func(t *testing.T) {
		_, docs := render(t, required())
		hook := func(d doc) (string, string, string) {
			return str(d, "metadata", "annotations", "helm.sh/hook"),
				str(d, "metadata", "annotations", "helm.sh/hook-weight"),
				str(d, "metadata", "annotations", "helm.sh/hook-delete-policy")
		}
		job := find(docs, "Job", "trove-migrate")
		if h, w, p := hook(job); h != "pre-install,pre-upgrade" || w != "0" || p != "before-hook-creation" {
			t.Errorf("Job hook = %q weight %q policy %q", h, w, p)
		}
		for _, d := range []doc{find(docs, "ConfigMap", "trove-migrate"), find(docs, "ServiceAccount", "trove-migrate")} {
			if h, w, _ := hook(d); h != "pre-install,pre-upgrade" || w != "-10" {
				t.Errorf("%s hook = %q weight %q, want weight -10", str(d, "kind"), h, w)
			}
		}
		pod := at(job, "spec", "template", "spec")
		if got := strings.Join(containerArgs(pod), " "); got != "migrate up" {
			t.Errorf("migration args = %q", got)
		}
		if str(pod, "restartPolicy") != "Never" || str(pod, "serviceAccountName") != "trove-migrate" {
			t.Errorf("migration pod restartPolicy/serviceAccountName = %q/%q", str(pod, "restartPolicy"), str(pod, "serviceAccountName"))
		}
		spec := at(job, "spec")
		if at(spec, "backoffLimit") != 2 || at(spec, "activeDeadlineSeconds") != 1800 || at(spec, "ttlSecondsAfterFinished") != 86400 {
			t.Errorf("Job limits = %v/%v/%v, want 2/1800/86400", at(spec, "backoffLimit"), at(spec, "activeDeadlineSeconds"), at(spec, "ttlSecondsAfterFinished"))
		}
		rules := list(spec, "podFailurePolicy", "rules")
		if len(rules) != 2 || str(rules[0], "action") != "Ignore" || str(rules[0], "onPodConditions", "0", "type") != "DisruptionTarget" ||
			str(rules[1], "action") != "FailJob" || at(rules[1], "onExitCodes", "values", "0") != 3 {
			t.Errorf("podFailurePolicy = %v", rules)
		}
		env := envByName(pod)
		if env["TROVE_DATABASE_PREFLIGHT_EXPECTED_WEB_REPLICAS"] == nil || env["TROVE_DATABASE_PREFLIGHT_EXPECTED_WORKER_REPLICAS"] == nil {
			t.Error("the migration Job lacks the expected-replica variables")
		}
		mainCfg := str(find(docs, "ConfigMap", "trove-config"), "data", "trove.yaml")
		if got := str(find(docs, "ConfigMap", "trove-migrate"), "data", "trove.yaml"); got != mainCfg {
			t.Error("the hook ConfigMap does not carry the same trove.yaml as <fullname>-config")
		}
	})

	t.Run("R-HA-019 migrations.mode disabled renders no Job", func(t *testing.T) {
		_, docs := render(t, required(), "migrations.mode=disabled")
		for _, k := range []string{"Job", "CronJob"} {
			if n := len(ofKind(docs, k)); n != 0 {
				t.Errorf("%d %s objects rendered", n, k)
			}
		}
		if find(docs, "ConfigMap", "trove-migrate") != nil || find(docs, "ServiceAccount", "trove-migrate") != nil {
			t.Error("hook resources rendered with migrations disabled")
		}
	})

	t.Run("R-HA-056 the Valkey wiring reaches web and worker only", func(t *testing.T) {
		_, docs := render(t, required())
		var cfg doc
		if err := yaml.Unmarshal([]byte(str(find(docs, "ConfigMap", "trove-config"), "data", "trove.yaml")), &cfg); err != nil {
			t.Fatal(err)
		}
		if str(cfg, "valkey", "mode") != "sentinel" || str(cfg, "valkey", "sentinel", "primary_name") != "trove" || len(list(cfg, "valkey", "sentinel", "addresses")) != 3 {
			t.Errorf("valkey config = %v", cfg["valkey"])
		}
		if str(cfg, "valkey", "tls", "mode") != "verify-full" {
			t.Errorf("valkey tls mode = %q, want verify-full", str(cfg, "valkey", "tls", "mode"))
		}
		for _, name := range []string{"trove-web", "trove-worker"} {
			ok := false
			for _, v := range list(podSpecs(docs)[name], "volumes") {
				if str(v, "secret", "secretName") == "trove-valkey" {
					ok = true
				}
			}
			if !ok {
				t.Errorf("%s does not mount the Valkey Secret", name)
			}
		}
		for _, v := range list(podSpecs(docs)["trove-migrate"], "volumes") {
			if strings.Contains(str(v, "secret", "secretName"), "valkey") {
				t.Error("the migration Job mounts a Valkey Secret")
			}
		}
	})
}

var (
	troveOnce sync.Once
	troveBin  string
	troveErr  error
)

// trove builds the binary once; the tests that need it run its configuration validator over
// what the chart renders.
func trove(t *testing.T) string {
	t.Helper()
	troveOnce.Do(func() {
		dir, err := os.MkdirTemp("", "trove-chart-test-")
		if err != nil {
			troveErr = err
			return
		}
		troveBin = filepath.Join(dir, "trove")
		cmd := exec.Command("go", "build", "-o", troveBin, "./cmd/trove")
		cmd.Dir = repoRoot
		if out, err := cmd.CombinedOutput(); err != nil {
			troveErr = errors.New("go build ./cmd/trove: " + err.Error() + "\n" + string(out))
		}
	})
	if troveErr != nil {
		t.Fatalf("the trove binary does not build: %v", troveErr)
	}
	return troveBin
}

func TestRenderedConfigurationPassesTheBinary(t *testing.T) {
	// R-OPS-028: one file, strict validation of every key and every TROVE_ variable. The chart's
	// file and the variables it sets must pass the binary's own loader, for every value set.
	t.Run("R-OPS-028 the rendered file and the chart's variables pass trove admin config validate", func(t *testing.T) {
		bin := trove(t)
		for _, f := range valueSets {
			t.Run(base(f), func(t *testing.T) {
				_, docs := render(t, []string{f})
				cfgPath := filepath.Join(t.TempDir(), "trove.yaml")
				if err := os.WriteFile(cfgPath, []byte(str(find(docs, "ConfigMap", "trove-config"), "data", "trove.yaml")), 0o600); err != nil {
					t.Fatal(err)
				}
				pods := map[string]any{
					"web":    at(find(docs, "Deployment", "trove-web"), "spec", "template", "spec"),
					"worker": at(find(docs, "Deployment", "trove-worker"), "spec", "template", "spec"),
				}
				for _, d := range ofKind(docs, "Deployment") {
					if n := str(d, "metadata", "name"); strings.HasPrefix(n, "trove-web-ep-") {
						pods[strings.TrimPrefix(n, "trove-")] = at(d, "spec", "template", "spec")
					}
				}
				if job := find(docs, "Job", "trove-migrate"); job != nil {
					pods["migrate"] = at(job, "spec", "template", "spec")
				}
				for name, pod := range pods {
					t.Run(name, func(t *testing.T) {
						cmd := exec.Command(bin, "admin", "config", "validate", "--config", cfgPath, "--format", "json")
						cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
						for n, e := range envByName(pod) {
							switch {
							case !strings.HasPrefix(n, "TROVE_"):
							case at(e, "value") != nil:
								cmd.Env = append(cmd.Env, n+"="+str(e, "value"))
							case n == "TROVE_RUNTIME_MEMORY_LIMIT_BYTES":
								cmd.Env = append(cmd.Env, n+"=1073741824")
							default: // downward API
								cmd.Env = append(cmd.Env, n+"=test")
							}
						}
						if name == "migrate" {
							// validate checks the rules of a serving replica, which want the bucket; the
							// Job itself needs no object store, so take the web pod's S3 settings.
							for n := range s3SettingEnv {
								if e := envByName(pods["web"])[n]; e != nil {
									cmd.Env = append(cmd.Env, n+"=test")
								}
							}
						}
						if out, err := cmd.CombinedOutput(); err != nil {
							t.Fatalf("trove admin config validate: %v\n%s", err, out)
						}
					})
				}
			})
		}
	})
}

// pod env as the binary sees it: literal values, the downward API as "test".
func podEnviron(pod any) []string {
	env := []string{"PATH=" + os.Getenv("PATH")}
	for n, e := range envByName(pod) {
		switch {
		case !strings.HasPrefix(n, "TROVE_"):
		case at(e, "value") != nil:
			env = append(env, n+"="+str(e, "value"))
		case n == "TROVE_RUNTIME_MEMORY_LIMIT_BYTES":
			env = append(env, n+"=1073741824")
		default:
			env = append(env, n+"=test")
		}
	}
	return env
}

func TestShutdownBudgetDerivation(t *testing.T) {
	// R-HA-011: the chart passes the grace period; the binary derives
	// server.shutdown_timeout = grace - health.shutdown_drain_delay - 5 s from it. This runs the
	// real binary over the rendered file and environment, so neither half can drift.
	cases := []struct {
		name    string
		sets    []string
		web     string
		worker  string
		sources string
	}{
		{"chart defaults: web 120 s / worker 300 s, drain 5 s", nil, "1m50s", "4m50s", "derived"},
		{"drain 15 s, worker 300 s", []string{"config.health.shutdown_drain_delay=15s"}, "1m40s", "4m40s", "derived"},
		{"web 45 s with drain 30 s leaves the 10 s the chart rule guarantees", []string{"web.terminationGracePeriodSeconds=45", "config.health.shutdown_drain_delay=30s"}, "10s", "4m25s", "derived"},
	}
	bin := trove(t)
	for _, c := range cases {
		t.Run("R-HA-011 the binary derives server.shutdown_timeout from the rendered grace period: "+c.name, func(t *testing.T) {
			_, docs := render(t, required(), c.sets...)
			cfgPath := filepath.Join(t.TempDir(), "trove.yaml")
			if err := os.WriteFile(cfgPath, []byte(str(find(docs, "ConfigMap", "trove-config"), "data", "trove.yaml")), 0o600); err != nil {
				t.Fatal(err)
			}
			for role, want := range map[string]string{"web": c.web, "worker": c.worker} {
				pod := at(find(docs, "Deployment", "trove-"+role), "spec", "template", "spec")
				cmd := exec.Command(bin, "admin", "config", "validate", "--config", cfgPath, "--format", "json")
				cmd.Env = podEnviron(pod)
				out, err := cmd.Output()
				if err != nil {
					t.Fatalf("%s: trove admin config validate: %v", role, err)
				}
				var res struct {
					Values map[string]struct {
						Value  any    `json:"value"`
						Source string `json:"source"`
					} `json:"values"`
				}
				if err := json.Unmarshal(out, &res); err != nil {
					t.Fatalf("%s: %v\n%s", role, err, out)
				}
				got := res.Values["server.shutdown_timeout"]
				if got.Value != want || got.Source != c.sources {
					t.Errorf("%s: server.shutdown_timeout = %v (%s), want %s (%s)", role, got.Value, got.Source, want, c.sources)
				}
			}
		})
	}
}

func equalMaps(a, b any) bool {
	am, ok1 := a.(map[string]any)
	bm, ok2 := b.(map[string]any)
	if !ok1 || !ok2 || len(am) != len(bm) {
		return false
	}
	for k, v := range am {
		if bm[k] != v {
			return false
		}
	}
	return true
}

// portsOf returns the ports one ingress rule admits, and its from peers.
func portsOf(rule any) (ports []int, from []any, hasFrom bool) {
	for _, p := range list(rule, "ports") {
		if n, ok := at(p, "port").(int); ok {
			ports = append(ports, n)
		}
	}
	f, has := at(rule, "from").([]any)
	return ports, f, has
}

func TestNetworkPolicy(t *testing.T) {
	t.Run("R-HA-042 web admits 8080 from anywhere and 8081 only from the monitoring namespace, worker admits 8081 only", func(t *testing.T) {
		eachSet(t, func(t *testing.T, _ string, _ string, docs []doc) {
			web := find(docs, "NetworkPolicy", "trove-web")
			worker := find(docs, "NetworkPolicy", "trove-worker")
			if web == nil || worker == nil {
				t.Fatal("NetworkPolicy trove-web or trove-worker not rendered")
			}
			if got := list(web, "spec", "policyTypes"); len(got) != 1 || got[0] != "Ingress" {
				t.Errorf("web policyTypes = %v, want [Ingress] only (egress is not rendered)", got)
			}
			rules := list(web, "spec", "ingress")
			if len(rules) != 2 {
				t.Fatalf("web has %d ingress rules, want 2 (8080 and 8081)", len(rules))
			}
			p0, f0, has0 := portsOf(rules[0])
			if len(p0) != 1 || p0[0] != 8080 || has0 {
				t.Errorf("web rule 1 = ports %v from %v (has from: %v), want 8080 from anywhere", p0, f0, has0)
			}
			p1, f1, has1 := portsOf(rules[1])
			if len(p1) != 1 || p1[0] != 8081 || !has1 || len(f1) != 1 ||
				str(f1[0], "namespaceSelector", "matchLabels", "kubernetes.io/metadata.name") != "monitoring" {
				t.Errorf("web rule 2 = ports %v from %v, want 8081 from the monitoring namespace only", p1, f1)
			}
			wr := list(worker, "spec", "ingress")
			if len(wr) != 1 {
				t.Fatalf("worker has %d ingress rules, want 1", len(wr))
			}
			if p, _, has := portsOf(wr[0]); len(p) != 1 || p[0] != 8081 || !has {
				t.Errorf("worker rule = ports %v (has from: %v), want 8081 from named peers only", p, has)
			}
		})
	})

	t.Run("R-HA-042 every policy selects the pods of its component and no others", func(t *testing.T) {
		_, docs := render(t, required())
		for _, c := range []struct{ policy, kind, workload string }{
			{"trove-web", "Deployment", "trove-web"},
			{"trove-worker", "Deployment", "trove-worker"},
			{"trove-migrate", "Job", "trove-migrate"},
		} {
			sel, _ := at(find(docs, "NetworkPolicy", c.policy), "spec", "podSelector", "matchLabels").(map[string]any)
			labels, _ := at(find(docs, c.kind, c.workload), "spec", "template", "metadata", "labels").(map[string]any)
			if len(sel) == 0 {
				t.Errorf("%s selects every pod in the namespace", c.policy)
			}
			for k, v := range sel {
				if labels[k] != v {
					t.Errorf("%s selects %s=%v, but the pods of %s carry %v", c.policy, k, v, c.workload, labels[k])
				}
			}
		}
		// web and worker must not select each other's pods.
		web, _ := at(find(docs, "NetworkPolicy", "trove-web"), "spec", "podSelector", "matchLabels").(map[string]any)
		worker, _ := at(find(docs, "NetworkPolicy", "trove-worker"), "spec", "podSelector", "matchLabels").(map[string]any)
		if web["app.kubernetes.io/component"] == worker["app.kubernetes.io/component"] {
			t.Error("web and worker policies select the same component")
		}
	})

	t.Run("R-HA-042 the migration Job accepts no ingress at all", func(t *testing.T) {
		_, docs := render(t, required())
		p := find(docs, "NetworkPolicy", "trove-migrate")
		if p == nil {
			t.Fatal("NetworkPolicy trove-migrate not rendered")
		}
		if got := list(p, "spec", "policyTypes"); len(got) != 1 || got[0] != "Ingress" {
			t.Errorf("policyTypes = %v", got)
		}
		if r := list(p, "spec", "ingress"); len(r) != 0 {
			t.Errorf("migrate ingress = %v, want none", r)
		}
		if !strings.Contains(str(p, "metadata", "annotations", "helm.sh/hook"), "pre-install") {
			t.Error("the migrate policy is not a hook resource like the Job it guards")
		}
	})

	t.Run("R-HA-042 http.from, ops.from and probeCIDRs are honoured, and empty ops peers close 8081", func(t *testing.T) {
		// os.MkdirTemp: helm -f splits a path at commas, and t.TempDir carries the test name.
		dir, err := os.MkdirTemp("", "np-")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		vals := filepath.Join(dir, "np.yaml")
		if err := os.WriteFile(vals, []byte(`networkPolicy:
  ingress:
    http:
      from:
        - namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: traefik}}
    ops:
      from:
        - podSelector: {matchLabels: {app: prometheus}}
  probeCIDRs: ["10.0.0.0/16"]
`), 0o600); err != nil {
			t.Fatal(err)
		}
		_, docs := render(t, append(required(), vals))
		rules := list(find(docs, "NetworkPolicy", "trove-web"), "spec", "ingress")
		_, f0, has0 := portsOf(rules[0])
		if !has0 || len(f0) != 1 || str(f0[0], "namespaceSelector", "matchLabels", "kubernetes.io/metadata.name") != "traefik" {
			t.Errorf("8080 from = %v, want the traefik namespace", f0)
		}
		_, f1, _ := portsOf(rules[1])
		if len(f1) != 2 || str(f1[0], "podSelector", "matchLabels", "app") != "prometheus" || str(f1[1], "ipBlock", "cidr") != "10.0.0.0/16" {
			t.Errorf("8081 from = %v, want the prometheus pods and the probe CIDR", f1)
		}

		_, docs = render(t, required(), "networkPolicy.ingress.ops.from=null")
		for _, role := range []string{"web", "worker"} {
			for _, r := range list(find(docs, "NetworkPolicy", "trove-"+role), "spec", "ingress") {
				if p, _, _ := portsOf(r); len(p) == 1 && p[0] == 8081 {
					t.Errorf("%s still admits 8081 although no peer is listed; an empty from would mean from anywhere", role)
				}
			}
		}
	})

	t.Run("R-HA-042 networkPolicy.enabled=false renders no policy", func(t *testing.T) {
		_, docs := render(t, required(), "networkPolicy.enabled=false")
		if n := len(ofKind(docs, "NetworkPolicy")); n != 0 {
			t.Errorf("%d NetworkPolicy objects rendered with the policy disabled", n)
		}
	})
}

func TestMigrationJobSecrets(t *testing.T) {
	t.Run("R-HA-030 the migration Job mounts no encryption key ring, the replicas do", func(t *testing.T) {
		eachSet(t, func(t *testing.T, _ string, out string, docs []doc) {
			job := find(docs, "Job", "trove-migrate")
			if job == nil {
				return // migrations.mode disabled
			}
			pod := at(job, "spec", "template", "spec")
			for _, v := range list(pod, "volumes") {
				if strings.Contains(str(v, "name"), "encryption") || str(v, "secret", "secretName") != "" && strings.Contains(str(v, "secret", "secretName"), "encryption") {
					t.Errorf("the migration Job mounts the key ring volume %q", str(v, "name"))
				}
			}
			for _, m := range list(pod, "containers", "0", "volumeMounts") {
				if strings.Contains(str(m, "mountPath"), "/encryption") {
					t.Errorf("the migration Job mounts %s", str(m, "mountPath"))
				}
			}
			for _, role := range []string{"web", "worker"} {
				found := false
				for _, m := range list(at(find(docs, "Deployment", "trove-"+role), "spec", "template", "spec"), "containers", "0", "volumeMounts") {
					found = found || str(m, "mountPath") == "/var/run/secrets/trove/encryption"
				}
				if !found {
					t.Errorf("trove-%s lost its key ring mount", role)
				}
			}
		})
	})
}

func TestTrustedProxiesExample(t *testing.T) {
	t.Run("R-ARCH-006 the values example names edge addresses, never a pod CIDR, and the chart ships no trusted proxy by default", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(chartDir, "values.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if !strings.Contains(line, "trusted_proxies:") {
				continue
			}
			if strings.Contains(line, "10.244.0.0/16") || strings.Contains(line, "/0") {
				t.Errorf("trusted_proxies example is a pod-wide range: %s", strings.TrimSpace(line))
			}
		}
		var vals doc
		if err := yaml.Unmarshal(raw, &vals); err != nil {
			t.Fatal(err)
		}
		if got := list(vals, "config", "server", "trusted_proxies"); len(got) != 0 {
			t.Errorf("default trusted_proxies = %v, want none", got)
		}
		if str(vals, "config", "server", "forwarded_header") != "x-forwarded-for" {
			t.Errorf("default forwarded_header = %q", str(vals, "config", "server", "forwarded_header"))
		}
	})
}
