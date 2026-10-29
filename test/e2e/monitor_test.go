//go:build e2e

package e2e

import (
	"strings"
	"sync"
	"testing"
	"time"
)

// monitor polls /healthz and /readyz of every web and worker pod once a second, straight at the
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
}

func startMonitor(t testing.TB) *monitor {
	m := &monitor{health: map[string]int{}, ready: map[string]int{}, seenOK: map[string]bool{}, degrade: map[string]int{}, stopCh: make(chan struct{}), restart: map[string]int{}}
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
					h, _ := ops(name, "/healthz")
					r, body := ops(name, "/readyz")
					m.mu.Lock()
					defer m.mu.Unlock()
					m.samples++
					if h != 200 {
						m.health[name]++
					}
					if r != 200 {
						m.ready[name]++
					} else {
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

func (m *monitor) stop() {
	close(m.stopCh)
	m.wg.Wait()
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
