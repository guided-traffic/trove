//go:build e2e

package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestE99Scatter is E0.11 and runs last, because it switches the cluster to the scatter profile:
// three web replicas and an edge that keeps no connection to a pod, so every request is balanced
// round robin on its own (deploy/dev/values-scatter.yaml, HAProxy Ingress balance-algorithm roundrobin and option http-server-close on trove's Ingress).
// M0's end-to-end checks must pass in it: the E0.3 checks, a load of 100 requests/s that
// records only 200, and the proof that requests really scatter. It returns the cluster to the default
// profile afterwards (TROVE_E2E_SCATTER_RETURN=0 keeps the profile).
func TestE99Scatter(t *testing.T) {
	dev := os.Getenv("TROVE_E2E_DEV_DIR")
	if dev == "" {
		t.Fatal("TROVE_E2E_DEV_DIR is not set; run the suite through deploy/dev/e2e.sh")
	}
	upScript := filepath.Join(dev, "up.sh")
	switchProfile := func(t *testing.T, profile string) {
		t.Helper()
		cmd := exec.Command(upScript)
		cmd.Dir = cfg.repo
		// The cluster name comes from the environment (TROVE_DEV_CLUSTER, as make dev-up got it):
		// CI names one cluster per run, so a fixed name here would make up.sh create a second one.
		cmd.Env = append(os.Environ(), "TROVE_PROFILE="+profile)
		out, err := cmd.CombinedOutput()
		_ = os.WriteFile(filepath.Join(cfg.out, "profile-"+profile+".log"), out, 0o644)
		if err != nil {
			t.Fatalf("up.sh with TROVE_PROFILE=%s: %v\n%s", profile, err, tail(string(out), 2000))
		}
	}
	if os.Getenv("TROVE_E2E_SCATTER_RETURN") != "0" {
		t.Cleanup(func() { switchProfile(t, "default") })
	}
	switchProfile(t, "scatter")
	waitFor(t, "three ready web pods", 3*time.Minute, 2*time.Second, func() bool {
		n := 0
		for _, p := range livePods(t, selWeb) {
			if p.Ready {
				n++
			}
		}
		return n == 3
	})

	t.Run("R-ARCH-032 E0.11 the M0 checks pass in the scatter profile: three web, two worker, ping OK", func(t *testing.T) {
		checkReleaseServes(t, 3, 2)
	})

	t.Run("R-ARCH-032 R-ARCH-031 E0.11 E0.12 the Valkey connection check passes with three web replicas", checkClientList)

	t.Run("R-ARCH-032 R-HA-037 E0.11 every request is balanced on its own: sequential requests reach all three replicas evenly", func(t *testing.T) {
		seen := map[string]int{}
		const n = 60
		for i := 0; i < n; i++ {
			code, h, _, err := edge("/api/v1/system/ping")
			if err != nil || code != 200 {
				t.Fatalf("request %d: %d %v", i, code, err)
			}
			seen[h.Get("X-Trove-Replica")]++
		}
		t.Logf("%d requests, each on a new connection: %v", n, seen)
		if len(seen) != 3 {
			t.Fatalf("requests reached %d replicas, want all 3 web replicas: %v", len(seen), seen)
		}
		for r, c := range seen {
			if c < n/3-3 || c > n/3+3 {
				t.Errorf("replica %s served %d of %d requests: not round robin", r, c, n)
			}
		}
	})

	t.Run("R-ARCH-032 E0.11 100 requests/s through the scatter edge: only 200, spread over the three replicas", func(t *testing.T) {
		k := startK6(t, "scatter", 10*time.Minute)
		time.Sleep(30 * time.Second)
		res := k.stop(t)
		res.only200(t)
		if len(res.Replicas) != 3 {
			t.Fatalf("k6 reached %d replicas: %v", len(res.Replicas), res.Replicas)
		}
		for r, c := range res.Replicas {
			if share := float64(c) / float64(res.Total); share < 0.28 || share > 0.38 {
				t.Errorf("replica %s served %.0f%% of the requests", r, share*100)
			}
		}
		_ = strings.TrimSpace
	})
}
