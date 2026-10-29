package principal

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
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
			Help: "Logins whose group claim was missing, by provider and reason (absent, wrong_type, overage).",
		}, []string{"provider", "reason"}),
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
	}
	if reg == nil {
		return m, nil
	}
	for _, c := range []prometheus.Collector{m.cacheBypassed, m.cacheInvalidations, m.groupClaimMissing, m.attempts, m.ratelimited, m.throttleExempt} {
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

// Do runs fn for k, or joins the running call. A joined call whose leader failed
// because the leader's context ended runs again with the caller's own context.
func (f *Flights[K, V]) Do(ctx context.Context, k K, bound time.Duration, fn func(context.Context) (V, error)) (V, error) {
	for attempt := 0; ; attempt++ {
		f.mu.Lock()
		if f.m == nil {
			f.m = map[K]*flight[V]{}
		}
		if cur, ok := f.m[k]; ok && time.Since(cur.start) < bound {
			f.mu.Unlock()
			select {
			case <-cur.done:
				if isContextErr(cur.err) && ctx.Err() == nil && attempt == 0 {
					continue
				}
				return cur.val, cur.err
			case <-ctx.Done():
				var zero V
				return zero, ctx.Err()
			}
		}
		fl := &flight[V]{start: time.Now(), done: make(chan struct{})}
		f.m[k] = fl
		f.mu.Unlock()

		fl.val, fl.err = fn(ctx)

		f.mu.Lock()
		if f.m[k] == fl {
			delete(f.m, k)
		}
		f.mu.Unlock()
		close(fl.done)
		return fl.val, fl.err
	}
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
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

// ThrottleExempt counts one credential that a throttled address could still
// validate.
func (m *Metrics) ThrottleExempt(via string) {
	if m != nil {
		m.throttleExempt.WithLabelValues(via).Inc()
	}
}
