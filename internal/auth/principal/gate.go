package principal

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/errs"
)

// Freshness tells whether the replica's last completed change-log poll started
// less than d ago. *cluster.Changes implements it (FreshWithin).
type Freshness interface {
	FreshWithin(d time.Duration) bool
}

// Gate is the freshness gate of R-AUTH-023 (Q-AUTH-10): the auth caches answer
// only while the replica's view of the change log is younger than the
// revocation bound; otherwise every lookup goes to PostgreSQL and is not cached.
type Gate struct {
	// Fresh is the change log; nil means a replica without one (a unit test),
	// whose caches are always usable.
	Fresh Freshness
	// Bound is auth.revocation_bound.
	Bound time.Duration
	// Metrics counts the lookups that bypass the caches; nil counts nothing.
	Metrics *Metrics
}

// Open reports whether the caches may answer. A closed gate counts one bypass.
func (g Gate) Open() bool {
	if g.Fresh == nil || g.Fresh.FreshWithin(g.Bound) {
		return true
	}
	g.Metrics.bypassed("changes_stale")
	return false
}

// Metrics are the identity metrics of R-AUTH-041 that the caches raise. A nil
// *Metrics is valid and counts nothing.
type Metrics struct {
	cacheBypassed      *prometheus.CounterVec
	cacheInvalidations prometheus.Counter
	groupClaimMissing  *prometheus.CounterVec
	attempts           *prometheus.CounterVec
	ratelimited        *prometheus.CounterVec
	throttleExempt     *prometheus.CounterVec
	lastUsedFlushFails prometheus.Counter
}

// NewMetrics creates the metrics and registers them on reg (nil: unregistered).
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		cacheBypassed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_auth_cache_bypassed_total",
			Help: "Credential and principal lookups that bypassed the auth caches because the replica's change-log poll was older than auth.revocation_bound.",
		}, []string{"reason"}),
		cacheInvalidations: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "trove_auth_cache_invalidations_total",
			Help: "Changes of the area auth applied to the auth caches of this replica.",
		}),
		groupClaimMissing: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_auth_group_claim_missing_total",
			Help: "Logins whose group claim was missing, by provider (name) and reason (absent, wrong_type, overage) (R-AUTH-008, Q-AUTH-39).",
		}, []string{"name", "reason"}),
		attempts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_auth_attempts_total",
			Help: "Authentication attempts of the stage, by credential method, outcome and reason.",
		}, []string{"method", "outcome", "reason"}),
		ratelimited: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_auth_ratelimited_total",
			Help: "Requests refused by an authentication limit (credential_failures).",
		}, []string{"limit"}),
		throttleExempt: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "trove_auth_throttle_exempt_total",
			Help: "Credentials a throttled address could still validate, by the exemption that applied (marker).",
		}, []string{"via"}),
		lastUsedFlushFails: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "trove_auth_lastused_flush_failures_total",
			Help: "Flushes of the token last-used buffer that failed; the batch is dropped and the next use records the token again (R-AUTH-031).",
		}),
	}
	if reg == nil {
		return m, nil
	}
	for _, c := range []prometheus.Collector{m.cacheBypassed, m.cacheInvalidations, m.groupClaimMissing, m.attempts, m.ratelimited, m.throttleExempt, m.lastUsedFlushFails} {
		if err := reg.Register(c); err != nil {
			var are prometheus.AlreadyRegisteredError
			if errors.As(err, &are) {
				return nil, errors.New("principal: identity metrics are already registered on this registerer")
			}
			return nil, err
		}
	}
	return m, nil
}

func (m *Metrics) bypassed(reason string) {
	m.Bypassed(reason)
}

// Bypassed counts one lookup that bypassed an auth cache, by reason; the session
// cache of the login area counts its own bypasses here.
func (m *Metrics) Bypassed(reason string) {
	if m != nil {
		m.cacheBypassed.WithLabelValues(reason).Inc()
	}
}

// Invalidated counts one applied change; the cache hooks of the identity
// packages call it.
func (m *Metrics) Invalidated() {
	if m != nil {
		m.cacheInvalidations.Inc()
	}
}

// Flights shares one lookup among concurrent callers while the gate is closed
// (R-AUTH-023): a caller joins a running lookup of the same key only if that
// lookup started less than the bound before the caller arrived, so the shared
// result is newer than every change older than the bound.
type Flights[K comparable, V any] struct {
	mu sync.Mutex
	m  map[K]*flight[V]
}

type flight[V any] struct {
	start time.Time
	done  chan struct{}
	val   V
	err   error
}

// flightTimeout bounds a shared lookup, which no request's end can cancel. It is above the retry budget (database.retry.max_elapsed, 30 s by
// default) plus one statement, because the budget is what ends a lookup during an outage: a bound below it would answer 503 in the middle of a
// failover that the budget rides out. It only ends a statement that hangs on a connection whose route is gone.
const flightTimeout = time.Minute

// Do returns the result of fn for k, from the running call or from a new one. The call runs on its own and every caller, the one that started it
// too, waits for it under its own context (Q-AUTH-208): a caller that goes away stops waiting and takes the answer from nobody, and a caller that
// stays gets the answer of the lookup it joined, within that lookup's retry budget.
func (f *Flights[K, V]) Do(ctx context.Context, k K, bound time.Duration, fn func(context.Context) (V, error)) (V, error) {
	f.mu.Lock()
	if f.m == nil {
		f.m = map[K]*flight[V]{}
	}
	fl, ok := f.m[k]
	if !ok || time.Since(fl.start) >= bound {
		fl = &flight[V]{start: time.Now(), done: make(chan struct{})}
		f.m[k] = fl
		go f.run(ctx, k, fl, fn)
	}
	f.mu.Unlock()
	select {
	case <-fl.done:
		return fl.val, fl.err
	case <-ctx.Done():
		var zero V
		return zero, ctx.Err()
	}
}

// run is the shared lookup. Its context keeps the values of the request that started it and not that request's end.
func (f *Flights[K, V]) run(ctx context.Context, k K, fl *flight[V], fn func(context.Context) (V, error)) {
	defer func() {
		// No net/http goroutine recovers a panic here: it would end the process and leave every waiter without an answer.
		if p := recover(); p != nil {
			var zero V
			fl.val, fl.err = zero, errs.E(errs.Internal, errs.CodeInternal, "The request failed.", errs.WithCause(fmt.Errorf("a shared lookup panicked: %v\n%s", p, debug.Stack())))
		}
		// Leave the map before the answer is out: a caller that comes after the answer starts its own lookup instead of joining one that has ended.
		f.mu.Lock()
		if f.m[k] == fl {
			delete(f.m, k)
		}
		f.mu.Unlock()
		close(fl.done)
	}()
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), flightTimeout)
	defer cancel()
	fl.val, fl.err = fn(fctx)
}

func (m *Metrics) claimMissing(provider, reason string) {
	if m != nil {
		m.groupClaimMissing.WithLabelValues(provider, reason).Inc()
	}
}

// Attempt counts one authentication attempt of the stage.
func (m *Metrics) Attempt(method, outcome, reason string) {
	if m != nil {
		m.attempts.WithLabelValues(method, outcome, reason).Inc()
	}
}

// RateLimited counts one request refused by an authentication limit.
func (m *Metrics) RateLimited(limit string) {
	if m != nil {
		m.ratelimited.WithLabelValues(limit).Inc()
	}
}

// LastUsedFlushFailed counts one failed flush of the token last-used buffer.
func (m *Metrics) LastUsedFlushFailed() {
	if m != nil {
		m.lastUsedFlushFails.Inc()
	}
}

// ThrottleExempt counts one credential that a throttled address could still
// validate.
func (m *Metrics) ThrottleExempt(via string) {
	if m != nil {
		m.throttleExempt.WithLabelValues(via).Inc()
	}
}
