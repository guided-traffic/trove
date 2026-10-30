//go:build integration

// Package proxy tests the remote engine against the integration stack: PostgreSQL,
// the S3 store, Valkey and, in-process, several replicas that share them. A replica
// here is the engine with everything it stands on and nothing above it: the
// PostgreSQL pools, the change log, the cache registry, the repository snapshot, the
// blob store with its spool, the item store, the upstream clients, a Valkey client
// and the invalidation set. What a format adds is replaced by the counting adapter of
// internal/proxy/proxytest, and the upstream by its counting TLS server, so the
// engine is measured on its own and not through npm.
//
// Valkey is used as the replicas use it (Sentinel discovery over TLS), and a replica
// can be given a client that cannot reach it, which is the path a stopped Valkey
// takes: the breaker and the unavailable answers. No test here stops Valkey or forces
// a failover; the variants with the real outage belong to the end-to-end set.
package proxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/blobtest"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/internal/proxy"
	"github.com/guided-traffic/trove/internal/proxy/invalidation"
	"github.com/guided-traffic/trove/internal/proxy/proxytest"
	"github.com/guided-traffic/trove/internal/proxy/upstream"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

const schemaName = "trove"

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestMain(m *testing.M) { os.Exit(testenv.Main(m)) }

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// env is one test's database, object-store prefix and Valkey keyspace.
type env struct {
	t     *testing.T
	db    string
	cfg   config.Database
	admin *pgx.Conn
	mu    sync.Mutex // the admin connection is not safe for concurrent use
	s3    config.Storage
	obj   blob.ObjectStore
	// ks is the keyspace id of the installation: the replicas of one test share it.
	ks string
	// prefix is the Valkey key prefix of this test.
	prefix string
	// ups are the upstreams the replicas trust.
	ups []*proxytest.Upstream
}

func newEnv(t *testing.T) *env {
	t.Helper()
	port, err := strconv.Atoi(testenv.Get(t, "TROVE_TEST_PG_PORT"))
	if err != nil {
		t.Fatalf("TROVE_TEST_PG_PORT: %v", err)
	}
	host, user, secret := testenv.Get(t, "TROVE_TEST_PG_HOST"), testenv.Get(t, "TROVE_TEST_PG_USER"), testenv.Get(t, "TROVE_TEST_PG_PASSWORD")
	connect := func(database string) *pgx.Conn {
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
	name := "trove_proxy_" + randHex(5)
	maint := connect("postgres")
	if _, err := maint.Exec(context.Background(), "CREATE DATABASE "+name+" ENCODING 'UTF8' TEMPLATE template0"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() { _, _ = maint.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)") })
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
		Pool:          config.DatabasePool{MaxConns: 24, MinConns: 1, MaxConnLifetime: 30 * time.Minute, MaxConnLifetimeJitter: time.Minute, MaxConnIdleTime: 5 * time.Minute, HealthCheckPeriod: 30 * time.Second},
		LeasePoolSize: 3, LeaseCheckInterval: time.Second,
		Session: config.DatabaseSession{
			StatementTimeout: 60 * time.Second, LockTimeout: 10 * time.Second,
			IdleInTransactionSessionTimeout: 120 * time.Second,
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
	e := &env{t: t, db: name, cfg: cfg, admin: connect(name), ks: randHex(8), prefix: "trove:itp" + randHex(3) + ":"}
	e.s3, e.obj = newS3(t)
	return e
}

func newS3(t *testing.T) (config.Storage, blob.ObjectStore) {
	t.Helper()
	dir := t.TempDir()
	writeSecret := func(file, v string) string {
		p := filepath.Join(dir, file)
		if err := os.WriteFile(p, []byte(v+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cfg := blobtest.Storage(t, t.TempDir())
	cfg.S3.Endpoint = testenv.Get(t, "TROVE_TEST_S3_ENDPOINT")
	cfg.S3.Region = testenv.Get(t, "TROVE_TEST_S3_REGION")
	cfg.S3.Bucket = testenv.Get(t, "TROVE_TEST_S3_BUCKET_BLOBS")
	cfg.S3.ForcePathStyle = testenv.Get(t, "TROVE_TEST_S3_FORCE_PATH_STYLE") == "true"
	cfg.S3.CAFile = testenv.CAFile(t)
	cfg.S3.Prefix = "it-proxy/" + randHex(5)
	cfg.S3.Credentials = config.StorageS3Credentials{
		Source:              "static",
		AccessKeyIDFile:     writeSecret("access-key", testenv.Get(t, "TROVE_TEST_S3_ACCESS_KEY")),
		SecretAccessKeyFile: writeSecret("secret-key", testenv.Get(t, "TROVE_TEST_S3_SECRET_KEY")),
	}
	cfg.Spool.MinFreeBytes = 0
	f := outbound.New(config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true}})
	c, err := s3.NewFromFactory(context.Background(), cfg.S3, f)
	if err != nil {
		t.Fatalf("object store client: %v", err)
	}
	t.Cleanup(func() { removeAll(c, cfg.S3.Prefix) })
	return cfg, c
}

func removeAll(c blob.ObjectStore, prefix string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	token := ""
	for {
		page, err := c.List(ctx, prefix+"/", blob.ListOptions{Token: token})
		if err != nil {
			return
		}
		for _, o := range page.Objects {
			_ = c.Delete(ctx, o.Key)
		}
		if page.Next == "" {
			return
		}
		token = page.Next
	}
}

func (e *env) exec(sql string, args ...any) {
	e.t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *env) number(sql string, args ...any) int64 {
	e.t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	var v int64
	if err := e.admin.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

// text reads the first column of the first row, "" when there is none.
func (e *env) text(sql string, args ...any) string {
	e.t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	var v string
	err := e.admin.QueryRow(context.Background(), sql, args...).Scan(&v)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

// upstream starts a counting upstream that the replicas of this test trust.
func (e *env) upstream() *proxytest.Upstream {
	u := proxytest.NewUpstream(e.t)
	e.ups = append(e.ups, u)
	return u
}

// remoteSettings are the defaults of R-REPO-024 with the upstream URL.
func remoteSettings(url string) repository.RemoteSettings {
	return repository.RemoteSettings{
		URL: strings.TrimSuffix(url, "/") + "/", ExtraHosts: []repository.ExtraHost{}, AuthMode: repository.AuthNone,
		Headers: []repository.Header{}, OutboundProxy: repository.OutboundProxy{Mode: repository.ProxyDefault},
		ConnectTimeoutMs: 10000, ReadTimeoutMs: 15000, CacheFillAction: repository.FillRead,
		Cache: repository.RemoteCache{
			StoreArtifacts: true, MetadataTTLSeconds: 60, MetadataRefreshTimeoutSeconds: 60, NotFoundTTLSeconds: 1800,
		},
		Integrity: repository.RemoteIntegrity{
			ChecksumPolicy: repository.ChecksumVerifyIfAvailable, BlockUnexpectedContentTypes: true, AllowedContentTypes: []string{},
		},
		Availability: repository.Availability{Mode: repository.AvailabilityOnline, FailureThreshold: 5, CircuitOpenSeconds: 300},
		Limits:       repository.RemoteLimits{MaxConcurrentFetches: 64},
	}
}

// remote creates a remote repository row for the upstream and returns it as the
// engine takes it. mutate changes the settings before they are stored.
func (e *env) remote(key string, up *proxytest.Upstream, mutate ...func(*repository.RemoteSettings)) proxy.Remote {
	e.t.Helper()
	s := remoteSettings(up.URL)
	for _, m := range mutate {
		m(&s)
	}
	raw, err := json.Marshal(map[string]any{"remote": s})
	if err != nil {
		e.t.Fatal(err)
	}
	e.exec("INSERT INTO repositories (key, format, class, config) VALUES ($1, 'proxytest', 'remote', $2::jsonb)", key, string(raw))
	return e.lookup(key)
}

// lookup reads a repository as a replica's snapshot would.
func (e *env) lookup(key string) proxy.Remote {
	e.t.Helper()
	r := e.newReader()
	repo, err := r.ByKey(context.Background(), key)
	if err != nil {
		e.t.Fatalf("ByKey(%s): %v", key, err)
	}
	rem, err := proxy.NewRemote(repo)
	if err != nil {
		e.t.Fatal(err)
	}
	return rem
}

func (e *env) newReader() *repository.SnapshotReader {
	db, err := postgres.Open(context.Background(), e.cfg, "it-reader", "web", quiet)
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(db.Close)
	reg, err := cache.NewRegistry(nil, prometheus.NewRegistry())
	if err != nil {
		e.t.Fatal(err)
	}
	r, err := repository.NewReader(repository.ReaderOptions{
		Runner: db, Catalog: catalog.Default(), Caches: reg, SafetyTTL: time.Minute, UnknownKeys: 10, LookupWait: time.Second, Log: quiet,
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

// recorder keeps the audit events of a replica that were committed or recorded.
type recorder struct {
	mu     sync.Mutex
	events []audit.Event
}

func (r *recorder) Record(_ context.Context, ev audit.Event) {
	r.mu.Lock()
	r.events = append(r.events, ev)
	r.mu.Unlock()
}

func (r *recorder) RecordTx(_ context.Context, tx meta.Tx, ev audit.Event) error {
	tx.After(func() {
		r.mu.Lock()
		r.events = append(r.events, ev)
		r.mu.Unlock()
	})
	return nil
}

func (r *recorder) ofType(name string) []audit.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []audit.Event
	for _, ev := range r.events {
		if ev.Type == name {
			out = append(out, ev)
		}
	}
	return out
}

// chaosStore wraps the object store of a replica so that a test can make it fail
// (R-STOR-036: the object store is unavailable) and can delay its writes.
type chaosStore struct {
	blob.ObjectStore
	down atomic.Bool
}

func (c *chaosStore) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, sum blob.Checksum) error {
	if c.down.Load() {
		return blob.ErrCircuitOpen
	}
	return c.ObjectStore.Put(ctx, key, body, size, sum)
}

func (c *chaosStore) CreateMultipart(ctx context.Context, key, algorithm string) (string, error) {
	if c.down.Load() {
		return "", blob.ErrCircuitOpen
	}
	return c.ObjectStore.CreateMultipart(ctx, key, algorithm)
}

// valkeyMode says how a replica reaches Valkey.
type valkeyMode int

const (
	// vkReal: Sentinel discovery over TLS, the way a replica is configured.
	vkReal valkeyMode = iota
	// vkUnreachable: a client whose Sentinels do not answer: the breaker and the
	// unavailable answers of a stopped Valkey (the real outage is an e2e test).
	vkUnreachable
)

// replicaOpts describe one in-process replica.
type replicaOpts struct {
	valkey valkeyMode
	// proxy mutates the proxy configuration.
	proxy func(*config.Proxy)
	// cluster mutates the cluster configuration.
	cluster func(*config.Cluster)
	// replicas is the number n of live replicas; nil is 1.
	replicas func() int
	// invalidations starts the invalidation set (R-REPO-045).
	invalidations bool
	// poll is cluster.epoch_poll_interval.
	poll time.Duration
	// flights wraps the cross-replica lock.
	flights func(cluster.FlightLock) cluster.FlightLock
	// upstreamLog receives the upstream log.
	upstreamLog io.Writer
	// ups are further upstreams the replica trusts.
	ups []*proxytest.Upstream
}

// replica is one in-process web replica.
type replica struct {
	t       *testing.T
	e       *env
	name    string
	reg     *prometheus.Registry
	db      *postgres.DB
	changes *cluster.Changes
	caches  *cache.Registry
	obj     *chaosStore
	blobs   *blob.Store
	items   *items.Store
	vk      *cluster.Valkey
	vkReg   *prometheus.Registry
	inv     *invalidation.Set
	invSvc  *invalidation.Service
	engine  *proxy.Engine
	rec     *recorder
}

func (e *env) newReplica(name string, o replicaOpts) *replica {
	t := e.t
	t.Helper()
	if o.poll == 0 {
		o.poll = 200 * time.Millisecond
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	r := &replica{t: t, e: e, name: name, reg: prometheus.NewRegistry(), rec: &recorder{}}
	t.Cleanup(r.stop)
	var err error
	if r.db, err = postgres.Open(ctx, e.cfg, name, "web", quiet); err != nil {
		t.Fatalf("Open(%s): %v", name, err)
	}
	clu := config.Cluster{
		LeaseRetryInterval: 500 * time.Millisecond, EpochPollInterval: o.poll, EpochMaxStaleness: 20 * time.Second,
		ChangeRetention: time.Hour, SignalCoalesceWindow: 20 * time.Millisecond, FlightLockTTL: 30 * time.Second,
	}
	if o.cluster != nil {
		o.cluster(&clu)
	}
	r.vk, r.vkReg = e.valkeyClient(name, o.valkey)
	opts := cluster.ChangesOptions{DB: r.db.ControlPool(), Config: clu, WatchdogTimeout: 30 * time.Second, Log: quiet, Metrics: r.reg}
	if r.changes, err = cluster.NewChanges(opts); err != nil {
		t.Fatal(err)
	}
	if r.caches, err = cache.NewRegistry(r.changes, r.reg); err != nil {
		t.Fatal(err)
	}
	r.changes.AddListener(r.caches)
	if o.invalidations {
		if r.inv, err = invalidation.NewSet(invalidation.SetOptions{Runner: r.db, Changes: r.changes, SafetyInterval: time.Minute, Log: quiet}); err != nil {
			t.Fatal(err)
		}
		if r.invSvc, err = invalidation.NewService(invalidation.ServiceOptions{Runner: r.db, Notifier: r.changes, Recorder: r.rec}); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.changes.Start(ctx); err != nil {
		t.Fatalf("changes.Start: %v", err)
	}
	if r.inv != nil {
		if err := r.inv.Start(ctx); err != nil {
			t.Fatalf("invalidation set: %v", err)
		}
	}

	cfg := e.s3
	cfg.Spool.Dir = t.TempDir()
	r.obj = &chaosStore{ObjectStore: e.obj}
	if r.blobs, err = blob.New(cfg, r.obj, r.db, blob.WithLogger(quiet), blob.WithWaitPoll(25*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if r.items, err = items.New(items.Options{Runner: r.db, Blobs: r.blobs, Recorder: r.rec, Storage: cfg, Log: quiet}); err != nil {
		t.Fatal(err)
	}

	upLog := io.Discard
	if o.upstreamLog != nil {
		upLog = o.upstreamLog
	}
	factory := proxytest.FactoryWithLog(t, slog.New(slog.NewJSONHandler(upLog, nil)), append(append([]*proxytest.Upstream(nil), e.ups...), o.ups...)...)
	pcfg := proxyConfig(t)
	if o.proxy != nil {
		o.proxy(&pcfg)
	}
	clients, err := upstream.New(upstream.Options{
		Factory: factory, Caches: r.caches, Proxy: pcfg, SafetyTTL: time.Minute, Log: quiet,
	})
	if err != nil {
		t.Fatal(err)
	}
	flights := cluster.NewFlightLock(r.vk, cluster.FlightLockOptions{
		TTL: clu.FlightLockTTL, PollInterval: pcfg.LockPollInterval(), InstanceUID: name + "-" + randHex(3), Registerer: r.reg,
	})
	if o.flights != nil {
		flights = o.flights(flights)
	}
	po := proxy.Options{
		Runner: r.db, Items: r.items, Blobs: r.blobs, Flights: flights, Negatives: cluster.NewNegativeStore(r.vk),
		Lookups: r.vk, Changes: r.changes, Clients: clients, Caches: r.caches, Replicas: o.replicas,
		Audit: r.rec, Config: pcfg, SafetyTTL: time.Minute, Metrics: r.reg, Log: quiet,
	}
	if r.inv != nil {
		po.Invalid = r.inv
	}
	if r.engine, err = proxy.New(po); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *replica) stop() {
	if r.engine != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = r.engine.Stop(ctx)
		cancel()
	}
	if r.inv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = r.inv.Stop(ctx)
		cancel()
	}
	if r.changes != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = r.changes.Stop(ctx)
		cancel()
	}
	if r.vk != nil {
		r.vk.Close()
	}
	if r.db != nil {
		r.db.Close()
	}
}

// proxyConfig is the proxy section with its defaults.
func proxyConfig(t *testing.T) config.Proxy {
	t.Helper()
	l, err := config.Load(config.Options{
		ReadFile: func(string) ([]byte, error) { return nil, os.ErrNotExist },
		Environ: []string{
			"TROVE_DATABASE_HOST=db.invalid", "TROVE_DATABASE_NAME=trove", "TROVE_STORAGE_S3_BUCKET=blobs",
			"TROVE_STORAGE_SPOOL_DIR=" + t.TempDir(),
		},
		Command: config.CommandMigrate,
	})
	if err != nil {
		t.Fatalf("proxy defaults: %v", err)
	}
	p := l.Config.Proxy
	p.LockPollIntervalMs = 100
	return p
}

// valkeyClient returns a Valkey client of the installation, or one that cannot
// reach its Sentinels.
func (e *env) valkeyClient(name string, mode valkeyMode) (*cluster.Valkey, *prometheus.Registry) {
	t := e.t
	t.Helper()
	v := testenv.ValkeyConfig(t)
	write := func(file, content string) string {
		p := filepath.Join(t.TempDir(), file)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cfg := config.Valkey{
		Mode: "sentinel", KeyPrefix: e.prefix,
		UsernameFile: write("username", v.Username+"\n"), PasswordFile: write("password", v.Password+"\n"),
		Sentinel: config.ValkeySentinel{
			Addresses: v.Sentinels, PrimaryName: v.PrimaryName, PasswordFile: write("sentinel-password", v.SentinelPassword+"\n"),
			TLS: true, RefreshInterval: time.Second,
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
	if mode == vkUnreachable {
		cfg.Sentinel.Addresses = []string{"127.0.0.1:1"}
	}
	reg := prometheus.NewRegistry()
	vk, err := cluster.NewValkey(context.Background(), cfg, name, "web", quiet, reg, cluster.WithKeyspaceID(e.ks))
	if err != nil {
		t.Fatalf("NewValkey: %v", err)
	}
	if mode == vkReal {
		deadline := time.Now().Add(15 * time.Second)
		for !vk.Available() {
			if time.Now().After(deadline) {
				t.Fatalf("Valkey did not become available: %v", vk.Status().DegradedReason)
			}
			time.Sleep(25 * time.Millisecond)
		}
	}
	return vk, reg
}

// --- driving the engine ---------------------------------------------------------------

// served is what a client got.
type served struct {
	status int
	header http.Header
	body   []byte
	err    error
	// aborted: the connection was cut without completing the response
	// (http.ErrAbortHandler), which is what a client of a failed transfer sees.
	aborted bool
}

func (s served) cacheStatus() string { return s.header.Get("Cache-Status") }

// metadata drives Metadata the way a format's handler would and returns the answer
// as a client sees it: the document's bytes with its Cache-Status, or the error.
func (r *replica) metadata(rem proxy.Remote, path string, mutate ...func(*proxy.Request)) served {
	rq := r.request(rem, path, proxy.Metadata, mutate...)
	doc, err := r.engine.Metadata(context.Background(), rq)
	return r.document(doc, err)
}

func (r *replica) document(doc *proxy.Document, err error) served {
	if err != nil {
		return errServed(err)
	}
	h := http.Header{"Cache-Status": {doc.Status.Header()}}
	if doc.Status.Negative {
		return served{status: http.StatusNotFound, header: h}
	}
	b, berr := doc.Bytes(context.Background())
	if berr != nil {
		return served{status: 500, header: h, err: berr}
	}
	return served{status: http.StatusOK, header: h, body: b}
}

// errServed renders an error as the problem answer the client would get.
func errServed(err error) served {
	rec := httptest.NewRecorder()
	errs.Write(rec, httptest.NewRequest(http.MethodGet, "/", nil), err, "it", nil)
	return served{status: rec.Code, header: rec.Header(), body: rec.Body.Bytes(), err: err}
}

// request builds the engine's request for a path of the test format.
func (r *replica) request(rem proxy.Remote, path string, kind proxy.Kind, mutate ...func(*proxy.Request)) proxy.Request {
	rq := proxy.Request{
		Remote: rem, Path: path, Kind: kind, Package: proxytest.Package(path), Adapter: &proxytest.Adapter{},
		Populate: proxytest.Allowed(), Caller: proxy.CallerFacts{Principal: "it-" + r.name, Endpoint: "default"},
	}
	for _, m := range mutate {
		m(&rq)
	}
	return rq
}

// serveFunc serves a stored item the way the host's item store does, without Range
// handling: the whole content, with the status line the engine's tests look at.
func (r *replica) serveFunc() proxy.ServeFunc {
	return func(ctx context.Context, w http.ResponseWriter, it items.Item) error {
		rc, err := r.items.Open(ctx, it, nil)
		if err != nil {
			return err
		}
		defer func() { _ = rc.Close() }()
		w.Header().Set("Content-Length", strconv.FormatInt(it.Size, 10))
		w.WriteHeader(http.StatusOK)
		_, err = io.Copy(w, rc)
		return err
	}
}

// artifact drives Artifact on a recorder, as a GET.
func (r *replica) artifact(rem proxy.Remote, path string, mutate ...func(*proxy.Request)) served {
	return r.artifactWith(rem, http.MethodGet, path, nil, mutate...)
}

func (r *replica) artifactWith(rem proxy.Remote, method, path string, hdr http.Header, mutate ...func(*proxy.Request)) (out served) {
	rq := r.request(rem, path, proxy.Artifact, mutate...)
	defer func() {
		if rec := recover(); rec != nil {
			if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
				out = served{aborted: true, err: err}
				return
			}
			panic(rec)
		}
	}()
	if rq.Serve == nil {
		rq.Serve = r.serveFunc()
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/"+path, nil)
	for k, v := range hdr {
		req.Header[k] = v
	}
	if err := r.engine.Artifact(context.Background(), rq, rec, req); err != nil {
		return errServed(err)
	}
	return served{status: rec.Code, header: rec.Header(), body: rec.Body.Bytes()}
}

// gauge reads one series of a family of a replica's registry; label is the first
// label value, empty for an unlabelled family.
func (r *replica) metric(name string, labels ...string) float64 {
	r.t.Helper()
	return gather(r.reg, name, labels...)
}

// gather reads one series of a registry: labels are name, value pairs.
func gather(reg *prometheus.Registry, name string, labels ...string) float64 {
	mfs, err := reg.Gather()
	if err != nil {
		panic(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if labelsMatch(m, labels) {
				switch {
				case m.GetGauge() != nil:
					return m.GetGauge().GetValue()
				case m.GetCounter() != nil:
					return m.GetCounter().GetValue()
				case m.GetHistogram() != nil:
					return float64(m.GetHistogram().GetSampleCount())
				}
			}
		}
	}
	return 0
}

func labelsMatch(m *dto.Metric, want []string) bool {
	for i := 0; i+1 < len(want); i += 2 {
		found := false
		for _, l := range m.GetLabel() {
			if l.GetName() == want[i] && l.GetValue() == want[i+1] {
				found = true
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// waitFor polls cond until it holds.
func waitFor(t testing.TB, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s did not happen within %s", what, within)
}

// notifyState announces a change of a repository's upstream state the way the replica
// that made it does (the change area upstream_state of R-ARCH-018), for a test that
// changes the rows itself.
func (r *replica) notifyState(rem proxy.Remote) {
	r.t.Helper()
	err := r.db.InTx(context.Background(), func(tx meta.Tx) error {
		return r.changes.Notify(tx, cluster.AreaUpstreamState, 1, map[string]int64{"r": rem.ID()})
	})
	if err != nil {
		r.t.Fatalf("Notify(upstream_state): %v", err)
	}
}
