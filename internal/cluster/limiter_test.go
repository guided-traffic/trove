package cluster

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// memWindows is the counter storage of the unit tests: what Valkey holds for
// the limiters, shared by the LimiterSets of several fake replicas.
type memWindows struct {
	mu     sync.Mutex
	counts map[string]int64
	calls  int
	down   bool // available() is false: the breaker is open
	fail   bool // every call fails
}

func newMemWindows() *memWindows { return &memWindows{counts: map[string]int64{}} }

func (m *memWindows) key(l, b string, idx int64) string { return fmt.Sprintf("%s/%s/%d", l, b, idx) }

func (m *memWindows) hit(_ context.Context, l, b string, idx int64, _ time.Duration) (int64, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.fail {
		return 0, 0, errors.New("boom")
	}
	m.counts[m.key(l, b, idx)]++
	return m.counts[m.key(l, b, idx)], m.counts[m.key(l, b, idx-1)], nil
}

func (m *memWindows) read(_ context.Context, l, b string, idx int64, _ time.Duration) (int64, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.fail {
		return 0, 0, errors.New("boom")
	}
	return m.counts[m.key(l, b, idx)], m.counts[m.key(l, b, idx-1)], nil
}

func (m *memWindows) count(_ context.Context, l, b string, idx int64, _ time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.fail {
		return errors.New("boom")
	}
	m.counts[m.key(l, b, idx)]++
	return nil
}

func (m *memWindows) available() bool { return !m.down }
func (m *memWindows) degraded()       {}

// limClock is a settable clock.
type limClock struct {
	mu sync.Mutex
	t  time.Time
}

func newLimClock() *limClock {
	// A window boundary of every window used below (1 s, 4 s, 60 s).
	return &limClock{t: time.Unix(1_800_000_000, 0)}
}
func (c *limClock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *limClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func replica(t *testing.T, st windowStore, clk *limClock, n int, reg prometheus.Registerer) *LimiterSet {
	t.Helper()
	ls, err := NewLimiterSet(LimiterOptions{store: st, Now: clk.now, LiveReplicas: func() int { return n }, Registry: reg})
	if err != nil {
		t.Fatal(err)
	}
	return ls
}

func TestLimiterCluster(t *testing.T) {
	t.Run("R-ARCH-034 three replicas admit at most 300 of 1000 requests in 2 s, like one replica", func(t *testing.T) {
		run := func(replicas int) int {
			st, clk := newMemWindows(), newLimClock()
			var ls []*Limiter
			for range replicas {
				ls = append(ls, replica(t, st, clk, replicas, nil).NewLimiter("api_read", ScopeCluster, 50, 200))
			}
			admitted := 0
			for i := range 1000 {
				clk.advance(2 * time.Second / 1000)
				if ls[i%replicas].Allow(t.Context(), "token-1") {
					admitted++
				}
			}
			return admitted
		}
		for _, n := range []int{1, 3} {
			if got := run(n); got > 300 || got < 150 {
				t.Errorf("%d replicas admitted %d of 1000, want at most 300", n, got)
			}
		}
	})

	t.Run("R-API-016 the window of the defaults is 4 s for read, 5 s for write and 4 s for anonymous", func(t *testing.T) {
		ls := replica(t, newMemWindows(), newLimClock(), 1, nil)
		for name, c := range map[string]struct {
			rate  float64
			burst int
			want  time.Duration
		}{"api_read": {50, 200, 4 * time.Second}, "api_write": {10, 50, 5 * time.Second}, "api_anonymous": {5, 20, 4 * time.Second}} {
			if got := ls.NewLimiter(name, ScopeCluster, c.rate, c.burst).Window(); got != c.want {
				t.Errorf("%s window = %s, want %s", name, got, c.want)
			}
		}
		if got := ls.NewLimiter("tiny", ScopeCluster, 100, 5).Window(); got != time.Second {
			t.Errorf("window below one second = %s, want 1s", got)
		}
	})

	t.Run("R-API-016 a refused request counts, a caller that ignores Retry-After stays refused", func(t *testing.T) {
		st, clk := newMemWindows(), newLimClock()
		l := replica(t, st, clk, 1, nil).NewLimiter("api_read", ScopeCluster, 50, 200)
		admitted := 0
		// 100 requests per second for 10 s, never waiting.
		for range 1000 {
			clk.advance(10 * time.Millisecond)
			if l.Allow(t.Context(), "greedy") {
				admitted++
			}
		}
		if admitted > 250 {
			t.Errorf("a client that ignores Retry-After was admitted %d times, want at most 250", admitted)
		}
		// A client that waits for each Retry-After is admitted more often.
		polite := 0
		for sent := 0; sent < 10*100; {
			v := l.Take(t.Context(), "polite")
			sent++
			if v.Allowed {
				polite++
				clk.advance(10 * time.Millisecond)
				continue
			}
			clk.advance(v.RetryAfter)
			sent += 99
		}
		if polite <= admitted {
			t.Errorf("the polite client was admitted %d times, the greedy one %d", polite, admitted)
		}
	})

	t.Run("R-API-016 Retry-After is the time after which the next request is admitted", func(t *testing.T) {
		st, clk := newMemWindows(), newLimClock()
		l := replica(t, st, clk, 1, nil).NewLimiter("api_write", ScopeCluster, 10, 50)
		var v Verdict
		for range 60 {
			v = l.Take(t.Context(), "t")
		}
		if v.Allowed || v.RetryAfter < time.Second || v.RetryAfter%time.Second != 0 || v.Mode != ModeCluster {
			t.Fatalf("verdict = %+v", v)
		}
		clk.advance(v.RetryAfter)
		if after := l.Take(t.Context(), "t"); !after.Allowed {
			t.Fatalf("not admitted after Retry-After %s: %+v", v.RetryAfter, after)
		}
	})

	t.Run("R-API-016 RateLimit fields: remaining and the time to the end of the window", func(t *testing.T) {
		clk := newLimClock()
		l := replica(t, newMemWindows(), clk, 1, nil).NewLimiter("api_read", ScopeCluster, 50, 200)
		clk.advance(time.Second)
		v := l.Take(t.Context(), "t")
		if !v.Allowed || v.Limit != 200 || v.Remaining != 199 || v.Window != 4*time.Second || v.Reset != 3*time.Second {
			t.Fatalf("verdict = %+v", v)
		}
	})

	t.Run("R-ARCH-034 buckets are independent", func(t *testing.T) {
		st, clk := newMemWindows(), newLimClock()
		l := replica(t, st, clk, 1, nil).NewLimiter("x", ScopeCluster, 1, 2)
		for range 5 {
			l.Allow(t.Context(), "a")
		}
		if l.Allow(t.Context(), "a") || !l.Allow(t.Context(), "b") {
			t.Fatal("a second bucket must not be slowed down")
		}
	})

	t.Run("R-ARCH-030 bucket keys reach the store only as hashes", func(t *testing.T) {
		st, clk := newMemWindows(), newLimClock()
		l := replica(t, st, clk, 1, nil).NewLimiter("auth.credential_failures", ScopeCluster, 1, 5)
		l.Allow(t.Context(), "user@example.com")
		for k := range st.counts {
			if strings.Contains(k, "example") || strings.Contains(k, "auth.") {
				t.Errorf("key %q holds a raw name", k)
			}
		}
	})
}

func TestLimiterFallback(t *testing.T) {
	for _, mode := range []string{"circuit open", "call failed"} {
		t.Run("R-ARCH-034 "+mode+": three replicas admit at most 300 of 1000 requests in 2 s and the gauge is 1", func(t *testing.T) {
			st, clk := newMemWindows(), newLimClock()
			st.down, st.fail = mode == "circuit open", mode == "call failed"
			regs := make([]*prometheus.Registry, 3)
			ls := make([]*Limiter, 3)
			for i := range ls {
				regs[i] = prometheus.NewRegistry()
				ls[i] = replica(t, st, clk, 3, regs[i]).NewLimiter("api_read", ScopeCluster, 50, 200)
			}
			admitted := 0
			for i := range 1000 {
				clk.advance(2 * time.Second / 1000)
				if ls[i%3].Allow(t.Context(), "token-1") {
					admitted++
				}
			}
			if admitted > 300 || admitted < 100 {
				t.Errorf("admitted %d of 1000, want at most 300", admitted)
			}
			for i, reg := range regs {
				mfs, err := reg.Gather()
				if err != nil {
					t.Fatal(err)
				}
				found := false
				for _, mf := range mfs {
					if mf.GetName() == "trove_ratelimit_fallback" {
						found = true
						if got := mf.GetMetric()[0].GetGauge().GetValue(); got != 1 {
							t.Errorf("replica %d: trove_ratelimit_fallback = %v, want 1", i, got)
						}
					}
				}
				if !found {
					t.Errorf("replica %d: trove_ratelimit_fallback missing", i)
				}
			}
			if mode == "circuit open" && st.calls != 0 {
				t.Errorf("an open circuit made %d calls to the store, want none", st.calls)
			}
		})
	}

	t.Run("R-ARCH-034 a fallback answer carries a Retry-After from the local bucket", func(t *testing.T) {
		st, clk := newMemWindows(), newLimClock()
		st.down = true
		l := replica(t, st, clk, 2, nil).NewLimiter("api_write", ScopeCluster, 10, 50)
		var v Verdict
		for range 40 {
			v = l.Take(t.Context(), "t")
		}
		if v.Allowed || v.Mode != ModeFallback || v.Limit != 25 || v.RetryAfter < time.Second {
			t.Fatalf("verdict = %+v", v)
		}
		clk.advance(v.RetryAfter)
		if !l.Allow(t.Context(), "t") {
			t.Fatal("not admitted after Retry-After")
		}
	})

	t.Run("R-ARCH-034 returning Valkey ends the fallback", func(t *testing.T) {
		st, clk := newMemWindows(), newLimClock()
		reg := prometheus.NewRegistry()
		l := replica(t, st, clk, 3, reg).NewLimiter("api_read", ScopeCluster, 50, 200)
		st.fail = true
		l.Allow(t.Context(), "t")
		if !l.inFallback.Load() {
			t.Fatal("not in fallback")
		}
		st.fail = false
		if v := l.Take(t.Context(), "t"); v.Mode != ModeCluster || l.inFallback.Load() {
			t.Fatalf("verdict = %+v", v)
		}
	})

	t.Run("R-ARCH-034 a replica-scoped limiter never uses the store", func(t *testing.T) {
		st, clk := newMemWindows(), newLimClock()
		l := replica(t, st, clk, 3, nil).NewLimiter("local", ScopeReplica, 1, 3)
		n := 0
		for range 10 {
			if l.Allow(t.Context(), "t") {
				n++
			}
		}
		if n != 3 || st.calls != 0 {
			t.Fatalf("admitted %d with %d store calls", n, st.calls)
		}
	})

	t.Run("R-ARCH-034 decisions are counted by limiter, mode and outcome", func(t *testing.T) {
		st, clk := newMemWindows(), newLimClock()
		ls := replica(t, st, clk, 1, nil)
		l := ls.NewLimiter("x", ScopeCluster, 1, 1)
		l.Allow(t.Context(), "a")
		l.Allow(t.Context(), "a")
		var m dto.Metric
		count := func(outcome string) float64 {
			m.Reset()
			if err := ls.decisions.WithLabelValues("x", "cluster", outcome).Write(&m); err != nil {
				t.Fatal(err)
			}
			return m.GetCounter().GetValue()
		}
		if count("admitted") != 1 || count("refused") != 1 {
			t.Fatal("decisions not counted")
		}
	})
}

func TestLimiterCountOnly(t *testing.T) {
	t.Run("R-ARCH-034 Exceeded reads, Count records, and the window slides", func(t *testing.T) {
		st, clk := newMemWindows(), newLimClock()
		l := replica(t, st, clk, 1, nil).NewLimiter("auth.credential_failures", ScopeCluster, 10.0/60, 10)
		if l.Window() != time.Minute {
			t.Fatalf("window = %s", l.Window())
		}
		for range 9 {
			if l.Exceeded(t.Context(), "alice") {
				t.Fatal("exceeded before the burst is used")
			}
			l.Count(t.Context(), "alice")
		}
		if l.Exceeded(t.Context(), "alice") {
			t.Fatal("exceeded one failure below the burst")
		}
		l.Count(t.Context(), "alice")
		if !l.Exceeded(t.Context(), "alice") {
			t.Fatal("not exceeded at exactly the burst: a limit of 10 admits 10 checks (Q-AUTH-20)")
		}
		if l.Exceeded(t.Context(), "bob") {
			t.Fatal("another bucket is affected")
		}
		clk.advance(3 * time.Minute)
		if l.Exceeded(t.Context(), "alice") {
			t.Fatal("still exceeded after two windows")
		}
	})

	t.Run("R-ARCH-034 count-only form falls back to a local share", func(t *testing.T) {
		st, clk := newMemWindows(), newLimClock()
		st.down = true
		l := replica(t, st, clk, 5, nil).NewLimiter("auth.credential_failures", ScopeCluster, 10.0/60, 10)
		// The share is max(1, 10/5) = 2: two failures use it up.
		l.Count(t.Context(), "alice")
		if l.Exceeded(t.Context(), "alice") {
			t.Fatal("exceeded below the local share")
		}
		l.Count(t.Context(), "alice")
		if !l.Exceeded(t.Context(), "alice") {
			t.Fatal("not exceeded at the local share")
		}
	})
}

func TestLimiterRegistration(t *testing.T) {
	ls := replica(t, newMemWindows(), newLimClock(), 1, nil)
	ls.NewLimiter("ok_name", ScopeCluster, 1, 1)
	for _, c := range []struct {
		name  string
		rate  float64
		burst int
		scope Scope
	}{
		{"Bad", 1, 1, ScopeCluster},
		{"has space", 1, 1, ScopeCluster},
		{"ok_name", 1, 1, ScopeCluster},
		{"zero_rate", 0, 1, ScopeCluster},
		{"zero_burst", 1, 0, ScopeCluster},
		{"bad_scope", 1, 1, "x"},
		{"too_long", 0.00001, 1000, ScopeCluster},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%+v was accepted", c)
				}
			}()
			ls.NewLimiter(c.name, c.scope, c.rate, c.burst)
		}()
	}
}

func TestLimiterFallbackBucketsAreBounded(t *testing.T) {
	st, clk := newMemWindows(), newLimClock()
	st.down = true
	l := replica(t, st, clk, 1, nil).NewLimiter("x", ScopeCluster, 1, 1)
	for i := range MaxFallbackBuckets + 10 {
		l.Allow(t.Context(), fmt.Sprint(i))
	}
	l.mu.Lock()
	n := len(l.buckets)
	l.mu.Unlock()
	if n > MaxFallbackBuckets {
		t.Fatalf("%d buckets, the bound is %d", n, MaxFallbackBuckets)
	}
}
