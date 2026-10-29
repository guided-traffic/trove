//go:build integration

// Package repository tests the repository model against the integration stack:
// create, read, update, list and delete with their audit events and change-log
// rows, the per-replica snapshot with its change-log invalidation and the lookup of
// a key the snapshot does not hold, each with several in-process "replicas" that
// have connection pools of their own and no Valkey (R-REPO-049: the bounds hold
// without it). Every test creates a database of its own, so parallel tests and
// agents never collide.
package repository

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/formats"
	_ "github.com/guided-traffic/trove/internal/formats/all"
	"github.com/guided-traffic/trove/internal/jobs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

const schemaName = "trove"

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func uuidV4() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func admin(t testing.TB, database string) *pgx.Conn {
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

// env is one test's migrated database.
type env struct {
	t     *testing.T
	db    string
	cfg   config.Database
	admin *pgx.Conn
}

// newEnv creates a database trove_repo_<random>, migrates it and drops it when the
// test ends.
func newEnv(t *testing.T) *env {
	t.Helper()
	host, user, secret := testenv.Get(t, "TROVE_TEST_PG_HOST"), testenv.Get(t, "TROVE_TEST_PG_USER"), testenv.Get(t, "TROVE_TEST_PG_PASSWORD")
	port, err := strconv.Atoi(testenv.Get(t, "TROVE_TEST_PG_PORT"))
	if err != nil {
		t.Fatalf("TROVE_TEST_PG_PORT: %v", err)
	}
	name := "trove_repo_" + randHex(5)
	maint := admin(t, "postgres")
	if _, err := maint.Exec(context.Background(), "CREATE DATABASE "+name+" ENCODING 'UTF8' TEMPLATE template0"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = maint.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	dir := t.TempDir()
	userFile, passFile := filepath.Join(dir, "username"), filepath.Join(dir, "password")
	if err := os.WriteFile(userFile, []byte(user+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passFile, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Database{
		Host: host, Port: port, Name: name, Schema: schemaName,
		UsernameFile: userFile, PasswordFile: passFile,
		SSLMode: "verify-full", SSLRootCertFile: testenv.CAFile(t),
		ConnectTimeout: 5 * time.Second, TargetSessionAttrs: "read-write",
		Pooler:        config.DatabasePooler{Mode: "none"},
		Pool:          config.DatabasePool{MaxConns: 8, MinConns: 1, MaxConnLifetime: 30 * time.Minute, MaxConnLifetimeJitter: time.Minute, MaxConnIdleTime: 5 * time.Minute, HealthCheckPeriod: 30 * time.Second},
		LeasePoolSize: 2, LeaseCheckInterval: time.Second,
		Session: config.DatabaseSession{
			StatementTimeout: 30 * time.Second, LockTimeout: 5 * time.Second,
			IdleInTransactionSessionTimeout: 60 * time.Second,
		},
		Retry:           config.DatabaseRetry{InitialBackoff: 50 * time.Millisecond, MaxBackoff: 200 * time.Millisecond, MaxElapsed: 2 * time.Second},
		Keepalive:       config.DatabaseKeepalive{Idle: 15 * time.Second, Interval: 5 * time.Second, Count: 3, UserTimeout: 30 * time.Second},
		ServerKeepalive: config.DatabaseServerKeepalive{Idle: 60 * time.Second, Interval: 10 * time.Second, Count: 6},
		Preflight:       config.DatabasePreflight{ConnectionBudget: "enforce", ReservedConnections: 10, AllowSuperuser: true},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := postgres.Migrate(ctx, cfg, postgres.MigrateOptions{
		Instance: "it-migrate", Version: "0.1.0", Log: quiet,
		Migrate: config.Migrate{AdvisoryLockWait: 20 * time.Second, DDLLockTimeout: 2 * time.Second, DDLLockRetries: 3},
	}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if testenv.PgPooled(t) {
		cfg = testenv.ThroughPgBouncer(t, cfg, user, maint)
	}
	e := &env{t: t, db: name, cfg: cfg, admin: admin(t, name)}
	e.exec("SET search_path TO " + schemaName)
	return e
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) scalar(sql string, args ...any) (v int64) {
	e.t.Helper()
	if err := e.admin.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

// recorder is an audit.Recorder that keeps what was recorded in committed
// transactions and can be told to fail, to show that a change never exists
// without its audit record (R-OPS-015).
type recorder struct {
	mu     sync.Mutex
	events []audit.Event
	fail   bool
}

func (r *recorder) RecordTx(_ context.Context, tx meta.Tx, ev audit.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail {
		return errors.New("audit store unavailable")
	}
	tx.After(func() {
		r.mu.Lock()
		r.events = append(r.events, ev)
		r.mu.Unlock()
	})
	return nil
}

func (r *recorder) Record(context.Context, audit.Event) {}

func (r *recorder) types() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.events))
	for i, ev := range r.events {
		out[i] = ev.Type
	}
	return out
}

func (r *recorder) last() audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.events[len(r.events)-1]
}

func (r *recorder) setFail(v bool) { r.mu.Lock(); r.fail = v; r.mu.Unlock() }

// countingRunner counts the transactions a replica's repository code starts, which
// is how the tests tell a lookup from a cache hit.
type countingRunner struct {
	meta.Runner
	n atomic.Int64
}

func (c *countingRunner) InTx(ctx context.Context, fn func(meta.Tx) error) error {
	c.n.Add(1)
	return c.Runner.InTx(ctx, fn)
}

// catalog is the format catalog the composition root builds from the registry.
type catalog struct{}

func (catalog) Format(name string) (repository.FormatInfo, bool) {
	f, ok := formats.Lookup(name)
	if !ok {
		return repository.FormatInfo{}, false
	}
	d := f.Descriptor()
	info := repository.FormatInfo{
		Name: string(d.ID), Layout: d.Layout, ProtocolBasePath: d.ProtocolBasePath,
		PatternSubject: repository.PatternSubject(d.PatternSubject), PackageNames: d.PackageNames,
	}
	for _, c := range d.Classes {
		info.Classes = append(info.Classes, repository.Class(c.String()))
	}
	return info, true
}

type replicaOpts struct {
	role        string
	deleteBatch int
	poll        time.Duration
	rec         *recorder
}

// replica is one in-process replica with pools and components of its own.
type replica struct {
	t       *testing.T
	name    string
	reg     *prometheus.Registry
	db      *postgres.DB
	runner  *countingRunner
	signals *cluster.Signals
	changes *cluster.Changes
	caches  *cache.Registry
	reader  *repository.SnapshotReader
	svc     *repository.Service
	rec     *recorder
	queue   *jobs.Queue
	worker  *jobs.Worker
	stopped sync.Once
}

func (e *env) newReplica(name string, o replicaOpts) *replica {
	t := e.t
	t.Helper()
	if o.role == "" {
		o.role = "web"
	}
	if o.poll == 0 {
		o.poll = 200 * time.Millisecond
	}
	if o.rec == nil {
		o.rec = &recorder{}
	}
	clu := config.Cluster{
		LeaseRetryInterval: 500 * time.Millisecond, EpochPollInterval: o.poll, EpochMaxStaleness: 20 * time.Second,
		ChangeRetention: time.Hour, SignalCoalesceWindow: 20 * time.Millisecond,
	}
	r := &replica{t: t, name: name, reg: prometheus.NewRegistry(), rec: o.rec}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var err error
	r.db, err = postgres.Open(ctx, e.cfg, name, o.role, quiet)
	if err != nil {
		t.Fatalf("Open(%s): %v", name, err)
	}
	t.Cleanup(r.stop)
	r.runner = &countingRunner{Runner: r.db}
	r.signals, err = cluster.NewSignals(cluster.SignalsOptions{Config: clu, Log: quiet, Metrics: r.reg})
	if err != nil {
		t.Fatal(err)
	}
	r.changes, err = cluster.NewChanges(cluster.ChangesOptions{
		DB: r.db.ControlPool(), Config: clu, WatchdogTimeout: 30 * time.Second, Log: quiet, Metrics: r.reg,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.caches, err = cache.NewRegistry(r.changes, r.reg)
	if err != nil {
		t.Fatal(err)
	}
	r.changes.AddListener(r.caches)
	if err := r.changes.Start(ctx); err != nil {
		t.Fatalf("changes.Start: %v", err)
	}

	reg := jobs.NewRegistry()
	reg.RegisterKind(repository.DeleteJobKind, repository.DeleteJobSpec(func(ctx context.Context, run *jobs.Run) error {
		return r.svc.RunDeleteJob(ctx, run)
	}))
	r.queue = jobs.NewQueue(jobs.QueueOptions{DB: r.db.Main(), Registry: reg, Signals: r.signals, Log: quiet})
	if o.role == "worker" {
		reg.Freeze()
		r.worker, err = jobs.NewWorker(jobs.WorkerOptions{
			Role: o.role, Instance: name, UID: uuidV4(), Main: r.db.Main(), Control: r.db.ControlPool(),
			Registry: reg, Signals: r.signals, WatchdogTimeout: 30 * time.Second, Log: quiet, Metrics: r.reg,
			Config: config.Jobs{
				Workers:       config.JobsWorkers{Default: 2, Maintenance: 1, Index: 1, Delivery: 1},
				PollInterval:  200 * time.Millisecond,
				LeaseDuration: 6 * time.Second, HeartbeatInterval: 500 * time.Millisecond,
				ScheduleTick: time.Second, Retention: 720 * time.Hour,
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := r.worker.Start(ctx); err != nil {
			t.Fatalf("worker.Start: %v", err)
		}
	}

	r.reader, err = repository.NewReader(repository.ReaderOptions{
		Runner: r.runner, Catalog: catalog{}, Caches: r.caches, Poller: r.changes,
		SafetyTTL: 5 * time.Minute, UnknownKeys: 10000, LookupWait: 5 * time.Second, Log: quiet,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.svc, err = repository.NewService(repository.Options{
		Runner: r.db, Catalog: catalog{}, Recorder: o.rec, Notifier: r.changes, Jobs: r.queue,
		DeleteBatch: o.deleteBatch, Log: quiet,
	})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *replica) stop() {
	r.stopped.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if r.worker != nil {
			r.worker.Drain(ctx)
			r.worker.Quiesce(ctx)
			_ = r.worker.Stop(ctx)
		}
		if r.changes != nil {
			_ = r.changes.Stop(ctx)
		}
		if r.signals != nil {
			r.signals.Close()
		}
		if r.db != nil {
			r.db.Close()
		}
	})
}

func waitFor(t testing.TB, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s did not happen within %s", what, within)
}

var caller = repository.Caller{Actor: audit.Actor{Kind: "user", ID: "0198a000-0000-7000-8000-000000000001", Name: "alice"}}

const genericBody = `{"key":%q,"class":"local","format":"generic"}`
