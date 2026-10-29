//go:build integration

// Package audit tests the audit trail against the integration stack: the
// transactional insert with its journal row, the asynchronous writer under a slow
// or absent PostgreSQL, deduplication, the partitions and the retention, the
// keyset query, and the privileges of the runtime role. Every test creates a
// database of its own, so parallel tests and agents never collide.
package audit

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

const schemaName = "trove"

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// stack is the PostgreSQL of the integration stack.
type stack struct {
	host         string
	port         int
	user, secret string
}

func getStack(t testing.TB) stack {
	t.Helper()
	port, err := strconv.Atoi(testenv.Get(t, "TROVE_TEST_PG_PORT"))
	if err != nil {
		t.Fatalf("TROVE_TEST_PG_PORT: %v", err)
	}
	return stack{
		host: testenv.Get(t, "TROVE_TEST_PG_HOST"), port: port,
		user: testenv.Get(t, "TROVE_TEST_PG_USER"), secret: testenv.Get(t, "TROVE_TEST_PG_PASSWORD"),
	}
}

func (s stack) admin(t testing.TB, database string) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg, err := pgx.ParseConfig(testenv.PgDSN(t, database))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect to %s: %v", database, err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func writeFile(t testing.TB, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// env is one test's migrated database.
type env struct {
	t     *testing.T
	st    stack
	db    string
	cfg   config.Database // the schema owner's login (the stack's superuser)
	admin *pgx.Conn
}

// newEnv creates a database trove_audit_<random>, migrates it with the retention
// of the default configuration and drops it when the test ends.
func newEnv(t *testing.T) *env {
	t.Helper()
	st := getStack(t)
	// The migrations grant the schema to trove_app only if the role exists when they run
	// (two-role mode creates it first, 00001_foundation.sql). On a fresh cluster the first
	// test of the package would otherwise migrate without the grant and the runtime role
	// would not see the schema (the missing USAGE reads as "relation does not exist").
	ensureRuntimeRole(t, st)
	name := "trove_audit_" + randHex(5)
	maint := st.admin(t, "postgres")
	if _, err := maint.Exec(context.Background(), "CREATE DATABASE "+name+" ENCODING 'UTF8' TEMPLATE template0"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = maint.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	cfg := config.Database{
		Host: st.host, Port: st.port, Name: name, Schema: schemaName,
		UsernameFile: writeFile(t, "username", st.user+"\n"), PasswordFile: writeFile(t, "password", st.secret+"\n"),
		SSLMode: "verify-full", SSLRootCertFile: testenv.CAFile(t),
		ConnectTimeout: 5 * time.Second, TargetSessionAttrs: "read-write",
		Pooler:        config.DatabasePooler{Mode: "none"},
		Pool:          config.DatabasePool{MaxConns: 6, MinConns: 1, MaxConnLifetime: 30 * time.Minute, MaxConnLifetimeJitter: time.Minute, MaxConnIdleTime: 5 * time.Minute, HealthCheckPeriod: 30 * time.Second},
		LeasePoolSize: 3, LeaseCheckInterval: time.Second,
		Session: config.DatabaseSession{
			StatementTimeout: 30 * time.Second, LockTimeout: 2 * time.Second,
			IdleInTransactionSessionTimeout: 60 * time.Second,
		},
		Retry:           config.DatabaseRetry{InitialBackoff: 50 * time.Millisecond, MaxBackoff: 200 * time.Millisecond, MaxElapsed: 2 * time.Second},
		Keepalive:       config.DatabaseKeepalive{Idle: 15 * time.Second, Interval: 5 * time.Second, Count: 3, UserTimeout: 30 * time.Second},
		ServerKeepalive: config.DatabaseServerKeepalive{Idle: 60 * time.Second, Interval: 10 * time.Second, Count: 6},
		Preflight:       config.DatabasePreflight{ConnectionBudget: "enforce", ReservedConnections: 10, AllowSuperuser: true},
	}
	days := 365
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := postgres.Migrate(ctx, cfg, postgres.MigrateOptions{
		Instance: "it-migrate", Version: "0.1.0", Log: quiet, AuditRetentionDays: &days,
		Migrate: config.Migrate{AdvisoryLockWait: 20 * time.Second, DDLLockTimeout: 2 * time.Second, DDLLockRetries: 3},
	}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return &env{t: t, st: st, db: name, cfg: cfg, admin: st.admin(t, name)}
}

// open opens the main pool as the schema owner.
func (e *env) open() *postgres.DB {
	e.t.Helper()
	return e.openWith(e.cfg)
}

func (e *env) openWith(cfg config.Database) *postgres.DB {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := postgres.Open(ctx, cfg, "it-audit-"+randHex(2), meta.RoleWeb, quiet)
	if err != nil {
		e.t.Fatalf("open: %v", err)
	}
	e.t.Cleanup(db.Close)
	return db
}

// runtimeCfg is the configuration of the runtime role trove_app, which holds
// INSERT and SELECT on audit_event and nothing more (Q-OPS-4).
func (e *env) runtimeCfg() config.Database {
	e.t.Helper()
	ensureRuntimeRole(e.t, e.st)
	c := e.cfg
	c.UsernameFile, c.PasswordFile = writeFile(e.t, "username", "trove_app\n"), writeFile(e.t, "password", "trove-app-it\n")
	c.Preflight.AllowSuperuser = false
	return c
}

// runtimeConn is a plain connection as trove_app.
func (e *env) runtimeConn() *pgx.Conn {
	e.t.Helper()
	ensureRuntimeRole(e.t, e.st)
	cfg, err := pgx.ParseConfig(testenv.PgDSN(e.t, e.db))
	if err != nil {
		e.t.Fatal(err)
	}
	cfg.User, cfg.Password = "trove_app", "trove-app-it"
	conn, err := pgx.ConnectConfig(context.Background(), cfg)
	if err != nil {
		e.t.Fatalf("connect as trove_app: %v", err)
	}
	e.t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

// ensureRuntimeRole creates the cluster-wide runtime role like the other
// integration packages do; it is never dropped.
func ensureRuntimeRole(t testing.TB, st stack) {
	t.Helper()
	admin := st.admin(t, "postgres")
	for range 5 {
		_, err := admin.Exec(context.Background(), `DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'trove_app') THEN
				CREATE ROLE trove_app LOGIN PASSWORD 'trove-app-it';
			END IF;
		END $$`)
		if err == nil {
			_, err = admin.Exec(context.Background(), `ALTER ROLE trove_app LOGIN PASSWORD 'trove-app-it'`)
		}
		if err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond) // a parallel package created it at the same moment
	}
	t.Fatal("could not create the runtime role trove_app")
}

func scalar[T any](t testing.TB, c *pgx.Conn, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := c.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func mustExec(t testing.TB, c *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := c.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// metrics counts what the writer reports.
type metrics struct{ recorded, dropped atomic.Int64 }

func (m *metrics) Recorded(string, audit.Outcome) { m.recorded.Add(1) }
func (m *metrics) Dropped()                       { m.dropped.Add(1) }

// logBuf is a concurrency-safe log sink.
type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }

func (l *logBuf) String() string       { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }
func (l *logBuf) logger() *slog.Logger { return slog.New(slog.NewJSONHandler(l, nil)) }

// writer builds a started audit writer on db; it is flushed when the test ends.
func (e *env) writer(db audit.DB, cfg config.Audit, now func() time.Time) (*audit.Writer, *metrics, *logBuf) {
	e.t.Helper()
	m, logs := &metrics{}, &logBuf{}
	w, err := audit.New(audit.Options{
		DB: db, Replica: "it-replica", Config: cfg, Metrics: m, Now: now,
		Log: logs.logger(), AppLog: quiet,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	w.Start()
	e.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = w.Flush(ctx)
	})
	return w, m, logs
}

func auditCfg() config.Audit {
	return config.Audit{
		RetentionDays: 365, AsyncBuffer: 10000, DenialDedupWindow: time.Minute,
		TokenRejectionDedupWindow: 5 * time.Minute,
	}
}
