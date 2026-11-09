//go:build e2e

package e2e

// The pooler profile's rows of R-HA-007 and R-HA-058 under the npm load (E5.10): an instance of the request pooler or of the
// control pooler deleted, gracefully and with --grace-period=0 --force, while the eight npm ci loops, the publish loop and k6
// run. The set is ha-pooler (deploy/dev/e2e.sh): the scatter profile with the poolers (TROVE_POOLER=1, deploy/dev/pooler.yaml,
// values-pooler.yaml). docs/developer/e2e.md#the-failure-suite-ha-faults describes the whole; Q-HA-250 to Q-HA-259 record the choices.

import (
	"fmt"
	"testing"
	"time"
)

func TestHAPooler_InstanceLoss(t *testing.T) {
	h := npmHAFx(t)
	requirePoolerProfile(t)
	maxElapsed := time.Duration(0)
	{
		var info struct {
			RetryMaxElapsedMS int64 `json:"retry_max_elapsed_ms"`
		}
		if err := jsonUnmarshal(driver(t, "info"), &info); err != nil {
			t.Fatal(err)
		}
		maxElapsed = time.Duration(info.RetryMaxElapsedMS) * time.Millisecond
	}

	for _, c := range []struct {
		pooler string
		forced bool
	}{{poolerRW, false}, {poolerRW, true}, {poolerControl, false}, {poolerControl, true}} {
		how := "deleted with its grace period"
		if c.forced {
			how = "force-deleted (--grace-period=0)"
		}
		t.Run(fmt.Sprintf("R-HA-058 R-HA-007 R-HA-052 E5.10 an instance of %s %s under the npm load: every npm command exits 0, no trove container restarts, %s", c.pooler, how, map[bool]string{
			false: "k6 only 2xx and 304 and /readyz 200 throughout",
			true:  "k6 failures only 503 within retry.max_elapsed of the kill and none after the pooler is whole again",
		}[c.forced]), func(t *testing.T) {
			var victim string
			r := runNPMFault(t, h, "pooler-"+c.pooler+map[bool]string{false: "-graceful", true: "-forced"}[c.forced], haFullLoad(), time.Minute, time.Minute, func() {
				pods := livePods(t, poolerSel(c.pooler))
				if len(pods) != 2 {
					t.Fatalf("%s has %d live instances, want 2", c.pooler, len(pods))
				}
				victim = pods[0].Name
				if c.forced {
					kn(t, "delete", "pod", victim, "--grace-period=0", "--force", "--wait=false")
				} else {
					kn(t, "delete", "pod", victim, "--wait=false")
				}
				waitFor(t, c.pooler+" whole again: two ready instances on two nodes, none the victim", 6*time.Minute, 2*time.Second, func() bool {
					nodes := map[string]bool{}
					ready := 0
					for _, p := range livePods(t, poolerSel(c.pooler)) {
						if p.Name != victim && p.Ready {
							ready++
							nodes[p.Node] = true
						}
					}
					return ready == 2 && len(nodes) == 2
				})
			})
			r.steady(t)
			r.res.allExitZero(t)
			r.res.loopsSpan(t, r.faultAt, r.faultEnd)
			if !c.forced {
				r.res.K6.only2xx304(t)
				if len(r.mon.ready) != 0 {
					t.Errorf("R-HA-058: /readyz answered non-200 during the graceful stop of a pooler instance: %v", r.mon.ready)
				}
			} else {
				w := haWindow{r.faultAt, r.faultAt.Add(maxElapsed)}
				n := r.res.k6OnlyIn(t, w)
				t.Logf("%d of %d k6 requests failed, all 503 within retry.max_elapsed (%s) of the kill", n, r.res.K6.Total, maxElapsed)
			}
			r.res.installPublished(t, h)
			troveSettled(t, 3, 3, 4)
		})
	}
}
