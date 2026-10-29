package postgres

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

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
}

// Open validates the configuration, opens the pools of the role, waits for the
// database up to retry.max_elapsed and runs the preflight of R-HA-013 and
// R-HA-014: server version, encoding, runtime role, and with a transaction-mode
// pooler the role defaults. A worker or all replica also checks that its session
// endpoint keeps session state (R-ARCH-017).
//
// instance is the replica's name (the pod name, or hostname-pid outside
// Kubernetes); it appears in application_name. role is web, worker, all or
// backup. A web replica opens no lease pool (R-ARCH-001).
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
	case meta.RoleWeb, meta.RoleWorker, meta.RoleAll, meta.RoleBackup:
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
	var err error
	if db.tls, err = newDBTLS(cfg, tlsfiles.WithLogger(log), tlsfiles.WithMetrics(o.tlsMetrics)); err != nil {
		return nil, err
	}

	pooled := cfg.Pooler.Mode == "transaction"
	mainMax := int32(cfg.Pool.MaxConns - 1)           //nolint:gosec // validated range
	mainMin := min(int32(cfg.Pool.MinConns), mainMax) //nolint:gosec // validated range
	pool := func(s poolSpec) (*pgxpool.Pool, error) {
		pc, err := newPoolConfig(cfg, s, db.tls)
		if err != nil {
			return nil, err
		}
		return pgxpool.NewWithConfig(ctx, pc)
	}
	common := poolSpec{
		instance: instance,
		lifetime: cfg.Pool.MaxConnLifetime, jitter: cfg.Pool.MaxConnLifetimeJitter,
		idleTime: cfg.Pool.MaxConnIdleTime, healthEach: cfg.Pool.HealthCheckPeriod,
	}

	ms := common
	ms.kind, ms.ep, ms.maxConns, ms.minConns = connKind{class: classMain, pooled: pooled}, mainEndpoint(cfg), mainMax, mainMin
	if db.main, err = pool(ms); err != nil {
		return nil, err
	}
	cs := common
	cs.kind, cs.ep, cs.maxConns, cs.minConns = connKind{class: classMain, pooled: pooled}, controlEndpoint(cfg), 1, 1
	if db.control, err = pool(cs); err != nil {
		db.Close()
		return nil, err
	}
	if meta.RunsWorkers(role) {
		ls := common
		// Session locks live as long as the connection: no lifetime, and an
		// idle connection holds no lock, so the idle timeout is harmless.
		ls.kind, ls.ep, ls.maxConns, ls.minConns = connKind{class: classLease}, sessionEndpoint(cfg), int32(cfg.LeasePoolSize), 0 //nolint:gosec // validated range
		ls.lifetime, ls.jitter = 0, 0
		if db.lease, err = pool(ls); err != nil {
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
	tlsMetrics *tlsfiles.Metrics
}

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

// Ready is the `database` readiness check (R-OPS-001): a statement on the
// control connection that also proves the server is a primary, so a replica
// connected to a demoted primary turns unready instead of failing writes.
func (db *DB) Ready(ctx context.Context) error {
	conn, err := db.control.Acquire(ctx)
	if err != nil {
		return err
	}
	var primary bool
	err = conn.QueryRow(ctx, "SELECT NOT pg_is_in_recovery()").Scan(&primary)
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
		}
	}
	return Stats{Main: conv(db.main), Control: conv(db.control), Lease: conv(db.lease)}
}

// Close closes every pool. It is safe to call more than once.
func (db *DB) Close() {
	db.closeOnce.Do(func() {
		for _, p := range []*pgxpool.Pool{db.main, db.control, db.lease} {
			if p != nil {
				p.Close()
			}
		}
	})
}
