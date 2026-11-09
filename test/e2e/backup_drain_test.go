//go:build e2e

package e2e

// R-BAK-029 scenario 8 (drain and crash): `kubectl drain` during a backup run and during a restore, and a kill -9 of each
// (R-BAK-010, R-BAK-012, R-BAK-021, R-HA-060). Every Job ends Complete without a manual step, with a retry budget of 0 where the
// requirement says an eviction does not consume it.

import (
	"crypto/rand"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// podLog follows the log of a pod from its start, so a test can act on a line the moment it appears and can read what the pod said
// after it is gone.
type podLog struct {
	mu   sync.Mutex
	text strings.Builder
	done chan struct{}
}

func (l *podLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text.Write(b)
}

func (l *podLog) has(s string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Contains(l.text.String(), s)
}

func (l *podLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.text.String()
}

// followPodLog runs `kubectl logs -f` on the pod, again until the container has started.
func followPodLog(ns, pod string) *podLog {
	l := &podLog{done: make(chan struct{})}
	go func() {
		defer close(l.done)
		for range 240 {
			cmd := exec.Command("kubectl", "--context", cfg.kubeContext, "-n", ns, "logs", "-f", "--all-containers", pod)
			cmd.Stdout, cmd.Stderr = l, l
			_ = cmd.Run()
			if l.has("restore checks passed") || l.has("backup run started") {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()
	return l
}

// await polls the log every 20 ms for the text.
func (l *podLog) await(t testing.TB, text string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !l.has(text) {
		if time.Now().After(deadline) {
			t.Fatalf("the log did not say %q within %s:\n%s", text, within, tail(l.String(), 2000))
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// restoreCopyStarted waits for the first restore pod of the Job and returns its log once the blob copy (step 2 of R-BAK-021, after the
// schema step) has begun, with nothing yet said about its end. The restore logs no progress of the copy, so the start line and the
// absence of the end line are the bounds; the copy is widened by many small objects (below), and the caller checks after the
// disturbance that the pod never reached "blobs copied" (copyInterrupted).
func restoreCopyStarted(t *testing.T) (pod string, l *podLog) {
	t.Helper()
	waitFor(t, "the restore pod", 3*time.Minute, 500*time.Millisecond, func() bool {
		pod = bakPodOf(t, restoreNS, "trove-restore")
		return pod != ""
	})
	l = followPodLog(restoreNS, pod)
	l.await(t, "schema at the backup's step", 3*time.Minute)
	if l.has("blobs copied") {
		t.Fatalf("the blob copy of %s ended before the test could disturb it:\n%s", pod, tail(l.String(), 1500))
	}
	return pod, l
}

// copyInterrupted fails unless the pod ended without ever saying that the blob copy was done: the disturbance landed in the copy.
func copyInterrupted(t *testing.T, pod string, l *podLog) {
	t.Helper()
	select {
	case <-l.done:
	case <-time.After(2 * time.Minute):
		t.Fatalf("the log of %s did not end after the disturbance", pod)
	}
	if l.has("blobs copied") {
		t.Fatalf("the restore pod %s finished its blob copy before the disturbance reached it (it did not interrupt anything):\n%s", pod, tail(l.String(), 1500))
	}
}

// bakWorkerNodes lists the worker nodes of the cluster.
func bakWorkerNodes(t testing.TB) []string {
	t.Helper()
	out, err := kubectl("get", "nodes", "-l", "!node-role.kubernetes.io/control-plane", "-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(out)
}

func podNode(t testing.TB, ns, pod string) string {
	t.Helper()
	out, err := kubectl("-n", ns, "get", "pod", pod, "-o", "jsonpath={.spec.nodeName}")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(out)
}

// podNodeIfAny is podNode for a pod that may be deleted while it is looked up: an error (the pod is gone) is the empty node.
func podNodeIfAny(ns, pod string) string {
	out, err := kubectl("-n", ns, "get", "pod", pod, "-o", "jsonpath={.spec.nodeName}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// bakDrainNode picks a worker node that hosts neither the PostgreSQL primary of the main installation nor the database of the
// restored one: a PodDisruptionBudget refuses the eviction of a primary, and the drain would not finish.
func bakDrainNode(t testing.TB) string {
	t.Helper()
	busy := map[string]bool{podNode(t, cfg.namespace, pgPrimary(t)): true}
	if out, err := kubectl("-n", restoreNS, "get", "pod", "trove-db-1", "-o", "jsonpath={.spec.nodeName}"); err == nil {
		busy[strings.TrimSpace(out)] = true
	}
	for _, n := range bakWorkerNodes(t) {
		if !busy[n] {
			return n
		}
	}
	t.Fatalf("every worker node hosts a database primary: %v", busy)
	return ""
}

// bakPodOf is the newest pod of a Job.
func bakPodOf(t testing.TB, ns, job string) string {
	t.Helper()
	out, err := kubectl("-n", ns, "get", "pods", "-l", "job-name="+job, "--sort-by=.metadata.creationTimestamp", "-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}")
	if err != nil {
		t.Fatal(err)
	}
	f := strings.Fields(out)
	if len(f) == 0 {
		return ""
	}
	return f[len(f)-1]
}

// bakWaitLog waits until the log of a pod contains the text.
func bakWaitLog(t testing.TB, ns, job, text string, within time.Duration) {
	t.Helper()
	waitFor(t, "the log of Job "+job+" to say "+text, within, time.Second, func() bool {
		return strings.Contains(bakJobLogs(ns, job), text)
	})
}

// bakDrain runs deploy/dev/drain.sh <node>: the drain alone, the rollouts, the uncordon.
func bakDrain(t testing.TB, node string) {
	t.Helper()
	script(t, "drain.sh", node)
}

// bakFreshPrefix is a target prefix no backup was written to: a full copy.
func bakFreshPrefix(run, what string) string { return fmt.Sprintf("trove-%s-%s", what, run) }

func TestBackupDrain(t *testing.T) {
	useBackupProfile(t)
	adm := bakAdmin(t)
	run := bakRun()
	// data to copy: a dozen files of 1 MiB, copied at 1 MiB/s into a fresh target, keep a backup running for about 15 seconds
	for i := range 12 {
		if r, err := retrying(func() (reply, error) {
			return bakPutFile(adm, fmt.Sprintf("drain-%s/%02d.bin", run, i), randomBytes(1<<20))
		}); err != nil || r.Status != 201 {
			t.Fatalf("file %d: %v %s", i, err, r)
		}
	}
	// many small objects: the restore streams them one at a time (a request each way per object), so its blob copy lasts seconds on any runner,
	// where a few large files copy in a blink; the restore has no rate limit to set
	for i := range 500 {
		if r, err := retrying(func() (reply, error) {
			return bakPutFile(adm, fmt.Sprintf("drain-%s/small/%03d.bin", run, i), randomBytes(4<<10))
		}); err != nil || r.Status != 201 {
			t.Fatalf("small file %d: %v %s", i, err, r)
		}
	}
	slow := func(prefix string) map[string]string {
		return map[string]string{"TROVE_BACKUP_TARGET_PREFIX": prefix, "TROVE_BACKUP_COPY_BYTES_PER_SECOND": "1MiB", "TROVE_BACKUP_COPY_CONCURRENCY": "1"}
	}
	zero := 0
	var drainBackupID string

	t.Run("R-HA-007 R-HA-052 R-HA-060 R-BAK-012 a node drain during a backup: the Job completes with backoffLimit 0 and the eviction is no failure", func(t *testing.T) {
		node := bakDrainNode(t)
		job := "bak-drain-" + run
		bakCreateJob(t, bakJob{name: job, backoff: &zero, env: slow(bakFreshPrefix(run, "drain")), node: node})
		bakWaitLog(t, cfg.namespace, job, "backup run started", 3*time.Minute)
		time.Sleep(5 * time.Second) // into the copy
		first := bakPodOf(t, cfg.namespace, job)
		start := time.Now()
		bakDrain(t, node)
		t.Logf("the drain of %s finished in %s while the backup ran", node, time.Since(start).Round(time.Second))
		// the Job's pod was pinned to the drained node; the next pod may run anywhere
		end := bakWaitJob(t, cfg.namespace, job, 15*time.Minute)
		if end.Condition != "Complete" {
			t.Fatalf("the Job ended %s (exits %v, %d pods):\n%s", end.Condition, end.Exits, end.Pods, tail(bakJobLogs(cfg.namespace, job), 2500))
		}
		t.Logf("pods %d, exits %v; the first pod %s", end.Pods, end.Exits, first)
		if end.Pods < 2 {
			t.Fatalf("the backup was not interrupted: the Job ran %d pod(s)", end.Pods)
		}
		drainBackupID = bakBackupID(bakJobLogs(cfg.namespace, job))
		timings.set("drainDuringBackup", map[string]any{"node": node, "pods": end.Pods, "exits": end.Exits})
	})

	t.Run("R-HA-007 R-HA-052 R-HA-060 R-BAK-010 kill -9 of the backup pod during copying: the retry pod takes over after the lease and the Job completes", func(t *testing.T) {
		job := "bak-kill-" + run
		two := 2
		env := slow(bakFreshPrefix(run, "kill"))
		env["TROVE_BACKUP_LEASE_DURATION"] = "20s"
		env["TROVE_BACKUP_HEARTBEAT_INTERVAL"] = "2s"
		env["TROVE_BACKUP_FINALIZE_TIMEOUT"] = "5s"
		bakCreateJob(t, bakJob{name: job, backoff: &two, env: env})
		bakWaitLog(t, cfg.namespace, job, "backup run started", 3*time.Minute)
		time.Sleep(5 * time.Second)
		pod := bakPodOf(t, cfg.namespace, job)
		if out, err := exec.Command("kubectl", "--context", cfg.kubeContext, "-n", cfg.namespace, "delete", "pod", pod, "--grace-period=0", "--force").CombinedOutput(); err != nil {
			t.Fatalf("kill %s: %v: %s", pod, err, out)
		}
		start := time.Now()
		end := bakWaitJob(t, cfg.namespace, job, 15*time.Minute)
		if end.Condition != "Complete" {
			t.Fatalf("the Job ended %s (exits %v):\n%s", end.Condition, end.Exits, tail(bakJobLogs(cfg.namespace, job), 2500))
		}
		t.Logf("killed %s; the Job completed %s later with %d pods, exits %v", pod, time.Since(start).Round(time.Second), end.Pods, end.Exits)
		if end.Pods < 2 {
			t.Fatalf("the Job ran %d pod(s): the kill did not reach the run", end.Pods)
		}
	})

	if drainBackupID == "" {
		t.Skip("no backup of the drain test to restore")
	}

	// The restore from the target the drain test wrote: every blob of the installation, streamed through the pod one at a time (the restore has no rate limit, so the server-side copy would end the step in seconds).
	restoreWith := func(t *testing.T, backoff string) {
		env := fmt.Sprintf(`restore.extraEnv=[{"name":"TROVE_BACKUP_COPY_SERVER_SIDE","value":"never"},{"name":"TROVE_BACKUP_COPY_CONCURRENCY","value":"1"},{"name":"TROVE_BACKUP_TARGET_PREFIX","value":%q}]`, bakFreshPrefix(run, "drain"))
		args := []string{"install", drainBackupID, "--set", "restore.backoffLimit=" + backoff, "--set-json", env}
		restoreNSScript(t, args...)
	}

	t.Run("R-HA-007 R-HA-052 R-HA-060 R-BAK-012 a node drain during the blob copy of a restore: the Job completes with restore.backoffLimit 0", func(t *testing.T) {
		restoreNSScript(t, "infra")
		// The restore Job has no scheduling values, so the pod is steered: every worker node but one is cordoned while the Job is created,
		// and the one that stays open holds neither database primary (a PodDisruptionBudget refuses their eviction).
		node := bakDrainNode(t)
		var others []string
		for _, n := range bakWorkerNodes(t) {
			if n != node {
				others = append(others, n)
			}
		}
		uncordon := func() {
			for _, n := range others {
				_, _ = kubectl("uncordon", n)
			}
			_, _ = kubectl("uncordon", node)
		}
		t.Cleanup(uncordon)
		for _, n := range others {
			if _, err := kubectl("cordon", n); err != nil {
				t.Fatal(err)
			}
		}
		restoreWith(t, "0")
		pod, rlog := restoreCopyStarted(t)
		if got := podNodeIfAny(restoreNS, pod); got != node {
			t.Fatalf("the restore pod runs on %s, not on %s", got, node)
		}
		start := time.Now()
		drain := exec.Command("kubectl", "--context", cfg.kubeContext, "drain", node, "--ignore-daemonsets", "--delete-emptydir-data", "--timeout=5m")
		var drainOut strings.Builder
		drain.Stdout, drain.Stderr = &drainOut, &drainOut
		if err := drain.Start(); err != nil {
			t.Fatal(err)
		}
		// once the pod is evicted the other nodes take the next one
		waitFor(t, "the restore pod to leave "+node, 2*time.Minute, time.Second, func() bool {
			p := bakPodOf(t, restoreNS, "trove-restore")
			// the evicted pod can be gone between the two lookups; a pod that no longer exists has left the node
			return p == "" || podNodeIfAny(restoreNS, p) != node
		})
		for _, n := range others {
			_, _ = kubectl("uncordon", n)
		}
		if err := drain.Wait(); err != nil {
			t.Fatalf("kubectl drain: %v\n%s", err, tail(drainOut.String(), 1500))
		}
		copyInterrupted(t, pod, rlog)
		t.Logf("the drain of %s finished in %s while the restore copied blobs", node, time.Since(start).Round(time.Second))
		end := bakWaitJob(t, restoreNS, "trove-restore", 20*time.Minute)
		if end.Condition != "Complete" {
			t.Fatalf("the restore Job ended %s (exits %v, %d pods):\n%s", end.Condition, end.Exits, end.Pods, tail(bakJobLogs(restoreNS, "trove-restore"), 2500))
		}
		if end.Pods < 2 {
			t.Fatalf("the restore was not interrupted: %d pod(s)", end.Pods)
		}
		timings.set("drainDuringRestore", map[string]any{"node": node, "pods": end.Pods, "exits": end.Exits})
	})

	t.Run("R-HA-007 R-HA-052 R-HA-060 R-BAK-021 kill -9 of the restore pod during the blob copy: the next pod resumes and the Job completes", func(t *testing.T) {
		restoreNSScript(t, "infra")
		restoreWith(t, "2")
		pod, rlog := restoreCopyStarted(t)
		if out, err := exec.Command("kubectl", "--context", cfg.kubeContext, "-n", restoreNS, "delete", "pod", pod, "--grace-period=0", "--force").CombinedOutput(); err != nil {
			t.Fatalf("kill %s: %v: %s", pod, err, out)
		}
		copyInterrupted(t, pod, rlog)
		end := bakWaitJob(t, restoreNS, "trove-restore", 20*time.Minute)
		if end.Condition != "Complete" {
			t.Fatalf("the restore Job ended %s (exits %v):\n%s", end.Condition, end.Exits, tail(bakJobLogs(restoreNS, "trove-restore"), 2500))
		}
		if end.Pods < 2 {
			t.Fatalf("the restore was not killed: %d pod(s)", end.Pods)
		}
		logs := bakJobLogs(restoreNS, "trove-restore")
		for _, want := range []string{"restore checks passed"} {
			if !strings.Contains(logs, want) {
				t.Errorf("the log lacks %q", want)
			}
		}
		// the restored database is whole: the anchor and the marker are gone and the schema is at the binary's step
		if got := krSQL(t, `SELECT count(*) FROM trove.system_state WHERE key = 'restore_in_progress'`); got != "0" {
			t.Errorf("restore_in_progress is still set (%s)", got)
		}
		if got := krSQL(t, `SELECT coalesce(obj_description('trove'::regnamespace, 'pg_namespace'), '')`); got != "" {
			t.Errorf("the anchor remains: %q", got)
		}
	})
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}
