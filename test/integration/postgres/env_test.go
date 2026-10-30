//go:build integration

// Package postgres tests the PostgreSQL layer against the integration stack:
// PostgreSQL 18 direct and through PgBouncer in transaction mode.
package postgres

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

const schemaName = "trove"

// stack describes the endpoints of the integration stack.
type stack struct {
	host         string
	port         int
	bouncerPort  int
	user, secret string
}

func getStack(t testing.TB) stack {
	t.Helper()
	port, err := strconv.Atoi(testenv.Get(t, "TROVE_TEST_PG_PORT"))
	if err != nil {
		t.Fatalf("TROVE_TEST_PG_PORT: %v", err)
	}
	bport, err := strconv.Atoi(testenv.Get(t, "TROVE_TEST_PG_PGBOUNCER_PORT"))
	if err != nil {
		t.Fatalf("TROVE_TEST_PG_PGBOUNCER_PORT: %v", err)
	}
	return stack{
		host: testenv.Get(t, "TROVE_TEST_PG_HOST"), port: port, bouncerPort: bport,
		user: testenv.Get(t, "TROVE_TEST_PG_USER"), secret: testenv.Get(t, "TROVE_TEST_PG_PASSWORD"),
	}
}

// admin connects to the maintenance database as the stack's superuser.
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

// newDatabase creates an empty database of its own (trove_pg_<random>) and drops
// it when the test ends, so parallel tests and agents never collide. The
// returned connection is an admin connection to that database.
func (s stack) newDatabase(t testing.TB) (name string, conn *pgx.Conn) {
	t.Helper()
	var b [5]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	name = "trove_pg_" + hex.EncodeToString(b[:])
	maint := s.admin(t, "postgres")
	ctx := context.Background()
	if _, err := maint.Exec(ctx, "CREATE DATABASE "+name+" ENCODING 'UTF8' TEMPLATE template0"); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = maint.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	return name, s.admin(t, name)
}

// credentials writes the username and password files of the configuration.
func (s stack) credentials(t testing.TB) (userFile, passFile string) {
	t.Helper()
	dir := t.TempDir()
	userFile, passFile = filepath.Join(dir, "username"), filepath.Join(dir, "password")
	if err := os.WriteFile(userFile, []byte(s.user+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(passFile, []byte(s.secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return userFile, passFile
}

// config is a direct configuration for the database over verify-full TLS against
// the stack's CA. The stack's role is a superuser, which is allowed explicitly.
func (s stack) config(t testing.TB, database string) config.Database {
	t.Helper()
	user, pass := s.credentials(t)
	return config.Database{
		Host: s.host, Port: s.port, Name: database, Schema: schemaName,
		UsernameFile: user, PasswordFile: pass,
		SSLMode: "verify-full", SSLRootCertFile: testenv.CAFile(t),
		ConnectTimeout: 5 * time.Second, TargetSessionAttrs: "read-write",
		Pooler:        config.DatabasePooler{Mode: "none"},
		Pool:          config.DatabasePool{MaxConns: 6, MinConns: 1, MaxConnLifetime: 30 * time.Minute, MaxConnLifetimeJitter: time.Minute, MaxConnIdleTime: 5 * time.Minute, HealthCheckPeriod: 30 * time.Second},
		LeasePoolSize: 2, LeaseCheckInterval: 5 * time.Second,
		Session: config.DatabaseSession{
			StatementTimeout: 30 * time.Second, LockTimeout: 5 * time.Second,
			IdleInTransactionSessionTimeout: 60 * time.Second,
		},
		Retry:           config.DatabaseRetry{InitialBackoff: 50 * time.Millisecond, MaxBackoff: 200 * time.Millisecond, MaxElapsed: 2 * time.Second},
		Keepalive:       config.DatabaseKeepalive{Idle: 15 * time.Second, Interval: 5 * time.Second, Count: 3, UserTimeout: 30 * time.Second},
		ServerKeepalive: config.DatabaseServerKeepalive{Idle: 60 * time.Second, Interval: 10 * time.Second, Count: 6},
		Preflight:       config.DatabasePreflight{ConnectionBudget: "enforce", ReservedConnections: 10, AllowSuperuser: true},
	}
}

// pooled turns cfg into a configuration whose main pool goes through PgBouncer
// in transaction mode and whose session class goes straight to PostgreSQL.
func (s stack) pooled(cfg config.Database) config.Database {
	cfg.Port = s.bouncerPort
	cfg.Pooler = config.DatabasePooler{Mode: "transaction", ServerConnections: 20}
	cfg.SessionHost, cfg.SessionPort = s.host, s.port
	return cfg
}

var (
	quiet = slog.New(slog.NewTextHandler(io.Discard, nil))
	// testVersion is the release the tests run as. Real releases are 0.x; the
	// contract-guard tests compare against explicit versions instead.
	testVersion = "0.1.0"
)

func ctxT(t testing.TB, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func opts(extra fs.FS) postgres.MigrateOptions {
	return postgres.MigrateOptions{
		Instance: "it-migrate", Version: testVersion, Steps: extra, Log: quiet,
		Migrate: config.Migrate{AdvisoryLockWait: 20 * time.Second, DDLLockTimeout: 2 * time.Second, DDLLockRetries: 3},
	}
}

// stepsWith returns the embedded migrations plus the given extra files, for
// tests that inject a failing, additional or edited step. A file with an
// embedded name replaces it.
func stepsWith(t testing.TB, extra map[string]string) fs.FS {
	t.Helper()
	out := fstest.MapFS{}
	emb := postgres.EmbeddedFS()
	entries, err := fs.ReadDir(emb, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		data, err := fs.ReadFile(emb, e.Name())
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = &fstest.MapFile{Data: data}
	}
	for name, content := range extra {
		out[name] = &fstest.MapFile{Data: []byte(content)}
	}
	return out
}

// nextStep names the file of the n-th test migration after the embedded ones
// (n = 1 is the first number no embedded migration uses), so that the tests that
// add a step of their own need no edit when a milestone embeds more migrations.
func nextStep(n int, name string) string {
	return fmt.Sprintf("%05d_%s.sql", postgres.RequiredSchema()+n, name)
}

// migrated returns a database with the embedded migrations applied.
func migrated(t testing.TB, st stack) (cfg config.Database, name string, admin *pgx.Conn) {
	t.Helper()
	name, admin = st.newDatabase(t)
	cfg = st.config(t, name)
	if _, err := postgres.Migrate(ctxT(t, 60*time.Second), cfg, opts(nil)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return cfg, name, admin
}

func scalar[T any](t testing.TB, c *pgx.Conn, sql string, args ...any) T {
	t.Helper()
	var v T
	if err := c.QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", strings.Join(strings.Fields(sql), " "), err)
	}
	return v
}

func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o600) }

// getStackAdmin connects as the stack's superuser to the database of cfg.
func getStackAdmin(t testing.TB, st stack, cfg config.Database) *pgx.Conn {
	return st.admin(t, cfg.Name)
}
