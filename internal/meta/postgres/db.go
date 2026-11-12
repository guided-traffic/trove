package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/tlsfiles"
)

// DB holds the connection classes of one replica (R-ARCH-019).
//
//   - Main is the request pool of class main. It keeps no state beyond a
//     transaction, so it works direct, behind a session-mode pooler and behind
//     a transaction-mode pooler.
//   - Control is the one connection of class main reserved for the loops that
//     readiness and job ownership depend on. It has a pool of its own, so
//     request traffic cannot starve it, and with a transaction-mode pooler it
//     connects to the control endpoint (Q-HA-20).
//   - LeasePool is the session-scoped pool of class lease, opened only by
//     replicas that run singleton work.
type DB struct {
	cfg      config.Database
	log      *slog.Logger
	instance string
	role     string

	main    *pgxpool.Pool
	control *pgxpool.Pool
	lease   *pgxpool.Pool
	retry   retrier
	tls     *dbTLS

	closeOnce sync.Once
	// life ends when CloseContext starts: the statements of the readiness probes, which no check deadline cuts, end with it, so a probe
	// that started before or during the close does not hold the control connection past the close deadline. Nil for a DB built by a
	// test or by the backup snapshot, whose probes then run to their own bound.
	life       context.Context
	endingLife context.CancelFunc
}

// Open validates the configuration, opens the pools of the role, waits for the
// database up to retry.max_elapsed and runs the preflight of R-HA-013 and
// R-HA-014: server version, encoding, runtime role, and with a transaction-mode
// pooler the role defaults. A worker or all replica also checks that its session
// endpoint keeps session state (R-ARCH-017).
//
// instance is the replica's name (the pod name, or hostname-pid outside
// Kubernetes); it appears in application_name. role is api, packages, web, worker, all or
// backup. A replica of a role that claims no jobs opens no lease pool (R-ARCH-001).
//
// A configuration error or a failed preflight is a plain error and will not
// pass by retrying; a database that stays unreachable is IsUnavailable.
func Open(ctx context.Context, cfg config.Database, instance, role string, log *slog.Logger, opts ...Option) (*DB, error) {
	if log == nil {
		log = slog.Default()
	}
	var o openOptions
	for _, opt := range opts {
		opt(&o)
	}
	switch role {
	case meta.RoleAPI, meta.RolePackages, meta.RoleWeb, meta.RoleWorker, meta.RoleAll, meta.RoleBackup:
	default:
		return nil, fmt.Errorf("database: unknown role %q", role)
	}
	if instance == "" {
		return nil, errors.New("database: the instance name is empty")
	}
	if err := checkConfig(cfg); err != nil {
		return nil, err
	}
	db := &DB{cfg: cfg, log: log, instance: instance, role: role, retry: newRetrier(cfg.Retry)}
	db.life, db.endingLife = context.WithCancel(context.Background())
	db.retry.metrics = o.metrics
	var err error
	if role == meta.RoleBackup {
		if err := refuseStandby(ctx, cfg, instance); err != nil {
			return nil, err
		}
	}
	if db.tls, err = newDBTLS(cfg, tlsfiles.WithLogger(log), tlsfiles.WithMetrics(o.tlsMetrics)); err != nil {
		return nil, err
	}

	pooled := cfg.Pooler.Mode == "transaction"
	mainMax := int32(cfg.Pool.MaxConns - 1)           //nolint:gosec // validated range
	mainMin := min(int32(cfg.Pool.MinConns), mainMax) //nolint:gosec // validated range
	pool := func(s poolSpec, label string) (*pgxpool.Pool, error) {
		pc, err := newPoolConfig(cfg, s, db.tls)
		if err != nil {
			return nil, err
		}
		var next pgx.QueryTracer
		if s.kind.class == classMain {
			next = o.queryTracer
		}
		pc.ConnConfig.Tracer = stmtTracer{next: next, pool: label, metrics: o.metrics}
		return pgxpool.NewWithConfig(ctx, pc)
	}
	common := poolSpec{
		instance: instance,
		lifetime: cfg.Pool.MaxConnLifetime, jitter: cfg.Pool.MaxConnLifetimeJitter,
		idleTime: cfg.Pool.MaxConnIdleTime, healthEach: cfg.Pool.HealthCheckPeriod,
	}

	if role == meta.RoleBackup {
		// The backup pod's one pooled connection is its control connection (run row, heartbeat, pins): class backup, on the
		// session host, shared by the request path and the control path. The snapshot and export connections come on top of
		// it, so a run holds 2 + backup.database.parallel connections, all named trove:<pod>:backup (R-BAK-005).
		if err := db.openBackupPool(ctx, common, pool); err != nil {
			return nil, err
		}
	} else {
		ms := common
		ms.kind, ms.ep, ms.maxConns, ms.minConns = connKind{class: classMain, pooled: pooled}, mainEndpoint(cfg), mainMax, mainMin
		if db.main, err = pool(ms, poolRequest); err != nil {
			return nil, err
		}
		cs := common
		cs.kind, cs.ep, cs.maxConns, cs.minConns = connKind{class: classMain, pooled: pooled}, controlEndpoint(cfg), 1, 1
		if db.control, err = pool(cs, poolControl); err != nil {
			db.Close()
			return nil, err
		}
	}
	if meta.RunsWorkers(role) {
		ls := common
		// Session locks live as long as the connection: no lifetime, and an
		// idle connection holds no lock, so the idle timeout is harmless.
		ls.kind, ls.ep, ls.maxConns, ls.minConns = connKind{class: classLease}, sessionEndpoint(cfg), int32(cfg.LeasePoolSize), 0 //nolint:gosec // validated range
		ls.lifetime, ls.jitter = 0, 0
		if db.lease, err = pool(ls, poolLease); err != nil {
			db.Close()
			return nil, err
		}
	}

	if err := db.retry.do(ctx, func() error { return db.preflight(ctx) }); err != nil {
		db.Close()
		return nil, err
	}
	if db.lease != nil {
		if err := db.checkLeaseEndpoint(ctx); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

// Option configures Open.
type Option func(*openOptions)

type openOptions struct {
	tlsMetrics  *tlsfiles.Metrics
	queryTracer pgx.QueryTracer
	metrics     *Metrics
}

// WithQueryTracer traces every statement of the request pool (and of the control connection). It is for the tests that count the
// statements of a code path against its budget (R-ARCH-027, R-VULN-011).
func WithQueryTracer(t pgx.QueryTracer) Option { return func(o *openOptions) { o.queryTracer = t } }

// WithMetrics records the retries, the unknown commits and the wait for a
// connection (NewMetrics).
func WithMetrics(m *Metrics) Option { return func(o *openOptions) { o.metrics = m } }

// WithTLSMetrics records the expiry, reload and reload-error families of the
// root CA and the client certificate (kind database).
func WithTLSMetrics(m *tlsfiles.Metrics) Option { return func(o *openOptions) { o.tlsMetrics = m } }

// Main is the request pool: every request-path transaction, job claims and
// completions, job handlers. It has database.pool.max_conns - 1 connections;
// the last one is the control connection. It never holds session state.
func (db *DB) Main() *pgxpool.Pool { return db.main }

// Control acquires the reserved control connection, for the change-log poll,
// the database check, the registry heartbeat and the job heartbeats. The
// caller releases it; loops should hold it for one iteration only.
func (db *DB) Control(ctx context.Context) (*pgxpool.Conn, error) { return db.control.Acquire(ctx) }

// ControlPool is the pool of the control connection (size 1), for code that
// prefers pool-level calls such as QueryRow.
func (db *DB) ControlPool() *pgxpool.Pool { return db.control }

// LeasePool is the session-scoped pool of class lease, or nil unless the role is
// worker or all (R-ARCH-017). A connection taken from it is held for as long as
// its session-level advisory lock is.
func (db *DB) LeasePool() *pgxpool.Pool { return db.lease }

// Instance is the name the connections carry in application_name.
func (db *DB) Instance() string { return db.instance }

// Role is the role the DB was opened for.
func (db *DB) Role() string { return db.role }

// Config returns the configuration the DB was opened with.
func (db *DB) Config() config.Database { return db.cfg }

// probeStatementBound is how long a statement of a readiness probe may run once the probe has its connection. The deadline of a
// check bounds the wait for the control connection and so the check's answer, not the statement: a statement cut by the check's
// deadline (200 ms in the integration tests, 2 s by default) makes the driver close the connection, and the control pool, one
// connection big, then hands out nothing to the change-log poll, the heartbeat and the drain until the closed connection is
// cleaned up, which the pool waits for (Q-HA-284). The bound is that of the loops' own calls.
const probeStatementBound = 10 * time.Second

// probeStatementContext is the context of a probe's statements: the values of ctx, the bound of probeStatementBound, not the
// end of ctx, and the end of the DB's life: closing the pools cancels a statement that is still running, so the connection it holds
// comes back before the close deadline (R-ARCH-024 step 6).
func (db *DB) probeStatementContext(ctx context.Context) (context.Context, context.CancelFunc) {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), probeStatementBound)
	if db.life == nil {
		return sctx, cancel
	}
	stop := context.AfterFunc(db.life, cancel)
	return sctx, func() { stop(); cancel() }
}

// Ready is the `database` readiness check (R-OPS-001): a statement on the
// control connection that also proves the server is a primary, so a replica
// connected to a demoted primary turns unready instead of failing writes.
// ctx bounds the wait for the connection; the statement runs under
// probeStatementBound (Q-HA-284).
func (db *DB) Ready(ctx context.Context) error {
	conn, err := db.control.Acquire(ctx)
	if err != nil {
		return err
	}
	sctx, cancel := db.probeStatementContext(ctx)
	defer cancel()
	var primary bool
	err = conn.QueryRow(sctx, "SELECT NOT pg_is_in_recovery()").Scan(&primary)
	switch {
	case err == nil && primary:
		conn.Release()
		return nil
	case err == nil:
		// The control pool holds one connection, so a connection that landed on a
		// standby would stay the control connection until its lifetime ends. Destroying
		// it makes the next check connect again and find the new primary (CR2 gap c).
		_ = conn.Hijack().Close(context.Background())
		return ErrStandby
	case conn.Conn().IsClosed() || classify(err) == retryFailover:
		_ = conn.Hijack().Close(context.Background())
	default:
		conn.Release()
	}
	return err
}

// ErrStandby is what Ready reports for a connection to a server in recovery: a
// demoted primary during a switchover.
var ErrStandby = errors.New("the connected server is a standby")

// IsOutage reports whether err is the database being away rather than wrong: a
// connectivity failure, a statement that timed out, or a connection to a
// standby. A schema that is behind, or a rejected credential, is not an outage.
// The readiness checks of the database give such a failure the outage grace
// (health.database_outage_grace).
func IsOutage(err error) bool { return IsUnavailable(err) || errors.Is(err, ErrStandby) }

// PoolStats are the connection counts of one pool.
type PoolStats struct {
	Max, Total, Idle, Acquired int32
	AcquireCount               int64
	AcquireWait                int64 // nanoseconds spent waiting for a connection, in total
	EmptyAcquireCount          int64 // acquisitions that had to wait because no connection was idle
	CanceledAcquireCount       int64 // acquisitions the caller's context ended before they got a connection
	NewConns                   int64 // connections opened
}

// Stats are the pool counts of the three classes; Lease is zero on a web replica.
type Stats struct{ Main, Control, Lease PoolStats }

// Stats reports the pool counts for the trove_db_pool_* metrics.
func (db *DB) Stats() Stats {
	conv := func(p *pgxpool.Pool) PoolStats {
		if p == nil {
			return PoolStats{}
		}
		s := p.Stat()
		return PoolStats{
			Max: s.MaxConns(), Total: s.TotalConns(), Idle: s.IdleConns(), Acquired: s.AcquiredConns(),
			AcquireCount: s.AcquireCount(), AcquireWait: int64(s.AcquireDuration()),
			EmptyAcquireCount: s.EmptyAcquireCount(), CanceledAcquireCount: s.CanceledAcquireCount(), NewConns: s.NewConnsCount(),
		}
	}
	return Stats{Main: conv(db.main), Control: conv(db.control), Lease: conv(db.lease)}
}

// Close closes every pool and waits for every acquired connection to come back. It is safe to call more than once: only the first
// call closes, so after a CloseContext it returns at once, whatever that call left open.
func (db *DB) Close() { _ = db.CloseContext(context.Background()) }

// CloseContext closes every pool at once and waits until ctx ends. A pool does not close before every connection that was
// acquired from it has been returned, so a check, a poll or a detached write that still holds one would hold the close phase of
// the shutdown (R-ARCH-024 step 6) past its deadline, and the pod past its grace period (R-HA-011). When ctx ends first the
// error names the pools that are still open with the connections they hold; the pools finish closing in the background as the
// holders return them. Only the first call closes; a later one returns nil.
func (db *DB) CloseContext(ctx context.Context) error {
	var err error
	db.closeOnce.Do(func() {
		if db.endingLife != nil {
			db.endingLife()
		}
		var pools []closingPool
		for _, p := range []struct {
			name string
			pool *pgxpool.Pool
		}{{"main", db.main}, {"control", db.control}, {"lease", db.lease}} {
			if p.pool != nil {
				pools = append(pools, closingPool{name: p.name, close: p.pool.Close, held: func() int32 { st := p.pool.Stat(); return st.AcquiredConns() + st.ConstructingConns() }})
			}
		}
		err = closePools(ctx, pools)
	})
	return err
}

// closingPool is one pool as closePools sees it.
type closingPool struct {
	name  string
	close func()
	// held is the connections the close waits for: acquired ones and ones still under construction.
	held func() int32
}

func closePools(ctx context.Context, pools []closingPool) error {
	open := make([]atomic.Bool, len(pools))
	var wg sync.WaitGroup
	for i, p := range pools {
		open[i].Store(true)
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.close()
			open[i].Store(false)
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
	}
	var held []string
	for i, p := range pools {
		if !open[i].Load() {
			continue
		}
		if n := p.held(); n == 1 {
			held = append(held, p.name+" pool (1 connection held)")
		} else if n > 1 {
			held = append(held, fmt.Sprintf("%s pool (%d connections held)", p.name, n))
		}
	}
	if len(held) == 0 { // every pool closed as the deadline passed, or the ones left open hold nothing
		return nil
	}
	return fmt.Errorf("database pools still open at the close deadline: %s: %w", strings.Join(held, ", "), context.Cause(ctx))
}
