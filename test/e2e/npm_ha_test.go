//go:build e2e

package e2e

// Drain and failover under npm load (WP2.8, roadmap M2 E2.11): variant A of R-HA-059's drain test and the pod-crash,
// PostgreSQL-switchover and Valkey-failover scenarios of R-HA-052, each under the npm part of R-HA-059's load
// (npm_load_test.go), in the scatter profile with three api, three packages and four worker replicas. The pod crash is TestNPMHACrash, a set
// of its own: three force-deletes when run by hand, the full ten with TROVE_E2E_NPM_CRASH_SOAK=1 (nightly and closing run).
// The Valkey outage of R-HA-052 belongs to the full suite and runs in the closing run (TestNPMHAClosing). Q-HA-65 and
// Q-HA-67 to Q-HA-69 record the choices.

import (
	"encoding/json"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestNPMHA(t *testing.T) {
	h := npmHAFx(t)

	t.Run("R-HA-059 R-HA-052 R-NPM-046 R-ARCH-024 R-HA-011 E2.11 drain variant A under the npm load: every npm command exits 0, k6 only 2xx and 304, p99 at most twice the warm-up's, every acknowledged publish installs, jobs resume with their attempt, no SIGKILL, nothing Pending for 60 s", func(t *testing.T) {
		npmDrainVariantA(t, h)
	})

	t.Run("R-HA-051 R-HA-052 R-HA-015 E2.11 dev-failover (PostgreSQL switchover) during the npm ci loops: no failed iteration, k6 only 2xx and 304", func(t *testing.T) {
		r := runNPMFault(t, h, "pg-switchover", haFullLoad(), time.Minute, time.Minute, func() { script(t, "failover.sh") })
		r.steady(t)
		r.res.allExitZero(t)
		r.res.loopsSpan(t, r.faultAt, r.faultEnd)
		r.res.K6.only2xx304(t)
		r.res.installPublished(t, h)
	})

	t.Run("R-HA-051 R-HA-052 R-HA-057 E2.11 dev-valkey-failover during the npm ci loops: no failed iteration, k6 only 2xx and 304, /readyz 200 throughout, every replica follows the new primary once", func(t *testing.T) {
		const changes = "trove_valkey_primary_changes_total"
		var before map[string]float64
		r := runNPMFault(t, h, "valkey-failover", haFullLoad(), time.Minute, time.Minute, func() {
			before = replicaMetric(t, changes)
			script(t, "valkey-failover.sh")
		})
		r.steady(t)
		r.res.allExitZero(t)
		r.res.loopsSpan(t, r.faultAt, r.faultEnd)
		r.res.K6.only2xx304(t)
		if len(r.mon.ready) != 0 {
			t.Errorf("/readyz answered non-200 during the Valkey failover: %v", r.mon.ready)
		}
		after := replicaMetric(t, changes)
		for p, v := range after {
			if d := v - before[p]; d != 1 {
				t.Errorf("R-HA-057: %s on %s rose by %v, want 1", changes, p, d)
			}
		}
		t.Logf("%s before %v, after %v", changes, before, after)
		r.res.installPublished(t, h)
	})
}

// TestNPMHACrash is R-HA-052's pod crash under the npm load: three force-deletes on every change, ten (20 minutes) with
// TROVE_E2E_NPM_CRASH_SOAK=1, which the nightly Performance workflow and the closing run set (Q-HA-67).
func TestNPMHACrash(t *testing.T) {
	h := npmHAFx(t)

	t.Run("R-HA-052 R-NPM-046 pod crash: a random api, packages or worker replica force-deleted every 2 min under the npm ci and publish loops, 3 times per change and 10 times over 20 min in the soak: no failed iteration, every listed version's tarball is readable", func(t *testing.T) {
		kills, every := 3, 2*time.Minute
		if os.Getenv("TROVE_E2E_NPM_CRASH_SOAK") == "1" {
			kills = 10
		}
		seed := uint64(time.Now().UnixNano())
		rng := rand.New(rand.NewPCG(seed, seed>>1))
		var victims []string
		r := runNPMFault(t, h, "pod-crash", npmLoadOpts{ciLoops: haCILoops, publish: true}, time.Minute, time.Minute, func() {
			for i := 0; i < kills; i++ {
				ps := livePods(t, selTrove)
				v := ps[rng.IntN(len(ps))]
				t.Logf("kill %d of %d: %s on %s", i+1, kills, v.Name, v.Node)
				forceDelete(t, v)
				victims = append(victims, v.Name)
				time.Sleep(every)
			}
		})
		t.Logf("seed %d, force-deleted %v", seed, victims)
		r.steady(t, victims...)
		r.res.allExitZero(t)
		r.res.loopsSpan(t, r.faultAt, r.faultEnd)
		r.res.committedTarballsReadable(t, h)
		r.res.installPublished(t, h)
		troveSettled(t, 3, 3, 4)
	})
}

// TestNPMHAClosing is the part of the scenarios that R-HA-052 runs in the full suite, the closing run only (Q-HA-67).
func TestNPMHAClosing(t *testing.T) {
	requireClosingRun(t)
	h := npmHAFx(t)

	t.Run("R-HA-052 R-HA-057 R-NPM-046 Valkey outage of 5 minutes with a packages pod deleted meanwhile, under the npm load: no failed iteration, k6 only 2xx and 304, the replacement becomes ready degraded, degraded clears within open_duration + health_check_interval + 5 s", func(t *testing.T) {
		var info struct {
			OpenMS   int64 `json:"valkey_open_duration_ms"`
			HealthMS int64 `json:"valkey_health_check_interval_ms"`
		}
		if err := jsonUnmarshal(driver(t, "info"), &info); err != nil {
			t.Fatal(err)
		}
		bound := time.Duration(info.OpenMS+info.HealthMS)*time.Millisecond + 5*time.Second
		var deleted, replacement string
		var back time.Duration
		startedAt := map[string]bool{}
		r := runNPMFault(t, h, "valkey-outage", haFullLoad(), time.Minute, time.Minute, func() {
			for _, p := range livePods(t, selTrove) {
				startedAt[p.Name] = true
			}
			script(t, "valkey-stop.sh")
			restarted := false
			defer func() {
				if !restarted { // a failure below ends the test; Valkey comes back all the same
					script(t, "valkey-start.sh")
				}
			}()
			down := time.Now()
			time.Sleep(2 * time.Minute)
			deleted = deletePackagesPod(t)
			waitFor(t, "a replacement packages pod ready in degraded mode", 3*time.Minute, 2*time.Second, func() bool {
				for _, p := range livePods(t, selPackages) {
					if startedAt[p.Name] || !p.Ready {
						continue
					}
					if code, body := ops(p.Name, "/readyz"); code == 200 && strings.Contains(body, `"valkey"`) {
						replacement = p.Name
						return true
					}
				}
				return false
			})
			t.Logf("deleted %s; %s became ready with valkey degraded while Valkey was down", deleted, replacement)
			time.Sleep(time.Until(down.Add(5 * time.Minute)))
			watch := startValkeyClearWatch()
			script(t, "valkey-start.sh")
			restarted = true
			var ok bool
			if back, ok = watch.wait(3 * time.Minute); !ok {
				t.Fatalf("R-HA-057: valkey was still degraded on a replica 3 minutes into the return (a Sentinel named a primary at %s)", watch.primaryAt.Format("15:04:05.000"))
			}
			t.Logf("a Sentinel named a primary at %s, the last replica cleared degraded at %s", watch.primaryAt.Format("15:04:05.000"), watch.clearAt.Format("15:04:05.000"))
		})
		r.steady(t, deleted)
		r.res.allExitZero(t)
		r.res.loopsSpan(t, r.faultAt, r.faultEnd)
		r.res.K6.only2xx304(t)
		// the deleted pod drains with its readiness down, and the replacement is not ready before it starts
		for p, n := range r.mon.ready {
			if r.initial[p] && p != deleted {
				t.Errorf("/readyz of %s answered non-200 %d times during the Valkey outage", p, n)
			}
		}
		if back > bound {
			t.Errorf("R-HA-057: degraded cleared %s after Valkey returned, bound open_duration + health_check_interval + 5 s = %s", back.Round(100*time.Millisecond), bound)
		}
		t.Logf("degraded cleared %s after the return (bound %s)", back.Round(100*time.Millisecond), bound)
		r.res.installPublished(t, h)
	})
}

// loadFaultRun is one fault under a load: what the load returned when it stopped, and what the monitor and the clock saw.
type loadFaultRun[R any] struct {
	res      R
	mon      *monitor
	initial  map[string]bool // the trove pods when the monitor started
	restarts map[string]int
	faultAt  time.Time
	faultEnd time.Time
}

// npmFaultRun is one fault under the npm load.
type npmFaultRun = loadFaultRun[npmLoadResult]

// runLoadFault starts the load on a settled release, lets it run for before, injects the fault (the fault waits for its own
// recovery), lets the load run for after and stops it. start begins the load and returns the function that ends it and
// returns its result; note adds the load's numbers to the JSON record next to the fault's.
func runLoadFault[R any](t *testing.T, record, name string, before, after time.Duration, start func() func(*testing.T) R, fault func(), note func(R) map[string]any) loadFaultRun[R] {
	t.Helper()
	troveSettled(t, 3, 3, 4)
	stop := start()
	time.Sleep(before)
	r := loadFaultRun[R]{initial: map[string]bool{}}
	for _, p := range livePods(t, selTrove) {
		r.initial[p.Name] = true
	}
	r.mon = startMonitor(t)
	r.mon.dir = filepath.Join(cfg.out, "podlogs-"+name)
	r.faultAt = time.Now()
	fault()
	r.faultEnd = time.Now()
	time.Sleep(after)
	r.mon.stop()
	r.restarts = r.mon.restarts(t)
	r.mon.report(t, r.restarts)
	r.res = stop(t)
	rec := map[string]any{
		"fault_seconds": r.faultEnd.Sub(r.faultAt).Seconds(), "readyz_non200": r.mon.ready, "healthz_non200": r.mon.health, "restarts": r.restarts,
	}
	maps.Copy(rec, note(r.res))
	noteResult(t, record, rec)
	t.Logf("%s: fault took %s; readyz non-200 per pod %v; healthz non-200 per pod %v; restarts %v",
		name, r.faultEnd.Sub(r.faultAt).Round(time.Second), r.mon.ready, r.mon.health, r.restarts)
	return r
}

// runNPMFault is runLoadFault under the npm load.
func runNPMFault(t *testing.T, h *npmHAFixture, name string, o npmLoadOpts, before, after time.Duration, fault func()) npmFaultRun {
	t.Helper()
	return runLoadFault(t, "npm-ha-"+name, name, before, after, func() func(*testing.T) npmLoadResult {
		return startNPMLoad(t, h, name, o).stop
	}, fault, func(res npmLoadResult) map[string]any {
		return map[string]any{"commands": len(res.Commands), "published": len(res.Published), "k6": k6Summary(res.K6)}
	})
}

// steady requires that no container restarted and that /healthz answered 200 throughout on every pod the fault did
// not remove: the deleted pods (and the replacements, which the monitor met before they ran) are left out.
func (r loadFaultRun[R]) steady(t *testing.T, removed ...string) {
	t.Helper()
	gone := map[string]bool{}
	for _, p := range removed {
		gone[p] = true
	}
	if len(r.restarts) != 0 {
		t.Errorf("restart counts rose: %v", r.restarts)
	}
	for p, n := range r.mon.health {
		if r.initial[p] && !gone[p] {
			t.Errorf("/healthz of %s answered non-200 %d times", p, n)
		}
	}
}

func k6Summary(k *npmK6Result) map[string]any {
	if k == nil {
		return nil
	}
	return map[string]any{"total": k.Total, "status": k.Status, "failures": len(k.Failures), "bad": k.Bad, "dropped": k.Dropped}
}

// drainLoad is a load the drain of variant A runs under; drainOutcome is what it returns when it stops. The drain
// (the node, the pod watch, the probe jobs, the events) is the same under the npm load and under npm and NuGet together,
// the conditions of the clients are the load's.
type drainLoad interface {
	begun() time.Time
	stop(t *testing.T) drainOutcome
}

type drainOutcome interface {
	allExitZero(t *testing.T)
	loopsSpan(t *testing.T, from, to time.Time)
	k6Holds(t *testing.T)
	// k6P99 is the 99th percentile of the duration of every k6 request started in [from, to), and their number
	k6P99(from, to time.Time) (time.Duration, int)
	k6First() time.Time
	installPublished(t *testing.T)
	// conditions are the load's own further conditions and reports (the minimum counts of R-ARCH-024, the p99 of one stream)
	conditions(t *testing.T, firstEviction, drainEnd time.Time)
	note() map[string]any
}

type npmDrainLoad struct {
	*npmLoad
	h *npmHAFixture
}

func (l npmDrainLoad) begun() time.Time { return l.started }
func (l npmDrainLoad) stop(t *testing.T) drainOutcome {
	return npmDrainOutcome{res: l.npmLoad.stop(t), h: l.h}
}

type npmDrainOutcome struct {
	res npmLoadResult
	h   *npmHAFixture
}

func (o npmDrainOutcome) allExitZero(t *testing.T)                   { o.res.allExitZero(t) }
func (o npmDrainOutcome) loopsSpan(t *testing.T, from, to time.Time) { o.res.loopsSpan(t, from, to) }
func (o npmDrainOutcome) k6Holds(t *testing.T)                       { o.res.K6.only2xx304(t) }
func (o npmDrainOutcome) k6P99(from, to time.Time) (time.Duration, int) {
	return o.res.K6.p99(from, to)
}
func (o npmDrainOutcome) k6First() time.Time                          { return o.res.K6.First }
func (o npmDrainOutcome) installPublished(t *testing.T)               { o.res.installPublished(t, o.h) }
func (o npmDrainOutcome) conditions(*testing.T, time.Time, time.Time) {}
func (o npmDrainOutcome) note() map[string]any {
	return map[string]any{"k6": k6Summary(o.res.K6), "commands": len(o.res.Commands), "published": len(o.res.Published)}
}

// npmDrainVariantA is R-HA-059's acceptance test, variant A, with the npm part of its load: two minutes of load before
// the drain and three after it (the length of the variant that runs on every change to main).
func npmDrainVariantA(t *testing.T, h *npmHAFixture) {
	drainVariantA(t, "npm", func() drainLoad { return npmDrainLoad{startNPMLoad(t, h, "drain", haFullLoad()), h} })
}

// drainVariantA is variant A of R-HA-059 under the load start begins: two minutes of load before the drain and three after
// it. kind ("npm", "nuget") names the artifacts. The flow is the one of every variant (drainUnderLoad, drain_load_test.go);
// extras are the parts of E5.9 beside the load.
func drainVariantA(t *testing.T, kind string, start func() drainLoad, extras ...drainExtra) {
	drainUnderLoad(t, kind, variantA, start, extras...)
}

// drainNodeVariantA picks the node of variant A (R-HA-059): it hosts an api, a packages and a worker pod, and neither the
// PostgreSQL primary (variant C, and its PodDisruptionBudget refuses the eviction) nor the Valkey primary (variant B).
// On three worker nodes every node hosts a Sentinel, and may host the Valkey replica or the PostgreSQL standby;
// their own disruption budgets drain them (Q-HA-68). A primary in the way is moved first, before the load starts.
func drainNodeVariantA(t *testing.T) string {
	t.Helper()
	for attempt := 0; attempt < 3; attempt++ {
		hosts := nodesHosting(t, selAPI, selPackages, selMainWorker)
		pgNode := strings.TrimSpace(kn(t, "get", "pod", pgPrimary(t), "-o", "jsonpath={.spec.nodeName}"))
		vkNode := strings.TrimSpace(kn(t, "get", "pod", valkeyPrimaryBySentinel(t), "-o", "jsonpath={.spec.nodeName}"))
		var both []string
		blockedByValkey := false
		for n := range hosts {
			switch n {
			case pgNode:
			case vkNode:
				blockedByValkey = true
			default:
				both = append(both, n)
			}
		}
		sort.Strings(both)
		if len(both) > 0 {
			t.Logf("node %s: PostgreSQL primary on %s, Valkey primary on %s", both[0], pgNode, vkNode)
			return both[0]
		}
		if blockedByValkey {
			t.Logf("the only node with an api, a packages and a worker pod besides the PostgreSQL primary's hosts the Valkey primary (%s); failing it over first", vkNode)
			script(t, "valkey-failover.sh")
			continue
		}
		t.Logf("the only node with an api, a packages and a worker pod is the PostgreSQL primary's (%s); switching the primary over first", pgNode)
		script(t, "failover.sh")
	}
	t.Fatal("no node hosts an api, a packages and a worker pod without the PostgreSQL and the Valkey primary")
	return ""
}

// valkeyPrimaryBySentinel is the pod the Sentinels name as the primary, which is the one the replicas follow. The
// role the Valkey pods report is not enough right after a failover: the promoted replica answers "master" before the
// old primary has been turned into a replica, so for a moment both do.
func valkeyPrimaryBySentinel(t testing.TB) string {
	t.Helper()
	if p, ok := sentinelPrimary(); ok {
		return p
	}
	t.Fatal("no Sentinel names a Valkey primary")
	return ""
}

// sentinelPrimary asks the Sentinels in turn; it never fails the test, so a goroutine may call it.
func sentinelPrimary() (string, bool) {
	for _, s := range []string{"trove-valkey-sentinel-0", "trove-valkey-sentinel-1", "trove-valkey-sentinel-2"} {
		out, err := kubectl("-n", cfg.namespace, "exec", s, "-c", "sentinel", "--", "sh", "-c",
			`exec valkey-cli --tls --cacert /tls/ca.crt -h 127.0.0.1 -p 36379 --no-auth-warning -a "$VALKEY_PASSWORD" sentinel get-master-addr-by-name trove-valkey`)
		host := strings.TrimSpace(strings.SplitN(out, "\n", 2)[0])
		if err == nil && host != "" && host != "(nil)" && !strings.Contains(host, "ERR") {
			return strings.SplitN(host, ".", 2)[0], true
		}
	}
	return "", false
}

// valkeyClearWatch measures R-HA-057's "after the return, degraded clears": from the first moment a Sentinel names a
// primary again until no trove pod lists valkey among its degraded checks. It polls while valkey-start.sh runs,
// because that script waits for both StatefulSets and the operator, and the replicas may have cleared before it ends.
type valkeyClearWatch struct {
	stop      chan struct{}
	done      chan struct{}
	primaryAt time.Time
	clearAt   time.Time
}

func startValkeyClearWatch() *valkeyClearWatch {
	w := &valkeyClearWatch{stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(w.done)
		for {
			select {
			case <-w.stop:
				return
			case <-time.After(250 * time.Millisecond):
			}
			now := time.Now()
			if w.primaryAt.IsZero() {
				if _, ok := sentinelPrimary(); ok {
					w.primaryAt = now
				}
				continue
			}
			raw, err := kubectl("-n", cfg.namespace, "get", "pods", "-l", selTrove, "-o", "json")
			var l struct {
				Items []struct {
					Metadata struct {
						Name              string
						DeletionTimestamp *time.Time
					}
				}
			}
			if err != nil || json.Unmarshal([]byte(raw), &l) != nil || len(l.Items) == 0 {
				continue
			}
			clear := true
			for _, p := range l.Items {
				if p.Metadata.DeletionTimestamp != nil {
					continue
				}
				if code, body := ops(p.Metadata.Name, "/readyz"); code != 200 || strings.Contains(body, `"valkey"`) {
					clear = false
					break
				}
			}
			if clear {
				w.clearAt = now
				return
			}
		}
	}()
	return w
}

// wait returns the time from the primary's return to the last replica's clearing, or false when that did not happen
// within the bound of the wait.
func (w *valkeyClearWatch) wait(within time.Duration) (time.Duration, bool) {
	select {
	case <-w.done:
	case <-time.After(within):
		close(w.stop)
		<-w.done
		return 0, false
	}
	if w.clearAt.IsZero() {
		return 0, false
	}
	return w.clearAt.Sub(w.primaryAt), true
}
