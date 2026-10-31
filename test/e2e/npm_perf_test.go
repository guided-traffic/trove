//go:build e2e

package e2e

// The load scripts of R-ARCH-027 against npm (WP2.8): perf/k6/npm.js runs against one web replica from inside the
// cluster and reports the P-targets an npm client meets. The dev profile is not the reference environment of
// R-ARCH-027, so the numbers are reported and not gated here; the gate is the run on the reference environment in
// roadmap E5.3, with GATE=1 (Q-ROAD-9). What fails this test is a script that does not run, or a request that is not
// answered with 200.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const perfSA = "e2e-npm-perf"

type perfScenario struct {
	OfferedPerS float64 `json:"offered_per_s"`
	ServedPerS  float64 `json:"served_per_s"`
	Failed      float64 `json:"failed"`
	P50MS       float64 `json:"p50_ms"`
	P99MS       float64 `json:"p99_ms"`
	MaxMS       float64 `json:"max_ms"`
}

func TestNPMPerf(t *testing.T) {
	h := npmHAFx(t)
	t.Run("R-ARCH-027 P-1 P-3 P-5 P-7 P-8 the npm load scripts of perf/k6 against one web replica from inside the cluster: every request answered 200, the targets reported (gated in E5.3 on the reference environment)", func(t *testing.T) {
		adm := h.fx.adm
		ensureSA(t, adm, perfSA)
		ensurePolicy(t, adm, perfSA, []string{npmLocalKey, npmRemoteKey, npmAllKey}, [2]any{"sa:" + perfSA, []string{"read"}})
		// R-API-016's package_read budget is per token: twenty tokens carry P-7's 2,000 requests/s
		tokens := pacedSATokens(t, adm, perfSA, 20, 3600)
		local := func(p string) string {
			return strings.Replace(p, "/api/npm/"+npmAllKey+"/", "/api/npm/"+npmLocalKey+"/", 1)
		}
		var localDocs, virtualDocs []string
		for _, name := range append(append([]string{}, h.locals...), h.blobPkg, h.smallPkg) {
			localDocs = append(localDocs, "/api/npm/"+npmLocalKey+"/"+pathEscapeName(name))
		}
		for _, name := range h.remoteRoots {
			virtualDocs = append(virtualDocs, "/api/npm/"+npmAllKey+"/"+pathEscapeName(name))
		}
		fixture, _ := json.Marshal(map[string]any{
			"tarball_1mib": local(h.blob1MiB), "tarball_50kib": local(h.small50KiB),
			"local_documents": localDocs, "virtual_documents": virtualDocs,
		})
		// every path answers before the load, so a failure in the load is the load's
		probe := adm.withToken(tokens[0])
		waitFor(t, "the perf token to read every path on every replica", 30*time.Second, 500*time.Millisecond, func() bool {
			for _, p := range append(append([]string{local(h.blob1MiB), local(h.small50KiB)}, localDocs...), virtualDocs...) {
				if r := probe.get(p); r.Status != http.StatusOK {
					return false
				}
			}
			return true
		})

		web := webReplicas(t, 1)[0]
		dir := filepath.Join(cfg.out, "perf-npm")
		_ = os.RemoveAll(dir)
		if err := os.MkdirAll(dir, 0o777); err != nil {
			t.Fatal(err)
		}
		_ = os.Chmod(dir, 0o777)
		if err := os.WriteFile(filepath.Join(dir, "tokens"), []byte(strings.Join(tokens, "\n")+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "perf-fixture.json"), fixture, 0o644); err != nil {
			t.Fatal(err)
		}
		// The k6 container joins the network namespace of the control-plane node, which routes the pod network, so it
		// reaches the pod's listener without the edge; the pod's certificate names localhost.
		node := strings.TrimPrefix(cfg.kubeContext, "kind-") + "-control-plane"
		name := fmt.Sprintf("trove-e2e-k6-perf-%d", os.Getpid())
		args := []string{
			"run", "--rm", "--name", name, "--network", "container:" + node,
			"--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
			"-v", filepath.Join(cfg.repo, "perf/k6") + ":/scripts:ro", "-v", dir + ":/out",
			"-e", "TARGET=https://localhost:8080", "-e", "HOST=localhost", "-e", "HOSTS_ADDR=" + web.IP + ":8080",
		}
		if cfg.caFile != "" {
			args = append(args, "-v", cfg.caFile+":/ca/ca.crt:ro", "-e", "SSL_CERT_FILE=/ca/ca.crt")
		}
		args = append(args, cfg.k6Image, "run", "--quiet", "--no-usage-report", "/scripts/npm.js")
		t.Cleanup(func() { _ = exec.Command("docker", "kill", name).Run() })
		start := time.Now()
		out, err := exec.Command("docker", args...).CombinedOutput()
		_ = os.WriteFile(filepath.Join(dir, "console.log"), out, 0o644)
		if err != nil {
			t.Fatalf("k6 perf/k6/npm.js against %s: %v\n%s", web.Name, err, tailLines(string(out), 30))
		}
		raw, err := os.ReadFile(filepath.Join(dir, "perf-npm.json"))
		if err != nil {
			t.Fatalf("k6 wrote no perf-npm.json: %v\n%s", err, tailLines(string(out), 30))
		}
		var res struct {
			Dropped   float64                 `json:"dropped_iterations"`
			Scenarios map[string]perfScenario `json:"scenarios"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			t.Fatal(err)
		}
		t.Logf("R-ARCH-027 against %s on the dev profile (%s, not the reference environment, not gated), dropped iterations %v:", web.Name, time.Since(start).Round(time.Second), res.Dropped)
		for _, id := range []string{"p1", "p3", "p5", "p7", "p8"} {
			s, ok := res.Scenarios[id]
			if !ok {
				t.Errorf("scenario %s is missing from the result", id)
				continue
			}
			t.Logf("  %s: offered %.0f/s, served %.0f/s, p50 %.1f ms, p99 %.1f ms, max %.1f ms, failed %.0f", id, s.OfferedPerS, s.ServedPerS, s.P50MS, s.P99MS, s.MaxMS, s.Failed)
			if s.Failed != 0 {
				t.Errorf("%s: %.0f requests were not answered with 200", id, s.Failed)
			}
			if s.ServedPerS == 0 {
				t.Errorf("%s: no request was served", id)
			}
		}
	})
}

// pathEscapeName is the escaped form npm sends for a package name: the slash of a scope as %2F.
func pathEscapeName(name string) string { return strings.Replace(name, "/", "%2F", 1) }
