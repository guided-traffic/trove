//go:build e2e

package e2e

import (
	"bufio"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// schedulerHolders asks PostgreSQL, the authority of the lease: the session advisory lock
// (class 1 = LockLease, object 1 = the scheduler lease, internal/cluster/locks.go) joined to the
// connection that holds it. The connection's application_name is trove:<pod>:lease.
const schedulerHoldersSQL = `SELECT a.application_name FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid
 WHERE l.locktype = 'advisory' AND l.classid = 1 AND l.objid = 1 AND l.objsubid = 2 AND l.granted ORDER BY 1`

func schedulerHolders(t testing.TB) []string {
	t.Helper()
	var out []string
	for _, line := range strings.Split(sql(t, schedulerHoldersSQL), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			parts := strings.Split(line, ":")
			if len(parts) != 3 || parts[0] != "trove" || parts[2] != "lease" {
				t.Fatalf("unexpected lease connection name %q", line)
			}
			out = append(out, parts[1])
		}
	}
	return out
}

func TestE06Lease(t *testing.T) {
	workers := map[string]bool{}
	for _, p := range livePods(t, selWorker) {
		workers[p.Name] = true
	}
	webs := map[string]bool{}
	for _, p := range livePods(t, selWeb) {
		webs[p.Name] = true
	}

	t.Run("R-HA-006 R-ARCH-001 E0.6 the scheduler lease is held by exactly one worker; no web replica holds a lease connection or a job", func(t *testing.T) {
		h := schedulerHolders(t)
		if len(h) != 1 || !workers[h[0]] {
			t.Fatalf("scheduler lease holders %v, want exactly one worker pod of %v", h, workers)
		}
		for _, line := range strings.Split(sql(t, `SELECT DISTINCT application_name FROM pg_stat_activity WHERE application_name LIKE 'trove:%:lease'`), "\n") {
			if line = strings.TrimSpace(line); line != "" && webs[strings.Split(line, ":")[1]] {
				t.Errorf("web replica holds a lease connection: %s", line)
			}
		}
		// Jobs run while this is sampled, so locked_by is populated.
		l := startDriverLoad(t, "e06-steady", 25*time.Second)
		for i := 0; i < 20; i++ {
			if n := sql(t, `SELECT count(*) FROM trove.job j JOIN trove.replica r ON r.instance_uid = j.locked_by WHERE 'web' = ANY(r.roles)`); n != "0" {
				t.Errorf("%s jobs are locked by a web replica", n)
			}
			time.Sleep(time.Second)
		}
		s := l.wait(t)
		noteResult(t, "e06-steady", s)
		// P-13 and P-14 on the dev profile, Valkey up.
		if s.Changes.NotPropagated != 0 || s.Changes.WriteErrors != 0 || s.Jobs.EnqueueErrors != 0 || s.Jobs.Unfinished != 0 {
			t.Errorf("steady load had errors: %+v %+v", s.Changes, s.Jobs)
		}
		if s.Changes.ValkeyUp.N < 15 || s.Changes.ValkeyUp.P99 > 100 {
			t.Errorf("R-ARCH-027 P-13 with Valkey: n=%d p99=%.1f ms, want n>=15 and p99 <= 100 ms", s.Changes.ValkeyUp.N, s.Changes.ValkeyUp.P99)
		}
		if s.Jobs.ValkeyUp.N < 15 || s.Jobs.ValkeyUp.P99 > 1000 {
			t.Errorf("R-ARCH-027 P-14 with Valkey: n=%d p99=%.1f ms, want n>=15 and p99 <= 1000 ms", s.Jobs.ValkeyUp.N, s.Jobs.ValkeyUp.P99)
		}
	})
	for _, c := range []struct {
		name string
		kill func(t *testing.T, victim pod)
		// graceful: the process gets SIGTERM and deregisters; otherwise it dies without cleanup.
		graceful bool
	}{
		{"force-deleting the lease holder", forceDelete, true},
		{"killing the lease holder's process with SIGKILL (a crash)", sigkill, false},
	} {
		t.Run("R-HA-006 R-OPS-003 E0.6 "+c.name+" moves the lease to a live worker, never two holders; "+map[bool]string{true: "the row of a clean exit is gone and the replacement is listed", false: "the registry shows the dead instance lost within 30 s and then the replacement"}[c.graceful], func(t *testing.T) {
			holders := schedulerHolders(t)
			if len(holders) != 1 {
				t.Fatalf("scheduler lease holders %v", holders)
			}
			var victim pod
			for _, p := range livePods(t, selWorker) {
				if p.Name == holders[0] {
					victim = p
				}
			}
			before, err := replicas()
			if err != nil {
				t.Fatal(err)
			}
			// Rows of earlier runs may carry the same pod name (a restart in place keeps it), so
			// the dead instance is the live row of the victim and a replacement is any row not known before.
			oldUID := ""
			known := map[string]bool{}
			for _, r := range before {
				known[r.InstanceUID] = true
				if r.Name == victim.Name && !r.Lost {
					oldUID = r.InstanceUID
				}
			}
			if oldUID == "" {
				t.Fatalf("no live registry row for %s", victim.Name)
			}
			w := startHolderWatch(t)
			time.Sleep(time.Second)
			beforePID := w.last().pids
			t0 := time.Now()
			c.kill(t, victim)

			var lostAfter, movedAfter, replacementAfter, goneAfter time.Duration
			var replacement string
			deadline := t0.Add(90 * time.Second)
			for time.Now().Before(deadline) {
				if movedAfter == 0 {
					for _, s := range w.samplesAfter(t0) {
						if s.n == 1 && s.pids != beforePID {
							movedAfter = s.at.Sub(t0)
							replacement = s.names
							break
						}
					}
				}
				if rs, err := replicas(); err == nil {
					present := false
					for _, r := range rs {
						if r.InstanceUID == oldUID {
							present = true
							if r.Lost && lostAfter == 0 {
								lostAfter = time.Since(t0)
							}
						}
						if !known[r.InstanceUID] && r.role() == "worker" && !r.Lost && r.State == "ready" && replacementAfter == 0 {
							replacementAfter = time.Since(t0)
						}
					}
					if !present && goneAfter == 0 {
						goneAfter = time.Since(t0)
					}
				}
				done := movedAfter != 0 && replacementAfter != 0 && (lostAfter != 0 || goneAfter != 0)
				if done {
					break
				}
				time.Sleep(500 * time.Millisecond)
			}
			samples := w.stop()
			maxHolders := 0
			for _, s := range samples {
				maxHolders = max(maxHolders, s.n)
			}
			t.Logf("%s: %d samples of pg_locks every 200 ms, most holders at once %d; lease moved to %q after %s; old instance lost after %s, row gone after %s; replacement ready after %s",
				victim.Name, len(samples), maxHolders, replacement, movedAfter.Round(10*time.Millisecond), lostAfter.Round(10*time.Millisecond), goneAfter.Round(10*time.Millisecond), replacementAfter.Round(10*time.Millisecond))
			noteResult(t, "e06-"+map[bool]string{true: "force-delete", false: "sigkill"}[c.graceful], map[string]any{
				"victim": victim.Name, "holder_after": replacement, "lease_moved_s": movedAfter.Seconds(), "lost_s": lostAfter.Seconds(),
				"row_gone_s": goneAfter.Seconds(), "replacement_s": replacementAfter.Seconds(), "max_holders": maxHolders, "samples": len(samples),
			})
			if movedAfter == 0 {
				t.Errorf("the scheduler lease did not move within 90 s")
			}
			if maxHolders > 1 {
				t.Errorf("%d connections held the scheduler lease at once", maxHolders)
			}
			if replacementAfter == 0 {
				t.Errorf("the registry did not list a ready replacement within 90 s")
			}
			if c.graceful {
				if goneAfter == 0 && lostAfter == 0 {
					t.Errorf("the deleted pod is neither gone nor lost in the registry")
				}
			} else if lostAfter == 0 || lostAfter > 32*time.Second {
				// A row is lost when its heartbeat is older than system.heartbeat_stale_after (30 s). The
				// heartbeat can have been written an instant before the kill, so the registry may need the
				// full 30 s; the 2 s are the polling of this test (every 500 ms, one API call each).
				t.Errorf("the registry showed the killed instance as lost after %s, want within system.heartbeat_stale_after (30 s) plus 2 s of observation", lostAfter)
			}
			if n := sql(t, `SELECT count(*) FROM trove.job j JOIN trove.replica r ON r.instance_uid = j.locked_by WHERE 'web' = ANY(r.roles)`); n != "0" {
				t.Errorf("%s jobs are locked by a web replica", n)
			}
			waitFor(t, "two ready workers", 3*time.Minute, 2*time.Second, func() bool {
				n := 0
				for _, p := range livePods(t, selWorker) {
					if p.Ready {
						n++
					}
				}
				return n == 2
			})
		})
	}
}

func forceDelete(t *testing.T, victim pod) {
	kn(t, "delete", "pod", victim.Name, "--grace-period=0", "--force", "--wait=false")
}

// sigkill ends the process of the pod's container without any chance to clean up: the container
// runtime on the node delivers SIGKILL, which is what a crash or an OOM kill looks like. The kubelet
// restarts the container in the same pod.
func sigkill(t *testing.T, victim pod) {
	t.Helper()
	crictl := func(args ...string) string {
		out, err := exec.Command("docker", append([]string{"exec", victim.Node, "crictl"}, args...)...).Output()
		if err != nil {
			t.Fatalf("crictl %v on %s: %v", args, victim.Node, err)
		}
		return strings.TrimSpace(string(out))
	}
	cid := crictl("ps", "-q", "--label", "io.kubernetes.pod.name="+victim.Name, "--label", "io.kubernetes.container.name=trove")
	pid := crictl("inspect", "-o", "go-template", "--template", "{{.info.pid}}", cid)
	if out, err := exec.Command("docker", "exec", victim.Node, "kill", "-9", pid).CombinedOutput(); err != nil {
		t.Fatalf("kill -9 %s on %s: %v %s", pid, victim.Node, err, out)
	}
}

type holderSample struct {
	at    time.Time
	n     int
	pids  string
	names string
}

// holderWatch streams the scheduler lease's holders from PostgreSQL five times a second: one psql
// session on the primary that repeats the query with \watch.
type holderWatch struct {
	cmd *exec.Cmd
	mu  sync.Mutex
	got []holderSample
	end chan struct{}
}

func startHolderWatch(t testing.TB) *holderWatch {
	w := &holderWatch{end: make(chan struct{})}
	query := `SELECT clock_timestamp(), count(*), coalesce(string_agg(a.pid::text, ',' ORDER BY a.pid), ''), coalesce(string_agg(split_part(a.application_name, ':', 2), ',' ORDER BY a.pid), '')
 FROM pg_locks l JOIN pg_stat_activity a ON a.pid = l.pid WHERE l.locktype = 'advisory' AND l.classid = 1 AND l.objid = 1 AND l.objsubid = 2 AND l.granted;
\watch 0.2
`
	w.cmd = exec.Command("kubectl", "--context", cfg.kubeContext, "-n", cfg.namespace, "exec", "-i", pgPrimary(t), "-c", "postgres", "--", "psql", "-U", "postgres", "-d", "trove", "-At", "-F", "|")
	w.cmd.Stdin = strings.NewReader(query)
	out, err := w.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(w.end)
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			f := strings.Split(sc.Text(), "|")
			if len(f) != 4 {
				continue
			}
			at, err := time.Parse("2006-01-02 15:04:05.999999-07", f[0])
			if err != nil {
				continue
			}
			n, _ := strconv.Atoi(f[1])
			w.mu.Lock()
			w.got = append(w.got, holderSample{at: at, n: n, pids: f[2], names: f[3]})
			w.mu.Unlock()
		}
	}()
	waitFor(t, "the first lease sample", 20*time.Second, 200*time.Millisecond, func() bool { return len(w.snapshot()) > 0 })
	t.Cleanup(func() { _ = w.cmd.Process.Kill() })
	return w
}

func (w *holderWatch) snapshot() []holderSample {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]holderSample(nil), w.got...)
}

func (w *holderWatch) last() holderSample {
	s := w.snapshot()
	if len(s) == 0 {
		return holderSample{}
	}
	return s[len(s)-1]
}

func (w *holderWatch) samplesAfter(t0 time.Time) []holderSample {
	var out []holderSample
	for _, s := range w.snapshot() {
		if s.at.After(t0) {
			out = append(out, s)
		}
	}
	return out
}

func (w *holderWatch) stop() []holderSample {
	_ = w.cmd.Process.Kill()
	<-w.end
	_ = w.cmd.Wait()
	return w.snapshot()
}
