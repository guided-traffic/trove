//go:build integration

package blob

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// pgEnv is a database of its own (trove_blob_<random>) with the embedded
// migrations applied, dropped when the test ends.
type pgEnv struct {
	db    *postgres.DB
	admin *pgx.Conn
	cfg   config.Database
	repo  int64
}

func newPG(tb testing.TB) *pgEnv {
	tb.Helper()
	host, user, secret := testenv.Get(tb, "TROVE_TEST_PG_HOST"), testenv.Get(tb, "TROVE_TEST_PG_USER"), testenv.Get(tb, "TROVE_TEST_PG_PASSWORD")
	port, err := strconv.Atoi(testenv.Get(tb, "TROVE_TEST_PG_PORT"))
	if err != nil {
		tb.Fatalf("TROVE_TEST_PG_PORT: %v", err)
	}
	connect := func(database string) *pgx.Conn {
		c, err := pgx.Connect(context.Background(), "postgres://"+user+":"+secret+"@"+host+":"+strconv.Itoa(port)+"/"+database+"?sslmode=disable")
		if err != nil {
			tb.Fatalf("connect to %s: %v", database, err)
		}
		tb.Cleanup(func() { _ = c.Close(context.Background()) })
		return c
	}
	name := "trove_blob_" + runID(tb)
	maint := connect("postgres")
	if _, err := maint.Exec(context.Background(), "CREATE DATABASE "+name+" ENCODING 'UTF8' TEMPLATE template0"); err != nil {
		tb.Fatalf("create database: %v", err)
	}
	tb.Cleanup(func() {
		_, _ = maint.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})

	dir := tb.TempDir()
	userFile, passFile := filepath.Join(dir, "username"), filepath.Join(dir, "password")
	if err := os.WriteFile(userFile, []byte(user+"\n"), 0o600); err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(passFile, []byte(secret+"\n"), 0o600); err != nil {
		tb.Fatal(err)
	}
	cfg := config.Database{
		Host: host, Port: port, Name: name, Schema: "trove",
		UsernameFile: userFile, PasswordFile: passFile,
		SSLMode: "disable", AllowInsecure: true,
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
	if _, err := postgres.Migrate(ctx, cfg, postgres.MigrateOptions{
		Instance: "it-blob-migrate", Version: "0.1.0", Log: quiet,
		Migrate: config.Migrate{AdvisoryLockWait: 20 * time.Second, DDLLockTimeout: 2 * time.Second, DDLLockRetries: 3},
	}); err != nil {
		tb.Fatalf("migrate: %v", err)
	}
	db, err := postgres.Open(ctx, cfg, "it-blob", "web", quiet)
	if err != nil {
		tb.Fatalf("open: %v", err)
	}
	tb.Cleanup(db.Close)
	p := &pgEnv{db: db, admin: connect(name), cfg: cfg}
	if _, err := p.admin.Exec(ctx, "SET search_path TO trove"); err != nil {
		tb.Fatal(err)
	}
	p.repo = p.scalarInt(tb, "INSERT INTO trove.repositories (key, format, class) VALUES ('generic-local', 'generic', 'local') RETURNING id")
	return p
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

// state returns state and content_status of a blob row, ok=false when none.
func (p *pgEnv) state(tb testing.TB, d blob.Digest) (state, status string, ok bool) {
	tb.Helper()
	err := p.admin.QueryRow(context.Background(), "SELECT state, content_status FROM trove.blobs WHERE sha256 = $1", d.Bytes()).Scan(&state, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false
	}
	if err != nil {
		tb.Fatal(err)
	}
	return state, status, true
}

// link returns the LinkFunc that adds a file item at path referencing the blob.
func (p *pgEnv) link(path string, sp *blob.Spooled) blob.LinkFunc {
	return func(ctx context.Context, tx meta.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO items (repo_id, path, parent_path, kind, blob_sha256, size)
			VALUES ($1, $2, '', 'file', $3, $4)`, p.repo, path, sp.SHA256.Bytes(), sp.Size)
		return err
	}
}

func dirEntries(tb testing.TB, dir string) []string {
	tb.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		tb.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}
