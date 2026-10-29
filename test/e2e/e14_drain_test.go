//go:build e2e

package e2e

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// podWatch records every change of the trove pods from `kubectl get pods -w`, stamped with the
// time it arrived: the time a pod spent Pending, and every container that was terminated by
// SIGKILL (exit code 137) or by the OOM killer. A watch sees each transition; polling would miss
// the final state of a pod that is deleted a moment later.
type podWatch struct {
	cmd     *exec.Cmd
	mu      sync.Mutex
	pending map[string]time.Time // pod -> first seen Pending
	pendFor map[string]time.Duration
	killed  []string
	seen    map[string]bool
	done    chan struct{}
	raw     *os.File
}

func startPodWatch(t testing.TB, selector string) *podWatch {
	since := time.Now()
	w := &podWatch{pending: map[string]time.Time{}, pendFor: map[string]time.Duration{}, seen: map[string]bool{}, done: make(chan struct{})}
	var err error
	if w.raw, err = os.Create(filepath.Join(cfg.out, "drain-pod-watch.jsonl")); err != nil {
		t.Fatal(err)
	}
	w.cmd = exec.Command("kubectl", "--context", cfg.kubeContext, "-n", cfg.namespace, "get", "pods", "-l", selector, "-w", "-o", "json", "--output-watch-events")
	out, err := w.cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := w.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(w.done)
		dec := json.NewDecoder(out)
		for dec.More() {
			var ev struct {
				Type   string
				Object struct {
					Metadata struct{ Name string }
					Status   struct {
						Phase             string
						ContainerStatuses []struct {
							Name  string
							State struct {
								Terminated *struct {
									ExitCode   int
									Reason     string
									FinishedAt time.Time
								}
							}
							LastState struct {
								Terminated *struct {
									ExitCode   int
									Reason     string
									FinishedAt time.Time
								}
							}
						}
					}
				}
			}
			if err := dec.Decode(&ev); err != nil {
				return
			}
			now := time.Now()
			b, _ := json.Marshal(map[string]any{"at": now, "type": ev.Type, "pod": ev.Object.Metadata.Name, "phase": ev.Object.Status.Phase})
			fmt.Fprintln(w.raw, string(b))
			name := ev.Object.Metadata.Name
			w.mu.Lock()
			w.seen[name] = true
			switch ev.Object.Status.Phase {
			case "Pending":
				if _, ok := w.pending[name]; !ok {
					w.pending[name] = now
				}
			default:
				if at, ok := w.pending[name]; ok {
					w.pendFor[name] = now.Sub(at)
					delete(w.pending, name)
				}
			}
			for _, c := range ev.Object.Status.ContainerStatuses {
				for _, term := range []*struct {
					ExitCode   int
					Reason     string
					FinishedAt time.Time
				}{c.State.Terminated, c.LastState.Terminated} {
					// Only terminations since the watch started: an earlier kill stays in lastState.
					if term != nil && term.FinishedAt.After(since) && (term.ExitCode == 137 || term.Reason == "OOMKilled") {
						w.killed = append(w.killed, fmt.Sprintf("%s/%s exit %d %s", name, c.Name, term.ExitCode, term.Reason))
					}
				}
			}
			w.mu.Unlock()
		}
	}()
	t.Cleanup(func() { w.stop() })
	return w
}

func (w *podWatch) stop() {
	if w.cmd.Process != nil {
		_ = w.cmd.Process.Kill()
	}
	<-w.done
	_ = w.cmd.Wait()
	_ = w.raw.Close()
}

// longestPending is the longest time a pod was Pending, counting a pod that is still Pending now.
func (w *podWatch) longestPending() (string, time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var name string
	var longest time.Duration
	for n, d := range w.pendFor {
		if d > longest {
			name, longest = n, d
		}
	}
	for n, at := range w.pending {
		if d := time.Since(at); d > longest {
			name, longest = n, d
		}
	}
	return name, longest
}

// drainNode picks the node of variant A (R-HA-059): it hosts a web and a worker pod and is not the
// PostgreSQL primary's node, whose PodDisruptionBudget refuses its eviction. When the only node that
// hosts both is the primary's, the primary is switched over first (variant B).
func drainNode(t *testing.T) string {
	t.Helper()
	for attempt := 0; attempt < 2; attempt++ {
		webs, workers := map[string]bool{}, map[string]bool{}
		for _, p := range livePods(t, selWeb) {
			webs[p.Node] = true
		}
		for _, p := range livePods(t, selWorker) {
			workers[p.Node] = true
		}
		primary := strings.TrimSpace(kn(t, "get", "pod", pgPrimary(t), "-o", "jsonpath={.spec.nodeName}"))
		var both []string
		for n := range webs {
			if workers[n] && n != primary {
				both = append(both, n)
			}
		}
		sort.Strings(both)
		if len(both) > 0 {
			return both[0]
		}
		t.Logf("the only node with a web and a worker pod is the PostgreSQL primary's (%s); switching the primary over first", primary)
		script(t, "failover.sh")
	}
	t.Fatal("no node hosts a web and a worker pod without the PostgreSQL primary")
	return ""
}

type probeJob struct {
	State        string          `json:"state"`
	Attempt      int             `json:"attempt"`
	MaxAttempts  int             `json:"max_attempts"`
	ProgressDone int             `json:"progress_done"`
	Checkpoint   json.RawMessage `json:"checkpoint"`
	Result       struct {
		Steps   int      `json:"steps"`
		Hops    []string `json:"hops"`
		Resumes []int    `json:"resumes"`
	} `json:"result"`
}

func TestE14Drain(t *testing.T) {
	t.Run("R-ARCH-024 R-HA-011 R-HA-059 E0.14 draining a node with a web and a worker pod under load: only 200, the jobs resume on the other worker from their cursor, no SIGKILL, nothing Pending for 60 s", func(t *testing.T) {
		const steps = 150
		node := drainNode(t)
		var onNode []pod
		for _, p := range livePods(t, selTrove) {
			if p.Node == node {
				onNode = append(onNode, p)
			}
		}
		t.Logf("draining %s, which hosts %v", node, podNames(onNode))
		var drainedWorker string
		for _, p := range onNode {
			if strings.Contains(p.Name, "-worker-") {
				drainedWorker = p.Name
			}
		}
		if drainedWorker == "" {
			t.Fatal("the chosen node has no worker pod")
		}
		// The shutdown of the drained pods is logged by the pods themselves; the logs are streamed so they outlive the pods.
		for _, p := range onNode {
			f, _ := os.Create(filepath.Join(cfg.out, "drain-"+p.Name+".log"))
			c := exec.Command("kubectl", "--context", cfg.kubeContext, "-n", cfg.namespace, "logs", "-f", "--tail=-1", p.Name)
			c.Stdout, c.Stderr = f, f
			_ = c.Start()
			t.Cleanup(func() { _ = c.Process.Kill(); _ = c.Wait(); _ = f.Close() })
		}

		watch := startPodWatch(t, selTrove)
		drv := startDriverLoad(t, "drain", 20*time.Minute)
		k := startK6(t, "drain", 30*time.Minute)

		// Probe jobs: enough that both workers carry some, so the drained one certainly runs a job.
		var ids []string
		for i := 0; i < 6; i++ {
			ids = append(ids, strings.TrimSpace(driver(t, "probe", "-steps", fmt.Sprint(steps), "-interval-ms", "1000")))
		}
		waitFor(t, "both workers to run a probe job", 60*time.Second, time.Second, func() bool {
			rows := sql(t, `SELECT r.name FROM trove.job j JOIN trove.replica r ON r.instance_uid = j.locked_by WHERE j.kind = 'system.probe' AND j.state = 'running' AND j.progress_total = 150 GROUP BY 1`)
			return len(strings.Fields(rows)) >= 2
		})
		owners := sql(t, `SELECT r.name || '=' || count(*) FROM trove.job j JOIN trove.replica r ON r.instance_uid = j.locked_by WHERE j.kind = 'system.probe' AND j.state = 'running' GROUP BY r.name`)
		t.Logf("running probe jobs per worker before the drain: %s", strings.ReplaceAll(owners, "\n", " "))
		time.Sleep(5 * time.Second)

		drainStart := time.Now()
		t.Logf("drain starts %s", drainStart.Format("15:04:05"))
		script(t, "drain.sh", node) // the drain alone: drain, wait for the rollouts, uncordon
		drainTook := time.Since(drainStart)
		t.Logf("drain.sh returned after %s", drainTook.Round(time.Second))

		// The jobs finish.
		finished := map[string]probeJob{}
		waitFor(t, "every probe job to end", 6*time.Minute, 5*time.Second, func() bool {
			for _, id := range ids {
				if _, ok := finished[id]; ok {
					continue
				}
				var j probeJob
				if err := jsonUnmarshal(driver(t, "job", id), &j); err != nil {
					t.Fatal(err)
				}
				if j.State == "succeeded" || j.State == "failed" || j.State == "cancelled" {
					finished[id] = j
				}
			}
			return len(finished) == len(ids)
		})
		moved := 0
		for _, id := range ids {
			j := finished[id]
			t.Logf("job %s: %s attempt %d/%d steps %d hops %d resumed at %v", id, j.State, j.Attempt, j.MaxAttempts, j.Result.Steps, len(j.Result.Hops), j.Result.Resumes)
			if j.State != "succeeded" || j.Result.Steps != steps {
				t.Errorf("job %s ended %s with %d steps, want succeeded with %d", id, j.State, j.Result.Steps, steps)
			}
			if j.Attempt != 1 {
				t.Errorf("job %s: attempt %d, want 1 (a release at shutdown does not count an attempt)", id, j.Attempt)
			}
			if len(j.Result.Hops) > 1 {
				moved++
				if len(j.Result.Resumes) == 0 || j.Result.Resumes[0] <= 0 || j.Result.Resumes[0] >= steps {
					t.Errorf("job %s moved but did not resume from a cursor inside the run: %v", id, j.Result.Resumes)
				}
			}
		}
		if moved == 0 {
			t.Errorf("no probe job moved to the other worker: the drained worker %s did not release any job", drainedWorker)
		}
		t.Logf("%d of %d probe jobs moved to the other worker and finished from their cursor", moved, len(ids))

		time.Sleep(10 * time.Second)
		watch.stop()
		driver(t, "stop")
		ds := drv.wait(t)
		res := k.stop(t)
		noteResult(t, "e14-drain", map[string]any{"node": node, "drain_seconds": drainTook.Seconds(), "k6_status": res.Status, "driver": ds, "pending_pods": watch.pendFor, "moved_jobs": moved})

		res.only200(t)
		if unc := strings.TrimSpace(kn(t, "get", "node", node, "-o", "jsonpath={.spec.unschedulable}")); unc == "true" {
			t.Errorf("node %s is still cordoned", node)
		}
		if len(watch.killed) != 0 {
			t.Errorf("containers were killed: %v", watch.killed)
		}
		if name, d := watch.longestPending(); d > 60*time.Second {
			t.Errorf("trove pod %s was Pending for %s", name, d.Round(time.Second))
		} else {
			t.Logf("longest Pending of a trove pod: %s (%s)", d.Round(100*time.Millisecond), name)
		}
		checkEvents(t, onNode)
		checkReleaseServes(t, 2, 2)
	})
}

func podNames(ps []pod) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Name)
	}
	return out
}

// checkEvents reads the kubelet's events for the drained pods: a container that does not stop within
// the grace period is killed, which the kubelet records; none of that may appear.
func checkEvents(t *testing.T, drained []pod) {
	t.Helper()
	raw := kn(t, "get", "events", "-o", "json")
	_ = os.WriteFile(filepath.Join(cfg.out, "drain-events.json"), []byte(raw), 0o644)
	var l struct {
		Items []struct {
			Reason, Message, Type string
			InvolvedObject        struct{ Name string }
		}
	}
	if err := json.Unmarshal([]byte(raw), &l); err != nil {
		t.Fatal(err)
	}
	mine := map[string]bool{}
	for _, p := range drained {
		mine[p.Name] = true
	}
	killing := 0
	sc := bufio.NewScanner(strings.NewReader(""))
	_ = sc
	for _, e := range l.Items {
		if !mine[e.InvolvedObject.Name] {
			continue
		}
		low := strings.ToLower(e.Message)
		if e.Reason == "Killing" {
			killing++
		}
		if strings.Contains(low, "sigkill") || strings.Contains(low, "exit code 137") || strings.Contains(low, "grace period") && e.Reason != "Killing" || e.Reason == "FailedKillPod" || strings.Contains(e.Reason, "OOM") {
			t.Errorf("event %s on %s: %s", e.Reason, e.InvolvedObject.Name, e.Message)
		}
	}
	t.Logf("%d Killing events (the kubelet's SIGTERM) for the drained pods %v, none with SIGKILL", killing, podNames(drained))
}
