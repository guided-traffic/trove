//go:build e2e

package e2e

// The failure scenarios of R-HA-052 and the rows of R-HA-007 that E5.10 adds to those of npm_ha_test.go and ha_suite_test.go, under
// the same load (npm_load_test.go: eight npm ci loops, a publish loop, k6 at 120 requests/s): the database at 0 instances for
// 2 minutes, and R-HA-057's Sentinel quorum loss, FLUSHALL and slow Valkey, with a Valkey credential mismatch and an object store
// that rejects trove's credential. The set is ha-faults (deploy/dev/e2e.sh); docs/developer/e2e.md#the-failure-suite-ha-faults
// describes the whole. Q-HA-250 to Q-HA-259 record the choices.

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/npmstub"
)

// ── helpers ──────────────────────────────────────────────────────────────────

// haInfo is what `driver info` reads from the configuration of the release.
type haInfo struct {
	OpenMS         int64 `json:"valkey_open_duration_ms"`
	HealthMS       int64 `json:"valkey_health_check_interval_ms"`
	RefreshMS      int64 `json:"valkey_sentinel_refresh_interval_ms"`
	CommandTimeout int64 `json:"valkey_command_timeout_ms"`
	S3OpenMS       int64 `json:"s3_open_duration_ms"`
}

func readHAInfo(t testing.TB) haInfo {
	t.Helper()
	var info haInfo
	if err := jsonUnmarshal(driver(t, "info"), &info); err != nil {
		t.Fatal(err)
	}
	return info
}

// podSeries reads one series without labels from a pod's /metrics; ok is false when the pod does not answer or has no such series.
func podSeries(podName, series string) (float64, bool) {
	code, body := ops(podName, "/metrics")
	if code != 200 {
		return 0, false
	}
	for _, l := range strings.Split(body, "\n") {
		if rest, found := strings.CutPrefix(l, series+" "); found {
			var v float64
			if _, err := fmt.Sscan(rest, &v); err == nil {
				return v, true
			}
		}
	}
	return 0, false
}

// servingNames are the ready api and packages pods, the ones that hold a Valkey client for requests.
func servingNames(t testing.TB) []string { t.Helper(); return readyServingPods(t) }

// waitSeries polls the series on every named pod every 250 ms until each one reads want, and returns the time it took. It fails
// the test when that takes longer than within.
func waitSeries(t testing.TB, pods []string, series string, want float64, within time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	for {
		var wrong []string
		for _, p := range pods {
			if v, ok := podSeries(p, series); !ok || v != want {
				wrong = append(wrong, p)
			}
		}
		if len(wrong) == 0 {
			return time.Since(start)
		}
		if time.Since(start) > within {
			t.Fatalf("%s did not read %v on %v within %s", series, want, wrong, within)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// seriesPeaks samples a series on the named pods every 500 ms until stopped and keeps the highest value each pod showed.
type seriesPeaks struct {
	mu    sync.Mutex
	peak  map[string]float64
	stop  chan struct{}
	done  chan struct{}
	every time.Duration
}

func startSeriesPeaks(pods []string, series string) *seriesPeaks {
	s := &seriesPeaks{peak: map[string]float64{}, stop: make(chan struct{}), done: make(chan struct{}), every: 500 * time.Millisecond}
	go func() {
		defer close(s.done)
		for {
			for _, p := range pods {
				if v, ok := podSeries(p, series); ok {
					s.mu.Lock()
					if v > s.peak[p] {
						s.peak[p] = v
					}
					s.mu.Unlock()
				}
			}
			select {
			case <-s.stop:
				return
			case <-time.After(s.every):
			}
		}
	}()
	return s
}

func (s *seriesPeaks) end() map[string]float64 {
	close(s.stop)
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]float64{}
	for k, v := range s.peak {
		out[k] = v
	}
	return out
}

// allReady is whether every live trove pod answers /readyz with 200 and the edge answers the ping.
func allReady(t testing.TB) bool {
	t.Helper()
	for _, p := range livePods(t, selTrove) {
		if code, _ := ops(p.Name, "/readyz"); code != 200 {
			return false
		}
	}
	code, _, _, err := edge("/api/v1/system/ping")
	return err == nil && code == 200
}

// valkeyDegraded is whether /readyz of the pod answers 200 and lists valkey among the degraded checks.
func valkeyDegraded(podName string) bool {
	code, body := ops(podName, "/readyz")
	return code == 200 && strings.Contains(body, `"valkey"`)
}

// k6OnlyIn requires that every k6 failure is a 503 of the given kind that ran inside the window, and returns how many there were.
func (r npmLoadResult) k6OnlyIn(t *testing.T, w haWindow, kinds ...string) int {
	t.Helper()
	if r.K6 == nil {
		t.Fatal("the load ran without k6")
	}
	var bad []npmK6Line
	for _, l := range r.K6.Failures {
		kindOK := len(kinds) == 0
		for _, k := range kinds {
			kindOK = kindOK || l.Kind == k
		}
		if !kindOK || l.Status != 503 || !w.overlaps(l.At, l.At.Add(time.Duration(l.MS)*time.Millisecond)) {
			bad = append(bad, l)
		}
	}
	if len(bad) != 0 {
		t.Errorf("%d of %d k6 failures were not a 503 of %v inside %s to %s: first %+v", len(bad), len(r.K6.Failures), kinds,
			w.from.Format("15:04:05"), w.to.Format("15:04:05"), bad[:min(5, len(bad))])
	}
	return len(r.K6.Failures)
}

// ── R-HA-052: PostgreSQL at 0 instances ──────────────────────────────────────

func TestHAFaults_PostgresOutage(t *testing.T) {
	h := npmHAFx(t)

	t.Run("R-HA-052 R-HA-007 R-HA-008 E5.10 PostgreSQL stopped in every instance for 2 minutes under the npm load: no container restarts, /healthz 200, every replica turns unready and ready again, back within 30 s of the database's return, failures only inside the outage and that bound and only 503, every acknowledged publish installs", func(t *testing.T) {
		const outage, recovery = 2 * time.Minute, 30 * time.Second
		var backAt time.Time
		var back time.Duration
		r := runNPMFault(t, h, "pg-outage", haFullLoad(), time.Minute, 90*time.Second, func() {
			script(t, "pg-stop.sh", "2m")
			backAt = time.Now() // the cluster reports two ready instances again
			back = waitFor(t, "every trove pod ready and the edge answering again", 5*time.Minute, 500*time.Millisecond, func() bool { return allReady(t) })
		})
		r.steady(t)
		turned := 0
		for _, p := range sortedKeys(r.initial) {
			if r.mon.ready[p] > 0 {
				turned++
			}
		}
		if turned != len(r.initial) {
			t.Errorf("R-HA-007: %d of %d replicas turned unready while the database had no instance: %v", turned, len(r.initial), r.mon.ready)
		}
		w := haWindow{r.faultAt, backAt.Add(recovery)}
		t.Logf("PostgreSQL was away %s to %s (fence and unfence %s); every pod ready %s after the cluster reported healthy", r.faultAt.Format("15:04:05"), backAt.Format("15:04:05"), r.faultEnd.Sub(r.faultAt).Round(time.Second), back.Round(100*time.Millisecond))
		if back > recovery {
			t.Errorf("R-HA-052: full recovery took %s after the database returned, bound %s", back.Round(100*time.Millisecond), recovery)
		}
		if r.faultEnd.Sub(r.faultAt) < outage {
			t.Errorf("the database was away %s, want at least %s", r.faultEnd.Sub(r.faultAt).Round(time.Second), outage)
		}
		failed := r.res.k6OnlyIn(t, w)
		t.Logf("%d of %d k6 requests failed, all 503 inside the outage and the recovery bound", failed, r.res.K6.Total)
		r.res.outageCommands(t, w)
		r.res.installPublished(t, h)
		troveSettled(t, 3, 3, 4)
	})
}

// ── R-HA-057: Sentinel quorum loss, FLUSHALL, a slow Valkey ──────────────────

func TestHAFaults_ValkeySentinelQuorumLoss(t *testing.T) {
	h := npmHAFx(t)

	t.Run("R-HA-052 R-HA-057 R-HA-007 E5.10 Sentinel quorum loss under the npm load (3, then 1, then 0 Sentinels, the primary healthy): every npm command exits 0, k6 only 2xx and 304, /readyz 200 and no valkey degraded on any replica, the gauge follows within valkey.sentinel.refresh_interval + 1 s, no failover", func(t *testing.T) {
		const gauge = "trove_valkey_sentinels_reachable"
		const changes = "trove_valkey_primary_changes_total"
		info := readHAInfo(t)
		bound := time.Duration(info.RefreshMS)*time.Millisecond + time.Second
		serving := servingNames(t)
		var to1, to0, to3 time.Duration
		var changesBefore map[string]float64
		r := runNPMFault(t, h, "sentinel-quorum-loss", haFullLoad(), time.Minute, time.Minute, func() {
			for _, p := range serving {
				if v, ok := podSeries(p, gauge); !ok || v != 3 {
					t.Errorf("R-HA-057: %s reads %v on %s with all three Sentinels up, want 3", gauge, v, p)
				}
			}
			changesBefore = replicaMetric(t, changes)
			restarted := false
			defer func() {
				if !restarted { // a failure below ends the test; the Sentinels come back all the same
					script(t, "valkey-start.sh")
				}
			}()
			script(t, "sentinel-scale.sh", "1")
			to1 = waitSeries(t, serving, gauge, 1, 2*time.Minute)
			time.Sleep(45 * time.Second)
			script(t, "sentinel-scale.sh", "0")
			to0 = waitSeries(t, serving, gauge, 0, 2*time.Minute)
			time.Sleep(45 * time.Second)
			script(t, "valkey-start.sh")
			restarted = true
			to3 = waitSeries(t, serving, gauge, 3, 2*time.Minute)
		})
		r.steady(t)
		r.res.allExitZero(t)
		r.res.loopsSpan(t, r.faultAt, r.faultEnd)
		r.res.K6.only2xx304(t)
		if len(r.mon.ready) != 0 {
			t.Errorf("R-HA-057: /readyz answered non-200 during the Sentinel quorum loss: %v", r.mon.ready)
		}
		if len(r.mon.degrade) != 0 {
			t.Errorf("R-HA-057: replicas reported valkey degraded while the primary was healthy: %v", r.mon.degrade)
		}
		t.Logf("%s followed the loss to 1 after %s and to 0 after %s (bound valkey.sentinel.refresh_interval + 1 s = %s, counted from the return of the scale), and was back at 3 after %s", gauge, to1.Round(10*time.Millisecond), to0.Round(10*time.Millisecond), bound, to3.Round(10*time.Millisecond))
		if to1 > bound || to0 > bound {
			t.Errorf("R-HA-057: the gauge needed %s to reach 1 and %s to reach 0, bound valkey.sentinel.refresh_interval + 1 s = %s", to1, to0, bound)
		}
		for p, v := range replicaMetric(t, changes) {
			if v != changesBefore[p] {
				t.Errorf("R-HA-057: %s on %s changed from %v to %v: a failover happened while the primary was healthy", changes, p, changesBefore[p], v)
			}
		}
		r.res.installPublished(t, h)
		troveSettled(t, 3, 3, 4)
	})
}

func TestHAFaults_ValkeyFlushAll(t *testing.T) {
	h := npmHAFx(t)

	t.Run("R-HA-052 R-HA-057 R-HA-007 E5.10 FLUSHALL on the Valkey primary while documents are in flight, under the npm load: every npm command exits 0, k6 only 2xx and 304, /readyz 200 throughout, every request for an in-flight document answers 200 and the counting upstream records at most one fetch per replica asked for it", func(t *testing.T) {
		s := stub(t)
		names := []string{"@cobalt/lucid-dial", "@cobalt/nimble-ledger", "@cobalt/ivory-ember"}
		const asked = 2 // burstOver asks two packages replicas
		repos := h.fx.upstreamRepos(t, "e510f", nil)
		statuses := map[string][]int{}
		fetches := map[string]int{}
		r := runNPMFault(t, h, "valkey-flushall", haFullLoad(), time.Minute, time.Minute, func() {
			s.resetAll(t)
			s.setMode(t, npmstub.Mode{DelayMS: map[string]int{"document": 4000}})
			defer s.resetAll(t)
			webs := packagesReplicas(t, 3)
			var mu sync.Mutex
			var wg sync.WaitGroup
			for _, name := range names {
				wg.Add(1)
				go func() {
					defer wg.Done()
					got := burstOver(t, webs, npmAddr(repos.remote)+"/"+url.PathEscape(name), h.fx.token, 10)
					mu.Lock()
					statuses[name] = got
					mu.Unlock()
				}()
			}
			time.Sleep(2 * time.Second) // one replica leads each flight and waits for the upstream, the others wait for it
			t.Logf("FLUSHALL: %s", strings.TrimSpace(valkeyPrimaryCLI(t, "FLUSHALL")))
			wg.Wait()
			c := s.counts(t)
			for _, name := range names {
				fetches[name] = c.Documents(name)
			}
		})
		r.steady(t)
		r.res.allExitZero(t)
		r.res.loopsSpan(t, r.faultAt, r.faultEnd)
		r.res.K6.only2xx304(t)
		if len(r.mon.ready) != 0 {
			t.Errorf("R-HA-057: /readyz answered non-200 around the FLUSHALL: %v", r.mon.ready)
		}
		for _, name := range names {
			for i, st := range statuses[name] {
				if st != 200 {
					t.Errorf("R-HA-057: request %d for %s answered %d", i, name, st)
				}
			}
			t.Logf("the upstream recorded %d document requests for %s (replicas asked: %d)", fetches[name], name, asked)
			if fetches[name] < 1 || fetches[name] > asked {
				t.Errorf("R-HA-057: the upstream recorded %d document requests for %s, want between 1 and %d (one per replica asked)", fetches[name], name, asked)
			}
		}
		r.res.installPublished(t, h)
		troveSettled(t, 3, 3, 4)
	})
}

func TestHAFaults_ValkeySlow(t *testing.T) {
	h := npmHAFx(t)

	t.Run("R-HA-052 R-HA-057 R-HA-007 E5.10 a Valkey that answers every command up to 2 s late under the npm load: every npm command exits 0, k6 only 2xx and 304, /readyz 200 throughout, the circuit opens on every serving replica, the k6 p99 stays below its baseline plus twice valkey.command_timeout, degraded clears within open_duration + health_check_interval + 5 s of the end", func(t *testing.T) {
		info := readHAInfo(t)
		commandTimeout := time.Duration(info.CommandTimeout) * time.Millisecond
		clearBound := time.Duration(info.OpenMS+info.HealthMS)*time.Millisecond + 5*time.Second
		serving := servingNames(t)
		var peaks *seriesPeaks
		var cleared time.Duration
		r := runNPMFault(t, h, "valkey-slow", haFullLoad(), time.Minute, time.Minute, func() {
			peaks = startSeriesPeaks(serving, "trove_valkey_circuit_open")
			script(t, "valkey-slow.sh", "3m", "2000")
			end := time.Now()
			cleared = waitFor(t, "valkey no longer degraded on any replica", 3*time.Minute, 250*time.Millisecond, func() bool {
				for _, p := range serving {
					if valkeyDegraded(p) {
						return false
					}
				}
				return true
			})
			t.Logf("the pause ended at %s; degraded had cleared on every serving replica %s later", end.Format("15:04:05"), cleared.Round(100*time.Millisecond))
		})
		opened := peaks.end()
		r.steady(t)
		r.res.allExitZero(t)
		r.res.loopsSpan(t, r.faultAt, r.faultEnd)
		r.res.K6.only2xx304(t)
		if len(r.mon.ready) != 0 {
			t.Errorf("R-HA-057: /readyz answered non-200 while Valkey was slow: %v", r.mon.ready)
		}
		for _, p := range serving {
			if opened[p] != 1 {
				t.Errorf("R-HA-057: the circuit of %s never opened while every Valkey command waited up to 2 s (peak %v)", p, opened[p])
			}
		}
		baseline, nBase := r.res.K6.p99(r.res.K6.First, r.faultAt)
		during, nDuring := r.res.K6.p99(r.faultAt, r.faultEnd)
		limit := baseline + 2*commandTimeout
		t.Logf("k6 p99: baseline %s over %d requests, while Valkey was slow %s over %d (bound baseline + 2 x valkey.command_timeout %s = %s)", baseline, nBase, during, nDuring, commandTimeout, limit)
		if nBase < 1000 || nDuring < 1000 {
			t.Errorf("too few k6 requests for the latency condition: %d before, %d during", nBase, nDuring)
		}
		if during > limit {
			t.Errorf("R-HA-057: the k6 p99 while Valkey was slow is %s, more than the baseline %s plus twice valkey.command_timeout (%s)", during, baseline, limit)
		}
		if cleared > clearBound {
			t.Errorf("R-HA-057: degraded cleared %s after the slowness ended, bound open_duration + health_check_interval + 5 s = %s", cleared.Round(100*time.Millisecond), clearBound)
		}
		r.res.installPublished(t, h)
		troveSettled(t, 3, 3, 4)
	})
}

// ── R-HA-007: Valkey credential mismatch ─────────────────────────────────────

func TestHAFaults_ValkeyCredentialMismatch(t *testing.T) {
	h := npmHAFx(t)

	t.Run("R-HA-007 R-HA-052 R-HA-056 E5.10 Valkey rejects trove's password under the npm load: running replicas stay ready with valkey degraded and no client fails, a replacement pod is not ready until the password matches again, and everything clears after the fix", func(t *testing.T) {
		serving := servingNames(t)
		var deleted, replacement string
		var degradedAfter, replacementReady, clearedAfter time.Duration
		samples := 0
		started := map[string]bool{}
		r := runNPMFault(t, h, "valkey-credential-mismatch", haFullLoad(), time.Minute, time.Minute, func() {
			for _, p := range livePods(t, selTrove) {
				started[p.Name] = true
			}
			fixed := false
			defer func() {
				if !fixed { // a failure below ends the test; the password comes back all the same
					script(t, "valkey-credential.sh", "fix")
				}
			}()
			script(t, "valkey-credential.sh", "break")
			broke := time.Now()
			waitFor(t, "valkey degraded on every serving replica", 2*time.Minute, 500*time.Millisecond, func() bool {
				for _, p := range serving {
					if !valkeyDegraded(p) {
						return false
					}
				}
				return true
			})
			degradedAfter = time.Since(broke)
			deleted = deletePackagesPod(t)
			waitFor(t, "a replacement packages pod", 2*time.Minute, 2*time.Second, func() bool {
				for _, p := range livePods(t, selPackages) {
					if !started[p.Name] {
						replacement = p.Name
						return true
					}
				}
				return false
			})
			for i := 0; i < 15; i++ { // R-HA-007: a configuration error gates the first readiness, so the replacement is not ready
				time.Sleep(2 * time.Second)
				samples++
				if p := podByName(t, replacement); p.Ready {
					t.Errorf("R-HA-007: the replacement %s turned ready at %s while Valkey rejected its password", replacement, time.Now().Format("15:04:05"))
				}
			}
			script(t, "valkey-credential.sh", "fix")
			fixed = true
			fixedAt := time.Now()
			replacementReady = waitFor(t, "the replacement pod ready", 4*time.Minute, time.Second, func() bool { return podByName(t, replacement).Ready })
			clearedAfter = waitFor(t, "valkey no longer degraded on any replica", 3*time.Minute, 500*time.Millisecond, func() bool {
				for _, p := range readyServingPods(t) {
					if valkeyDegraded(p) {
						return false
					}
				}
				return true
			})
			t.Logf("valkey degraded on every serving replica %s after the password changed; replacement %s ready %s and degraded cleared %s after the fix (%s)", degradedAfter.Round(time.Second), replacement, replacementReady.Round(time.Second), clearedAfter.Round(time.Second), fixedAt.Format("15:04:05"))
		})
		r.steady(t, deleted)
		r.res.allExitZero(t)
		r.res.loopsSpan(t, r.faultAt, r.faultEnd)
		r.res.K6.only2xx304(t)
		for p, n := range r.mon.ready {
			if r.initial[p] && p != deleted {
				t.Errorf("R-HA-007: /readyz of %s answered non-200 %d times while Valkey rejected the password", p, n)
			}
		}
		if samples < 10 {
			t.Errorf("the replacement was sampled %d times, want at least 10", samples)
		}
		r.res.installPublished(t, h)
		troveSettled(t, 3, 3, 4)
	})
}

// ── R-HA-007: the object store rejects trove's credential ────────────────────

func TestHAFaults_ObjectStoreCredentialRejected(t *testing.T) {
	h := npmHAFx(t)

	t.Run("R-HA-007 R-HA-052 R-STOR-031 R-STOR-033 the object store's secret key replaced by a wrong one under the npm load: the circuit opens, documents and the API keep answering, blobs answer 503, no replica turns unready, the log names the credential source and no secret, and everything works after the key is put back", func(t *testing.T) {
		info := readHAInfo(t)
		open := time.Duration(info.S3OpenMS) * time.Millisecond
		serving := servingNames(t)
		const field = "secret-access-key"
		orig := strings.TrimSpace(kn(t, "get", "secret", "trove-s3", "-o", `go-template={{index .data "`+field+`"}}`))
		if orig == "" {
			t.Fatal("the Secret trove-s3 has no " + field)
		}
		const wrong = "e2e-wrong-secret-key-0123456789abcdef0123456789"
		var patchedAt, restoredAt, closedAt time.Time
		var peaks *seriesPeaks
		patch := func(b64 string) {
			kn(t, "patch", "secret", "trove-s3", "--type=merge", "-p", `{"data":{"`+field+`":"`+b64+`"}}`)
		}
		r := runNPMFault(t, h, "s3-credential-rejected", haFullLoad(), time.Minute, 2*time.Minute, func() {
			peaks = startSeriesPeaks(serving, "trove_storage_s3_circuit_open")
			restored := false
			// The internal-secrets-operator owns the field (annotation iso.gtrfc.com/autogenerate); the annotations come off while
			// the key is wrong, so that the operator has nothing to put back, and go back on with the key (secrets.yaml)
			kn(t, "annotate", "secret", "trove-s3", "iso.gtrfc.com/autogenerate-", "iso.gtrfc.com/length-")
			putBack := func() {
				patch(orig)
				kn(t, "annotate", "secret", "trove-s3", "--overwrite", "iso.gtrfc.com/autogenerate=secret-access-key", "iso.gtrfc.com/length=40")
			}
			defer func() {
				if !restored { // a failure below ends the test; the key comes back all the same
					putBack()
				}
			}()
			patchedAt = time.Now()
			patch(base64.StdEncoding.EncodeToString([]byte(wrong)))
			go func() { // says whether anything puts the Secret back while the test waits
				for i := 0; i < 12; i++ {
					time.Sleep(20 * time.Second)
					if got, err := kubectl("-n", cfg.namespace, "get", "secret", "trove-s3", "-o", `go-template={{index .data "`+field+`"}}`); err == nil && strings.TrimSpace(got) != base64.StdEncoding.EncodeToString([]byte(wrong)) {
						fmt.Printf("the Secret trove-s3 no longer holds the wrong key %ds after the patch\n", (i+1)*20)
						return
					}
				}
			}()
			// the kubelet writes the new file into the pods within a minute or two; the store rejects every request signed with it
			waitFor(t, "the circuit open on a serving replica", 5*time.Minute, time.Second, func() bool {
				for _, p := range serving {
					if v, ok := podSeries(p, "trove_storage_s3_circuit_open"); ok && v == 1 {
						return true
					}
				}
				return false
			})
			time.Sleep(30 * time.Second)
			putBack()
			restored = true
			restoredAt = time.Now()
			waitFor(t, "the circuit closed on every serving replica", 5*time.Minute, time.Second, func() bool {
				for _, p := range serving {
					if v, ok := podSeries(p, "trove_storage_s3_circuit_open"); !ok || v != 0 {
						return false
					}
				}
				return true
			})
			closedAt = time.Now()
		})
		opened := peaks.end()
		r.steady(t)
		r.res.loopsSpan(t, r.faultAt, r.faultEnd)
		w := haWindow{patchedAt, closedAt.Add(open + 10*time.Second)}
		t.Logf("the key was wrong from %s to %s, the circuits were closed again at %s; circuit peaks %v", patchedAt.Format("15:04:05"), restoredAt.Format("15:04:05"), closedAt.Format("15:04:05"), opened)
		if n := r.res.k6OnlyIn(t, w, "tarball"); n == 0 {
			t.Errorf("R-HA-007: no blob request failed while the store rejected the credential: the rejection did not show")
		}
		if r.res.K6.Status["document 200"] == 0 || r.res.K6.Status["tarball 200"] == 0 {
			t.Errorf("k6 did not exercise documents and tarballs: %v", r.res.K6.Status)
		}
		for p, n := range r.mon.ready {
			if r.initial[p] {
				t.Errorf("R-HA-008: /readyz of %s answered non-200 %d times while the store rejected the credential", p, n)
			}
		}
		r.res.outageCommands(t, w)
		credLog := regexp.MustCompile(`credential_source"?[=:]"?static`)
		named := 0
		for _, p := range serving {
			log := kn(t, "logs", p, "--since=30m")
			if strings.Contains(log, wrong) || strings.Contains(log, base64.StdEncoding.EncodeToString([]byte(wrong))) {
				t.Errorf("R-HA-007: the log of %s holds the wrong secret key", p)
			}
			if credLog.MatchString(log) && strings.Contains(log, "rejects the credential") {
				named++
			}
		}
		if named == 0 {
			t.Errorf("R-HA-007: no serving replica logged that the store rejects the credential with its credential source")
		}
		r.res.installPublished(t, h)
		troveSettled(t, 3, 3, 4)
	})
}
