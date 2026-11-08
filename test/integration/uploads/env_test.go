//go:build integration

// Package uploads tests the multi-request upload sessions (R-STOR-011) against the integration stack: PostgreSQL for the session
// rows, the S3 store for the multipart uploads and the staging objects, and several replicas (each with its own blob store, spool
// and session service) on both. Nothing here reads TROVE_TEST_VALKEY_*: a session needs no Valkey (R-OCI-032).
//
// Variables read (testenv): TROVE_TEST_PG_*, TROVE_TEST_S3_ENDPOINT, _REGION, _ACCESS_KEY, _SECRET_KEY, _BUCKET_BLOBS and
// _FORCE_PATH_STYLE.
package uploads

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
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/blob/blobtest"
	"github.com/guided-traffic/trove/internal/blob/s3"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/internal/outbound"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func runID(tb testing.TB) string {
	tb.Helper()
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		tb.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

// s3Env is the object store under test, with a prefix of its own in the shared bucket.
type s3Env struct {
	cfg    config.StorageS3
	raw    blob.ObjectStore
	prefix string
}

func newS3(tb testing.TB) *s3Env {
	tb.Helper()
	dir := tb.TempDir()
	writeSecret := func(file, v string) string {
		p := filepath.Join(dir, file)
		if err := os.WriteFile(p, []byte(v+"\n"), 0o600); err != nil {
			tb.Fatal(err)
		}
		return p
	}
	cfg := blobtest.Storage(tb, dir).S3
	cfg.Endpoint = testenv.Get(tb, "TROVE_TEST_S3_ENDPOINT")
	cfg.Region = testenv.Get(tb, "TROVE_TEST_S3_REGION")
	cfg.Bucket = testenv.Get(tb, "TROVE_TEST_S3_BUCKET_BLOBS")
	cfg.ForcePathStyle = testenv.Get(tb, "TROVE_TEST_S3_FORCE_PATH_STYLE") == "true"
	cfg.AllowInsecureEndpoint = false
	cfg.CAFile = testenv.CAFile(tb)
	cfg.Prefix = "it-uploads/" + runID(tb)
	cfg.Credentials = config.StorageS3Credentials{
		Source:              "static",
		AccessKeyIDFile:     writeSecret("access-key", testenv.Get(tb, "TROVE_TEST_S3_ACCESS_KEY")),
		SecretAccessKeyFile: writeSecret("secret-key", testenv.Get(tb, "TROVE_TEST_S3_SECRET_KEY")),
	}
	f := outbound.New(config.Outbound{TLS: config.OutboundTLS{UseSystemRoots: true}})
	c, err := s3.NewFromFactory(context.Background(), cfg, f)
	if err != nil {
		tb.Fatalf("object store client: %v", err)
	}
	e := &s3Env{cfg: cfg, raw: c, prefix: cfg.Prefix}
	tb.Cleanup(func() { e.removeAll(tb) })
	return e
}

// removeAll deletes every object and unfinished upload below the test's prefix.
func (e *s3Env) removeAll(tb testing.TB) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ups, _ := e.raw.ListMultipart(ctx, e.prefix+"/")
	for _, u := range ups {
		_ = e.raw.AbortMultipart(ctx, u.Key, u.UploadID)
	}
	for _, k := range e.keys(tb, e.prefix+"/") {
		_ = e.raw.Delete(ctx, k)
	}
}

// keys lists every object below a prefix.
func (e *s3Env) keys(tb testing.TB, prefix string) []string {
	tb.Helper()
	var out []string
	token := ""
	for {
		page, err := e.raw.List(context.Background(), prefix, blob.ListOptions{Token: token})
		if err != nil {
			tb.Fatal(err)
		}
		for _, o := range page.Objects {
			out = append(out, o.Key)
		}
		if page.Next == "" {
			return out
		}
		token = page.Next
	}
}

// storage returns the storage configuration of a replica of this store.
func (e *s3Env) storage(tb testing.TB) config.Storage {
	tb.Helper()
	cfg := blobtest.Storage(tb, tb.TempDir())
	cfg.S3 = e.cfg
	cfg.Upload.BlobWaitTimeout = 60 * time.Second
	cfg.Uploads.PartSize = 5 << 20 // the smallest part S3 takes
	return cfg
}

// pgEnv is a database of its own with the embedded migrations applied, dropped when the test ends.
type pgEnv struct {
	db    *postgres.DB
	admin *pgx.Conn
	repo  int64
	other int64
}

// template is the migrated database the tests of the run clone.
var template struct {
	once sync.Once
	name string
	err  error
}

// templateDB creates and migrates the template database on first use and returns its name.
func templateDB(tb testing.TB, maint *pgx.Conn, base config.Database) string {
	tb.Helper()
	template.once.Do(func() {
		name := "trove_uploads_tmpl_" + runID(tb)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if _, err := maint.Exec(ctx, "CREATE DATABASE "+name+" ENCODING 'UTF8' TEMPLATE template0"); err != nil {
			template.err = fmt.Errorf("create the template database: %w", err)
			return
		}
		template.name = name // set before the migration, so that the drop at the end of the run finds it
		cfg := base
		cfg.Name = name
		_, template.err = postgres.Migrate(ctx, cfg, postgres.MigrateOptions{
			Instance: "it-uploads-migrate", Version: "0.1.0", Log: quiet,
			Migrate: config.Migrate{AdvisoryLockWait: 20 * time.Second, DDLLockTimeout: 2 * time.Second, DDLLockRetries: 3},
		})
		if template.err != nil {
			template.err = fmt.Errorf("migrate the template database: %w", template.err)
		}
	})
	if template.err != nil {
		tb.Fatal(template.err)
	}
	return template.name
}

// dropTemplate removes the template database at the end of the run.
func dropTemplate() {
	if template.name == "" {
		return
	}
	cfg, err := pgx.ParseConfig(os.Getenv("TROVE_TEST_PG_DSN"))
	if err != nil {
		return
	}
	cfg.Database = "postgres"
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	c, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return
	}
	defer c.Close(ctx)
	_, _ = c.Exec(ctx, "DROP DATABASE IF EXISTS "+template.name+" WITH (FORCE)")
}

func newPG(tb testing.TB) *pgEnv {
	tb.Helper()
	host, user, secret := testenv.Get(tb, "TROVE_TEST_PG_HOST"), testenv.Get(tb, "TROVE_TEST_PG_USER"), testenv.Get(tb, "TROVE_TEST_PG_PASSWORD")
	port, err := strconv.Atoi(testenv.Get(tb, "TROVE_TEST_PG_PORT"))
	if err != nil {
		tb.Fatalf("TROVE_TEST_PG_PORT: %v", err)
	}
	connect := func(database string) *pgx.Conn {
		c, err := pgx.Connect(context.Background(), testenv.PgDSN(tb, database))
		if err != nil {
			tb.Fatalf("connect to %s: %v", database, err)
		}
		tb.Cleanup(func() { _ = c.Close(context.Background()) })
		return c
	}
	name := "trove_uploads_" + runID(tb)
	maint := connect("postgres")

	dir := tb.TempDir()
	userFile, passFile := filepath.Join(dir, "username"), filepath.Join(dir, "password")
	if err := os.WriteFile(userFile, []byte(user+"\n"), 0o600); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(passFile, []byte(secret+"\n"), 0o600); err != nil {
		tb.Fatal(err)
	}
	base := config.Database{
		Host: host, Port: port, Name: name, Schema: "trove",
		UsernameFile: userFile, PasswordFile: passFile,
		SSLMode: "verify-full", SSLRootCertFile: testenv.CAFile(tb),
		ConnectTimeout: 5 * time.Second, TargetSessionAttrs: "read-write",
		Pooler:        config.DatabasePooler{Mode: "none"},
		Pool:          config.DatabasePool{MaxConns: 24, MinConns: 1, MaxConnLifetime: 30 * time.Minute, MaxConnLifetimeJitter: time.Minute, MaxConnIdleTime: 5 * time.Minute, HealthCheckPeriod: 30 * time.Second},
		LeasePoolSize: 2, LeaseCheckInterval: 5 * time.Second,
		Session: config.DatabaseSession{
			StatementTimeout: 30 * time.Second, LockTimeout: 20 * time.Second,
			IdleInTransactionSessionTimeout: 60 * time.Second,
		},
		Retry:           config.DatabaseRetry{InitialBackoff: 50 * time.Millisecond, MaxBackoff: 200 * time.Millisecond, MaxElapsed: 5 * time.Second},
		Keepalive:       config.DatabaseKeepalive{Idle: 15 * time.Second, Interval: 5 * time.Second, Count: 3, UserTimeout: 30 * time.Second},
		ServerKeepalive: config.DatabaseServerKeepalive{Idle: 60 * time.Second, Interval: 10 * time.Second, Count: 6},
		Preflight:       config.DatabasePreflight{ConnectionBudget: "enforce", ReservedConnections: 10, AllowSuperuser: true},
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	// The schema is migrated once per run, into a template database that every test clones: a migration of all the files takes
	// longer than a test under the load of a shared stack, and the clone is the same state.
	src := templateDB(tb, maint, base)
	if _, err := maint.Exec(ctx, "CREATE DATABASE "+name+" ENCODING 'UTF8' TEMPLATE "+src); err != nil {
		tb.Fatalf("create database: %v", err)
	}
	tb.Cleanup(func() {
		_, _ = maint.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	cfg := base
	if testenv.PgPooled(tb) {
		cfg = testenv.ThroughPgBouncer(tb, cfg, user, maint)
	}
	db, err := postgres.Open(ctx, cfg, "it-uploads", "web", quiet)
	if err != nil {
		tb.Fatalf("open: %v", err)
	}
	tb.Cleanup(db.Close)
	p := &pgEnv{db: db, admin: connect(name)}
	if _, err := p.admin.Exec(ctx, "SET search_path TO trove"); err != nil {
		tb.Fatal(err)
	}
	p.repo = p.newRepo(tb, "docker-local")
	p.other = p.newRepo(tb, "docker-other")
	return p
}

func (p *pgEnv) newRepo(tb testing.TB, key string) int64 {
	tb.Helper()
	p.exec(tb, `INSERT INTO trove.project (id, key, display_name) VALUES (gen_random_uuid(), $1, $1) ON CONFLICT (key) DO NOTHING`, testenv.DefaultProject)
	return p.scalarInt(tb, `INSERT INTO trove.repositories (key, format, class, project_id) SELECT $1, 'generic', 'local', id FROM trove.project WHERE key = $2 RETURNING id`, key, testenv.DefaultProject)
}

func (p *pgEnv) exec(tb testing.TB, sql string, args ...any) {
	tb.Helper()
	if _, err := p.admin.Exec(context.Background(), sql, args...); err != nil {
		tb.Fatalf("%s: %v", sql, err)
	}
}

func (p *pgEnv) scalarInt(tb testing.TB, sql string, args ...any) int64 {
	tb.Helper()
	var v int64
	if err := p.admin.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		tb.Fatalf("%s: %v", sql, err)
	}
	return v
}

func (p *pgEnv) count(tb testing.TB, from string, args ...any) int64 {
	tb.Helper()
	return p.scalarInt(tb, "SELECT count(*) FROM trove."+from, args...)
}

func (p *pgEnv) sessionState(tb testing.TB, id string) string {
	tb.Helper()
	var s string
	err := p.admin.QueryRow(context.Background(), "SELECT state FROM trove.upload_sessions WHERE id = $1::uuid", id).Scan(&s)
	if errors.Is(err, pgx.ErrNoRows) {
		return ""
	}
	if err != nil {
		tb.Fatal(err)
	}
	return s
}

// link is the LinkFunc of a commit that adds the file item for the blob and ends the session in the same transaction.
func (p *pgEnv) link(path string, sp *blob.Spooled, markDone func(context.Context, meta.Tx) error) blob.LinkFunc {
	return func(ctx context.Context, tx meta.Tx) error {
		ptx, ok := postgres.Unwrap(tx)
		if !ok {
			return errors.New("not a transaction of internal/meta/postgres")
		}
		if _, err := ptx.Exec(ctx, `INSERT INTO items (repo_id, path, parent_path, kind, blob_sha256, size)
			VALUES ($1, $2, '', 'file', $3, $4) ON CONFLICT DO NOTHING`, p.repo, path, sp.SHA256.Bytes(), sp.Size); err != nil {
			return err
		}
		return markDone(ctx, tx)
	}
}
