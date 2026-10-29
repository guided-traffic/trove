//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// helmUpgrade upgrades the release with the values it has (up.sh computed them: the dev values, the
// profile, the node ranges) and the extra arguments on top.
func helmUpgrade(extra ...string) (string, error) {
	args := []string{"--kube-context", cfg.kubeContext, "upgrade", "trove", filepath.Join(cfg.repo, "deploy/helm/trove"), "-n", cfg.namespace, "--reuse-values"}
	args = append(args, extra...)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "helm", args...)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	return buf.String(), err
}

// TestE04MigrationFailure is the chart part of E0.4 and the "Migration failure" row of R-HA-052:
// a migration Job that fails leaves the running replicas serving and makes helm upgrade fail.
// The Job fails for a real reason, not an injected one: it connects with a database role whose
// password is wrong (secrets.databaseMigration, the credential of the migration Job only).
func TestE04MigrationFailure(t *testing.T) {
	t.Run("R-HA-052 R-HA-019 E0.4 a failing migration Job leaves the replicas serving and makes helm upgrade fail", func(t *testing.T) {
		kn(t, "delete", "secret", "trove-e2e-db-wrong", "--ignore-not-found")
		kn(t, "create", "secret", "generic", "trove-e2e-db-wrong", "--from-literal=username=trove", "--from-literal=password=not-the-password")
		t.Cleanup(func() { _, _ = kubectl("-n", cfg.namespace, "delete", "secret", "trove-e2e-db-wrong") })

		before := map[string]string{}
		for _, p := range livePods(t, selTrove) {
			before[p.Name] = p.UID
		}
		beforeGen := strings.TrimSpace(kn(t, "get", "deployment", "trove-web", "-o", "jsonpath={.metadata.generation}"))

		// Requests flow through the Ingress while the upgrade runs.
		var mu sync.Mutex
		codes := map[int]int{}
		stop := make(chan struct{})
		done := make(chan struct{})
		go func() {
			defer close(done)
			for {
				select {
				case <-stop:
					return
				default:
				}
				c, _, _, _ := edge("/api/v1/system/ping")
				mu.Lock()
				codes[c]++
				mu.Unlock()
				time.Sleep(50 * time.Millisecond)
			}
		}()

		start := time.Now()
		out, err := helmUpgrade("--set", "secrets.databaseMigration.existingSecret=trove-e2e-db-wrong", "--set", "migrations.backoffLimit=0", "--wait", "--timeout", "3m")
		took := time.Since(start)
		close(stop)
		<-done
		t.Logf("helm upgrade took %s, err=%v\n%s", took.Round(time.Second), err, strings.TrimSpace(out))
		if err == nil {
			t.Fatal("helm upgrade succeeded although the migration Job cannot authenticate")
		}
		if !strings.Contains(out, "migrate") {
			t.Errorf("the helm error does not name the migration Job: %s", out)
		}
		jobState := strings.TrimSpace(kn(t, "get", "job", "trove-migrate", "-o", "jsonpath={.status.conditions[*].type}"))
		if !strings.Contains(jobState, "Failed") {
			t.Errorf("the migration Job conditions are %q, want Failed", jobState)
		}
		logs, _ := kubectl("-n", cfg.namespace, "logs", "job/trove-migrate")
		t.Logf("migration Job log (tail): %s", tail(logs, 600))

		mu.Lock()
		t.Logf("ping during the failed upgrade: %v", codes)
		for c, n := range codes {
			if c != 200 {
				t.Errorf("%d requests answered %d while the migration failed", n, c)
			}
		}
		mu.Unlock()
		after := livePods(t, selTrove)
		if len(after) != len(before) {
			t.Errorf("%d trove pods after, %d before", len(after), len(before))
		}
		for _, p := range after {
			if before[p.Name] != p.UID {
				t.Errorf("pod %s was replaced by the failed upgrade", p.Name)
			}
			if !p.Ready {
				t.Errorf("pod %s is not ready", p.Name)
			}
		}
		if gen := strings.TrimSpace(kn(t, "get", "deployment", "trove-web", "-o", "jsonpath={.metadata.generation}")); gen != beforeGen {
			t.Errorf("the web Deployment changed (generation %s -> %s) although the pre-upgrade hook failed", beforeGen, gen)
		}
		if st := strings.TrimSpace(helmStatus(t)); st != "failed" {
			t.Errorf("helm release status = %q, want failed", st)
		}

		// The repair is the same upgrade with the right credential.
		out, err = helmUpgrade("--set", "secrets.databaseMigration.existingSecret=", "--wait", "--timeout", "5m")
		if err != nil {
			t.Fatalf("the repairing helm upgrade failed: %v\n%s", err, out)
		}
		if st := strings.TrimSpace(helmStatus(t)); st != "deployed" {
			t.Errorf("helm release status after the repair = %q, want deployed", st)
		}
		checkReleaseServes(t, 2, 2)
	})
}

func helmStatus(t testing.TB) string {
	t.Helper()
	out, err := exec.Command("helm", "--kube-context", cfg.kubeContext, "-n", cfg.namespace, "status", "trove", "-o", "json").Output()
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	i := strings.Index(s, `"status":"`)
	if i < 0 {
		t.Fatalf("no status in %s", s)
	}
	s = s[i+len(`"status":"`):]
	return s[:strings.Index(s, `"`)]
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}
