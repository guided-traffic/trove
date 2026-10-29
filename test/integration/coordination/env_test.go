//go:build integration

// Package coordination tests the coordination layer against the integration
// stack: the job queue and its workers, leases with fencing, the change log with
// its Valkey fast path, the in-process caches and the replica registry, each
// with several in-process "replicas" that have connection pools of their own.
// Every test creates a database of its own, so parallel tests and agents never
// collide.
package coordination

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/jobs"
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

func uuidV4() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
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
	cfg, err := pgx.ParseConfig("postgres://" + s.user + ":" + s.secret + "@" + s.host + ":" + strconv.Itoa(s.port) + "/" + database + "?sslmode=disable")
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

// env is one test's database and Valkey keyspace.
type env struct {
	t     *testing.T
	st    stack
	db    string
	cfg   config.Database
	admin *pgx.Conn
	ks    string // keyspace ID shared by the replicas of the test
	vcfg  config.Valkey
}

// newEnv creates a database trove_coord_<random>, migrates it and drops it when
// the test ends.
func newEnv(t *testing.T) *env {
	t.Helper()
	st := getStack(t)
	name := "trove_coord_" + randHex(5)
	maint := st.admin(t, "postgres")
	if _, err := maint.Exec(context.Background(), "CREATE DATABASE "+name+" ENCODING 'UTF8' TEMPLATE template0"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = maint.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	dir := t.TempDir()
	userFile, passFile := filepath.Join(dir, "username"), filepath.Join(dir, "password")
	if err := os.WriteFile(userFile, []byte(st.user+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passFile, []byte(st.secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Database{
		Host: st.host, Port: st.port, Name: name, Schema: schemaName,
		UsernameFile: userFile, PasswordFile: passFile,
		SSLMode: "disable", AllowInsecure: true,
		ConnectTimeout: 5 * time.Second, TargetSessionAttrs: "read-write",
		Pooler:        config.DatabasePooler{Mode: "none"},
		Pool:          config.DatabasePool{MaxConns: 6, MinConns: 1, MaxConnLifetime: 30 * time.Minute, MaxConnLifetimeJitter: time.Minute, MaxConnIdleTime: 5 * time.Minute, HealthCheckPeriod: 30 * time.Second},
		LeasePoolSize: 3, LeaseCheckInterval: time.Second,
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
	return &env{t: t, st: st, db: name, cfg: cfg, admin: st.admin(t, name), ks: randHex(8), vcfg: valkeyConfig(t)}
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// keyPrefix separates this run's Valkey keys; it starts with the ACL pattern of
// the stack (trove:*).
var keyPrefix = "trove:itc" + randHex(3) + ":"

// valkeyConfig is the configuration of a replica that talks to the stack's
// Valkey the way the chart configures it.
func valkeyConfig(t *testing.T) config.Valkey {
	v := testenv.ValkeyConfig(t)
	return config.Valkey{
		Mode:         "sentinel",
		KeyPrefix:    keyPrefix,
		UsernameFile: writeFile(t, "username", v.Username+"\n"),
		PasswordFile: writeFile(t, "password", v.Password+"\n"),
		Sentinel: config.ValkeySentinel{
			Addresses: v.Sentinels, PrimaryName: v.PrimaryName,
			PasswordFile: writeFile(t, "sentinel-password", v.SentinelPassword+"\n"),
			TLS:          true, RefreshInterval: 2 * time.Second,
		},
		TLS:                  config.ValkeyTLS{Mode: "verify-full", CAFile: v.CAFile},
		ConnectTimeout:       2 * time.Second,
		CommandTimeout:       500 * time.Millisecond,
		SentinelQueryTimeout: 300 * time.Millisecond,
		MaxConnLifetime:      30 * time.Minute,
		HealthCheckInterval:  500 * time.Millisecond,
		Reconnect:            config.ValkeyReconnect{InitialBackoff: 100 * time.Millisecond, MaxBackoff: time.Second},
		CircuitBreaker:       config.ValkeyBreaker{FailureThreshold: 5, OpenDuration: 2 * time.Second},
	}
}

// deadValkey is the same configuration pointed at a Sentinel that does not
// answer: the client is constructed degraded and keeps trying (Q-HA-13).
func deadValkey(c config.Valkey) config.Valkey {
	c.Sentinel.Addresses = []string{"127.0.0.1:1"}
	return c
}

type vkMode int

const (
	vkNone vkMode = iota
	vkLive
	vkDead
)

// replicaOpts describe one in-process replica.
type replicaOpts struct {
	role   string
	valkey vkMode
	jobs   config.Jobs
	clu    config.Cluster
	sys    config.System
	// kinds registers the replica's kinds in its own registry, like the kinds of
	// a process.
	kinds func(r *jobs.Registry, rep *replica)
	// ready is the readiness the claim loop sees; nil means ready.
	ready func() bool
	// maintenance are the task schedules of the configuration.
	maintenance config.Maintenance
	sources     []jobs.ScheduleSource
	// noWorker skips the job worker of a worker replica (lease tests).
	noWorker bool
	// lateKeyspace creates the Valkey client without the keyspace ID, as a
	// replica whose system_state is not readable yet; the test sets it later.
	lateKeyspace bool
}

func defaultJobs() config.Jobs {
	return config.Jobs{
		Workers:       config.JobsWorkers{Default: 4, Maintenance: 1, Index: 2, Delivery: 8},
		PollInterval:  time.Second,
		LeaseDuration: 6 * time.Second, HeartbeatInterval: 500 * time.Millisecond,
		ScheduleTick: time.Second, Retention: 720 * time.Hour,
	}
}

func defaultCluster() config.Cluster {
	return config.Cluster{
		LeaseRetryInterval: 500 * time.Millisecond, EpochPollInterval: time.Second, EpochMaxStaleness: 20 * time.Second,
		ChangeRetention: time.Hour, SignalCoalesceWindow: 20 * time.Millisecond,
	}
}

func defaultSystem() config.System {
	return config.System{HeartbeatInterval: 500 * time.Millisecond, HeartbeatStaleAfter: 3 * time.Second}
}

// replica is one in-process replica with pools and components of its own.
type replica struct {
	t    *testing.T
	name string
	uid  string
	role string
	o    replicaOpts
	reg  *prometheus.Registry

	db       *postgres.DB
	vk       *cluster.Valkey
	signals  *cluster.Signals
	changes  *cluster.Changes
	caches   *cache.Registry
	leases   *cluster.Leases
	registry *cluster.Registry
	worker   *jobs.Worker
	queue    *jobs.Queue
	jobsReg  *jobs.Registry

	stopOnce sync.Once
}

// newReplica builds the replica the way the composition root does and starts it.
func (e *env) newReplica(name string, o replicaOpts) *replica {
	t := e.t
	t.Helper()
	if o.jobs.PollInterval == 0 {
		o.jobs = defaultJobs()
	}
	if o.clu.EpochPollInterval == 0 {
		o.clu = defaultCluster()
	}
	if o.sys.HeartbeatInterval == 0 {
		o.sys = defaultSystem()
	}
	r := &replica{t: t, name: name, uid: uuidV4(), role: o.role, o: o, reg: prometheus.NewRegistry()}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var err error
	r.db, err = postgres.Open(ctx, e.cfg, name, o.role, quiet)
	if err != nil {
		t.Fatalf("Open(%s, %s): %v", name, o.role, err)
	}
	t.Cleanup(func() { r.stop() })

	switch o.valkey {
	case vkLive, vkDead:
		cfg := e.vcfg
		if o.valkey == vkDead {
			cfg = deadValkey(cfg)
		}
		var vopts []cluster.ValkeyOption
		if !o.lateKeyspace {
			vopts = append(vopts, cluster.WithKeyspaceID(e.ks))
		}
		r.vk, err = cluster.NewValkey(ctx, cfg, name, o.role, quiet, prometheus.NewRegistry(), vopts...)
		if err != nil {
			t.Fatalf("NewValkey(%s): %v", name, err)
		}
	}
	var bus cluster.Bus
	if r.vk != nil {
		bus = r.vk
	}
	r.signals, err = cluster.NewSignals(cluster.SignalsOptions{Bus: bus, Config: o.clu, Log: quiet, Metrics: r.reg})
	if err != nil {
		t.Fatal(err)
	}
	r.changes, err = cluster.NewChanges(cluster.ChangesOptions{
		DB: r.db.ControlPool(), Bus: bus, Config: o.clu, WatchdogTimeout: 30 * time.Second, Log: quiet, Metrics: r.reg,
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

	r.jobsReg = jobs.NewRegistry()
	r.queue = jobs.NewQueue(jobs.QueueOptions{DB: r.db.Main(), Registry: r.jobsReg, Signals: r.signals, Log: quiet})
	if o.kinds != nil {
		o.kinds(r.jobsReg, r)
	}

	isWorker := o.role == "worker" || o.role == "all"
	if isWorker {
		r.leases, err = cluster.NewLeases(cluster.LeaseOptions{
			Pool: r.db.LeasePool(), Config: o.clu, CheckInterval: e.cfg.LeaseCheckInterval,
			WatchdogTimeout: 30 * time.Second, Log: quiet, Metrics: r.reg,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	leases := r.leases
	r.registry, err = cluster.NewRegistry(cluster.RegistryOptions{
		DB: r.db.ControlPool(),
		Identity: cluster.ReplicaIdentity{
			UID: r.uid, Name: name, Version: "0.1.0", Revision: "it", Role: o.role, StartedAt: time.Now(),
			SecretKeyIDs: []string{"k1"}, ActiveSecretKeyID: "k1",
		},
		Snapshot: func() cluster.ReplicaSnapshot { return cluster.ReplicaSnapshot{State: cluster.ReplicaReady} },
		Leases: func() []cluster.HeldInfo {
			if leases == nil {
				return nil
			}
			return leases.Held()
		},
		Config: o.sys, WatchdogTimeout: 30 * time.Second, Log: quiet,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.registry.Start(ctx); err != nil {
		t.Fatalf("registry.Start: %v", err)
	}

	if isWorker {
		if !o.noWorker {
			r.jobsReg.Freeze()
			r.worker, err = jobs.NewWorker(jobs.WorkerOptions{
				Role: o.role, Instance: name, UID: r.uid, Main: r.db.Main(), Control: r.db.ControlPool(),
				Registry: r.jobsReg, Config: o.jobs, Maintenance: o.maintenance, Signals: r.signals, Leases: r.leases,
				Ready: o.ready, WatchdogTimeout: 30 * time.Second, Log: quiet, Metrics: r.reg, Sources: o.sources,
			})
			if err != nil {
				t.Fatal(err)
			}
			r.queue = r.worker.Queue()
			if err := r.worker.Start(ctx); err != nil {
				t.Fatalf("worker.Start: %v", err)
			}
		}
		if err := r.leases.Start(ctx); err != nil {
			t.Fatalf("leases.Start: %v", err)
		}
	}
	return r
}

// stop shuts the replica down the way the composition root does: drain, settle,
// quiesce, then the components in reverse order. It is safe to call twice.
func (r *replica) stop() {
	r.stopOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if r.registry != nil {
			r.registry.Drain(ctx)
		}
		if r.worker != nil {
			r.worker.Drain(ctx)
			sctx, scancel := context.WithTimeout(ctx, time.Second)
			_ = r.worker.Settle(sctx)
			scancel()
			r.worker.Quiesce(ctx)
			_ = r.worker.Stop(ctx)
		}
		if r.leases != nil {
			_ = r.leases.Stop(ctx)
		}
		if r.registry != nil {
			_ = r.registry.Stop(ctx)
		}
		if r.changes != nil {
			_ = r.changes.Stop(ctx)
		}
		if r.signals != nil {
			r.signals.Close()
		}
		if r.vk != nil {
			r.vk.Close()
		}
		if r.db != nil {
			r.db.Close()
		}
	})
}

// waitFor polls cond until it holds or the time is up.
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

func waitValkey(t *testing.T, r *replica) {
	t.Helper()
	waitFor(t, "Valkey becoming available on "+r.name, 20*time.Second, r.vk.Available)
}

// scalar runs a query on the test database as the superuser.
func (e *env) scalar(sql string, args ...any) (v int64) {
	e.t.Helper()
	if err := e.admin.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

// percentile returns the p-th percentile (0..1) of the durations.
func percentile(d []time.Duration, p float64) time.Duration {
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	if len(s) == 0 {
		return 0
	}
	i := int(float64(len(s)-1) * p)
	if p >= 0.99 {
		i = len(s) - 1 // with fewer than 100 samples p99 is the maximum
	}
	return s[i]
}
