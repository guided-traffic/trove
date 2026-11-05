//go:build integration

package mirror

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/npmstub"
)

// The engine against the throttling stub (E6b.4; R-REPO-054, R-REPO-059): the answers of a registry that rate-limits a client, with
// the real clock, the real stack and the stub's own record of what it was asked when.

const throttleScope = "@acme"

// sampler reads one series of a replica's metrics every 100 ms.
type sampler struct {
	mu     sync.Mutex
	series string
	at     []time.Time
	val    []float64
	cancel context.CancelFunc
	done   chan struct{}
}

func (r *rig) sample(rep *testenv.Replica, name string) *sampler {
	ctx, cancel := context.WithCancel(context.Background())
	s := &sampler{series: series(name), cancel: cancel, done: make(chan struct{})}
	url := "http://" + rep.OpsAddr() + "/metrics"
	go func() {
		defer close(s.done)
		for ctx.Err() == nil {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err == nil {
				if res, err := testenv.Client.Do(req); err == nil {
					b, _ := io.ReadAll(res.Body)
					res.Body.Close()
					if v := testenv.MetricValue(string(b), s.series); v >= 0 {
						s.mu.Lock()
						s.at, s.val = append(s.at, time.Now()), append(s.val, v)
						s.mu.Unlock()
					}
				}
			}
			select {
			case <-ctx.Done():
			case <-time.After(100 * time.Millisecond):
			}
		}
	}()
	r.t.Cleanup(s.stop)
	return s
}

func (s *sampler) stop() { s.cancel(); <-s.done }

// between returns the samples taken in [from, to].
func (s *sampler) between(from, to time.Time) (out []float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, at := range s.at {
		if !at.Before(from) && !at.After(to) {
			out = append(out, s.val[i])
		}
	}
	return out
}

func maxOf(v []float64) (m float64) {
	for _, x := range v {
		m = max(m, x)
	}
	return m
}

// throttleRig is a public registry with n packages under @acme, a runner and a client-serving replica, the stub throttling as the
// mode says, and a mirror of the given concurrency.
func throttleRig(t *testing.T, n, concurrency int, th npmstub.Throttle, clock mirrorClock) (*rig, *testenv.Replica, *testenv.Replica) {
	t.Helper()
	o := rigOptions{public: true, include: []string{throttleScope + "/*"}, mirror: settings{"concurrency": concurrency}}
	if clock != nil {
		o.clock = clock
	}
	r := newRig(t, o)
	r.publishMany(throttleScope, n)
	r.setStub(npmstub.Mode{Throttle: &th})
	return r, r.runner("runner"), r.web("web")
}

type mirrorClock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

// tarballTimes are the arrival times of the tarball requests, in order.
func (r *rig) tarballTimes() (out []time.Time) {
	for _, h := range tarballHits(r.traf.since(0)) {
		out = append(out, h.at)
	}
	return out
}

// waitTarballs waits until the stub has received n tarball requests and returns the time of the n-th.
func (r *rig) waitTarballs(n int) time.Time {
	r.t.Helper()
	var at time.Time
	testenv.WaitFor(r.t, fmt.Sprintf("the stub to receive %d tarball requests", n), passWithin, func() bool {
		ts := r.tarballTimes()
		if len(ts) < n {
			return false
		}
		at = ts[n-1]
		return true
	})
	return at
}

// resumeAfter is how long after t the next request of any kind arrived, ignoring the 300 ms after t in which requests that were
// in flight when the wait began may still arrive.
func (r *rig) resumeAfter(t time.Time) time.Duration {
	r.t.Helper()
	for _, h := range r.traf.since(0) {
		if h.at.After(t.Add(300 * time.Millisecond)) {
			return h.at.Sub(t)
		}
	}
	r.t.Fatalf("no request arrived after %s", t)
	return 0
}

func within(t *testing.T, what string, got, lo, hi time.Duration) {
	t.Helper()
	if got < lo || got > hi {
		t.Errorf("%s: %s, want between %s and %s", what, got.Round(10*time.Millisecond), lo, hi)
	}
}

func TestR_REPO_054_RetryAfterSecondsPausesThePassAndHalvesItsWindow(t *testing.T) {
	r, run, web := throttleRig(t, 20, 4, npmstub.Throttle{Kind: npmstub.Throttle429, Class: npmstub.KindTarball, From: 11, RetryAfterSeconds: 7}, nil)
	window := r.sample(run, "trove_mirror_concurrency")
	// A client read before the pass: the client-serving replica has touched the repository, so it exports its upstream gauge.
	if a := r.get(web, throttleScope+"%2fp000"); a.Status != http.StatusOK {
		t.Fatalf("GET %s/p000: %d %s", throttleScope, a.Status, a.Body)
	}
	t0 := r.pass(run)
	hit11 := r.waitTarballs(11)

	t.Run("R-REPO-054 the pass sends no request for 7 s, a client reads a stored version meanwhile, and the upstream stays up", func(t *testing.T) {
		time.Sleep(1500 * time.Millisecond)
		stored := r.storedItems()
		if stored == 0 {
			t.Fatal("nothing is stored before the rate limit, so the client read below proves nothing")
		}
		var first string
		r.st.Query(`SELECT i.path FROM items i JOIN repositories r ON r.id = i.repo_id WHERE r.key = $1 AND i.path LIKE '%.tgz' ORDER BY i.path LIMIT 1`,
			func(rows pgx.Rows) error { return rows.Scan(&first) }, repoKey)
		doc := first[:strings.Index(first, "/-/")]
		mark := r.traf.mark()
		if a := r.get(web, first); a.Status != http.StatusOK {
			t.Errorf("GET %s while the upstream throttles the pass: %d %.200s", first, a.Status, a.Body)
		}
		if a := r.get(web, strings.Replace(doc, "/", "%2f", 1)); a.Status != http.StatusOK {
			t.Errorf("GET the document of %s: %d %.200s", doc, a.Status, a.Body)
		}
		if hits := r.traf.since(mark); len(hits) != 0 {
			t.Errorf("a client read of a stored version reached the upstream while the pass waits: %+v", hits)
		}
		for _, rep := range []*testenv.Replica{web, run} {
			if v := testenv.MetricValue(rep.Metrics(t), series("trove_remote_upstream_up")); v != 1 {
				t.Errorf("trove_remote_upstream_up on %s is %v while the upstream throttles the pass, want 1", rep.Name, v)
			}
		}
	})

	r.waitPass(t0, "full", "complete", passWithin)
	window.stop()

	t.Run("R-REPO-054 the first request after the answer comes 7 s later, and the pass completes with every tarball stored", func(t *testing.T) {
		within(t, "the pause after Retry-After: 7", r.resumeAfter(hit11), 6700*time.Millisecond, 9*time.Second)
		if n := r.storedItems(); n != 20 {
			t.Errorf("%d tarballs stored, want 20", n)
		}
		if n := r.stub.Throttled(); n != 1 {
			t.Errorf("the stub throttled %d requests, want 1", n)
		}
		if st := r.summary(); st["outcome"] != "complete" || st["itemsStored"] != float64(20) {
			t.Errorf("summary %v", st)
		}
	})

	t.Run("R-REPO-054 trove_mirror_concurrency is at most half of its value before the answer while the pass waits", func(t *testing.T) {
		before := maxOf(window.between(t0, hit11.Add(100*time.Millisecond)))
		during := window.between(hit11.Add(time.Second), hit11.Add(6*time.Second))
		if before < 2 || len(during) == 0 {
			t.Fatalf("the window was %v before the answer, %d samples during the wait", before, len(during))
		}
		if m := maxOf(during); m > before/2 {
			t.Errorf("the window during the wait is %v, before the answer it was %v: want at most half", m, before)
		}
		if got := r.metric(run, "trove_mirror_rate_limited_total", "reason", "too_many_requests"); got < 1 {
			t.Errorf("trove_mirror_rate_limited_total{reason=too_many_requests} = %v", got)
		}
		if got := r.metric(run, "trove_mirror_rate_limit_wait_seconds_total"); got < 6.5 || got > 7.5 {
			t.Errorf("trove_mirror_rate_limit_wait_seconds_total = %v, want 7", got)
		}
	})

	t.Run("R-REPO-057 the upstream log records of the throttled pass carry flight_role mirror and the job", func(t *testing.T) {
		seen429 := false
		var n int
		for _, line := range strings.Split(run.Logs.String(), "\n") {
			var rec map[string]any
			if json.Unmarshal([]byte(line), &rec) != nil || rec["log"] != "upstream" || rec["purpose"] != "remote" {
				continue
			}
			n++
			if rec["flight_role"] != "mirror" || rec["job_id"] == nil || rec["job_id"] == "" {
				t.Errorf("an upstream record of the pass: flight_role %v job_id %v: %.200s", rec["flight_role"], rec["job_id"], line)
			}
			if rec["status"] == float64(429) {
				seen429 = true
			}
		}
		if n == 0 || !seen429 {
			t.Errorf("%d upstream records, 429 seen: %v", n, seen429)
		}
	})
}

func TestR_REPO_054_RetryAfterAsAnHTTPDate(t *testing.T) {
	r, run, _ := throttleRig(t, 10, 4, npmstub.Throttle{Kind: npmstub.Throttle429Date, Class: npmstub.KindTarball, From: 6, RetryAfterSeconds: 5}, nil)
	t0 := r.pass(run)
	hit := r.waitTarballs(6)
	r.waitPass(t0, "full", "complete", passWithin)
	t.Run("R-REPO-054 a Retry-After date 5 s ahead pauses the pass for 5 s plus or minus 1 s", func(t *testing.T) {
		// An HTTP date has whole seconds: the stub's date is the second 5 s ahead, cut down, so the wait is 4 to 5 s.
		within(t, "the pause after Retry-After as a date", r.resumeAfter(hit), 4*time.Second, 6*time.Second)
		if n := r.storedItems(); n != 10 {
			t.Errorf("%d tarballs stored", n)
		}
	})
}

func TestR_REPO_054_SlowDownWithoutAHintBacksOffExponentially(t *testing.T) {
	clock := &offsetClock{}
	r, run, _ := throttleRig(t, 8, 1, npmstub.Throttle{Kind: npmstub.Throttle503SlowDown, Class: npmstub.KindTarball, From: 3, Count: 3}, clock)
	t0 := r.pass(run)
	r.waitPass(t0, "full", "complete", passWithin)

	t.Run("R-REPO-054 three SlowDown answers in a row pause the pass within [0.5, 1], [1, 2] and [2, 4] seconds", func(t *testing.T) {
		ts := r.tarballTimes()
		if len(ts) < 6 {
			t.Fatalf("%d tarball requests", len(ts))
		}
		// Requests 3, 4 and 5 are throttled, one at a time (the window is 1); the pause is the gap to the next request.
		within(t, "first pause", ts[3].Sub(ts[2]), 450*time.Millisecond, 1400*time.Millisecond)
		within(t, "second pause", ts[4].Sub(ts[3]), 950*time.Millisecond, 2400*time.Millisecond)
		within(t, "third pause", ts[5].Sub(ts[4]), 1950*time.Millisecond, 4400*time.Millisecond)
		var waits []time.Duration
		for _, s := range clock.sleeps() {
			if s >= 400*time.Millisecond {
				waits = append(waits, s)
			}
		}
		if len(waits) != 3 {
			t.Fatalf("the engine slept %v, want three waits", waits)
		}
		for i, rng := range [][2]time.Duration{{500 * time.Millisecond, time.Second}, {time.Second, 2 * time.Second}, {2 * time.Second, 4 * time.Second}} {
			if waits[i] < rng[0] || waits[i] > rng[1] {
				t.Errorf("wait %d is %s, want within %s to %s", i+1, waits[i], rng[0], rng[1])
			}
		}
	})

	t.Run("R-REPO-054 the pass then completes and every tarball is stored once", func(t *testing.T) {
		if n := r.storedItems(); n != 8 {
			t.Errorf("%d tarballs stored, want 8", n)
		}
		if n := len(r.tarballTimes()); n != 8+3 {
			t.Errorf("the stub received %d tarball requests, want the 8 and the 3 it refused", n)
		}
		if got := r.metric(run, "trove_mirror_rate_limited_total", "reason", "slow_down"); got != 3 {
			t.Errorf("trove_mirror_rate_limited_total{reason=slow_down} = %v, want 3", got)
		}
		if got := r.metric(run, "trove_mirror_items_total", "result", "stored"); got != 8 {
			t.Errorf("trove_mirror_items_total{result=stored} = %v, want 8", got)
		}
	})
}

func TestR_REPO_054_AQuotaHeaderNamesTheWait(t *testing.T) {
	r, run, _ := throttleRig(t, 10, 4, npmstub.Throttle{Kind: npmstub.Throttle403Quota, Class: npmstub.KindTarball, From: 5, RetryAfterSeconds: 4}, nil)
	t0 := r.pass(run)
	hit := r.waitTarballs(5)
	r.waitPass(t0, "full", "complete", passWithin)
	t.Run("R-REPO-054 a 403 with X-RateLimit-Remaining 0 and a reset 4 s ahead pauses the pass for about 4 s", func(t *testing.T) {
		// X-RateLimit-Reset is whole seconds since the epoch: the reset is cut down, so the wait is 3 to 4 s.
		within(t, "the pause after the quota answer", r.resumeAfter(hit), 2700*time.Millisecond, 5*time.Second)
		if got := r.metric(run, "trove_mirror_rate_limited_total", "reason", "quota_header"); got < 1 {
			t.Errorf("trove_mirror_rate_limited_total{reason=quota_header} = %v", got)
		}
		if n := r.storedItems(); n != 10 {
			t.Errorf("%d tarballs stored", n)
		}
	})
}

func TestR_REPO_054_ALongWaitEndsTheJobDeferredAndTheNextJobContinuesAtThatTime(t *testing.T) {
	clock := &offsetClock{}
	r, run, web := throttleRig(t, 10, 2, npmstub.Throttle{Kind: npmstub.Throttle429, Class: npmstub.KindTarball, From: 4, Count: 1, RetryAfterSeconds: 3600}, clock)
	t0 := r.pass(run)
	deadline := time.Now().Add(passWithin)
	for {
		n, err := r.st.TryNumber(`SELECT count(*) FROM job WHERE kind = 'mirror-pass' AND state = 'succeeded' AND result->>'deferred' = 'rate_limited'`)
		if err == nil && n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the job did not end deferred\n%s", r.diag())
		}
		time.Sleep(100 * time.Millisecond)
	}
	ended := time.Now()
	mark := r.traf.mark()

	t.Run("R-REPO-054 the job ends with the result deferred and the status shows rateLimit.until an hour ahead", func(t *testing.T) {
		res := r.jobResult("succeeded")
		until, err := time.Parse(time.RFC3339, fmt.Sprint(res["until"]))
		if res["deferred"] != "rate_limited" || err != nil {
			t.Fatalf("job result %v", res)
		}
		within(t, "the end of the wait", time.Until(until), 59*time.Minute, 61*time.Minute)
		a := r.api(web, http.MethodGet, "/api/v1/repositories/"+repoKey+"/mirror", nil)
		doc := a.JSON(t)
		rl, _ := doc["rateLimit"].(map[string]any)
		statusUntil, err := time.Parse(time.RFC3339, fmt.Sprint(rl["until"]))
		if err != nil || doc["state"] != "waiting" {
			t.Fatalf("status %s", a.Body)
		}
		within(t, "rateLimit.until", time.Until(statusUntil), 59*time.Minute, 61*time.Minute)
		if rl["lastStatus"] != float64(429) || rl["waits"] != float64(1) {
			t.Errorf("rateLimit %v", rl)
		}
	})

	t.Run("R-REPO-054 the stub records no request of the mirror in the next 60 s", func(t *testing.T) {
		// The wait is an hour on the engine's clock. A dispatch now, a dispatch 59 s on and a few seconds of watching show that no
		// pass starts and nothing is sent; the 60 s themselves are not waited for.
		r.dispatch(run)
		time.Sleep(3 * time.Second)
		clock.advance(59 * time.Second)
		r.dispatch(run)
		time.Sleep(3 * time.Second)
		if hits := r.traf.since(mark); len(hits) != 0 {
			t.Errorf("%d requests after the job ended deferred: %+v", len(hits), hits[0])
		}
		if n := r.st.Number(`SELECT count(*) FROM job WHERE kind = 'mirror-pass'`); n != 1 {
			t.Errorf("%d mirror-pass jobs: the dispatch enqueued another before the wait was over", n)
		}
		if time.Since(ended) < 6*time.Second {
			t.Error("the observation was too short")
		}
	})

	t.Run("R-REPO-054 an hour on, the next job continues the pass from its pending names and completes it", func(t *testing.T) {
		r.setStub(npmstub.Mode{})
		clock.advance(time.Hour)
		r.dispatch(run)
		r.waitPass(t0, "full", "complete", passWithin)
		if n := r.storedItems(); n != 10 {
			t.Errorf("%d tarballs stored, want 10", n)
		}
		if n := r.st.Number(`SELECT count(*) FROM job WHERE kind = 'mirror-pass'`); n != 2 {
			t.Errorf("%d mirror-pass jobs, want the deferred one and its continuation", n)
		}
	})
}
