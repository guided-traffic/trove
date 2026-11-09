//go:build e2e

package e2e

// R-HA-057's planned failover with its 10-second bound for every replica, under the npm load (E5.10). The failover itself under
// the load is TestNPMHA; this test adds the bound that one counts per replica. The set is ha-faults.

import (
	"sync"
	"testing"
	"time"
)

func TestHAFaults_ValkeyPlannedFailover(t *testing.T) {
	h := npmHAFx(t)

	t.Run("R-HA-052 R-HA-057 E5.10 SENTINEL FAILOVER under the npm load: every npm command exits 0, k6 only 2xx and 304, /readyz 200 throughout, trove_valkey_primary_changes_total rises by one on every replica within 10 s of the request", func(t *testing.T) {
		const changes = "trove_valkey_primary_changes_total"
		const bound = 10 * time.Second
		pods := podNames(livePods(t, selTrove))
		var before map[string]float64
		followed := map[string]time.Duration{}
		var mu sync.Mutex
		r := runNPMFault(t, h, "valkey-planned-failover", haFullLoad(), time.Minute, time.Minute, func() {
			before = replicaMetric(t, changes)
			asked := time.Now()
			stop := make(chan struct{})
			done := make(chan struct{})
			go func() {
				defer close(done)
				for {
					for _, p := range pods {
						mu.Lock()
						_, seen := followed[p]
						mu.Unlock()
						if v, ok := podSeries(p, changes); ok && !seen && v > before[p] {
							mu.Lock()
							followed[p] = time.Since(asked)
							mu.Unlock()
						}
					}
					select {
					case <-stop:
						return
					case <-time.After(250 * time.Millisecond):
					}
				}
			}()
			script(t, "valkey-failover.sh")
			close(stop)
			<-done
		})
		r.steady(t)
		r.res.allExitZero(t)
		r.res.loopsSpan(t, r.faultAt, r.faultEnd)
		r.res.K6.only2xx304(t)
		if len(r.mon.ready) != 0 {
			t.Errorf("R-HA-057: /readyz answered non-200 during the planned failover: %v", r.mon.ready)
		}
		for _, p := range pods {
			d, ok := followed[p]
			t.Logf("%s followed the new primary %s after the request", p, d.Round(10*time.Millisecond))
			if !ok || d > bound {
				t.Errorf("R-HA-057: %s did not follow the new primary within %s of SENTINEL FAILOVER (followed: %v after %s)", p, bound, ok, d)
			}
		}
		for p, v := range replicaMetric(t, changes) {
			if v-before[p] != 1 {
				t.Errorf("R-HA-057: %s on %s rose by %v, want 1", changes, p, v-before[p])
			}
		}
		r.res.installPublished(t, h)
		troveSettled(t, 3, 3, 4)
	})
}
