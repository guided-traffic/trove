//go:build e2e

package e2e

// The main installation unreachable (R-BAS-027, R-BAS-049): a NetworkPolicy of the test cuts the Bastion off from the main
// installation, with the short periods of deploy/dev/bastion-chart-common.yaml (answers.period 30 s, grace_period 90 s).

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// basCutMain cuts the Bastion of site s off from the main installation and returns the function that ends the cut. The chart
// renders no egress rule (R-BAS-047, ADR-154), so the cut is an egress policy of the test's own on the release's pods: it admits
// what the Bastion uses besides the main installation - DNS, the Bastion's Valkey in its namespace (deploy/dev/bastion.yaml, the
// Valkey operator's TLS ports) and the bucket in the SeaweedFS of the main namespace - and nothing else. Ending the cut deletes it.
func basCutMain(t testing.TB, s *basSite) func() {
	t.Helper()
	name := "e2e-cut-main-" + s.variant
	tcp := func(ports ...int) []any {
		out := make([]any, 0, len(ports))
		for _, p := range ports {
			out = append(out, map[string]any{"protocol": "TCP", "port": p})
		}
		return out
	}
	labels := func(kv ...string) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return map[string]any{"matchLabels": m}
	}
	manifest, err := json.Marshal(map[string]any{
		"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy",
		"metadata": map[string]any{"name": name, "namespace": basNS(), "labels": map[string]string{"app.kubernetes.io/part-of": "trove-e2e"}},
		"spec": map[string]any{
			"podSelector": labels("app.kubernetes.io/instance", s.release, "app.kubernetes.io/component", "bastion"),
			"policyTypes": []string{"Egress"},
			"egress": []any{
				map[string]any{
					"to":    []any{map[string]any{"namespaceSelector": labels("kubernetes.io/metadata.name", "kube-system"), "podSelector": labels("k8s-app", "kube-dns")}},
					"ports": append(tcp(53), map[string]any{"protocol": "UDP", "port": 53}),
				},
				map[string]any{
					"to":    []any{map[string]any{"podSelector": labels("app.kubernetes.io/instance", "bastion-valkey")}},
					"ports": tcp(16379, 36379),
				},
				map[string]any{
					"to":    []any{map[string]any{"namespaceSelector": labels("kubernetes.io/metadata.name", cfg.namespace), "podSelector": labels("app.kubernetes.io/name", "trove-s3")}},
					"ports": tcp(9000),
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("kubectl", "--context", cfg.kubeContext, "apply", "-f", "-")
	cmd.Stdin = bytes.NewReader(manifest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("apply the NetworkPolicy %s: %v: %s", name, err, out)
	}
	ended := false
	end := func() {
		if ended {
			return
		}
		ended = true
		kb(t, "delete", "networkpolicy", name, "--ignore-not-found")
	}
	t.Cleanup(end)
	return end
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

// reachableGauge reads trove_bastion_main_reachable of every ready replica.
func basReachable(t testing.TB, s *basSite) []float64 {
	t.Helper()
	var out []float64
	for _, p := range basReadyPods(t, s) {
		out = append(out, basMetrics(t, p.Name)["trove_bastion_main_reachable"])
	}
	return out
}

func allEqual(v []float64, want float64) bool {
	for _, x := range v {
		if x != want {
			return false
		}
	}
	return len(v) > 0
}

// TestBastion_R_BAS_049_MainUnreachable: the cut; warm runs succeed for the grace period, a cold package and an unseen token
// get 503, everything 503 after period + grace_period; the gauges the alert TroveBastionMainUnreachable reads stay 0 for its
// `for: 2m` (there is no Prometheus in the profile, Q-BAS-112); after the cut ends both replicas return to reachable.
func TestBastion_R_BAS_049_MainUnreachable(t *testing.T) {
	env := basFx(t)
	site := env.bkt
	setN, setD := env.npmSets["npm"], env.nuget
	c := env.npmClient(t, "npm", 0, "cut", env.full.Token)
	dir := env.basNPMProject(t, c, setN, "cut", site)
	nc := env.nugetClient(t, 0, "cut", env.full.Token)
	env.configureNuGet(t, nc, site.reg)
	project := setD.project(nc, "consumer")

	unseen := newSATokenWaiting(t, env.adm, basFullSA, 3600, map[string]any{"inherit": true}).Token
	cold := "bas" + env.run + "-cold-package"
	env.publishNPM(t, basNPMRepo, cold, "1.0.0", nil)

	// answers obtained now are the ones the grace period extends: let the earlier ones expire, then use the token
	time.Sleep(basAnswerPeriod + 3*time.Second)
	c.basInstall(t, dir, setN)
	setD.restore(t, nc, project)
	obtained := time.Now()

	endCut := basCutMain(t, site)
	cutAt := time.Now()
	// A NetworkPolicy stops new connections only: the replicas keep the connection to the main installation they hold. The api
	// and packages pods of the main installation are deleted, which ends those connections, and the connections the replicas then open are
	// the new ones that the policy drops. (A replica that was restarted instead could not use its answers: it has no feed
	// history, R-BAS-026.)
	kn(t, "delete", "pod", "-l", selServing, "--wait=false", "--grace-period=1")
	waitFor(t, "trove_bastion_main_reachable 0 on both replicas", 60*time.Second, time.Second, func() bool { return allEqual(basReachable(t, site), 0) })
	t.Logf("both replicas unreachable %s after the cut", time.Since(cutAt).Round(time.Second))
	for _, p := range basReadyPods(t, site) {
		if m := basMetrics(t, p.Name); m["trove_bastion_main_unreachable_since_timestamp_seconds"] <= 0 {
			t.Errorf("%s: trove_bastion_main_unreachable_since_timestamp_seconds = %v", p.Name, m["trove_bastion_main_unreachable_since_timestamp_seconds"])
		}
	}

	t.Run("warm runs succeed for the grace period", func(t *testing.T) {
		c.basInstall(t, dir, setN)
		setD.restore(t, nc, project)
	})
	t.Run("a cold package and an unseen token get 503", func(t *testing.T) {
		r := site.get(t, env.full.Token, npmDocPath(cold))
		if r.Status != 503 || r.Code() != "main_unreachable" || r.Header.Get("Retry-After") != "30" {
			t.Errorf("a cold package: %s (Retry-After %q), want 503 main_unreachable with Retry-After 30", r, r.Header.Get("Retry-After"))
		}
		r = site.get(t, unseen, npmDocPath(env.open))
		if r.Status != 503 || r.Code() != "main_unreachable" {
			t.Errorf("an unseen token: %s, want 503 main_unreachable", r)
		}
	})

	// everything 503 once period + grace_period have passed since the last answer was obtained
	until := obtained.Add(basAnswerPeriod + basGrace + 15*time.Second)
	t.Logf("waiting until %s after the last answer for the grace to end", time.Until(until).Round(time.Second))
	time.Sleep(time.Until(until))
	t.Run("everything is 503 after period + grace_period", func(t *testing.T) {
		for _, p := range []string{npmDocPath(env.open), npmDocPath(setN.root) + "/-/" + setN.root + "-1.0.0.tgz"} {
			r := site.get(t, env.full.Token, p)
			if r.Status != 503 {
				t.Errorf("GET %s after the grace period: %s, want 503", p, r)
			}
		}
		c.wipeClientState(dir) // the client cache would serve the packages
		r := c.run(dir, "ci")
		// the 503 is asserted above on the requests themselves; npm retries a 503 and, after its retries, may report the last attempt's
		// reset instead (seen once: ECONNRESET), so the client's verdict is that the install failed
		if r.Code == 0 {
			t.Errorf("npm ci after the grace period succeeded, want a failure:\n%s", tailLines(r.Out, 10))
		}
		t.Logf("npm ci after the grace period: exit %d: %s", r.Code, strings.ReplaceAll(tailLines(r.Out, 3), "\n", " | "))
	})

	// TroveBastionMainUnreachable: max(trove_bastion_main_reachable) == 0 for 2 minutes
	for time.Since(cutAt) < 2*time.Minute+10*time.Second {
		if !allEqual(basReachable(t, site), 0) {
			t.Fatalf("a replica was reachable again before the cut ended: %v", basReachable(t, site))
		}
		time.Sleep(5 * time.Second)
	}
	t.Logf("trove_bastion_main_reachable stayed 0 on every replica for %s: TroveBastionMainUnreachable's expression holds for its `for`", time.Since(cutAt).Round(time.Second))
	for _, p := range basReadyPods(t, site) {
		m := basMetrics(t, p.Name)
		if m["trove_bastion_grace_period_seconds"] != basGrace.Seconds() || m["trove_bastion_answer_period_seconds"] != basAnswerPeriod.Seconds() {
			t.Errorf("%s: grace %v period %v", p.Name, m["trove_bastion_grace_period_seconds"], m["trove_bastion_answer_period_seconds"])
		}
	}

	endCut()
	waitServingSettled(t)
	back := time.Now()
	waitFor(t, "both replicas reachable again", 40*time.Second, time.Second, func() bool { return allEqual(basReachable(t, site), 1) })
	t.Logf("both replicas reachable %s after the cut ended", time.Since(back).Round(time.Second))
	t.Run("after the policy is removed the Bastion serves and reports its buffered download events", func(t *testing.T) {
		c.basInstall(t, dir, setN)
		setD.restore(t, nc, project)
		site.get(t, env.full.Token, npmDocPath(cold)).want(t, "the cold package after the recovery", 200)
		waitFor(t, "the audit buffers to be empty", 60*time.Second, 2*time.Second, func() bool { return basSum(t, site, "trove_bastion_audit_buffer_events") == 0 })
	})
}
