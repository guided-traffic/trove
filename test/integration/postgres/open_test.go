//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

func open(t *testing.T, cfg config.Database, instance, role string) *postgres.DB {
	t.Helper()
	db, err := postgres.Open(ctxT(t, 30*time.Second), cfg, instance, role, quiet)
	if err != nil {
		t.Fatalf("Open(%s, %s): %v", instance, role, err)
	}
	t.Cleanup(db.Close)
	return db
}

// classesOf lists the trove application_name classes of one instance in the
// database, from the server's point of view.
func classesOf(t *testing.T, admin *pgx.Conn, instance string) map[string]int {
	t.Helper()
	rows, err := admin.Query(context.Background(),
		`SELECT application_name, count(*) FROM pg_stat_activity
		  WHERE datname = current_database() AND application_name LIKE $1 GROUP BY 1`, "trove:"+instance+":%")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			t.Fatal(err)
		}
		out[strings.TrimPrefix(name, "trove:"+instance+":")] = n
	}
	return out
}

// warm opens every connection a DB keeps minimally, so pg_stat_activity shows them.
func warm(t *testing.T, db *postgres.DB) {
	t.Helper()
	ctx := ctxT(t, 20*time.Second)
	if err := db.Main().Ping(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	if lp := db.LeasePool(); lp != nil {
		c, err := lp.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		c.Release()
	}
}

func TestOpenConnectionClasses(t *testing.T) {
	st := getStack(t)
	cfg, _, admin := migrated(t, st)

	t.Run("R-ARCH-019 connection names are trove:<instance>:<class> in pg_stat_activity", func(t *testing.T) {
		db := open(t, cfg, "pod-worker-1", meta.RoleWorker)
		warm(t, db)
		got := classesOf(t, admin, "pod-worker-1")
		if got["main"] < 2 || got["lease"] != 1 {
			t.Fatalf("classes = %v, want main (request pool and control connection) and one lease connection", got)
		}
		for c := range got {
			if c != "main" && c != "lease" {
				t.Errorf("unexpected class %q", c)
			}
		}
	})

	t.Run("R-ARCH-001 a web role Open has no lease pool and no lease connection", func(t *testing.T) {
		db := open(t, cfg, "pod-web-1", meta.RoleWeb)
		warm(t, db)
		if db.LeasePool() != nil {
			t.Fatal("LeasePool() must be nil for a web replica")
		}
		got := classesOf(t, admin, "pod-web-1")
		if _, hasLease := got["lease"]; hasLease || got["main"] < 2 {
			t.Fatalf("classes = %v", got)
		}
		if n := scalar[int](t, admin, `SELECT count(*) FROM pg_stat_activity WHERE application_name LIKE 'trove:pod-web-%:lease'`); n != 0 {
			t.Fatalf("lease connections of web replicas: %d", n)
		}
	})

	t.Run("R-ARCH-017 worker and all roles have a lease pool of lease_pool_size connections", func(t *testing.T) {
		for _, role := range []string{meta.RoleWorker, meta.RoleAll} {
			db := open(t, cfg, "pod-"+role, role)
			if db.LeasePool() == nil || db.LeasePool().Config().MaxConns != int32(cfg.LeasePoolSize) {
				t.Fatalf("role %s: lease pool %v", role, db.LeasePool())
			}
		}
	})

	t.Run("R-ARCH-019 the request pool has max_conns-1 connections, one is the control connection", func(t *testing.T) {
		db := open(t, cfg, "pod-budget", meta.RoleWeb)
		if got := db.Main().Config().MaxConns; got != int32(cfg.Pool.MaxConns-1) {
			t.Fatalf("request pool max = %d, want %d", got, cfg.Pool.MaxConns-1)
		}
		if got := db.ControlPool().Config().MaxConns; got != 1 {
			t.Fatalf("control pool max = %d", got)
		}
		// Exhaust the request pool: the control connection still answers.
		ctx := ctxT(t, 20*time.Second)
		var held []*pgxConn
		for i := 0; i < int(db.Main().Config().MaxConns); i++ {
			c, err := db.Main().Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			held = append(held, c)
		}
		defer func() {
			for _, c := range held {
				c.Release()
			}
		}()
		short, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if _, err := db.Main().Acquire(short); err == nil {
			t.Fatal("the request pool must be exhausted")
		}
		if err := db.Ready(ctx); err != nil {
			t.Fatalf("Ready with an exhausted request pool: %v", err)
		}
		c, err := db.Control(ctx)
		if err != nil {
			t.Fatal(err)
		}
		c.Release()
	})

	t.Run("R-ARCH-019 max_conns below 2 is refused naming the key", func(t *testing.T) {
		bad := cfg
		bad.Pool.MaxConns = 1
		_, err := postgres.Open(ctxT(t, 10*time.Second), bad, "pod-x", meta.RoleWeb, quiet)
		if err == nil || !strings.Contains(err.Error(), "database.pool.max_conns") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-HA-015 direct connections carry the session parameters and the server keepalive", func(t *testing.T) {
		db := open(t, cfg, "pod-params", meta.RoleWorker)
		ctx := ctxT(t, 10*time.Second)
		check := func(pool interface {
			QueryRow(context.Context, string, ...any) pgx.Row
		}, label string,
		) {
			t.Helper()
			for setting, want := range map[string]string{
				"statement_timeout": "30s", "lock_timeout": "5s", "idle_in_transaction_session_timeout": "1min",
				"search_path": "trove", "TimeZone": "UTC", "tcp_keepalives_idle": "60",
				"tcp_keepalives_interval": "10", "tcp_keepalives_count": "6",
			} {
				var got string
				if err := pool.QueryRow(ctx, "SELECT current_setting($1)", setting).Scan(&got); err != nil {
					t.Fatalf("%s %s: %v", label, setting, err)
				}
				if got != want {
					t.Errorf("%s: %s = %q, want %q", label, setting, got, want)
				}
			}
		}
		check(db.Main(), "main")
		check(db.ControlPool(), "control")
		check(db.LeasePool(), "lease")
	})

	t.Run("R-HA-013 Ready proves a primary and fails once the pools are closed", func(t *testing.T) {
		db, err := postgres.Open(ctxT(t, 10*time.Second), cfg, "pod-ready", meta.RoleWeb, quiet)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Ready(ctxT(t, 5*time.Second)); err != nil {
			t.Fatal(err)
		}
		db.Close()
		db.Close() // idempotent
		if err := db.Ready(ctxT(t, 5*time.Second)); err == nil {
			t.Fatal("Ready after Close must fail")
		}
	})

	t.Run("R-ARCH-026 the schema check applies the compatibility gate to the database", func(t *testing.T) {
		db := open(t, cfg, "pod-gate", meta.RoleWeb)
		if err := db.CheckSchema(ctxT(t, 5*time.Second), "0.1.0"); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(context.Background(), "UPDATE trove.schema_state SET compat_floor = '0.9.0'"); err != nil {
			t.Fatal(err)
		}
		var fe *postgres.FloorError
		if err := db.CheckSchema(ctxT(t, 5*time.Second), "0.1.0"); !errors.As(err, &fe) || err.Error() != "schema requires trove >= 0.9.0" {
			t.Fatalf("err = %v", err)
		}
		if _, err := admin.Exec(context.Background(), "UPDATE trove.schema_state SET compat_floor = '0.0.0', applied = applied - 1"); err != nil {
			t.Fatal(err)
		}
		if err := db.CheckSchema(ctxT(t, 5*time.Second), "0.1.0"); !errors.Is(err, postgres.ErrSchemaBehind) {
			t.Fatalf("err = %v", err)
		}
	})
}

type pgxConn = pgxpool.Conn

func TestOpenPreflight(t *testing.T) {
	st := getStack(t)
	name, _ := st.newDatabase(t)

	t.Run("R-HA-014 a superuser runtime role is refused naming database.preflight.allow_superuser", func(t *testing.T) {
		cfg := st.config(t, name)
		cfg.Preflight.AllowSuperuser = false
		_, err := postgres.Open(ctxT(t, 10*time.Second), cfg, "pod-su", meta.RoleWeb, quiet)
		if err == nil || !strings.Contains(err.Error(), "database.preflight.allow_superuser") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-HA-015 TLS is on by default: verify-full against a server without TLS fails", func(t *testing.T) {
		cfg := st.config(t, name)
		cfg.SSLMode, cfg.AllowInsecure = "verify-full", false
		cfg.Retry.MaxElapsed = 500 * time.Millisecond
		_, err := postgres.Open(ctxT(t, 20*time.Second), cfg, "pod-tls", meta.RoleWeb, quiet)
		if err == nil {
			t.Fatal("Open must not fall back to a plain connection")
		}
	})

	t.Run("R-HA-015 ssl_mode disable without allow_insecure is refused before any connection", func(t *testing.T) {
		cfg := st.config(t, name)
		cfg.AllowInsecure = false
		_, err := postgres.Open(ctxT(t, 10*time.Second), cfg, "pod-ins", meta.RoleWeb, quiet)
		if err == nil || !strings.Contains(err.Error(), "allow_insecure") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-HA-015 a wrong password in the file fails the connection", func(t *testing.T) {
		cfg := st.config(t, name)
		if err := writeFile(cfg.PasswordFile, "wrong\n"); err != nil {
			t.Fatal(err)
		}
		cfg.Retry.MaxElapsed = 500 * time.Millisecond
		if _, err := postgres.Open(ctxT(t, 20*time.Second), cfg, "pod-pw", meta.RoleWeb, quiet); err == nil {
			t.Fatal("Open with a wrong password must fail")
		}
	})

	t.Run("R-HA-017 a rotated password file applies to new connections without a restart", func(t *testing.T) {
		cfg := st.config(t, name)
		db := open(t, cfg, "pod-rot", meta.RoleWeb)
		warm(t, db)
		if err := writeFile(cfg.PasswordFile, "rotated-and-wrong\n"); err != nil {
			t.Fatal(err)
		}
		// Existing connections keep working; a new connection reads the file again.
		if err := db.Main().Ping(ctxT(t, 5*time.Second)); err != nil {
			t.Fatalf("an established connection must keep working: %v", err)
		}
		db.Main().Reset()
		if err := db.Main().Ping(ctxT(t, 5*time.Second)); err == nil {
			t.Fatal("a new connection must use the rotated (wrong) password")
		}
		if err := writeFile(cfg.PasswordFile, st.secret+"\n"); err != nil {
			t.Fatal(err)
		}
		if err := db.Main().Ping(ctxT(t, 10*time.Second)); err != nil {
			t.Fatalf("after restoring the password: %v", err)
		}
	})
}

// unwrapTx is the pgx transaction behind a meta.Tx, for tests that run SQL on it.
func unwrapTx(t testing.TB, tx meta.Tx) pgx.Tx {
	t.Helper()
	ptx, ok := postgres.Unwrap(tx)
	if !ok {
		t.Fatal("not a transaction of internal/meta/postgres")
	}
	return ptx
}

func TestInTx(t *testing.T) {
	st := getStack(t)
	cfg, _, admin := migrated(t, st)
	db := open(t, cfg, "pod-tx", meta.RoleWeb)
	ctx := ctxT(t, 60*time.Second)

	raise := func(ctx context.Context, tx pgx.Tx, code string) error {
		_, err := tx.Exec(ctx, fmt.Sprintf(`DO $$ BEGIN RAISE EXCEPTION 'injected' USING ERRCODE = '%s'; END $$`, code))
		return err
	}

	t.Run("R-ARCH-019 InTx retries a serialization failure and runs the transaction again from scratch", func(t *testing.T) {
		attempts := 0
		err := db.InPgxTx(ctx, func(tx pgx.Tx) error {
			attempts++
			if _, err := tx.Exec(ctx, `INSERT INTO trove.system_state (key, value) VALUES ('t_retry', to_jsonb($1::int))`, attempts); err != nil {
				return err
			}
			if attempts < 3 {
				return raise(ctx, tx, "40001")
			}
			return nil
		})
		if err != nil || attempts != 3 {
			t.Fatalf("attempts = %d, err = %v", attempts, err)
		}
		if got := scalar[int](t, admin, `SELECT (value #>> '{}')::int FROM trove.system_state WHERE key = 't_retry'`); got != 3 {
			t.Fatalf("committed value = %d: earlier attempts must have rolled back", got)
		}
	})

	t.Run("R-ARCH-019 InTx retries a deadlock error up to three times and returns the error", func(t *testing.T) {
		attempts := 0
		err := db.InPgxTx(ctx, func(tx pgx.Tx) error { attempts++; return raise(ctx, tx, "40P01") })
		var pe *pgconn.PgError
		if attempts != 4 || !asPg(err, &pe) || pe.Code != "40P01" {
			t.Fatalf("attempts = %d, err = %v", attempts, err)
		}
	})

	t.Run("R-ARCH-019 InTx does not retry other errors", func(t *testing.T) {
		attempts := 0
		err := db.InPgxTx(ctx, func(tx pgx.Tx) error {
			attempts++
			_, err := tx.Exec(ctx, `INSERT INTO trove.repositories (key, format, class) VALUES ('Bad Key', 'raw', 'local')`)
			return err
		})
		var pe *pgconn.PgError
		if attempts != 1 || !asPg(err, &pe) || pe.Code != "23514" {
			t.Fatalf("attempts = %d, err = %v", attempts, err)
		}
	})

	t.Run("R-ARCH-019 transactions run at READ COMMITTED", func(t *testing.T) {
		var level string
		if err := db.InPgxTx(ctx, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, "SHOW transaction_isolation").Scan(&level)
		}); err != nil || level != "read committed" {
			t.Fatalf("level = %q, err = %v", level, err)
		}
	})

	t.Run("R-ARCH-018 Before hooks run last inside the transaction, After hooks run once after the commit", func(t *testing.T) {
		var order []string
		err := db.InTx(ctx, func(tx meta.Tx) error {
			tx.After(func() { order = append(order, "after") })
			tx.Before(func(ctx context.Context, tx meta.Tx) error {
				order = append(order, "before")
				// The hook sees the transaction's own uncommitted write.
				var n int
				if err := unwrapTx(t, tx).QueryRow(ctx, `SELECT count(*) FROM trove.repositories WHERE key = 'hooked'`).Scan(&n); err != nil || n != 1 {
					return fmt.Errorf("before hook: n = %d: %w", n, err)
				}
				_, err := unwrapTx(t, tx).Exec(ctx, `UPDATE trove.cluster_epoch SET epoch = epoch + 1, changed_at = now() WHERE area = 'cache'`)
				return err
			})
			order = append(order, "body")
			_, err := unwrapTx(t, tx).Exec(ctx, `INSERT INTO trove.repositories (key, format, class) VALUES ('hooked', 'raw', 'local')`)
			return err
		})
		if err != nil || strings.Join(order, ",") != "body,before,after" {
			t.Fatalf("order = %v, err = %v", order, err)
		}
		if scalar[int64](t, admin, `SELECT epoch FROM trove.cluster_epoch WHERE area = 'cache'`) != 1 {
			t.Fatal("the Before hook's write must have committed with the transaction")
		}
	})

	t.Run("R-ARCH-018 a failing Before hook rolls back and After hooks do not run", func(t *testing.T) {
		after := false
		boom := errors.New("hook failed")
		err := db.InTx(ctx, func(tx meta.Tx) error {
			tx.After(func() { after = true })
			tx.Before(func(context.Context, meta.Tx) error { return boom })
			_, err := unwrapTx(t, tx).Exec(ctx, `INSERT INTO trove.repositories (key, format, class) VALUES ('rolled-back', 'raw', 'local')`)
			return err
		})
		if !errors.Is(err, boom) || after {
			t.Fatalf("err = %v, after = %v", err, after)
		}
		if scalar[int](t, admin, `SELECT count(*) FROM trove.repositories WHERE key = 'rolled-back'`) != 0 {
			t.Fatal("the body's write survived the failing hook")
		}
	})

	t.Run("R-ARCH-019 a retried Tx registers its hooks again and the failed attempt's After hooks never run", func(t *testing.T) {
		attempts, afters := 0, 0
		err := db.InTx(ctx, func(tx meta.Tx) error {
			attempts++
			tx.After(func() { afters++ })
			if attempts == 1 {
				_, err := unwrapTx(t, tx).Exec(ctx, `DO $$ BEGIN RAISE EXCEPTION 'x' USING ERRCODE = '40001'; END $$`)
				return err
			}
			return nil
		})
		if err != nil || attempts != 2 || afters != 1 {
			t.Fatalf("attempts %d afters %d err %v", attempts, afters, err)
		}
	})

	t.Run("R-ARCH-019 OnRollback runs after a failed attempt and a failed Before hook, never after a commit, and the slot starts empty each attempt", func(t *testing.T) {
		type key struct{}
		var rolled, committed, seenSlot int
		attempts := 0
		err := db.InTx(ctx, func(tx meta.Tx) error {
			attempts++
			if tx.Value(key{}) != nil {
				seenSlot++
			}
			tx.SetValue(key{}, attempts)
			tx.OnRollback(func() { rolled++ })
			tx.After(func() { committed++ })
			if attempts == 1 {
				_, err := unwrapTx(t, tx).Exec(ctx, `DO $$ BEGIN RAISE EXCEPTION 'x' USING ERRCODE = '40001'; END $$`)
				return err
			}
			return nil
		})
		if err != nil || attempts != 2 || rolled != 1 || committed != 1 || seenSlot != 0 {
			t.Fatalf("attempts %d rolled %d committed %d slot-leaks %d err %v", attempts, rolled, committed, seenSlot, err)
		}

		rolled = 0
		boom := errors.New("hook failed")
		err = db.InTx(ctx, func(tx meta.Tx) error {
			tx.OnRollback(func() { rolled++ })
			tx.Before(func(context.Context, meta.Tx) error { return boom })
			return nil
		})
		if !errors.Is(err, boom) || rolled != 1 {
			t.Fatalf("failed Before hook: rolled %d err %v", rolled, err)
		}

		rolled = 0
		err = db.InTx(ctx, func(tx meta.Tx) error {
			tx.OnRollback(func() { rolled++ })
			return boom
		})
		if !errors.Is(err, boom) || rolled != 1 {
			t.Fatalf("failed body: rolled %d err %v", rolled, err)
		}

		rolled = 0
		if err := db.InTx(ctx, func(tx meta.Tx) error { tx.OnRollback(func() { rolled++ }); return nil }); err != nil || rolled != 0 {
			t.Fatalf("committed: rolled %d err %v", rolled, err)
		}
	})

	t.Run("R-ARCH-008 a meta.Tx offers no way to run SQL: only hooks and the value slot", func(t *testing.T) {
		var names []string
		typ := reflect.TypeFor[meta.Tx]()
		for i := range typ.NumMethod() {
			names = append(names, typ.Method(i).Name)
		}
		if got, want := strings.Join(names, ","), "After,Before,OnRollback,SetValue,Value"; got != want {
			t.Fatalf("meta.Tx methods = %s, want %s", got, want)
		}
		_ = db.InTx(ctx, func(tx meta.Tx) error {
			for _, forbidden := range []string{"Exec", "Query", "QueryRow", "Begin", "Commit", "Rollback"} {
				if _, ok := reflect.TypeOf(tx).MethodByName(forbidden); ok {
					t.Errorf("the transaction handed to callers has %s", forbidden)
				}
			}
			return nil
		})
	})

	t.Run("R-ARCH-019 the control connection is not needed by a request path: SchemaState reads on the main pool", func(t *testing.T) {
		held, err := db.Control(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer held.Release()
		sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		st, err := db.SchemaState(sctx)
		if err != nil || st.Applied != postgres.RequiredSchema() {
			t.Fatalf("SchemaState with the control connection held: %+v, %v", st, err)
		}
	})

	t.Run("R-ARCH-019 RequestTx sets the request-path statement timeout; a query past it is unavailable", func(t *testing.T) {
		err := db.RequestTx(ctx, 300*time.Millisecond, func(tx meta.Tx) error {
			_, err := unwrapTx(t, tx).Exec(ctx, "SELECT pg_sleep(5)")
			return err
		})
		if err == nil || !postgres.IsUnavailable(err) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-ARCH-019 a cancelled context returns its error and releases the connection", func(t *testing.T) {
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		if err := db.InPgxTx(cctx, func(pgx.Tx) error { return nil }); err == nil {
			t.Fatal("expected an error")
		}
		if db.Stats().Main.Acquired != 0 {
			t.Fatalf("stats = %+v", db.Stats().Main)
		}
	})
}

func TestFailoverRetry(t *testing.T) {
	st := getStack(t)
	cfg, _, admin := migrated(t, st)
	cfg.Retry.MaxElapsed = 10 * time.Second
	db := open(t, cfg, "pod-failover", meta.RoleWeb)
	warm(t, db)
	ctx := ctxT(t, 60*time.Second)

	t.Run("R-HA-015 a transaction whose connection is terminated mid-statement runs again on a new connection", func(t *testing.T) {
		var attempts atomic.Int32
		err := db.InPgxTx(ctx, func(tx pgx.Tx) error {
			n := attempts.Add(1)
			if n == 1 {
				var pid int32
				if err := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
					return err
				}
				// The server drops the connection under the transaction, as a failover does.
				scalar[bool](t, admin, "SELECT pg_terminate_backend($1)", pid)
			}
			var one int
			return tx.QueryRow(ctx, "SELECT 1").Scan(&one)
		})
		if err != nil || attempts.Load() != 2 {
			t.Fatalf("attempts = %d, err = %v", attempts.Load(), err)
		}
	})

	t.Run("R-HA-015 retries are bounded by retry.max_elapsed and end as an unavailable database", func(t *testing.T) {
		bad := cfg
		bad.Retry.MaxElapsed = 1500 * time.Millisecond
		d2 := open(t, bad, "pod-bounded", meta.RoleWeb)
		warm(t, d2)
		// Every attempt is cut again, so the retry must end by itself.
		count := 0
		start := time.Now()
		err := d2.InPgxTx(ctx, func(tx pgx.Tx) error {
			count++
			var pid int32
			if err := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
				return err
			}
			scalar[bool](t, admin, "SELECT pg_terminate_backend($1)", pid)
			var one int
			return tx.QueryRow(ctx, "SELECT 1").Scan(&one)
		})
		took := time.Since(start)
		if err == nil || !postgres.IsUnavailable(err) || count < 2 || took > 6*time.Second {
			t.Fatalf("count = %d, took %s, err = %v", count, took, err)
		}
	})
}

// loadPooler keeps many clients busy on PgBouncer so that sequential statements
// of one client land on different server connections.
func loadPooler(t *testing.T, st stack, database string) (stop func()) {
	t.Helper()
	dsn := fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=disable&default_query_exec_mode=simple_protocol",
		st.user, st.secret, st.host, st.bouncerPort, database)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := pgx.Connect(ctx, dsn)
			if err != nil {
				return
			}
			defer func() { _ = conn.Close(context.Background()) }()
			for ctx.Err() == nil {
				_, _ = conn.Exec(ctx, "SELECT pg_sleep(0.002)")
			}
		}()
	}
	time.Sleep(300 * time.Millisecond)
	return func() { cancel(); wg.Wait() }
}

func TestSessionGuard(t *testing.T) {
	st := getStack(t)
	cfg, name, admin := migrated(t, st)

	t.Run("R-ARCH-017 a web replica pointed at the same pooler starts and becomes ready", func(t *testing.T) {
		for _, stmt := range []string{"statement_timeout = '30s'", "lock_timeout = '5s'", "idle_in_transaction_session_timeout = '60s'", "search_path = trove"} {
			if _, err := admin.Exec(context.Background(), fmt.Sprintf("ALTER ROLE %s IN DATABASE %s SET %s", st.user, name, stmt)); err != nil {
				t.Fatal(err)
			}
		}
		db := open(t, st.pooled(cfg), "pod-guard-web", meta.RoleWeb)
		if err := db.Ready(ctxT(t, 10*time.Second)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-ARCH-017 a worker whose session host is a transaction-mode pooler exits with the session-state message", func(t *testing.T) {
		bad := st.pooled(cfg)
		bad.SessionHost, bad.SessionPort = st.host, st.bouncerPort // the session host is the pooler
		stop := loadPooler(t, st, name)
		defer stop()
		_, err := postgres.Open(ctxT(t, 30*time.Second), bad, "pod-guard", meta.RoleWorker, quiet)
		if !errors.Is(err, postgres.ErrSessionState) ||
			err.Error() != "session connection must keep session state; set database.session_host to a direct or session-mode endpoint" {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestTransactionPooler(t *testing.T) {
	st := getStack(t)
	cfg, name, admin := migrated(t, st)
	pooled := st.pooled(cfg)

	for _, stmt := range []string{
		"statement_timeout = '30s'", "lock_timeout = '5s'", "idle_in_transaction_session_timeout = '60s'", "search_path = trove",
	} {
		if _, err := admin.Exec(context.Background(), fmt.Sprintf("ALTER ROLE %s IN DATABASE %s SET %s", st.user, name, stmt)); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("R-HA-058 behind the pooler the login's role defaults are verified at start-up", func(t *testing.T) {
		// A database of its own: the pooler keeps server connections per database.
		bareName, _ := st.newDatabase(t)
		bare := st.pooled(st.config(t, bareName))
		_, err := postgres.Open(ctxT(t, 20*time.Second), bare, "pod-nodefaults", meta.RoleWeb, quiet)
		if err == nil || !strings.Contains(err.Error(), "role defaults") || !strings.Contains(err.Error(), "statement_timeout") ||
			!strings.Contains(err.Error(), st.user) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-ARCH-019 the main pool works through PgBouncer in transaction mode", func(t *testing.T) {
		db := open(t, pooled, "pod-pooled", meta.RoleWeb)
		if mode := db.Main().Config().ConnConfig.DefaultQueryExecMode; mode != pgx.QueryExecModeExec {
			t.Fatalf("exec mode = %v, want the pooler-safe exec mode", mode)
		}
		if err := db.Ready(ctxT(t, 10*time.Second)); err != nil {
			t.Fatal(err)
		}
		// The role defaults reach pooled connections.
		var path, name string
		if err := db.InPgxTx(ctxT(t, 10*time.Second), func(tx pgx.Tx) error {
			if err := tx.QueryRow(context.Background(), "SELECT current_setting('search_path')").Scan(&path); err != nil {
				return err
			}
			return tx.QueryRow(context.Background(), "SELECT current_setting('application_name')").Scan(&name)
		}); err != nil || path != "trove" || name != "trove:pod-pooled:main" {
			t.Fatalf("search_path %q, application_name %q, err %v", path, name, err)
		}
	})

	t.Run("R-ARCH-019 a mixed workload of many statement shapes keeps working through the pooler", func(t *testing.T) {
		db := open(t, pooled, "pod-mixed", meta.RoleWeb)
		var wg sync.WaitGroup
		var failures atomic.Int32
		var firstErr atomic.Value
		fail := func(err error) {
			failures.Add(1)
			firstErr.CompareAndSwap(nil, err.Error())
		}
		// More distinct statements than max_prepared_statements (100) of the pooler.
		for g := 0; g < 16; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 0; i < 40; i++ {
					err := db.InPgxTx(ctxT(t, 30*time.Second), func(tx pgx.Tx) error {
						ctx := context.Background()
						if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '10s'"); err != nil {
							return err
						}
						if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(4, $1::int)", int32(g)); err != nil {
							return err
						}
						var n int
						sql := fmt.Sprintf("SELECT %d + $1::int /* shape %d-%d */", g, g, i)
						if err := tx.QueryRow(ctx, sql, i).Scan(&n); err != nil {
							return err
						}
						if n != g+i {
							return fmt.Errorf("got %d, want %d", n, g+i)
						}
						_, err := tx.Exec(ctx, `INSERT INTO trove.system_state (key, value) VALUES ($1, to_jsonb($2::int))
							ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, "t_mixed_"+strconv.Itoa(g), i)
						return err
					})
					if err != nil {
						fail(err)
						return
					}
				}
			}(g)
		}
		wg.Wait()
		if failures.Load() != 0 {
			t.Fatalf("%d goroutines failed, first: %v", failures.Load(), firstErr.Load())
		}
		// SET LOCAL did not leak into the next transaction on the shared server connection.
		var timeout string
		if err := db.InPgxTx(ctxT(t, 10*time.Second), func(tx pgx.Tx) error {
			return tx.QueryRow(context.Background(), "SHOW statement_timeout").Scan(&timeout)
		}); err != nil || timeout != "30s" {
			t.Fatalf("statement_timeout = %q, err = %v", timeout, err)
		}
	})

	t.Run("R-ARCH-019 the pooler's server connections carry the application_name of the replica", func(t *testing.T) {
		db := open(t, pooled, "pod-named", meta.RoleWorker)
		warm(t, db)
		got := classesOf(t, admin, "pod-named")
		if got["main"] == 0 || got["lease"] != 1 {
			t.Fatalf("classes = %v", got)
		}
		// The lease connection bypasses the pooler: the session host is PostgreSQL itself.
		var port int
		if err := admin.QueryRow(context.Background(),
			`SELECT count(*) FROM pg_stat_activity WHERE application_name = 'trove:pod-named:lease' AND client_port > 0`).Scan(&port); err != nil || port != 1 {
			t.Fatalf("lease connections: %d, %v", port, err)
		}
	})

	t.Run("R-HA-058 a worker behind the pooler holds session locks on the session host", func(t *testing.T) {
		db := open(t, pooled, "pod-lock", meta.RoleWorker)
		ctx := ctxT(t, 10*time.Second)
		c1, err := db.LeasePool().Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer c1.Release()
		c2, err := db.LeasePool().Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer c2.Release()
		var got1, got2 bool
		if err := c1.QueryRow(ctx, "SELECT pg_try_advisory_lock(1, 1)").Scan(&got1); err != nil || !got1 {
			t.Fatalf("first lock: %v %v", got1, err)
		}
		if err := c2.QueryRow(ctx, "SELECT pg_try_advisory_lock(1, 1)").Scan(&got2); err != nil || got2 {
			t.Fatalf("second session must not get the lease: %v %v", got2, err)
		}
		var held bool
		if err := c1.QueryRow(ctx, "SELECT pg_advisory_unlock(1, 1)").Scan(&held); err != nil || !held {
			t.Fatalf("unlock: %v %v", held, err)
		}
	})

	t.Run("R-HA-058 the control connection uses the control endpoint", func(t *testing.T) {
		split := pooled
		split.ControlHost, split.ControlPort = st.host, st.port // a pool of its own: PostgreSQL directly
		db := open(t, split, "pod-control", meta.RoleWeb)
		ctx := ctxT(t, 10*time.Second)
		if err := db.Ready(ctx); err != nil {
			t.Fatal(err)
		}
		var port int
		if err := db.ControlPool().QueryRow(ctx, "SELECT inet_server_port()").Scan(&port); err != nil || port == 0 {
			t.Fatalf("control port %d, err %v", port, err)
		}
		// Saturate the request pool through the pooler: control stays responsive.
		var held []*pgxConn
		for i := 0; i < int(db.Main().Config().MaxConns); i++ {
			c, err := db.Main().Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			held = append(held, c)
		}
		defer func() {
			for _, c := range held {
				c.Release()
			}
		}()
		if err := db.Ready(ctx); err != nil {
			t.Fatalf("Ready with an exhausted request pool: %v", err)
		}
	})
}

func TestStatsAndNames(t *testing.T) {
	st := getStack(t)
	cfg, _, _ := migrated(t, st)
	db := open(t, cfg, "pod-stats", meta.RoleAll)
	warm(t, db)
	s := db.Stats()
	if s.Main.Max != int32(cfg.Pool.MaxConns-1) || s.Control.Max != 1 || s.Lease.Max != int32(cfg.LeasePoolSize) {
		t.Fatalf("stats = %+v", s)
	}
	classes := []string{}
	for c := range classesOf(t, getStackAdmin(t, st, cfg), "pod-stats") {
		classes = append(classes, c)
	}
	sort.Strings(classes)
	if strings.Join(classes, ",") != "lease,main" {
		t.Fatalf("classes = %v", classes)
	}
}
