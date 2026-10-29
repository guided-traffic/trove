//go:build integration

package smoke

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

func connect(ctx context.Context, t *testing.T, dsn string, mode pgx.QueryExecMode) *pgx.Conn {
	t.Helper()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	cfg.DefaultQueryExecMode = mode
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func TestPostgresDirect(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn := connect(ctx, t, testenv.PostgresDSN(t), pgx.QueryExecModeCacheStatement)

	var raw string
	if err := conn.QueryRow(ctx, "SHOW server_version_num").Scan(&raw); err != nil {
		t.Fatalf("server_version_num: %v", err)
	}
	num, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("server_version_num %q: %v", raw, err)
	}
	if num < 160000 || num >= 170000 {
		t.Fatalf("server_version_num = %d, the integration tier runs PostgreSQL 16 (the floor version)", num)
	}

	var one int
	if err := conn.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("SELECT 1 = %d, %v", one, err)
	}

	// Session-scoped state works on a direct connection: this is what the lease
	// pool and the migration runner rely on.
	var locked bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(424242)").Scan(&locked); err != nil || !locked {
		t.Fatalf("session advisory lock: %v %v", locked, err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_unlock(424242)"); err != nil {
		t.Fatalf("unlock: %v", err)
	}
}

func TestPostgresThroughPgBouncer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dsn := testenv.PgBouncerDSN(t)

	// The query path of a transaction-mode pooler: a transaction on a pooled
	// connection.
	conn := connect(ctx, t, dsn, pgx.QueryExecModeCacheStatement)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	var one int
	if err := tx.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Fatalf("SELECT 1 = %d, %v", one, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// The pool mode is the one the stack promises. The admin console speaks the
	// simple protocol only.
	adminDSN := strings.Replace(dsn, "/trove?", "/pgbouncer?", 1)
	admin := connect(ctx, t, adminDSN, pgx.QueryExecModeSimpleProtocol)
	rows, err := admin.Query(ctx, "SHOW CONFIG")
	if err != nil {
		t.Fatalf("SHOW CONFIG: %v", err)
	}
	defer rows.Close()
	mode := ""
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			t.Fatalf("scan: %v", err)
		}
		if vals[0] == "pool_mode" {
			mode, _ = vals[1].(string)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if mode != "transaction" {
		t.Fatalf("PgBouncer pool_mode = %q, want transaction", mode)
	}
}

// TestPostgresIsTLSOnly proves that neither PostgreSQL nor PgBouncer takes a plaintext session
// (INV-20, R-HA-063): a client that refuses TLS is rejected by the server, not downgraded.
func TestPostgresIsTLSOnly(t *testing.T) {
	for name, dsn := range map[string]string{"postgres": testenv.PostgresDSN(t), "pgbouncer": testenv.PgBouncerDSN(t)} {
		t.Run("INV-20 "+name+" refuses a plaintext connection", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			u, err := url.Parse(dsn)
			if err != nil {
				t.Fatal(err)
			}
			u.RawQuery = "sslmode=disable"
			conn, err := pgx.Connect(ctx, u.String())
			if err == nil {
				_ = conn.Close(ctx)
				t.Fatalf("%s accepted a connection with sslmode=disable", name)
			}
		})
		t.Run("INV-20 "+name+" is not satisfied by a client that does not trust the CA", func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			u, err := url.Parse(dsn)
			if err != nil {
				t.Fatal(err)
			}
			u.RawQuery = "sslmode=verify-full"
			conn, err := pgx.Connect(ctx, u.String())
			if err == nil {
				_ = conn.Close(ctx)
				t.Fatalf("%s verified against the system roots", name)
			}
		})
	}
}
