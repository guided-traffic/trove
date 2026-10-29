// Package health is the readiness and liveness registry of a replica
// (R-OPS-001, R-ARCH-024, Q-OPS-1, Q-OPS-12, Q-HA-13).
//
// Readiness is a set of named checks. A background loop runs them every
// health.check_interval, each bounded by health.check_timeout, and the probe
// handlers answer from the cached results only: a probe never opens a database
// connection or sends a Valkey command. A check changes state only after
// health.failure_threshold consecutive failures (pass to fail) or
// health.success_threshold consecutive successes (fail to pass).
//
// A check has one of three kinds:
//   - gating: a failure makes the replica unready (AddReadiness with first=false);
//   - first: gating until the replica has been ready once, reported afterwards
//     (AddReadiness with first=true; the object store, Q-OPS-2);
//   - reported: never gating, except that an error marked with ConfigError holds
//     back the first readiness (AddReported; Valkey, Q-HA-13).
//
// A failing reported check shows under "degraded" and the replica stays ready.
//
// A gating check may carry an outage grace (WithGrace). Once the replica has been
// ready, a failure that the check marks as Transient (a database that is
// unreachable, not one that refuses the schema) shows as "degraded" and does not
// gate until it has lasted the grace; after that it gates like any other. The
// first readiness never gets a grace.
//
// Liveness is a watchdog over trove's own loops (Q-OPS-1): each loop records
// progress at the end of every iteration, and /healthz fails only when one has
// not finished an iteration within its bound. Liveness never reads a readiness
// result.
package health

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"runtime/pprof"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/guided-traffic/trove/internal/config"
)

// Status is the state of one check as /readyz?verbose=1 and the replica
// registry report it.
type Status string

// The three states of a check (R-OPS-001).
const (
	Pass     Status = "pass"
	Fail     Status = "fail"     // a gating check that failed
	Degraded Status = "degraded" // a reported check that failed
)

// State is the replica state of the registry row (R-OPS-003).
type State string

// The replica states.
const (
	StateStarting State = "starting" // not ready yet, never was
	StateReady    State = "ready"
	StateUnready  State = "unready" // was ready, a gating check fails
	StateDraining State = "draining"
)

// Names of the checks every replica has that this package defines itself.
const (
	CheckDraining = "draining"
	// LoopChecks is the watchdog name of the check loop (R-OPS-001).
	LoopChecks = "health.checks"
)

// dumpLimit truncates the goroutine dump of a stuck loop (R-OPS-001).
const dumpLimit = 256 << 10

// CheckFunc runs one check. It must honour its context: the loop abandons a
// check that exceeds health.check_timeout, but the call keeps running.
type CheckFunc func(ctx context.Context) error

// Observer is told after every evaluation of a check; it feeds the gauge
// trove_health_check_passing{name}. passing is false for a failing check,
// whatever its kind.
type Observer func(name string, passing bool)

type kind int

const (
	kindGating kind = iota
	kindFirst
	kindReported
)

type check struct {
	name  string
	kind  kind
	fn    CheckFunc
	every time.Duration // 0 means health.check_interval
	grace time.Duration // 0 means a failure gates as soon as it is debounced

	mu         sync.Mutex
	running    bool
	lastRun    time.Time
	pending    bool // no result yet
	failing    bool // debounced state
	consecFail int
	consecPass int
	lastErr    error
	reason     string
	configErr  bool
	since      time.Time
	latency    time.Duration
	// runSince is when the current run of failures began (its first failing
	// answer, before the debounce). The run ends only once the check passes again
	// (success_threshold), so neither a flapping recovery nor a change of the
	// failure's cause restarts the clock.
	runSince time.Time
	// runBroken is set by the first failure in the run that is not transient. It
	// stays set until the run ends: a check that has been failing for a reason
	// that does not pass by itself never earns a grace (R-OPS-001).
	runBroken bool
}

// CheckOption adjusts one check.
type CheckOption func(*check)

// WithInterval runs the check at its own interval instead of
// health.check_interval (the valkey check runs every valkey.health_check_interval).
func WithInterval(d time.Duration) CheckOption { return func(c *check) { c.every = d } }

// WithGrace makes a gating check tolerate an outage: once the replica has been
// ready, a failure marked Transient does not make it unready until the failure
// has lasted d (health.database_outage_grace for the database checks). Until
// then the check shows as degraded. d <= 0 means no grace.
func WithGrace(d time.Duration) CheckOption { return func(c *check) { c.grace = d } }

type loop struct {
	progress func() time.Time
	max      time.Duration
	reported bool
}

// Health is the registry. The zero value is not usable; call New.
type Health struct {
	cfg      config.Health
	log      *slog.Logger
	replica  string
	role     string
	now      func() time.Time
	observer Observer

	draining atomic.Bool

	// gate and wg track the goroutines that run checks. Close stops new ones and
	// waits for the running ones, so nothing logs after Run has returned.
	gate   sync.RWMutex
	closed bool
	wg     sync.WaitGroup

	mu        sync.RWMutex
	checks    []*check
	loops     map[string]*loop
	everReady bool
	checkedAt time.Time
	wake      chan struct{}
}

// Option adjusts New.
type Option func(*Health)

// WithLogger sets the logger for check transitions and the stuck-loop record.
func WithLogger(l *slog.Logger) Option { return func(h *Health) { h.log = l } }

// WithIdentity sets the replica name and role shown by /readyz?verbose=1.
func WithIdentity(replica, role string) Option {
	return func(h *Health) { h.replica, h.role = replica, role }
}

// WithClock replaces time.Now; tests use it to age a loop's progress.
func WithClock(now func() time.Time) Option { return func(h *Health) { h.now = now } }

// WithObserver registers the callback behind trove_health_check_passing.
func WithObserver(o Observer) Option { return func(h *Health) { h.observer = o } }

// New returns an empty registry. A registry without checks is ready.
func New(cfg config.Health, opts ...Option) *Health {
	h := &Health{
		cfg:   cfg,
		now:   time.Now,
		loops: map[string]*loop{},
		wake:  make(chan struct{}, 1),
	}
	for _, o := range opts {
		o(h)
	}
	if h.log == nil {
		h.log = slog.New(slog.DiscardHandler)
	}
	return h
}

// AddReadiness registers a check. With first false it gates readiness for the
// life of the process; with first true it gates only until the replica has been
// ready once and is reported afterwards.
func (h *Health) AddReadiness(name string, fn CheckFunc, first bool, opts ...CheckOption) {
	k := kindGating
	if first {
		k = kindFirst
	}
	h.add(name, k, fn, opts)
}

// AddReported registers a reported check: its failure never makes the replica
// unready. An error marked with ConfigError holds back the first readiness
// only (Q-HA-13).
func (h *Health) AddReported(name string, fn CheckFunc, opts ...CheckOption) {
	h.add(name, kindReported, fn, opts)
}

func (h *Health) add(name string, k kind, fn CheckFunc, opts []CheckOption) {
	c := &check{name: name, kind: k, fn: fn, pending: true, failing: true, reason: "starting"}
	for _, o := range opts {
		o(c)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range h.checks {
		if e.name == name {
			panic("health: check " + name + " registered twice")
		}
	}
	h.checks = append(h.checks, c)
}

// AddLiveness registers a watched loop: progress returns the time its last
// iteration finished, and the loop is stuck when that is older than max. A
// loop that stops on purpose calls RemoveLiveness first (R-OPS-001).
func (h *Health) AddLiveness(name string, progress func() time.Time, max time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.loops[name] = &loop{progress: progress, max: max}
}

// RemoveLiveness takes a loop out of the watchdog.
func (h *Health) RemoveLiveness(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.loops, name)
}

// Loop is a watched loop with a built-in progress clock.
type Loop struct {
	h    *Health
	name string
	last atomic.Int64
}

// Loop registers a watched loop and counts the registration as progress, so a
// loop that registers when it starts cannot be stuck before its first iteration.
// Call Beat at the end of every iteration, failed ones included, and Stop when
// the loop ends on purpose.
func (h *Health) Loop(name string, max time.Duration) *Loop {
	l := &Loop{h: h, name: name}
	l.Beat()
	h.AddLiveness(name, l.Last, max)
	return l
}

// Beat records that an iteration finished.
func (l *Loop) Beat() { l.last.Store(l.h.now().UnixNano()) }

// Last returns the time of the last Beat.
func (l *Loop) Last() time.Time { return time.Unix(0, l.last.Load()) }

// Stop leaves the watchdog.
func (l *Loop) Stop() { l.h.RemoveLiveness(l.name) }

// SetDraining fails readiness at once and without debounce (R-ARCH-024 step 1).
// Liveness is unaffected.
func (h *Health) SetDraining() {
	if h.draining.Swap(true) {
		return
	}
	h.log.Info("readiness off: draining")
	if h.observer != nil {
		h.observer(CheckDraining, false)
	}
}

// Draining reports whether SetDraining was called.
func (h *Health) Draining() bool { return h.draining.Load() }

// Refresh runs every check now and waits for the results. The composition root
// calls it when the start-up check turns true, so first readiness does not wait
// for the next tick.
func (h *Health) Refresh(ctx context.Context) { h.runDue(ctx, true) }

// Run is the check loop. It returns when ctx ends. It watches itself under the
// name LoopChecks with a bound of three check intervals.
//
// When ctx ends, Run waits for the checks that are still running before it
// returns (see Close): the check functions got a cancelled context, and none of
// them runs, logs or records a result once Run is done.
func (h *Health) Run(ctx context.Context) {
	self := h.Loop(LoopChecks, 3*h.cfg.CheckInterval)
	defer self.Stop()
	defer h.Close()
	h.runDue(ctx, true)
	self.Beat()
	t := time.NewTicker(h.tick())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-h.wake:
		}
		h.runDue(ctx, false)
		self.Beat()
	}
}

// closeWait bounds how long Close waits for a check that ignores its context.
func (h *Health) closeWait() time.Duration { return h.cfg.CheckTimeout + time.Second }

// Close stops the registry from starting checks and waits for the running ones,
// at most health.check_timeout plus one second: a check that ignores its context
// cannot hold the shutdown. It is safe to call more than once; Run calls it.
func (h *Health) Close() {
	h.gate.Lock()
	h.closed = true
	h.gate.Unlock()
	done := make(chan struct{})
	go func() { h.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(h.closeWait()):
		h.log.Warn("a readiness check did not stop in time")
	}
}

// tick is the smallest interval any check asks for.
func (h *Health) tick() time.Duration {
	d := h.cfg.CheckInterval
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, c := range h.checks {
		if c.every > 0 && c.every < d {
			d = c.every
		}
	}
	return d
}

// runDue runs the checks whose interval has elapsed (all of them with force).
func (h *Health) runDue(ctx context.Context, force bool) {
	h.mu.RLock()
	cs := append([]*check(nil), h.checks...)
	h.mu.RUnlock()
	now := h.now()
	var wg sync.WaitGroup
	for _, c := range cs {
		every := c.every
		if every == 0 {
			every = h.cfg.CheckInterval
		}
		c.mu.Lock()
		due := force || now.Sub(c.lastRun) >= every-every/10
		c.mu.Unlock()
		if !due {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.runCheck(ctx, c)
		}()
	}
	wg.Wait()
	h.mu.Lock()
	h.checkedAt = h.now()
	h.mu.Unlock()
	h.evaluate()
}

// errCheckTimeout is the result of a check that exceeded health.check_timeout.
var errCheckTimeout = reasoned{errors.New("check timed out"), "timeout"}

type reasoned struct {
	error
	reason string
}

func (r reasoned) Unwrap() error        { return r.error }
func (r reasoned) HealthReason() string { return r.reason }

type configError struct{ reasoned }

func (configError) HealthConfigError() bool { return true }

// WithReason attaches the short, sanitised reason /readyz?verbose=1 shows for
// err. The text of err itself is never shown: it may name hosts (R-OPS-001).
func WithReason(err error, reason string) error {
	if err == nil {
		return nil
	}
	return reasoned{err, reason}
}

// ConfigError marks err as a configuration error: a rejected credential, a failed
// TLS verification or an unknown primary name. A reported check that fails with
// one holds back the first readiness (Q-HA-13). reason is the check's reason
// (auth, tls, no_primary).
func ConfigError(err error, reason string) error {
	if err == nil {
		return nil
	}
	return configError{reasoned{err, reason}}
}

// Transient marks err as an outage that may pass by itself, such as an
// unreachable database: a check with a grace (WithGrace) keeps the replica ready
// while the failures stay transient and younger than the grace. The text of err
// is not changed.
func Transient(err error) error {
	if err == nil {
		return nil
	}
	return transientError{err}
}

type transientError struct{ error }

func (e transientError) Unwrap() error       { return e.error }
func (transientError) HealthTransient() bool { return true }

// IsTransient reports whether err (or an error it wraps) is marked Transient. A
// check that exceeded health.check_timeout counts: a hang is not a verdict.
func IsTransient(err error) bool {
	if errors.Is(err, errCheckTimeout) {
		return true
	}
	var t interface{ HealthTransient() bool }
	return errors.As(err, &t) && t.HealthTransient()
}

// IsConfigError reports whether err (or an error it wraps) is a ConfigError.
func IsConfigError(err error) bool {
	var c interface{ HealthConfigError() bool }
	return errors.As(err, &c) && c.HealthConfigError()
}

func reasonOf(err error) string {
	var r interface{ HealthReason() string }
	if errors.As(err, &r) {
		return r.HealthReason()
	}
	return "failed"
}

// runCheck evaluates one check, bounded by health.check_timeout, and applies the
// debounce. A check that ignores its context and hangs is reported as timed out
// and is not started again while its first call still runs.
func (h *Health) runCheck(ctx context.Context, c *check) {
	c.mu.Lock()
	c.lastRun = h.now()
	if c.running {
		c.mu.Unlock()
		h.record(c, errCheckTimeout, 0)
		return
	}
	c.running = true
	c.mu.Unlock()

	h.gate.RLock()
	if h.closed {
		h.gate.RUnlock()
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
		return
	}
	h.wg.Add(1)
	h.gate.RUnlock()

	cctx, cancel := context.WithTimeout(ctx, h.cfg.CheckTimeout)
	defer cancel()
	start := time.Now()
	done := make(chan error, 1)
	go func() {
		defer h.wg.Done()
		err := callGuarded(cctx, c.fn)
		c.mu.Lock()
		c.running = false
		c.mu.Unlock()
		done <- err
	}()
	var err error
	select {
	case err = <-done:
	case <-cctx.Done():
		if ctx.Err() != nil {
			return // shutting down: no result
		}
		err = errCheckTimeout
	}
	h.record(c, err, time.Since(start))
}

// callGuarded runs fn and turns a panic into a failed check.
func callGuarded(ctx context.Context, fn CheckFunc) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = reasoned{errors.New("check panicked"), "panic"}
		}
	}()
	return fn(ctx)
}

// record applies one result with the debounce of R-OPS-001.
func (h *Health) record(c *check, err error, latency time.Duration) {
	c.mu.Lock()
	wasFailing := c.failing
	c.latency = latency
	if err != nil {
		if c.runSince.IsZero() {
			c.runSince = h.now()
		}
		if !IsTransient(err) {
			c.runBroken = true
		}
	}
	if err == nil {
		c.consecPass++
		c.consecFail = 0
		if c.failing && (c.pending || c.consecPass >= h.cfg.SuccessThreshold) {
			c.failing, c.lastErr, c.reason, c.configErr = false, nil, "", false
			c.since = h.now()
		}
		if !c.failing {
			c.runSince, c.runBroken = time.Time{}, false
		}
	} else {
		c.consecFail++
		c.consecPass = 0
		if c.pending || (!c.failing && c.consecFail >= h.cfg.FailureThreshold) {
			c.failing = true
			c.since = h.now()
		}
		if c.failing {
			c.lastErr, c.reason, c.configErr = err, reasonOf(err), IsConfigError(err)
		}
	}
	first := c.pending
	c.pending = false
	failing, reason, name := c.failing, c.reason, c.name
	c.mu.Unlock()

	if failing != wasFailing || (failing && first) {
		if failing {
			h.log.Warn("readiness check failing", "check", name, "reason", reason, "error", err)
		} else {
			h.log.Info("readiness check passing", "check", name)
		}
	}
	if h.observer != nil {
		h.observer(name, !failing)
	}
}

// CheckResult is the cached result of one check.
type CheckResult struct {
	Name        string
	Status      Status
	Reason      string
	ConfigError bool
	Since       time.Time
	Latency     time.Duration
}

// Snapshot is the cached view the probes and the replica registry read.
type Snapshot struct {
	State   State
	Checks  []CheckResult
	Failing []string // gating checks that fail, including "draining"
	// Serving is true while the replica answers requests: no gating check other
	// than "draining" fails, and a draining replica had been ready before. It is
	// what GET /api/v1/system/ping reports (R-OPS-002), so the drain delay
	// serves every request that still arrives.
	Serving   bool
	Degraded  []string // reported checks that fail
	CheckedAt time.Time
}

// evaluate latches the first readiness. It is called after every round of checks
// and after SetDraining.
func (h *Health) evaluate() { _ = h.Snapshot() }

// Snapshot evaluates the cached results. It runs no check.
func (h *Health) Snapshot() Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := Snapshot{CheckedAt: h.checkedAt}
	for _, c := range h.checks {
		c.mu.Lock()
		r := CheckResult{Name: c.name, Status: Pass, Since: c.since, Latency: c.latency}
		if c.failing {
			r.Reason, r.ConfigError = c.reason, c.configErr
			if h.gates(c) {
				r.Status = Fail
				s.Failing = append(s.Failing, c.name)
			} else {
				r.Status = Degraded
				s.Degraded = append(s.Degraded, c.name)
			}
		}
		c.mu.Unlock()
		s.Checks = append(s.Checks, r)
	}
	dr := CheckResult{Name: CheckDraining, Status: Pass}
	if h.draining.Load() {
		dr.Status = Fail
		s.Failing = append(s.Failing, CheckDraining)
	}
	s.Checks = append(s.Checks, dr)
	sort.Strings(s.Failing)
	sort.Strings(s.Degraded)

	ready := len(s.Failing) == 0
	if ready {
		h.everReady = true
	}
	s.Serving = true
	for _, name := range s.Failing {
		if name != CheckDraining {
			s.Serving = false
		}
	}
	if h.draining.Load() && !h.everReady {
		s.Serving = false
	}
	switch {
	case h.draining.Load():
		s.State = StateDraining
	case ready:
		s.State = StateReady
	case h.everReady:
		s.State = StateUnready
	default:
		s.State = StateStarting
	}
	return s
}

// gates reports whether a failing check holds the replica back right now. The
// caller holds h.mu and c.mu.
func (h *Health) gates(c *check) bool {
	switch c.kind {
	case kindGating:
		if c.grace > 0 && h.everReady && !c.runBroken && !c.runSince.IsZero() && h.now().Sub(c.runSince) < c.grace {
			return false
		}
		return true
	case kindFirst:
		return !h.everReady
	default:
		return !h.everReady && c.configErr
	}
}

// Ready reports whether the replica may take work: no gating check fails and it
// is not draining (Q-OPS-12). A ready worker claims jobs, a ready web replica
// answers /api/v1/system/ping with 200.
func (h *Health) Ready() bool { return len(h.Snapshot().Failing) == 0 }

// Serving reports whether the replica answers requests (Snapshot.Serving).
func (h *Health) Serving() bool { return h.Snapshot().Serving }

// Degraded returns the reported checks that currently fail.
func (h *Health) Degraded() []string { return h.Snapshot().Degraded }

// Failing returns the gating checks that currently fail.
func (h *Health) Failing() []string { return h.Snapshot().Failing }

// Stuck returns the watched loops that have not finished an iteration within
// their bound. The first time a loop is reported stuck, one ERROR record names
// it and carries a goroutine dump (R-OPS-001).
func (h *Health) Stuck() []string {
	now := h.now()
	h.mu.Lock()
	defer h.mu.Unlock()
	var stuck []string
	for name, l := range h.loops {
		if age := now.Sub(l.progress()); age > l.max {
			stuck = append(stuck, name)
			if !l.reported {
				l.reported = true
				h.log.Error("watched loop is stuck", "loop", name, "silent_for", age.String(), "bound", l.max.String(), "goroutines", goroutineDump())
			}
		} else {
			l.reported = false
		}
	}
	sort.Strings(stuck)
	return stuck
}

// goroutineDump renders the goroutine profile, truncated to 256 KiB.
func goroutineDump() string {
	var b bytes.Buffer
	if p := pprof.Lookup("goroutine"); p != nil {
		_ = p.WriteTo(&b, 2)
	}
	if b.Len() > dumpLimit {
		b.Truncate(dumpLimit)
	}
	return b.String()
}
