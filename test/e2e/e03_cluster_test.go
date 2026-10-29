//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// checkReleaseServes is the part of E0.3 that also holds in the scatter profile (E0.11): every
// trove pod answers 200 on :8081/readyz, the registry lists the replicas of both roles and the
// Ingress answers ping with OK.
func checkReleaseServes(t *testing.T, wantWeb, wantWorker int) {
	t.Helper()
	webs, workers := livePods(t, selWeb), livePods(t, selWorker)
	if len(webs) != wantWeb || len(workers) != wantWorker {
		t.Fatalf("%d web and %d worker pods, want %d and %d", len(webs), len(workers), wantWeb, wantWorker)
	}
	for _, p := range append(append([]pod{}, webs...), workers...) {
		if !p.Ready {
			t.Errorf("pod %s is not Ready", p.Name)
		}
		if code, body := ops(p.Name, "/readyz"); code != 200 {
			t.Errorf("%s :8081/readyz = %d %s", p.Name, code, strings.TrimSpace(body))
		}
	}
	var rs []replica
	waitFor(t, "the registry to list every replica", 90*time.Second, 2*time.Second, func() bool {
		var err error
		rs, err = replicas()
		if err != nil {
			return false
		}
		web, worker := 0, 0
		for _, r := range rs {
			switch {
			case r.Lost:
			case r.role() == "web":
				web++
			case r.role() == "worker":
				worker++
			}
		}
		return web == wantWeb && worker == wantWorker
	})
	names := map[string]string{}
	for _, r := range rs {
		if !r.Lost {
			names[r.Name] = r.role()
		}
	}
	for _, p := range webs {
		if names[p.Name] != "web" {
			t.Errorf("web pod %s is not listed as role web (%q)", p.Name, names[p.Name])
		}
	}
	for _, p := range workers {
		if names[p.Name] != "worker" {
			t.Errorf("worker pod %s is not listed as role worker (%q)", p.Name, names[p.Name])
		}
	}
	code, _, body, err := edge("/api/v1/system/ping")
	if err != nil || code != 200 || strings.TrimSpace(body) != "OK" {
		t.Errorf("ping through the Ingress: %d %q %v", code, body, err)
	}
}

func TestE03DevProfile(t *testing.T) {
	t.Run("R-HA-051 R-HA-002 E0.3 two web and two worker replicas ready, listed by the registry, ping OK through the Ingress", func(t *testing.T) {
		checkReleaseServes(t, 2, 2)
	})
	t.Run("R-HA-051 E0.3 the dependencies are up: two PostgreSQL instances, two Valkey pods, three Sentinels", func(t *testing.T) {
		if got := strings.TrimSpace(kn(t, "get", "cluster", "trove-db", "-o", "jsonpath={.status.readyInstances}")); got != "2" {
			t.Errorf("trove-db has %s ready instances, want 2", got)
		}
		for sts, want := range map[string]string{"trove-valkey": "2", "trove-valkey-sentinel": "3"} {
			if got := strings.TrimSpace(kn(t, "get", "statefulset", sts, "-o", "jsonpath={.status.readyReplicas}")); got != want {
				t.Errorf("%s has %s ready pods, want %s", sts, got, want)
			}
		}
	})
}

func TestE07PodSecurity(t *testing.T) {
	t.Run("R-HA-033 E0.7 the namespace enforces the restricted Pod Security Standard and the trove pods run in it", func(t *testing.T) {
		got := strings.TrimSpace(kn(t, "get", "namespace", cfg.namespace, "-o", "jsonpath={.metadata.labels.pod-security\\.kubernetes\\.io/enforce}"))
		if got != "restricted" {
			t.Fatalf("pod-security.kubernetes.io/enforce = %q, want restricted", got)
		}
		for _, p := range livePods(t, selTrove) {
			if p.Phase != "Running" {
				t.Errorf("pod %s is %s", p.Name, p.Phase)
			}
		}
		// The enforcement is live: the API server refuses a privileged pod in this namespace.
		_, err := kubectl("-n", cfg.namespace, "run", "e2e-pss-probe", "--image=pss-probe-not-pulled", "--restart=Never", "--dry-run=server",
			`--overrides={"spec":{"containers":[{"name":"x","image":"pss-probe-not-pulled","securityContext":{"privileged":true}}]}}`)
		if err == nil || !strings.Contains(err.Error(), "PodSecurity") {
			t.Errorf("a privileged pod was not refused by Pod Security admission: %v", err)
		}
	})
}

// TestE07NetworkPolicy shows that the chart's NetworkPolicy (R-HA-042) is enforced by the cluster's
// CNI, not only rendered: a pod of another namespace reaches the public port of a web pod and not
// its ops port. (Not in the exit criteria; the dev profile has to prove that the policy it relies on
// for the probes and the proxy works the way the chart says.)
func TestE07NetworkPolicy(t *testing.T) {
	t.Run("R-HA-042 E0.7 the NetworkPolicy is enforced: a pod of another namespace reaches :8080 of a web pod and not :8081", func(t *testing.T) {
		image := os.Getenv("TROVE_E2E_OPENSSL_IMAGE")
		if image == "" {
			t.Fatal("TROVE_E2E_OPENSSL_IMAGE is not set; run the suite through deploy/dev/e2e.sh")
		}
		web := livePods(t, selWeb)[0]
		ip := strings.TrimSpace(kn(t, "get", "pod", web.Name, "-o", "jsonpath={.status.podIP}"))
		name := "e2e-netpol-probe"
		_, _ = kubectl("-n", "default", "delete", "pod", name, "--ignore-not-found", "--wait=true")
		t.Cleanup(func() { _, _ = kubectl("-n", "default", "delete", "pod", name, "--ignore-not-found", "--wait=false") })
		// Both ports speak TLS (tls.enabled): the probe completes a handshake and reads the status line, so a
		// REACHED means a TLS answer from trove and a BLOCKED a connection the policy dropped. The Host is the default
		// endpoint's (the chart gives it a host check as soon as a further endpoint exists).
		script := fmt.Sprintf(`get() { printf 'GET %%s HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n' "$2" | timeout 6 openssl s_client -quiet -connect %[1]s:"$1" 2>/dev/null | head -1; }; `+
			`case "$(get 8081 /readyz)" in HTTP/*" 200"*) echo OPS_REACHED ;; *) echo OPS_BLOCKED ;; esac; `+
			`case "$(get 8080 /api/v1/system/ping)" in HTTP/*" 200"*) echo HTTP_REACHED ;; *) echo HTTP_BLOCKED ;; esac`, ip)
		if _, err := kubectl("-n", "default", "run", name, "--image="+image, "--restart=Never", "--command", "--", "sh", "-c", script); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the probe pod to finish", 3*time.Minute, 2*time.Second, func() bool {
			ph, _ := kubectl("-n", "default", "get", "pod", name, "-o", "jsonpath={.status.phase}")
			return ph == "Succeeded" || ph == "Failed"
		})
		out, _ := kubectl("-n", "default", "logs", name)
		t.Logf("from namespace default to %s (%s): %s", web.Name, ip, strings.ReplaceAll(strings.TrimSpace(out), "\n", " "))
		if !strings.Contains(out, "OPS_BLOCKED") {
			t.Errorf("the ops port of a web pod is reachable from another namespace: the CNI does not enforce the NetworkPolicy")
		}
		if !strings.Contains(out, "HTTP_REACHED") {
			t.Errorf("the public port of a web pod is not reachable from another namespace: %q", out)
		}
	})
}
