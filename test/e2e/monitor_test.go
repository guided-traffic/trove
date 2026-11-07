//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// monitor polls /healthz and /readyz of every api, packages and worker pod once a second, straight at the
// pod through the API server, so the answer is the replica's own and not the Service's.
type monitor struct {
	mu      sync.Mutex
	samples int
	health  map[string]int // pod -> non-200 answers of /healthz
	ready   map[string]int // pod -> non-200 answers of /readyz
	seenOK  map[string]bool
	degrade map[string]int // pod -> 200 answers of /readyz that carry "degraded"
	stopCh  chan struct{}
	wg      sync.WaitGroup
	restart map[string]int
	// failed holds the first answers of each pod that were not 200, as they came (status, the body or the transport error,
	// and the time), so that the job log names the check or the transport that failed (F-66).
	failed  map[string][]string
	started time.Time
	// dir, when set, receives the log of a pod each time its probes have failed 1, 16, 31, ... times: the container is
	// killed by its liveness probe after a minute of failures, and the log of the first instance is gone with the second.
	dir string
	cap sync.WaitGroup
	bad map[string]int // pod -> samples with a non-200 answer
}

// failedKept is how many non-200 answers of a pod the monitor writes down, and failedBody how much of each body.
const (
	failedKept = 4
	failedBody = 400
)

func startMonitor(t testing.TB) *monitor {
	m := &monitor{
		health: map[string]int{}, ready: map[string]int{}, seenOK: map[string]bool{}, degrade: map[string]int{}, stopCh: make(chan struct{}), restart: map[string]int{},
		failed: map[string][]string{}, bad: map[string]int{}, started: time.Now(),
	}
	for _, p := range livePods(t, selTrove) {
		m.restart[p.Name] = p.Restarts
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		var names []string
		for n := 0; ; n++ {
			select {
			case <-m.stopCh:
				return
			case <-time.After(time.Second):
			}
			if n%5 == 0 {
				names = names[:0]
				ps, err := kubectl("-n", cfg.namespace, "get", "pods", "-l", selTrove, "-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}")
				if err == nil {
					names = append(names, strings.Fields(ps)...)
				}
			}
			var wg sync.WaitGroup
			for _, name := range names {
				wg.Add(1)
				go func() {
					defer wg.Done()
					at := time.Now()
					h, hbody := ops(name, "/healthz")
					r, body := ops(name, "/readyz")
					m.mu.Lock()
					defer m.mu.Unlock()
					m.samples++
					if h != 200 {
						m.health[name]++
						m.note(name, "/healthz", at, h, hbody)
					}
					if r != 200 {
						m.ready[name]++
						m.note(name, "/readyz", at, r, body)
					}
					if h != 200 || r != 200 {
						if m.bad[name]%15 == 0 {
							m.capture(name, at)
						}
						m.bad[name]++
					}
					if r == 200 {
						m.seenOK[name] = true
						if strings.Contains(body, "degraded") {
							m.degrade[name]++
						}
					}
				}()
			}
			wg.Wait()
		}
	}()
	return m
}

// note writes down a non-200 answer of a pod, the first failedKept of each pod. m.mu is held.
func (m *monitor) note(pod, path string, at time.Time, code int, body string) {
	if len(m.failed[pod]) >= failedKept {
		return
	}
	body = strings.Join(strings.Fields(body), " ")
	if len(body) > failedBody {
		body = body[:failedBody]
	}
	m.failed[pod] = append(m.failed[pod], fmt.Sprintf("%s %s answered %d at %s: %s", pod, path, code, at.Format("15:04:05.000"), body))
}

// report logs the first non-200 answers of every pod, and keeps the logs of the pods that answered or restarted: a pod that a
// later fault deletes or replaces takes its log with it, and the log of the stall is the evidence (F-66). Each log is cut from
// the start of the monitor to now, the previous instance of a restarted pod is kept too, and the files go to the artifact
// directory next to the load records.
func (m *monitor) report(t testing.TB, restarted map[string]int) {
	m.mu.Lock()
	failed := make(map[string][]string, len(m.failed))
	for p, l := range m.failed {
		failed[p] = append([]string(nil), l...)
	}
	m.mu.Unlock()
	keep := map[string]bool{}
	for p, l := range failed {
		keep[p] = true
		for _, line := range l {
			t.Logf("monitor: %s", line)
		}
	}
	for p := range restarted {
		keep[p] = true
	}
	dir := m.dir
	since := m.started.Add(-time.Minute).UTC().Format(time.RFC3339)
	for p := range keep {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Logf("monitor: %v", err)
			return
		}
		for _, previous := range []bool{false, true} {
			args := []string{"-n", cfg.namespace, "logs", p, "-c", "trove", "--timestamps", "--since-time=" + since}
			file := p + ".log"
			if previous {
				if restarted[p] == 0 {
					continue
				}
				args, file = append(args, "--previous"), p+".previous.log"
			}
			out, err := kubectl(args...)
			if err != nil {
				out += "\n" + err.Error()
			}
			_ = os.WriteFile(filepath.Join(dir, file), []byte(out), 0o644)
		}
	}
}

// capture writes the log of a pod that is failing now to m.dir, in the background.
func (m *monitor) capture(pod string, at time.Time) {
	if m.dir == "" {
		return
	}
	m.cap.Add(1)
	go func() {
		defer m.cap.Done()
		out, err := kubectl("-n", cfg.namespace, "logs", pod, "-c", "trove", "--timestamps", "--since-time="+m.started.Add(-time.Minute).UTC().Format(time.RFC3339))
		if err != nil {
			out += "\n" + err.Error()
		}
		if os.MkdirAll(m.dir, 0o755) == nil {
			_ = os.WriteFile(filepath.Join(m.dir, pod+".at-"+at.Format("150405")+".log"), []byte(out), 0o644)
		}
	}()
}

func (m *monitor) stop() {
	close(m.stopCh)
	m.wg.Wait()
	m.cap.Wait()
}

// restarts returns the pods whose restart count rose since the monitor started.
func (m *monitor) restarts(t testing.TB) map[string]int {
	out := map[string]int{}
	for _, p := range livePods(t, selTrove) {
		if was, ok := m.restart[p.Name]; ok && p.Restarts > was {
			out[p.Name] = p.Restarts - was
		}
	}
	return out
}
