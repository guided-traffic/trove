package cluster

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/config"
)

// ErrNoLeasePool is returned when a lease manager is created without a lease
// pool: a web replica holds no session connection (R-ARCH-001, R-ARCH-017).
var ErrNoLeasePool = errors.New("cluster: this replica has no lease pool; only worker and all replicas hold leases")

// Causes of the end of a lease, as seen from Held.Context().
var (
	// ErrLeaseLost means the lease connection died or failed its check; the
	// server released the lock or will within its keepalive window.
	ErrLeaseLost = errors.New("cluster: the lease was lost")
	// ErrLeaseReleased means the replica gave the lease up on purpose.
	ErrLeaseReleased = errors.New("cluster: the lease was released")
)

// LeaseOptions configure [NewLeases].
type LeaseOptions struct {
	// Pool is the lease pool (postgres.DB.LeasePool). It is nil on a web
	// replica, which makes NewLeases return [ErrNoLeasePool].
	Pool *pgxpool.Pool
	// Config carries lease_retry_interval.
	Config config.Cluster
	// CheckInterval is database.lease_check_interval.
	CheckInterval   time.Duration
	WatchdogTimeout time.Duration
	Watchdog        Watchdog
	Log             *slog.Logger
	Metrics         prometheus.Registerer
}

// Leases runs the advisory-lock leases of a worker replica (R-ARCH-017,
// R-HA-006). A lease is a session-level lock on one lease-pool connection;
// the singleton's writes run on that same connection, so a replica that lost
// its session cannot commit anything.
type Leases struct {
	o   LeaseOptions
	log *slog.Logger
	m   *leaseMetrics

	mu      sync.Mutex
	holders map[LeaseID]*holder
	held    map[LeaseID]*Held
	started bool
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

type leaseMetrics struct {
	lost *prometheus.CounterVec
	held *prometheus.GaugeVec
}

// holder is the loop that tries to hold one lease.
type holder struct {
	id       LeaseID
	work     func(ctx context.Context, h *Held)
	released bool // guarded by Leases.mu
}

// NewLeases creates the lease manager.
func NewLeases(o LeaseOptions) (*Leases, error) {
	if o.Pool == nil {
		return nil, ErrNoLeasePool
	}
	m := &leaseMetrics{
		lost: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "trove_lease_lost_total", Help: "Leases lost because the lease connection failed, by task."}, []string{"task"}),
		held: prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "trove_lease_held", Help: "1 while this replica holds the lease of the task."}, []string{"task"}),
	}
	if err := register(o.Metrics, m.lost, m.held); err != nil {
		return nil, err
	}
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	return &Leases{o: o, log: log, m: m, holders: map[LeaseID]*holder{}, held: map[LeaseID]*Held{}}, nil
}

// Name implements the composition root's Component.
func (l *Leases) Name() string { return "leases" }

// Hold registers work that runs only while this replica holds the lease. The
// replica tries the lease every cluster.lease_retry_interval; work gets a
// context that ends when the lease is lost or released, and Held to write
// through. When work returns the lease is released and tried again after the
// interval. Hold may be called before or after Start.
func (l *Leases) Hold(id LeaseID, work func(ctx context.Context, h *Held)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, dup := l.holders[id]; dup {
		panic(fmt.Sprintf("cluster: lease %q is held twice by this replica", id.name))
	}
	h := &holder{id: id, work: work}
	l.holders[id] = h
	if l.started {
		l.wg.Add(1)
		go l.run(h)
	}
}

// Start begins the lease loop (watched as cluster.leases) and every registered
// holder.
func (l *Leases) Start(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.started {
		return errors.New("cluster: leases already started")
	}
	l.started = true
	l.ctx, l.cancel = context.WithCancel(context.WithoutCancel(ctx))
	l.wg.Add(1)
	go l.checkLoop()
	for _, h := range l.holders {
		l.wg.Add(1)
		go l.run(h)
	}
	return nil
}

// Release gives a lease up at once and stops trying it: the scheduler lease on
// drain, so another worker schedules from its next retry (R-ARCH-024 step 1).
// It returns when the lock is released or ctx ends.
func (l *Leases) Release(ctx context.Context, id LeaseID) {
	l.mu.Lock()
	if h := l.holders[id]; h != nil {
		h.released = true
	}
	held := l.held[id]
	l.mu.Unlock()
	if held == nil {
		return
	}
	held.cancel(ErrLeaseReleased)
	select {
	case <-held.done:
	case <-ctx.Done():
	}
}

// Stop releases every lease explicitly (pg_advisory_unlock), closes the loops
// and returns (R-ARCH-024 step 6).
func (l *Leases) Stop(ctx context.Context) error {
	l.mu.Lock()
	cancel := l.cancel
	l.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	done := make(chan struct{})
	go func() { l.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// HeldInfo describes a lease this replica holds, for the replica registry.
type HeldInfo struct {
	Name  string    `json:"name"`
	Since time.Time `json:"since"`
}

// Held lists the leases this replica holds now.
func (l *Leases) Held() []HeldInfo {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]HeldInfo, 0, len(l.held))
	for id, h := range l.held {
		if h.Valid() {
			out = append(out, HeldInfo{Name: id.name, Since: h.since})
		}
	}
	return out
}

func (l *Leases) run(h *holder) {
	defer l.wg.Done()
	for {
		l.mu.Lock()
		stop := h.released || l.ctx.Err() != nil
		l.mu.Unlock()
		if stop {
			return
		}
		held, err := l.acquire(l.ctx, h.id)
		switch {
		case err != nil:
			if l.ctx.Err() == nil {
				l.log.Warn("lease not acquired", "lease", h.id.name, "error", err)
			}
		case held != nil:
			l.log.Info("lease acquired", "lease", h.id.name)
			l.runWork(h, held)
		}
		l.mu.Lock()
		stop = h.released
		l.mu.Unlock()
		if stop {
			return
		}
		select {
		case <-l.ctx.Done():
			return
		case <-time.After(l.o.Config.LeaseRetryInterval):
		}
	}
}

func (l *Leases) runWork(h *holder, held *Held) {
	defer l.finish(held)
	h.work(held.ctx, held)
}

// acquire tries the lease once. It returns nil, nil when another replica holds
// it. There is no fencing token: writes of the holder run on the lease's own
// session (R-HA-006), so a holder that lost the lock cannot write.
func (l *Leases) acquire(ctx context.Context, id LeaseID) (*Held, error) {
	actx, cancel := context.WithTimeout(ctx, callTimeout(l.o.WatchdogTimeout))
	defer cancel()
	conn, err := l.o.Pool.Acquire(actx)
	if err != nil {
		return nil, fmt.Errorf("taking a lease connection: %w", err)
	}
	destroy := func() { _ = conn.Hijack().Close(context.WithoutCancel(ctx)) }
	var got bool
	if err := conn.QueryRow(actx, "SELECT pg_try_advisory_lock($1::int, $2::int)", LockLease, id.number).Scan(&got); err != nil {
		destroy()
		return nil, err
	}
	if !got {
		conn.Release()
		return nil, nil
	}
	hctx, hcancel := context.WithCancelCause(ctx)
	h := &Held{ID: id, l: l, conn: conn, ctx: hctx, cancel: hcancel, since: time.Now().UTC(), done: make(chan struct{})}
	h.valid.Store(true)
	l.mu.Lock()
	l.held[id] = h
	l.mu.Unlock()
	l.m.held.WithLabelValues(id.name).Set(1)
	return h, nil
}

// finish releases a lease whose work has returned, or that was lost.
func (l *Leases) finish(h *Held) {
	wasValid := h.valid.Swap(false)
	h.cancel(ErrLeaseReleased)
	if wasValid {
		h.release()
	}
	l.mu.Lock()
	if l.held[h.ID] == h {
		delete(l.held, h.ID)
	}
	l.mu.Unlock()
	l.m.held.WithLabelValues(h.ID.name).Set(0)
	close(h.done)
	l.log.Info("lease released", "lease", h.ID.name)
}

// lose marks a lease lost: the holder's context ends, the loss is counted and
// the connection destroyed, which also ends the server session.
func (l *Leases) lose(h *Held, cause error) {
	if !h.valid.CompareAndSwap(true, false) {
		return
	}
	h.cancel(fmt.Errorf("%w: %w", ErrLeaseLost, cause))
	l.m.lost.WithLabelValues(h.ID.name).Inc()
	l.log.Warn("lease lost", "lease", h.ID.name, "error", cause)
	go h.destroy()
}

// checkLoop pings every held lease connection with SELECT 1 each
// database.lease_check_interval and records progress whether the ping
// succeeded or failed (R-ARCH-017).
func (l *Leases) checkLoop() {
	defer l.wg.Done()
	loop := WatchLoop(l.o.Watchdog, "cluster.leases", l.o.WatchdogTimeout)
	defer loop.Stop()
	t := time.NewTicker(l.o.CheckInterval)
	defer t.Stop()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-t.C:
		}
		l.mu.Lock()
		hs := make([]*Held, 0, len(l.held))
		for _, h := range l.held {
			hs = append(hs, h)
		}
		l.mu.Unlock()
		for _, h := range hs {
			if !h.Valid() {
				continue
			}
			pctx, cancel := context.WithTimeout(l.ctx, min(callTimeout(l.o.WatchdogTimeout), l.o.CheckInterval))
			if err := h.ping(pctx); err != nil && l.ctx.Err() == nil {
				l.lose(h, err)
			}
			cancel()
		}
		loop.Beat()
	}
}
