//go:build integration

package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/create"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/backup/target"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/blobtest"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/observability"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// install is one installation of the create tests: a database of its own with the embedded migrations, a blob store on a
// prefix of the stack's blob bucket, a replica's blob store and collector on both, and the means to run the create pipeline
// against a target.
type install struct {
	t      *testing.T
	dbName string
	dbCfg  config.Database
	admin  *pgx.Conn
	web    *postgres.DB
	repo   int64

	s3cfg  config.StorageS3
	obj    blob.ObjectStore
	client *s3.Client
	prefix string

	store *blob.Store
	coll  *blob.Collector
	gc    config.StorageGC

	pods atomic.Int32
}

func runID() string {
	var b [5]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func secretFile(t testing.TB, dir, name, v string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(v+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// newInstall creates the database, the schema, the canary row the manifest needs and a blob store.
func newInstall(t *testing.T) *install {
	t.Helper()
	in := &install{t: t}
	connect := func(database string) *pgx.Conn {
		c, err := pgx.Connect(context.Background(), testenv.PgDSN(t, database))
		if err != nil {
			t.Fatalf("connect to %s: %v", database, err)
		}
		t.Cleanup(func() { _ = c.Close(context.Background()) })
		return c
	}
	var err error
	in.dbName = "trove_bak_" + runID()
	maint := connect("postgres")
	if _, err := maint.Exec(context.Background(), "CREATE DATABASE "+in.dbName+" ENCODING 'UTF8' TEMPLATE template0"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() { _, _ = maint.Exec(context.Background(), "DROP DATABASE IF EXISTS "+in.dbName+" WITH (FORCE)") })

	dir := t.TempDir()
	in.dbCfg = pgConfig(t, dir, in.dbName)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := postgres.Migrate(ctx, in.dbCfg, postgres.MigrateOptions{
		Instance: "it-bak-migrate", Version: "0.1.0", Log: quiet,
		Migrate: config.Migrate{AdvisoryLockWait: 20 * time.Second, DDLLockTimeout: 2 * time.Second, DDLLockRetries: 3},
	}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	in.admin = connect(in.dbName)
	in.exec("SET search_path TO trove")
	in.exec(`INSERT INTO trove.secret_key_canary (key_id, ciphertext) VALUES ('2026-10', 'v1:2026-10:AAAA')`)
	in.exec(`INSERT INTO trove.project (id, key, display_name) VALUES (gen_random_uuid(), 'it', 'it')`)
	in.repo = in.newRepo("generic-local", "local")
	if in.web, err = postgres.Open(ctx, in.dbCfg, "it-bak-web", "web", quiet); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(in.web.Close)

	in.s3cfg = sourceConfig(t, dir)
	in.prefix = in.s3cfg.Prefix
	factory := outbound.New(config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true}})
	in.client, err = s3.NewFromFactory(ctx, in.s3cfg, factory)
	if err != nil {
		t.Fatal(err)
	}
	in.obj = in.client
	t.Cleanup(func() { removeAll(in.obj, in.prefix) })

	storage := blobtest.Storage(t, t.TempDir())
	storage.S3 = in.s3cfg
	storage.Upload.BlobWaitTimeout = 60 * time.Second
	storage.GC.GracePeriod = 0
	storage.GC.DeleteSettle = 150 * time.Millisecond
	storage.GC.HandoverDelay = 1200 * time.Millisecond
	storage.GC.BatchSize = 25
	in.gc = storage.GC
	in.store, err = blob.New(storage, in.obj, in.web, blob.WithLogger(quiet), blob.WithWaitPoll(25*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	in.coll = blob.NewCollector(storage.GC, storage.S3.Prefix, in.obj, in.web, blob.WithCollectorLogger(quiet))
	return in
}

func sourceConfig(t testing.TB, dir string) config.StorageS3 {
	cfg := blobtest.Storage(t, dir).S3
	cfg.Endpoint = testenv.Get(t, "TROVE_TEST_S3_ENDPOINT")
	cfg.Region = testenv.Get(t, "TROVE_TEST_S3_REGION")
	cfg.Bucket = testenv.Get(t, "TROVE_TEST_S3_BUCKET_BLOBS")
	cfg.ForcePathStyle = testenv.Get(t, "TROVE_TEST_S3_FORCE_PATH_STYLE") == "true"
	cfg.CAFile = testenv.CAFile(t)
	cfg.Prefix = "it-bak-src/" + runID()
	cfg.Credentials = config.StorageS3Credentials{
		Source:              "static",
		AccessKeyIDFile:     secretFile(t, dir, "src-id", testenv.Get(t, "TROVE_TEST_S3_ACCESS_KEY")),
		SecretAccessKeyFile: secretFile(t, dir, "src-secret", testenv.Get(t, "TROVE_TEST_S3_SECRET_KEY")),
	}
	return cfg
}

func removeAll(obj blob.ObjectStore, prefix string) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	token := ""
	for {
		page, err := obj.List(ctx, prefix+"/", blob.ListOptions{Token: token})
		if err != nil {
			return
		}
		for _, o := range page.Objects {
			_ = obj.Delete(ctx, o.Key)
		}
		if page.Next == "" {
			return
		}
		token = page.Next
	}
}

func (in *install) exec(sql string, args ...any) {
	in.t.Helper()
	if _, err := in.admin.Exec(context.Background(), sql, args...); err != nil {
		in.t.Fatalf("%s: %v", sql, err)
	}
}

func (in *install) number(sql string, args ...any) int64 {
	in.t.Helper()
	var v int64
	if err := in.admin.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		in.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func (in *install) newRepo(key, class string) int64 {
	in.t.Helper()
	return in.number(`INSERT INTO trove.repositories (key, format, class, project_id) SELECT $1, 'generic', $2, id FROM trove.project WHERE key = 'it' RETURNING id`, key, class)
}

// upload ingests, commits and links a file item to the repository.
func (in *install) upload(ctx context.Context, repo int64, path string, data []byte) (blob.Digest, error) {
	sp, err := in.store.Ingest(ctx, bytes.NewReader(data), blob.IngestOptions{ContentLength: int64(len(data))})
	if err != nil {
		return blob.Digest{}, err
	}
	defer sp.Close()
	_, err = in.store.Commit(ctx, sp, func(ctx context.Context, tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errors.New("not a PostgreSQL transaction")
		}
		_, err := ptx.Exec(ctx, `INSERT INTO items (repo_id, path, parent_path, kind, blob_sha256, size) VALUES ($1, $2, '', 'file', $3, $4)`, repo, path, sp.SHA256.Bytes(), sp.Size)
		return err
	})
	return sp.SHA256, err
}

func (in *install) mustUpload(repo int64, path, content string) blob.Digest {
	in.t.Helper()
	d, err := in.upload(context.Background(), repo, path, []byte(content))
	if err != nil {
		in.t.Fatal(err)
	}
	return d
}

// deleteItem removes the item; the release trigger queues the blob for the collector.
func (in *install) deleteItem(path string) {
	in.t.Helper()
	in.exec(`DELETE FROM trove.items WHERE path = $1`, path)
}

func (in *install) blobState(d blob.Digest) (string, bool) {
	var st string
	err := in.admin.QueryRow(context.Background(), `SELECT state FROM trove.blobs WHERE sha256 = $1`, d.Bytes()).Scan(&st)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false
	}
	if err != nil {
		in.t.Fatal(err)
	}
	return st, true
}

// collect runs one full and one incremental collection.
func (in *install) collect(ctx context.Context) (blob.GCResult, error) {
	var total blob.GCResult
	for _, mode := range []blob.GCMode{blob.GCFull, blob.GCIncremental} {
		res, err := in.coll.Run(ctx, blob.RunOptions{Mode: mode})
		total.Held += res.Held
		total.Deleted += res.Deleted
		total.HoldSkipped = total.HoldSkipped || res.HoldSkipped
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// rig is what a create run is built from; a test changes it before it runs.
type rig struct {
	cfg    config.Backup
	tgt    target.Store
	mutate func(*create.Deps)
	// role is the database credential of the run; empty means the stack's superuser.
	dbCfg   *config.Database
	metrics *backup.Metrics
}

func (in *install) backupConfig(t testing.TB) config.Backup {
	t.Helper()
	l, err := config.Load(config.Options{
		ReadFile: func(string) ([]byte, error) { return nil, fs.ErrNotExist },
		Environ: []string{
			"TROVE_DATABASE_HOST=db.invalid", "TROVE_DATABASE_NAME=trove", "TROVE_STORAGE_S3_BUCKET=blobs",
			"TROVE_BACKUP_TARGET_BUCKET=" + testenv.Get(t, "TROVE_TEST_S3_BUCKET_BACKUPS"),
		},
		Command: config.CommandBackup,
	})
	if err != nil {
		t.Fatalf("backup defaults: %v", err)
	}
	cfg := l.Config.Backup
	tc := targetConfig(t, testenv.Get(t, "TROVE_TEST_S3_BUCKET_BACKUPS"), workingSSE(t))
	cfg.Target = tc.Target
	cfg.Database.PartSize = tc.Database.PartSize
	cfg.Copy.PartSize = tc.Copy.PartSize
	cfg.Database.Parallel = 2
	cfg.Database.LockTimeout = 5 * time.Second
	cfg.Database.MigrationWait = 5 * time.Second
	cfg.LeaseDuration = time.Minute
	cfg.HeartbeatInterval = 200 * time.Millisecond
	cfg.FinalizeTimeout = 5 * time.Second
	return cfg
}

// newRig returns the configuration and the real target of a run.
func (in *install) newRig(t *testing.T) *rig {
	t.Helper()
	cfg := in.backupConfig(t)
	tgt := newTarget(t, cfg)
	cleanup(t, tgt, cfg.Target.Prefix+"/")
	return &rig{cfg: cfg, tgt: tgt}
}

// run builds the pipeline of the rig and runs it once.
func (in *install) run(ctx context.Context, r *rig, opts backup.CreateOptions) (*backup.Result, error) {
	p, db := in.pipeline(r)
	defer db.Close()
	return p.Create(ctx, opts)
}

func (in *install) pipeline(r *rig) (*create.Pipeline, *postgres.DB) {
	in.t.Helper()
	cfgDB := in.dbCfg
	if r.dbCfg != nil {
		cfgDB = *r.dbCfg
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	instance := fmt.Sprintf("it-bak-pod%d", in.pods.Add(1))
	db, err := postgres.Open(ctx, cfgDB, instance, meta.RoleBackup, quiet)
	if err != nil {
		in.t.Fatalf("open the backup database: %v", err)
	}
	facts := backup.NewPGFacts(db, cfgDB.Schema)
	d := create.Deps{
		Cfg: r.cfg, SourceRef: format.StorageRef{Endpoint: in.s3cfg.Endpoint, Bucket: in.s3cfg.Bucket, Prefix: in.s3cfg.Prefix},
		Instance: instance, Version: "0.1.0", Revision: "it", BackupRole: "it",
		Store: backup.NewPGStore(db), Facts: facts, Grants: facts, Pins: facts, BlobState: facts,
		Snapshot: backup.NewPGSnapshotter(db),
		Source:   backup.NewStoreSource(in.obj, in.s3cfg.Prefix, in.s3cfg.Endpoint, in.s3cfg.Bucket),
		Target:   r.tgt, Events: backup.NewPGEvents(db, instance, quiet),
		Metrics: r.metrics, Log: quiet,
	}
	if d.Metrics == nil {
		d.Metrics = backup.NewMetrics(observability.NewMetrics(meta.RoleBackup))
	}
	if r.mutate != nil {
		r.mutate(&d)
	}
	p, err := create.New(d)
	if err != nil {
		in.t.Fatal(err)
	}
	return p, db
}

func manifestOf(t testing.TB, tgt target.Store, cfg config.Backup, id string) *format.Manifest {
	t.Helper()
	rc, _, err := tgt.Get(context.Background(), format.Layout{Prefix: cfg.Target.Prefix}.Manifest(format.ID(id)))
	if err != nil {
		t.Fatalf("manifest of %s: %v", id, err)
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	m, err := format.DecodeManifest(b)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func poolHas(t testing.TB, tgt target.Store, cfg config.Backup, d format.Digest) bool {
	t.Helper()
	_, err := tgt.Head(context.Background(), format.Layout{Prefix: cfg.Target.Prefix}.Pool(d))
	if err != nil && !errors.Is(err, target.ErrNotFound) {
		t.Fatal(err)
	}
	return err == nil
}

func exit(err error) int { return backup.ExitCode(err) }

// pgConfig is the database section for the stack's PostgreSQL and a database of the given name.
func pgConfig(t testing.TB, dir, name string) config.Database {
	t.Helper()
	host, user, secret := testenv.Get(t, "TROVE_TEST_PG_HOST"), testenv.Get(t, "TROVE_TEST_PG_USER"), testenv.Get(t, "TROVE_TEST_PG_PASSWORD")
	port, err := strconv.Atoi(testenv.Get(t, "TROVE_TEST_PG_PORT"))
	if err != nil {
		t.Fatal(err)
	}
	return config.Database{
		Host: host, Port: port, Name: name, Schema: "trove",
		UsernameFile: secretFile(t, dir, "username", user), PasswordFile: secretFile(t, dir, "password", secret),
		SSLMode: "verify-full", SSLRootCertFile: testenv.CAFile(t),
		ConnectTimeout: 5 * time.Second, TargetSessionAttrs: "read-write",
		Pooler:        config.DatabasePooler{Mode: "none"},
		Pool:          config.DatabasePool{MaxConns: 24, MinConns: 1, MaxConnLifetime: 30 * time.Minute, MaxConnLifetimeJitter: time.Minute, MaxConnIdleTime: 5 * time.Minute, HealthCheckPeriod: 30 * time.Second},
		LeasePoolSize: 2, LeaseCheckInterval: 5 * time.Second,
		Session:         config.DatabaseSession{StatementTimeout: 30 * time.Second, LockTimeout: 20 * time.Second, IdleInTransactionSessionTimeout: 60 * time.Second},
		Retry:           config.DatabaseRetry{InitialBackoff: 50 * time.Millisecond, MaxBackoff: 200 * time.Millisecond, MaxElapsed: 5 * time.Second},
		Keepalive:       config.DatabaseKeepalive{Idle: 15 * time.Second, Interval: 5 * time.Second, Count: 3, UserTimeout: 30 * time.Second},
		ServerKeepalive: config.DatabaseServerKeepalive{Idle: 60 * time.Second, Interval: 10 * time.Second, Count: 6},
		Preflight:       config.DatabasePreflight{ConnectionBudget: "enforce", ReservedConnections: 10, AllowSuperuser: true},
	}
}
