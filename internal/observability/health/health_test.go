package health

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
)

func cfg() config.Health {
	return config.Health{
		CheckInterval: 200 * time.Millisecond, CheckTimeout: 100 * time.Millisecond,
		FailureThreshold: 2, SuccessThreshold: 2, WatchdogTimeout: time.Minute,
	}
}

// toggle is a check whose result a test flips.
type toggle struct{ err atomic.Pointer[error] }

func (t *toggle) set(err error) {
	if err == nil {
		t.err.Store(nil)
		return
	}
	t.err.Store(&err)
}

func (t *toggle) check(context.Context) error {
	if p := t.err.Load(); p != nil {
		return *p
	}
	return nil
}

func get(t *testing.T, h http.Handler, target string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %q is not JSON: %v", rec.Body.String(), err)
	}
	return rec.Code, body
}

func TestReadiness(t *testing.T) {
	t.Run("R-OPS-001 a replica without a passing start-up check is unready", func(t *testing.T) {
		h := New(cfg())
		var ok toggle
		ok.set(errors.New("not yet"))
		h.AddReadiness("startup", ok.check, false)
		h.Refresh(t.Context())
		code, body := get(t, h.ReadyHandler(), "/readyz")
		if code != 503 || body["status"] != "unready" {
			t.Fatalf("got %d %v", code, body)
		}
		if f, _ := body["failing"].([]any); len(f) != 1 || f[0] != "startup" {
			t.Fatalf("failing = %v", body["failing"])
		}
		if h.Snapshot().State != StateStarting {
			t.Fatalf("state = %s", h.Snapshot().State)
		}
		ok.set(nil)
		h.Refresh(t.Context())
		h.Refresh(t.Context()) // success_threshold 2
		code, body = get(t, h.ReadyHandler(), "/readyz")
		if code != 200 || body["status"] != "ready" {
			t.Fatalf("got %d %v", code, body)
		}
	})

	t.Run("R-OPS-001 failure and success thresholds debounce a gating check", func(t *testing.T) {
		h := New(cfg()) // failure_threshold 2, success_threshold 2
		var db toggle
		h.AddReadiness("database", db.check, false)
		h.Refresh(t.Context())
		if !h.Ready() {
			t.Fatal("first success must pass the check at once")
		}
		db.set(errors.New("down"))
		h.Refresh(t.Context())
		if !h.Ready() {
			t.Fatal("one failure must not fail a check with failure_threshold 2")
		}
		h.Refresh(t.Context())
		if h.Ready() {
			t.Fatal("two consecutive failures must fail the check")
		}
		db.set(nil)
		h.Refresh(t.Context())
		if h.Ready() {
			t.Fatal("one success must not pass a check with success_threshold 2")
		}
		h.Refresh(t.Context())
		if !h.Ready() {
			t.Fatal("two consecutive successes must pass the check")
		}
		if h.Snapshot().State != StateReady {
			t.Fatalf("state = %s", h.Snapshot().State)
		}
	})

	t.Run("R-OPS-001 an unready replica that was ready reports state unready", func(t *testing.T) {
		c := cfg()
		c.FailureThreshold = 1
		h := New(c)
		var db toggle
		h.AddReadiness("database", db.check, false)
		h.Refresh(t.Context())
		db.set(errors.New("down"))
		h.Refresh(t.Context())
		if s := h.Snapshot().State; s != StateUnready {
			t.Fatalf("state = %s, want unready", s)
		}
	})

	t.Run("Q-OPS-2 a first-readiness check gates once and is reported afterwards", func(t *testing.T) {
		c := cfg()
		c.FailureThreshold = 1
		h := New(c)
		var store toggle
		store.set(errors.New("no bucket"))
		h.AddReadiness("objectStore", store.check, true)
		h.Refresh(t.Context())
		if h.Ready() {
			t.Fatal("a first check must gate the first readiness")
		}
		store.set(nil)
		h.Refresh(t.Context())
		h.Refresh(t.Context())
		if !h.Ready() {
			t.Fatal("not ready after the check passed")
		}
		store.set(errors.New("outage"))
		h.Refresh(t.Context())
		code, body := get(t, h.ReadyHandler(), "/readyz")
		if code != 200 {
			t.Fatalf("a failing first-check after first readiness must not gate: %d %v", code, body)
		}
		if d, _ := body["degraded"].([]any); len(d) != 1 || d[0] != "objectStore" {
			t.Fatalf("degraded = %v", body["degraded"])
		}
	})

	t.Run("Q-OPS-2 require_object_store keeps the check gating (first=false)", func(t *testing.T) {
		c := cfg()
		c.FailureThreshold = 1
		h := New(c)
		var store toggle
		h.AddReadiness("objectStore", store.check, false)
		h.Refresh(t.Context())
		store.set(errors.New("outage"))
		h.Refresh(t.Context())
		if h.Ready() {
			t.Fatal("a gating object-store check must make the replica unready")
		}
	})

	t.Run("Q-HA-13 a reported check gates the first readiness on configuration errors only", func(t *testing.T) {
		c := cfg()
		c.FailureThreshold = 1
		h := New(c)
		var vk toggle
		h.AddReported("valkey", vk.check)

		vk.set(errors.New("unreachable"))
		h.Refresh(t.Context())
		code, body := get(t, h.ReadyHandler(), "/readyz")
		if code != 200 || body["degraded"] == nil {
			t.Fatalf("unreachable Valkey must degrade, not gate: %d %v", code, body)
		}

		h2 := New(c)
		var vk2 toggle
		h2.AddReported("valkey", vk2.check)
		vk2.set(ConfigError(errors.New("WRONGPASS"), "auth"))
		h2.Refresh(t.Context())
		code, body = get(t, h2.ReadyHandler(), "/readyz?verbose=1")
		if code != 503 {
			t.Fatalf("a configuration error must hold back the first readiness: %d %v", code, body)
		}
		v := body["checks"].(map[string]any)["valkey"].(map[string]any)
		if v["status"] != "fail" || v["reason"] != "auth" || v["configError"] != true {
			t.Fatalf("valkey check = %v", v)
		}
		if strings.Contains(string(mustJSON(body)), "WRONGPASS") {
			t.Fatal("verbose output must not carry the error text")
		}

		// After the first readiness the same error is only reported.
		vk2.set(nil)
		h2.Refresh(t.Context())
		h2.Refresh(t.Context()) // success_threshold 2
		if !h2.Ready() {
			t.Fatal("not ready after recovery")
		}
		vk2.set(ConfigError(errors.New("WRONGPASS"), "auth"))
		h2.Refresh(t.Context())
		if code, body = get(t, h2.ReadyHandler(), "/readyz"); code != 200 || body["degraded"] == nil {
			t.Fatalf("a configuration error after first readiness must degrade: %d %v", code, body)
		}
	})

	t.Run("R-OPS-001 draining fails readiness at once and liveness stays 200", func(t *testing.T) {
		h := New(cfg())
		var ok toggle
		h.AddReadiness("startup", ok.check, false)
		h.Refresh(t.Context())
		if !h.Ready() {
			t.Fatal("setup: not ready")
		}
		h.SetDraining()
		code, body := get(t, h.ReadyHandler(), "/readyz")
		if code != 503 {
			t.Fatalf("readyz = %d %v", code, body)
		}
		if f, _ := body["failing"].([]any); len(f) != 1 || f[0] != CheckDraining {
			t.Fatalf("failing = %v", body["failing"])
		}
		if code, _ = get(t, h.LiveHandler(), "/healthz"); code != 200 {
			t.Fatalf("healthz = %d while draining, want 200", code)
		}
		if h.Snapshot().State != StateDraining {
			t.Fatalf("state = %s", h.Snapshot().State)
		}
	})

	t.Run("R-OPS-001 a probe answers from cached results and runs no check", func(t *testing.T) {
		h := New(cfg())
		var calls atomic.Int64
		h.AddReadiness("database", func(context.Context) error { calls.Add(1); return nil }, false)
		h.Refresh(t.Context())
		before := calls.Load()
		for range 1000 {
			get(t, h.ReadyHandler(), "/readyz")
			get(t, h.LiveHandler(), "/healthz")
		}
		if calls.Load() != before {
			t.Fatalf("probes ran %d checks", calls.Load()-before)
		}
	})

	t.Run("R-OPS-001 a check that exceeds check_timeout fails with reason timeout", func(t *testing.T) {
		c := cfg()
		c.FailureThreshold = 1
		h := New(c)
		release := make(chan struct{})
		defer close(release)
		h.AddReadiness("database", func(context.Context) error { <-release; return nil }, false)
		start := time.Now()
		h.Refresh(t.Context())
		if time.Since(start) > 2*time.Second {
			t.Fatal("Refresh waited for a hung check")
		}
		_, body := get(t, h.ReadyHandler(), "/readyz?verbose=1")
		db := body["checks"].(map[string]any)["database"].(map[string]any)
		if db["status"] != "fail" || db["reason"] != "timeout" {
			t.Fatalf("database = %v", db)
		}
	})

	t.Run("R-OPS-001 a panicking check is a failed check", func(t *testing.T) {
		c := cfg()
		c.FailureThreshold = 1
		h := New(c)
		h.AddReadiness("boom", func(context.Context) error { panic("x") }, false)
		h.Refresh(t.Context())
		if h.Ready() {
			t.Fatal("a panicking check must not pass")
		}
	})

	t.Run("R-OPS-001 the check loop runs checks at their own interval", func(t *testing.T) {
		h := New(cfg())
		var fast, slow atomic.Int64
		h.AddReadiness("fast", func(context.Context) error { fast.Add(1); return nil }, false, WithInterval(30*time.Millisecond))
		h.AddReadiness("slow", func(context.Context) error { slow.Add(1); return nil }, false)
		ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
		defer cancel()
		h.Run(ctx)
		if fast.Load() < 5 {
			t.Fatalf("fast check ran %d times", fast.Load())
		}
		if slow.Load() > 4 {
			t.Fatalf("slow check ran %d times in 500ms at a 200ms interval", slow.Load())
		}
	})

	t.Run("R-OPS-001 the observer sees each result", func(t *testing.T) {
		var mu sync.Mutex
		seen := map[string]bool{}
		h := New(cfg(), WithObserver(func(name string, passing bool) { mu.Lock(); seen[name] = passing; mu.Unlock() }))
		var db toggle
		db.set(errors.New("down"))
		h.AddReadiness("database", db.check, false)
		h.Refresh(t.Context())
		mu.Lock()
		defer mu.Unlock()
		if v, ok := seen["database"]; !ok || v {
			t.Fatalf("seen = %v", seen)
		}
	})
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func TestLiveness(t *testing.T) {
	var now atomic.Int64
	base := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	now.Store(base.UnixNano())
	clock := func() time.Time { return time.Unix(0, now.Load()).UTC() }
	advance := func(d time.Duration) { now.Add(int64(d)) }

	t.Run("R-OPS-001 liveness fails when a watched loop stops making progress, with one dump", func(t *testing.T) {
		var logs bytes.Buffer
		log := slog.New(slog.NewJSONHandler(&logs, nil))
		h := New(cfg(), WithClock(clock), WithLogger(log))
		l := h.Loop("jobs.claim", time.Minute)
		if code, _ := get(t, h.LiveHandler(), "/healthz"); code != 200 {
			t.Fatalf("healthz = %d at registration", code)
		}
		advance(30 * time.Second)
		l.Beat()
		advance(59 * time.Second)
		if code, _ := get(t, h.LiveHandler(), "/healthz"); code != 200 {
			t.Fatalf("healthz = %d within the bound", code)
		}
		advance(2 * time.Second)
		code, body := get(t, h.LiveHandler(), "/healthz")
		if code != 503 || body["status"] != "stuck" {
			t.Fatalf("healthz = %d %v", code, body)
		}
		if s, _ := body["stuck"].([]any); len(s) != 1 || s[0] != "jobs.claim" {
			t.Fatalf("stuck = %v", body["stuck"])
		}
		get(t, h.LiveHandler(), "/healthz")
		if n := strings.Count(logs.String(), "watched loop is stuck"); n != 1 {
			t.Fatalf("%d stuck records, want exactly one", n)
		}
		if !strings.Contains(logs.String(), "goroutine") || !strings.Contains(logs.String(), `"loop":"jobs.claim"`) {
			t.Fatalf("record lacks the loop name or the goroutine dump: %.300s", logs.String())
		}
		l.Beat()
		if code, _ = get(t, h.LiveHandler(), "/healthz"); code != 200 {
			t.Fatalf("healthz = %d after progress", code)
		}
	})

	t.Run("R-OPS-001 a loop that stops on purpose leaves the watchdog first", func(t *testing.T) {
		h := New(cfg(), WithClock(clock))
		l := h.Loop("jobs.reaper", time.Second)
		advance(time.Hour)
		l.Stop()
		if code, _ := get(t, h.LiveHandler(), "/healthz"); code != 200 {
			t.Fatalf("healthz = %d after Stop", code)
		}
	})

	t.Run("Q-OPS-1 liveness never reads a readiness result", func(t *testing.T) {
		h := New(cfg(), WithClock(clock))
		var db toggle
		db.set(errors.New("postgres down"))
		h.AddReadiness("database", db.check, false)
		h.Refresh(t.Context())
		if code, _ := get(t, h.ReadyHandler(), "/readyz"); code != 503 {
			t.Fatalf("setup: readyz = %d", code)
		}
		advance(24 * time.Hour)
		if code, _ := get(t, h.LiveHandler(), "/healthz"); code != 200 {
			t.Fatalf("healthz = %d during a dependency outage", code)
		}
	})

	t.Run("R-OPS-001 the check loop is itself watched with a bound of three intervals", func(t *testing.T) {
		h := New(cfg(), WithClock(clock))
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { h.Run(ctx); close(done) }()
		deadline := time.Now().Add(2 * time.Second)
		for {
			h.mu.RLock()
			_, ok := h.loops[LoopChecks]
			h.mu.RUnlock()
			if ok {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("check loop did not register with the watchdog")
			}
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
		<-done
		h.mu.RLock()
		_, ok := h.loops[LoopChecks]
		h.mu.RUnlock()
		if ok {
			t.Fatal("check loop must leave the watchdog when it stops")
		}
	})
}

func TestRunWaitsForChecks(t *testing.T) {
	t.Run("R-ARCH-024 Run returns only after a check that was running has finished, so nothing logs after it", func(t *testing.T) {
		var logs bytes.Buffer // deliberately unsynchronised: a late write is a data race
		log := slog.New(slog.NewJSONHandler(&logs, nil))
		h := New(cfg(), WithLogger(log))
		started := make(chan struct{})
		var once sync.Once
		h.AddReadiness("slow", func(ctx context.Context) error {
			once.Do(func() { close(started) })
			<-ctx.Done()
			time.Sleep(60 * time.Millisecond) // finishes its work after the cancellation
			log.Info("late write of a check")
			return ctx.Err()
		}, false)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { h.Run(ctx); close(done) }()
		<-started
		cancel()
		<-done
		if !strings.Contains(logs.String(), "late write of a check") {
			t.Fatal("Run returned before the running check finished")
		}
		// Nothing starts after Run: a Refresh is a no-op.
		before := logs.Len()
		h.Refresh(context.Background())
		if logs.Len() != before {
			t.Fatal("a check ran after Run returned")
		}
	})

	t.Run("R-ARCH-024 a check that ignores its context cannot hold Close longer than check_timeout plus a second", func(t *testing.T) {
		c := cfg()
		c.CheckTimeout = 100 * time.Millisecond
		h := New(c)
		release := make(chan struct{})
		defer close(release)
		h.AddReadiness("stuck", func(context.Context) error { <-release; return nil }, false)
		go h.Refresh(context.Background())
		time.Sleep(50 * time.Millisecond)
		start := time.Now()
		h.Close()
		if d := time.Since(start); d > 3*time.Second {
			t.Fatalf("Close took %v", d)
		}
	})
}

func TestServing(t *testing.T) {
	t.Run("R-OPS-002 a draining replica that was ready still serves, so the drain delay serves requests", func(t *testing.T) {
		h := New(cfg())
		var db toggle
		h.AddReadiness("database", db.check, false)
		h.Refresh(t.Context())
		if !h.Serving() || !h.Ready() {
			t.Fatal("a replica with passing checks must be ready and serving")
		}
		h.SetDraining()
		if h.Ready() {
			t.Fatal("readiness must fail at once when draining")
		}
		if !h.Serving() {
			t.Fatal("a draining replica must keep serving")
		}
	})

	t.Run("R-OPS-002 a draining replica whose gating check fails does not serve", func(t *testing.T) {
		c := cfg()
		c.FailureThreshold = 1
		h := New(c)
		var db toggle
		h.AddReadiness("database", db.check, false)
		h.Refresh(t.Context())
		h.SetDraining()
		db.set(errors.New("down"))
		h.Refresh(t.Context())
		if h.Serving() {
			t.Fatal("a failing database must make ping fail even while draining")
		}
	})

	t.Run("R-OPS-002 a replica that never became ready does not serve, draining or not", func(t *testing.T) {
		h := New(cfg())
		var st toggle
		st.set(errors.New("not yet"))
		h.AddReadiness("startup", st.check, false)
		h.Refresh(t.Context())
		if h.Serving() {
			t.Fatal("a starting replica must not serve")
		}
		st.set(nil)
		h.SetDraining() // drain before the first readiness: it was never ready
		if h.Serving() {
			t.Fatal("a replica that drains before it was ever ready must not serve")
		}
	})
}

func TestOutageGrace(t *testing.T) {
	type clock struct {
		mu sync.Mutex
		t  time.Time
	}
	newHealth := func(grace time.Duration) (*Health, *toggle, func(time.Duration)) {
		clk := &clock{t: time.Unix(1_700_000_000, 0)}
		h := New(cfg(), WithClock(func() time.Time { clk.mu.Lock(); defer clk.mu.Unlock(); return clk.t }))
		var db toggle
		h.AddReadiness("database", db.check, false, WithGrace(grace))
		advance := func(d time.Duration) { clk.mu.Lock(); clk.t = clk.t.Add(d); clk.mu.Unlock() }
		h.Refresh(t.Context())
		return h, &db, advance
	}
	outage := Transient(errors.New("connection refused"))

	t.Run("R-OPS-001 a transient failure shorter than the grace keeps the replica ready and shows degraded", func(t *testing.T) {
		h, db, advance := newHealth(20 * time.Second)
		db.set(outage)
		for range 3 { // 3 checks, 5 s apart: 10 s of failure, past failure_threshold 2
			advance(5 * time.Second)
			h.Refresh(t.Context())
		}
		code, body := get(t, h.ReadyHandler(), "/readyz?verbose=1")
		if code != 200 || body["status"] != "ready" {
			t.Fatalf("got %d %v", code, body)
		}
		if d, _ := body["degraded"].([]any); len(d) != 1 || d[0] != "database" {
			t.Fatalf("degraded = %v", body["degraded"])
		}
		if st := h.Snapshot().Checks[0].Status; st != Degraded {
			t.Fatalf("status = %s", st)
		}
	})

	t.Run("R-OPS-001 the failure fails readiness once it has lasted the grace, and recovery follows the success threshold", func(t *testing.T) {
		h, db, advance := newHealth(20 * time.Second)
		db.set(outage)
		for range 5 {
			advance(5 * time.Second)
			h.Refresh(t.Context())
		}
		if h.Ready() {
			t.Fatal("25 s of outage with a 20 s grace must fail readiness")
		}
		db.set(nil)
		h.Refresh(t.Context())
		if h.Ready() {
			t.Fatal("one success must not pass a check with success_threshold 2")
		}
		h.Refresh(t.Context())
		if !h.Ready() {
			t.Fatal("two successes must restore readiness")
		}
	})

	t.Run("R-OPS-001 a failure that is not transient gates at once, grace or not", func(t *testing.T) {
		h, db, advance := newHealth(20 * time.Second)
		db.set(errors.New("schema is behind"))
		for range 2 {
			advance(5 * time.Second)
			h.Refresh(t.Context())
		}
		if h.Ready() {
			t.Fatal("a failure that is not marked transient must gate after the debounce")
		}
	})

	t.Run("R-OPS-001 passes that restore the check between two outages restart the grace", func(t *testing.T) {
		h, db, advance := newHealth(20 * time.Second)
		for range 2 {
			db.set(outage)
			for range 3 {
				advance(5 * time.Second)
				h.Refresh(t.Context())
			}
			if !h.Ready() {
				t.Fatal("a 15 s outage is inside the 20 s grace, however many outages came before")
			}
			db.set(nil)
			for range 2 { // success_threshold 2
				advance(5 * time.Second)
				h.Refresh(t.Context())
			}
		}
		if !h.Ready() {
			t.Fatal("two 15 s outages with a recovery between them stay inside the grace")
		}
	})

	t.Run("R-OPS-001 a single pass during the outage does not restart the grace", func(t *testing.T) {
		h, db, advance := newHealth(20 * time.Second)
		db.set(outage)
		for range 3 {
			advance(5 * time.Second)
			h.Refresh(t.Context())
		}
		db.set(nil)
		advance(5 * time.Second)
		h.Refresh(t.Context()) // one pass: the check is still failing (success_threshold 2)
		db.set(outage)
		for range 2 {
			advance(5 * time.Second)
			h.Refresh(t.Context())
		}
		if h.Ready() {
			t.Fatal("25 s since the first failure must be past the 20 s grace")
		}
	})

	t.Run("R-OPS-001 CR2-1 a check already failing for a non-transient reason never earns a grace", func(t *testing.T) {
		h, db, advance := newHealth(20 * time.Second)
		db.set(errors.New("schema is behind"))
		for range 3 {
			advance(5 * time.Second)
			h.Refresh(t.Context())
		}
		if h.Ready() {
			t.Fatal("setup: a schema that is behind must gate")
		}
		db.set(outage) // the database goes away as well
		advance(5 * time.Second)
		h.Refresh(t.Context())
		if h.Ready() {
			t.Fatalf("a replica whose schema is behind turned ready on a transient answer; failing=%v degraded=%v", h.Failing(), h.Degraded())
		}
	})

	t.Run("R-OPS-001 CR2-1 the grace is not reopened by a non-transient answer inside one failing run", func(t *testing.T) {
		h, db, advance := newHealth(20 * time.Second)
		db.set(outage)
		for range 6 { // 30 s
			advance(5 * time.Second)
			h.Refresh(t.Context())
		}
		if h.Ready() {
			t.Fatal("setup: 30 s of outage with a 20 s grace must gate")
		}
		db.set(errors.New("password authentication failed"))
		advance(5 * time.Second)
		h.Refresh(t.Context())
		db.set(outage)
		advance(5 * time.Second)
		h.Refresh(t.Context())
		if h.Ready() {
			t.Fatal("after 40 s of one unbroken failing run the replica is ready again (grace reopened)")
		}
	})

	t.Run("R-OPS-001 gap-b a second check with the same grace never takes the replica out before the database check does", func(t *testing.T) {
		clk := &clock{t: time.Unix(1_700_000_000, 0)}
		h := New(cfg(), WithClock(func() time.Time { clk.mu.Lock(); defer clk.mu.Unlock(); return clk.t }))
		var db, inv toggle
		h.AddReadiness("database", db.check, false, WithGrace(20*time.Second))
		h.AddReadiness("invalidation", inv.check, false, WithGrace(20*time.Second))
		h.Refresh(t.Context())
		// The database goes away at once; the poll goes stale 20 s later, as
		// cluster.epoch_max_staleness (20 s) makes it.
		db.set(outage)
		for i := range 8 { // 40 s in steps of 5 s
			clk.mu.Lock()
			clk.t = clk.t.Add(5 * time.Second)
			clk.mu.Unlock()
			if i >= 4 {
				inv.set(outage)
			}
			h.Refresh(t.Context())
			failing := h.Failing()
			for _, f := range failing {
				if f == "invalidation" && !slices.Contains(failing, "database") {
					t.Fatalf("at %d s the invalidation check gated alone; failing=%v", (i+1)*5, failing)
				}
			}
		}
		if h.Ready() {
			t.Fatal("40 s of outage must have gated through the database check")
		}
	})

	t.Run("R-OPS-001 the first readiness gets no grace", func(t *testing.T) {
		clk := time.Unix(1_700_000_000, 0)
		h := New(cfg(), WithClock(func() time.Time { return clk }))
		var db toggle
		db.set(outage)
		h.AddReadiness("database", db.check, false, WithGrace(20*time.Second))
		h.Refresh(t.Context())
		if h.Ready() {
			t.Fatal("a replica that never was ready must not become ready on a failing database")
		}
	})

	t.Run("R-OPS-001 a check without a grace gates as before", func(t *testing.T) {
		h, db, advance := newHealth(0)
		db.set(outage)
		for range 2 {
			advance(5 * time.Second)
			h.Refresh(t.Context())
		}
		if h.Ready() {
			t.Fatal("grace 0 means a transient failure gates after the debounce")
		}
	})
}
