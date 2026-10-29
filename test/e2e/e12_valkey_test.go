//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// valkeyCLI runs valkey-cli as the default user inside a Valkey pod, over TLS (port 16379). The password
// is read in the pod from the environment the Valkey operator sets from the Secret.
func valkeyCLI(t testing.TB, podName string, args ...string) string {
	t.Helper()
	cmd := append([]string{
		"-n", cfg.namespace, "exec", podName, "-c", "valkey", "--", "sh", "-c",
		`exec valkey-cli --tls --cacert /tls/ca.crt -h 127.0.0.1 -p 16379 --no-auth-warning -a "$VALKEY_PASSWORD" "$@"`, "sh",
	}, args...)
	out, err := kubectl(cmd...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func valkeyPrimaryPod(t testing.TB) string {
	t.Helper()
	for _, p := range []string{"trove-valkey-0", "trove-valkey-1"} {
		if strings.HasPrefix(valkeyCLI(t, p, "role"), "master") {
			return p
		}
	}
	t.Fatal("no Valkey pod reports role master")
	return ""
}

// clonedReplica starts a pod from the web Deployment's template with a mutation applied: a
// replica that joins the release, for the start-up cases of E0.12. The pod keeps the readiness
// probe, so its Ready condition is the answer of /readyz.
func clonedReplica(t *testing.T, name string, mutate func(spec map[string]any)) {
	t.Helper()
	raw := kn(t, "get", "deployment", "trove-web", "-o", "json")
	var d map[string]any
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		t.Fatal(err)
	}
	tmpl := d["spec"].(map[string]any)["template"].(map[string]any)
	spec := tmpl["spec"].(map[string]any)
	delete(spec, "topologySpreadConstraints")
	spec["terminationGracePeriodSeconds"] = 20
	labels := tmpl["metadata"].(map[string]any)["labels"].(map[string]any)
	labels["app.kubernetes.io/component"] = "e2e-replica" // not selected by the Services or the PodDisruptionBudgets
	mutate(spec)
	p := map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": name, "namespace": cfg.namespace, "labels": labels}, "spec": spec}
	b, _ := json.Marshal(p)
	f := filepath.Join(cfg.out, name+".json")
	if err := os.WriteFile(f, b, 0o600); err != nil {
		t.Fatal(err)
	}
	kn(t, "apply", "-f", f)
	t.Cleanup(func() { _, _ = kubectl("-n", cfg.namespace, "delete", "pod", name, "--grace-period=1", "--wait=false") })
}

func addEnv(spec map[string]any, name, value string) {
	c := spec["containers"].([]any)[0].(map[string]any)
	env, _ := c["env"].([]any)
	c["env"] = append(env, map[string]any{"name": name, "value": value})
}

// checkClientList is the connection check of E0.12: CLIENT LIST on the Valkey primary shows connections
// named trove:<pod>:<class> from every web and worker pod, over TLS only, at most six per replica.
func checkClientList(t *testing.T) {
	t.Helper()
	primary := valkeyPrimaryPod(t)
	if got := valkeyCLI(t, primary, "config", "get", "port"); !strings.Contains(got, "\n0") {
		t.Errorf("the plaintext port of %s is not 0: %q", primary, got)
	}
	tlsPort := valkeyCLI(t, primary, "config", "get", "tls-port")
	t.Logf("%s: tls-port %s", primary, strings.ReplaceAll(strings.TrimSpace(tlsPort), "\n", " "))
	want := map[string]bool{}
	for _, p := range append(livePods(t, selWeb), livePods(t, selWorker)...) {
		want[p.Name] = true
	}
	names := regexp.MustCompile(`name=trove:([^: ]+):(\S+)`)
	per := map[string]map[string]int{}
	for _, line := range strings.Split(valkeyCLI(t, primary, "client", "list"), "\n") {
		if m := names.FindStringSubmatch(line); m != nil {
			if per[m[1]] == nil {
				per[m[1]] = map[string]int{}
			}
			per[m[1]][m[2]]++
		}
	}
	t.Logf("connections per replica and class: %v", per)
	for pod := range want {
		n := 0
		for _, c := range per[pod] {
			n += c
		}
		if n == 0 {
			t.Errorf("no connection named trove:%s:… on %s", pod, primary)
		}
		if n > 6 {
			t.Errorf("%s holds %d connections, at most 6 allowed (R-ARCH-031)", pod, n)
		}
	}
	// A plaintext connection never gets an answer: the server has no plaintext port (port 0), and the TLS port
	// refuses a client that does not speak TLS.
	out, _ := kubectl("-n", cfg.namespace, "exec", primary, "-c", "valkey", "--", "sh", "-c", "valkey-cli -h 127.0.0.1 -p 16379 ping 2>&1 | head -1")
	if strings.TrimSpace(out) == "PONG" {
		t.Errorf("plaintext PING was answered on the TLS port 16379")
	}
}

func TestE12Valkey(t *testing.T) {
	t.Run("R-ARCH-031 R-ARCH-019 E0.12 CLIENT LIST on the primary shows trove:<pod>:… from every pod, TLS only, at most six per replica", checkClientList)

	t.Run("R-HA-056 Q-HA-13 E0.12 a new replica with a wrong primary_name stays not ready and logs the cause", func(t *testing.T) {
		clonedReplica(t, "trove-e2e-wrong-primary", func(spec map[string]any) { addEnv(spec, "TROVE_VALKEY_SENTINEL_PRIMARY_NAME", "wrong-primary") })
		expectStuckWithCause(t, "trove-e2e-wrong-primary")
	})

	t.Run("R-HA-056 Q-HA-13 E0.12 a new replica with a wrong password stays not ready and logs the cause", func(t *testing.T) {
		kn(t, "delete", "secret", "trove-e2e-valkey-wrong", "--ignore-not-found")
		kn(t, "create", "secret", "generic", "trove-e2e-valkey-wrong", "--from-literal=password=not-the-password")
		t.Cleanup(func() { _, _ = kubectl("-n", cfg.namespace, "delete", "secret", "trove-e2e-valkey-wrong") })
		clonedReplica(t, "trove-e2e-wrong-password", func(spec map[string]any) {
			for _, v := range spec["volumes"].([]any) {
				vol := v.(map[string]any)
				if vol["name"] == "secret-valkey" {
					vol["secret"].(map[string]any)["secretName"] = "trove-e2e-valkey-wrong"
				}
			}
		})
		expectStuckWithCause(t, "trove-e2e-wrong-password")
	})

	t.Run("R-HA-056 Q-HA-13 E0.12 with every Sentinel unreachable at its first start a new replica becomes ready, degraded valkey", func(t *testing.T) {
		clonedReplica(t, "trove-e2e-no-sentinel", func(spec map[string]any) {
			addEnv(spec, "TROVE_VALKEY_SENTINEL_ADDRESSES", `["127.0.0.1:36379"]`)
		})
		var body string
		took := waitFor(t, "the replica to become ready", 3*time.Minute, 2*time.Second, func() bool {
			code, b := ops("trove-e2e-no-sentinel", "/readyz?verbose=1")
			body = b
			return code == 200
		})
		var r struct {
			Status   string   `json:"status"`
			Degraded []string `json:"degraded"`
		}
		if err := json.Unmarshal([]byte(body), &r); err != nil {
			t.Fatalf("readyz body %q: %v", body, err)
		}
		if len(r.Degraded) != 1 || r.Degraded[0] != "valkey" {
			t.Errorf(`readyz degraded = %v, want ["valkey"]; body %s`, r.Degraded, strings.TrimSpace(body))
		}
		t.Logf("ready after %s: %s", took.Round(time.Second), strings.TrimSpace(body))
	})
}

// expectStuckWithCause waits for the replica to start, shows that it does not become ready for
// 45 s while /readyz names valkey as failing, and finds the cause in its log.
func expectStuckWithCause(t *testing.T, name string) {
	t.Helper()
	waitFor(t, name+" to run", 2*time.Minute, 2*time.Second, func() bool {
		code, _ := ops(name, "/healthz")
		return code == 200
	})
	deadline := time.Now().Add(45 * time.Second)
	var body string
	for time.Now().Before(deadline) {
		code, b := ops(name, "/readyz?verbose=1")
		body = b
		if code == 200 {
			t.Fatalf("%s became ready although valkey is misconfigured: %s", name, b)
		}
		time.Sleep(3 * time.Second)
	}
	if !strings.Contains(body, "valkey") {
		t.Errorf("/readyz of %s does not name valkey: %s", name, strings.TrimSpace(body))
	}
	logs := kn(t, "logs", name)
	_ = os.WriteFile(filepath.Join(cfg.out, name+".log"), []byte(logs), 0o644)
	var cause []string
	for _, l := range strings.Split(logs, "\n") {
		low := strings.ToLower(l)
		if strings.Contains(low, "valkey") && (strings.Contains(low, `"level":"error"`) || strings.Contains(low, `"level":"warn"`)) {
			cause = append(cause, l)
		}
	}
	if len(cause) == 0 {
		t.Errorf("the log of %s names no valkey warning or error", name)
	} else {
		t.Logf("%s readyz: %s", name, strings.TrimSpace(body))
		t.Logf("%s cause in the log (%d lines), first: %s", name, len(cause), cause[0])
	}
	_ = fmt.Sprint()
}
