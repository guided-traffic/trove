//go:build integration

// Package virtual tests the virtual engine against the integration stack: the real repository
// snapshot, the routing with its claims, the package rows and rev_seq, the change log between
// several in-process "replicas" that have connection pools of their own, and no Valkey (the
// signal path has no bus, as on a replica whose Valkey client is unavailable). The remote engine is
// the fake of virtualtest, the decision point a table of grants: the unit tests of internal/virtual
// run the real decision point. Every test creates a database of its own.
package virtual

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/cache"
	"github.com/guided-traffic/trove/internal/cluster"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/formathost"
	"github.com/guided-traffic/trove/internal/items"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/packages"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/internal/virtual"
	vt "github.com/guided-traffic/trove/internal/virtual/virtualtest"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

const schemaName = "trove"

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
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
	t   *testing.T
	cfg config.Database
}

// newEnv creates a database trove_virt_<random>, migrates it and drops it when the test ends.
func newEnv(t *testing.T) *env {
	t.Helper()
	host, user, secret := testenv.Get(t, "TROVE_TEST_PG_HOST"), testenv.Get(t, "TROVE_TEST_PG_USER"), testenv.Get(t, "TROVE_TEST_PG_PASSWORD")
	port, err := strconv.Atoi(testenv.Get(t, "TROVE_TEST_PG_PORT"))
	if err != nil {
		t.Fatalf("TROVE_TEST_PG_PORT: %v", err)
	}
	name := "trove_virt_" + randHex(5)
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
	return &env{t: t, cfg: cfg}
}

// trackingRunner counts the transactions that are open right now, which is how the tests show that
// the shadowing check holds none while it probes.
type trackingRunner struct {
	meta.Runner
	open atomic.Int64
}

func (r *trackingRunner) InTx(ctx context.Context, fn func(meta.Tx) error) error {
	r.open.Add(1)
	defer r.open.Add(-1)
	return r.Runner.InTx(ctx, fn)
}

// noItems is the item store of a repository that holds no artifacts: the tests resolve documents.
type noItems struct{ formathost.ItemOps }

func (noItems) Live(context.Context, items.RepoRef, string) (items.Item, bool, error) {
	return items.Item{}, false, nil
}

// dbDocs is the document source of the test format over the real package rows.
type dbDocs struct{ pkgs *packages.Store }

func (d dbDocs) Document(ctx context.Context, repo int64, pkg string) ([]byte, int64, bool, error) {
	vs, rev, err := d.pkgs.Versions(ctx, repo, pkg)
	if err != nil {
		return nil, 0, false, err
	}
	names := make([]string, len(vs))
	for i, v := range vs {
		names[i] = v.Version
	}
	return vt.Doc(names), rev, len(vs) > 0, nil
}

// grants is the decision point of the integration tests: who reads which repository, who may
// fill the caches of the remote repositories and who holds manage on the local ones.
type grants struct {
	reads  map[string][]string
	fills  map[string]bool
	manage map[string]bool
}

var _ virtual.Decider = grants{}

func (g grants) can(sub authz.Subject, key string) bool {
	for _, k := range g.reads[sub.Name] {
		if k == key {
			return true
		}
	}
	return false
}

func (g grants) MemberUsableOn(_ context.Context, _ *authz.Index, sub authz.Subject, member string, _ authz.Coordinate) (bool, bool) {
	read := g.can(sub, member)
	return read, read && g.fills[sub.Name]
}

func (g grants) ReadableOn(_ context.Context, _ *authz.Index, sub authz.Subject, keys []string) ([]string, error) {
	var out []string
	for _, k := range keys {
		if g.can(sub, k) {
			out = append(out, k)
		}
	}
	return out, nil
}

func (g grants) HoldsOn(*authz.Index, authz.Subject, string) bool { return false }

func (g grants) CheckOn(_ *authz.Index, sub authz.Subject, items []authz.CheckItem) ([]bool, error) {
	out := make([]bool, len(items))
	for i := range items {
		out[i] = g.manage[sub.Name]
	}
	return out, nil
}

// replica is one in-process replica with pools and components of its own.
type replica struct {
	t       *testing.T
	name    string
	reg     *prometheus.Registry
	db      *postgres.DB
	runner  *trackingRunner
	signals *cluster.Signals
	changes *cluster.Changes
	caches  *cache.Registry
	reader  *repository.SnapshotReader
	routing *repository.Routing
	svc     *repository.Service
	pkgs    *packages.Store
	merger  *vt.Merger
	engine  *virtual.Resolver
}

type replicaOpts struct {
	grants grants
	remote *vt.Remote
	poll   time.Duration
}

func (e *env) newReplica(name string, o replicaOpts) *replica {
	t := e.t
	t.Helper()
	if o.poll == 0 {
		o.poll = 200 * time.Millisecond
	}
	clu := config.Cluster{
		LeaseRetryInterval: 500 * time.Millisecond, EpochPollInterval: o.poll, EpochMaxStaleness: 20 * time.Second,
		ChangeRetention: time.Hour, SignalCoalesceWindow: 20 * time.Millisecond,
	}
	r := &replica{t: t, name: name, reg: prometheus.NewRegistry()}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var err error
	if r.db, err = postgres.Open(ctx, e.cfg, name, "web", quiet); err != nil {
		t.Fatalf("Open(%s): %v", name, err)
	}
	t.Cleanup(r.stop)
	r.runner = &trackingRunner{Runner: r.db}
	// No bus: the signal path is local only, as on a replica whose Valkey client is unavailable.
	if r.signals, err = cluster.NewSignals(cluster.SignalsOptions{Config: clu, Log: quiet, Metrics: r.reg}); err != nil {
		t.Fatal(err)
	}
	if r.changes, err = cluster.NewChanges(cluster.ChangesOptions{
		DB: r.db.ControlPool(), Config: clu, WatchdogTimeout: 30 * time.Second, Log: quiet, Metrics: r.reg,
	}); err != nil {
		t.Fatal(err)
	}
	if r.caches, err = cache.NewRegistry(r.changes, r.reg); err != nil {
		t.Fatal(err)
	}
	r.changes.AddListener(r.caches)
	if err := r.changes.Start(ctx); err != nil {
		t.Fatalf("changes.Start: %v", err)
	}
	cat := vt.Catalog()
	if r.reader, err = repository.NewReader(repository.ReaderOptions{
		Runner: r.runner, Catalog: cat, Caches: r.caches, Poller: r.changes,
		SafetyTTL: 5 * time.Minute, UnknownKeys: 10000, LookupWait: 5 * time.Second, Log: quiet,
	}); err != nil {
		t.Fatal(err)
	}
	if r.svc, err = repository.NewService(repository.Options{Runner: r.db, Catalog: cat, Notifier: r.changes, Log: quiet}); err != nil {
		t.Fatal(err)
	}
	if r.routing, err = repository.NewRouting(repository.RoutingOptions{
		Runner: r.runner, Reader: r.reader, Catalog: cat, Caches: r.caches, Notifier: r.changes,
		SafetyTTL: 5 * time.Minute, Log: quiet,
	}); err != nil {
		t.Fatal(err)
	}
	if r.pkgs, err = packages.NewStore(r.runner); err != nil {
		t.Fatal(err)
	}
	r.merger = vt.NewMerger(dbDocs{r.pkgs})
	vo := virtual.Options{
		Router: r.routing, Decider: o.grants, Locals: formathost.NewVirtualLocals(r.pkgs, noItems{}),
		Caches: r.caches, CacheBytes: 1 << 20, Signals: r.signals, Metrics: r.reg,
	}
	if o.remote != nil {
		vo.Remotes = o.remote
	}
	if r.engine, err = virtual.New(vo); err != nil {
		t.Fatal(err)
	}
	if err := r.engine.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return r
}

func (r *replica) stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if r.engine != nil {
		r.engine.Stop()
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
}

var caller = repository.Caller{Actor: audit.Actor{Kind: "user", ID: "0198a000-0000-7000-8000-000000000001", Name: "alice"}}

// create makes a repository of the test format; extra is the members of the document after the
// key, class and format.
func (r *replica) create(key, class, extra string) repository.Repository {
	r.t.Helper()
	doc := fmt.Sprintf(`{"key":%q,"class":%q,"format":%q`, key, class, vt.Format)
	if class == "remote" && extra == "" {
		extra = `"remote":{"url":"https://upstream.example.test/"}`
	}
	if extra != "" {
		doc += "," + extra
	}
	if _, err := r.svc.Create(context.Background(), caller, []byte(doc+"}")); err != nil {
		r.t.Fatalf("create %s: %v", key, err)
	}
	return r.repo(key)
}

// repo reads a repository from this replica's snapshot, waiting for the change log to bring it.
func (r *replica) repo(key string) repository.Repository {
	r.t.Helper()
	v, err := r.reader.ByKey(context.Background(), key)
	if err != nil {
		r.t.Fatalf("ByKey(%s) on %s: %v", key, r.name, err)
	}
	return v
}

func (r *replica) id(key string) int64 {
	n, err := strconv.ParseInt(r.repo(key).ID, 10, 64)
	if err != nil {
		r.t.Fatal(err)
	}
	return n
}

// publish adds a version to a package of a local repository, in one transaction that raises the
// revision the way a publish does.
func (r *replica) publish(repoKey, pkg, version string) {
	r.t.Helper()
	ctx := context.Background()
	id := r.id(repoKey)
	err := r.db.InTx(ctx, func(tx meta.Tx) error {
		w, err := packages.NewWriter(tx, "")
		if err != nil {
			return err
		}
		ref, _, err := w.EnsureLocked(ctx, id, pkg, pkg)
		if err != nil {
			return err
		}
		_, err = w.InsertVersion(ctx, ref, packages.NewVersion{Version: version})
		return err
	})
	if err != nil {
		r.t.Fatalf("publish %s@%s to %s: %v", pkg, version, repoKey, err)
	}
}

func metadataOf(pkg string) virtual.Descriptor {
	return virtual.Descriptor{Format: vt.Format, Path: pkg, Kind: "metadata", Package: pkg}
}

// metadata resolves a document through a virtual repository for a subject.
func (r *replica) metadata(v string, sub authz.Subject, pkg string) (*virtual.Merged, error) {
	r.t.Helper()
	ctx := context.Background()
	c := virtual.Caller{Subject: sub}
	p, err := r.engine.Plan(ctx, c, r.repo(v), metadataOf(pkg))
	if err != nil {
		r.t.Fatal(err)
	}
	return r.engine.Metadata(ctx, c, p, r.merger)
}

func (r *replica) mergedEntries() int {
	for _, s := range r.caches.Stats() {
		if s.Name == virtual.CacheMerged {
			return s.Entries
		}
	}
	return -1
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

func subject(name string) authz.Subject { return authz.Subject{Name: name, ID: "user-" + name} }
