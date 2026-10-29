//go:build e2e

package e2e

import (
	"sort"
	"strings"
	"testing"
	"time"
)

// faultRun is one E0.13 scenario: the k6 load at 100 requests/s through the Ingress, the driver
// that writes a change and enqueues a job every second, and a monitor of every replica's probes,
// all running while fault injects the failure and waits for the recovery.
type faultRun struct {
	k6       k6result
	driver   loadSummary
	mon      *monitor
	restarts map[string]int
	faultAt  time.Time
	faultEnd time.Time
}

func runFault(t *testing.T, name string, fault func()) faultRun {
	t.Helper()
	d := startDriverLoad(t, name, 15*time.Minute)
	k := startK6(t, name, 20*time.Minute)
	time.Sleep(10 * time.Second) // a steady state before the fault
	mon := startMonitor(t)
	var r faultRun
	r.faultAt = time.Now()
	fault()
	r.faultEnd = time.Now()
	time.Sleep(20 * time.Second) // and after it
	mon.stop()
	driver(t, "stop")
	r.driver = d.wait(t)
	r.k6 = k.stop(t)
	r.mon = mon
	r.restarts = mon.restarts(t)
	noteResult(t, "e13-"+name, map[string]any{
		"fault_seconds": r.faultEnd.Sub(r.faultAt).Seconds(), "k6_status": r.k6.Status, "k6_replicas": r.k6.Replicas,
		"readyz_non200": mon.ready, "healthz_non200": mon.health, "restarts": r.restarts, "driver": r.driver,
	})
	t.Logf("%s: fault took %s; readyz non-200 per pod %v; healthz non-200 per pod %v; restarts %v",
		name, r.faultEnd.Sub(r.faultAt).Round(time.Second), mon.ready, mon.health, r.restarts)
	if len(r.restarts) != 0 {
		t.Errorf("restart counts rose: %v", r.restarts)
	}
	if len(mon.health) != 0 {
		t.Errorf("/healthz answered non-200: %v", mon.health)
	}
	return r
}

func (r faultRun) valkeyBounds(t *testing.T) {
	t.Helper()
	s := r.driver
	pollEpoch, pollJobs := float64(s.Bounds["epoch_poll_interval_ms"]), float64(s.Bounds["jobs_poll_interval_ms"])
	if s.Changes.NotPropagated != 0 || s.Changes.WriteErrors != 0 {
		t.Errorf("changes: %d not propagated, %d write errors (first: %v)", s.Changes.NotPropagated, s.Changes.WriteErrors, s.FirstErrors)
	}
	if s.Jobs.EnqueueErrors != 0 || s.Jobs.Unfinished != 0 {
		t.Errorf("jobs: %d enqueue errors, %d unfinished (first: %v)", s.Jobs.EnqueueErrors, s.Jobs.Unfinished, s.FirstErrors)
	}
	if s.Changes.All.N < 20 || s.Jobs.All.N < 20 {
		t.Errorf("too few samples: %d changes, %d jobs", s.Changes.All.N, s.Jobs.All.N)
	}
	if s.Changes.All.P99 > pollEpoch+1000 {
		t.Errorf("R-ARCH-027 P-13: change propagation p99 %.0f ms above cluster.epoch_poll_interval + 1 s (%.0f ms)", s.Changes.All.P99, pollEpoch+1000)
	}
	if s.Jobs.All.P99 > pollJobs+1000 {
		t.Errorf("R-ARCH-027 P-14: job pick-up p99 %.0f ms above jobs.poll_interval + 1 s (%.0f ms)", s.Jobs.All.P99, pollJobs+1000)
	}
}

func TestE13Failover(t *testing.T) {
	t.Run("R-HA-051 R-HA-015 R-HA-052 E0.13 dev-failover (PostgreSQL switchover) under load: k6 records only 200", func(t *testing.T) {
		r := runFault(t, "pg-switchover", func() { script(t, "failover.sh") })
		r.k6.only200(t)
	})

	t.Run("R-HA-015 R-HA-052 E0.13 PostgreSQL primary force-deleted: failures only within retry.max_elapsed, all 503, recovery without intervention", func(t *testing.T) {
		maxElapsed := time.Duration(0)
		var info struct {
			RetryMaxElapsedMS int64 `json:"retry_max_elapsed_ms"`
		}
		if err := jsonUnmarshal(driver(t, "info"), &info); err != nil {
			t.Fatal(err)
		}
		maxElapsed = time.Duration(info.RetryMaxElapsedMS) * time.Millisecond
		r := runFault(t, "pg-kill", func() { script(t, "pg-kill.sh") })
		var late []failure
		other := 0
		for _, f := range r.k6.Failures {
			if f.Status != 503 {
				other++
			}
			if f.At.After(r.faultAt.Add(maxElapsed)) {
				late = append(late, f)
			}
		}
		var first, last time.Time
		for _, f := range r.k6.Failures {
			if first.IsZero() || f.At.Before(first) {
				first = f.At
			}
			if f.At.After(last) {
				last = f.At
			}
		}
		t.Logf("%d failures of %d requests, status %v; first %s after the kill, last %s after; retry.max_elapsed %s",
			len(r.k6.Failures), r.k6.Total, r.k6.Status, first.Sub(r.faultAt).Round(100*time.Millisecond), last.Sub(r.faultAt).Round(100*time.Millisecond), maxElapsed)
		if other != 0 {
			t.Errorf("%d failures were not 503: %v", other, r.k6.Status)
		}
		if len(late) != 0 {
			t.Errorf("%d failures later than retry.max_elapsed (%s) after the kill, the last at +%s", len(late), maxElapsed, last.Sub(r.faultAt).Round(100*time.Millisecond))
		}
		if tailFails := countAfter(r.k6.Failures, r.faultEnd); tailFails != 0 {
			t.Errorf("%d failures after the cluster reported healthy: no recovery without intervention", tailFails)
		}
		if code, _, body, err := edge("/api/v1/system/ping"); err != nil || code != 200 {
			t.Errorf("ping after the recovery: %d %q %v", code, body, err)
		}
		if r.driver.Changes.Written < 20 || r.driver.Jobs.Enqueued < 20 {
			t.Errorf("the driver wrote %d changes and %d jobs: writes did not resume", r.driver.Changes.Written, r.driver.Jobs.Enqueued)
		}
	})

	t.Run("R-ARCH-024 R-HA-034 E0.13 PostgreSQL stopped for 30 s: /healthz stays 200, /readyz turns 503 and back, no restart", func(t *testing.T) {
		r := runFault(t, "pg-stop", func() { script(t, "pg-stop.sh", "30s") })
		turned := 0
		for _, n := range r.mon.ready {
			if n > 0 {
				turned++
			}
		}
		if turned == 0 {
			t.Errorf("/readyz never answered 503 on any replica while PostgreSQL was stopped")
		}
		for _, p := range livePods(t, selTrove) {
			if code, body := ops(p.Name, "/readyz"); code != 200 {
				t.Errorf("%s /readyz = %d after the recovery: %s", p.Name, code, strings.TrimSpace(body))
			}
		}
		for _, f := range r.k6.Failures {
			if f.Status != 503 {
				t.Errorf("failure %+v is not a 503", f)
				break
			}
		}
		if r.driver.Changes.Written < 20 {
			t.Errorf("the driver wrote %d changes: writes did not resume", r.driver.Changes.Written)
		}
	})

	for _, c := range []struct {
		name, script string
		args         []string
	}{
		{"dev-valkey-failover", "valkey-failover.sh", nil},
		{"dev-valkey-kill", "valkey-kill.sh", nil},
		{"dev-valkey-outage 60s", "valkey-outage.sh", []string{"60s"}},
	} {
		t.Run("R-HA-056 Q-HA-13 R-ARCH-027 E0.13 "+c.name+" under load: only 200, /readyz 200 on every replica throughout, changes and jobs keep P-13 and P-14 without Valkey", func(t *testing.T) {
			r := runFault(t, strings.ReplaceAll(c.name, " ", "-"), func() { script(t, c.script, c.args...) })
			r.k6.only200(t)
			if len(r.mon.ready) != 0 {
				t.Errorf("/readyz answered non-200 during the Valkey fault: %v", r.mon.ready)
			}
			pods := make([]string, 0)
			for p := range r.mon.degrade {
				pods = append(pods, p)
			}
			sort.Strings(pods)
			t.Logf("replicas that reported degraded while ready: %v", pods)
			r.valkeyBounds(t)
			if strings.Contains(c.name, "outage") {
				if r.driver.Changes.ValkeyDown.N == 0 && r.driver.Changes.Transition.N == 0 {
					t.Errorf("no change was written while Valkey was down: the outage was not covered")
				}
				if len(r.mon.degrade) == 0 {
					t.Errorf(`no replica reported "degraded" in /readyz during a 60 s Valkey outage`)
				}
			}
		})
	}
}

func countAfter(fs []failure, at time.Time) int {
	n := 0
	for _, f := range fs {
		if f.At.After(at) {
			n++
		}
	}
	return n
}
