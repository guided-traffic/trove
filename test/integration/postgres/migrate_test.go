//go:build integration

package postgres

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/meta/postgres"
)

func TestMigrateEmptyDatabase(t *testing.T) {
	st := getStack(t)

	t.Run("R-ARCH-026 on an empty PostgreSQL 16 the runner creates the schema", func(t *testing.T) {
		name, admin := st.newDatabase(t)
		cfg := st.config(t, name)
		if v := scalar[int](t, admin, "SELECT current_setting('server_version_num')::int"); v < 160000 || v >= 170000 {
			t.Fatalf("the integration tier runs the floor version 16, server_version_num = %d", v)
		}

		res, err := postgres.Migrate(ctxT(t, 60*time.Second), cfg, opts(nil))
		if err != nil {
			t.Fatal(err)
		}
		embedded, _ := postgres.EmbeddedSteps()
		if len(res.Applied) != len(embedded) || res.NothingToDo || res.SchemaApplied != postgres.RequiredSchema() {
			t.Fatalf("result = %+v", res)
		}
		if res.CompatFloor != "0.0.0" {
			t.Fatalf("compat floor = %q", res.CompatFloor)
		}
		if n := scalar[int](t, admin, "SELECT count(*) FROM trove.schema_migration"); n != len(embedded) {
			t.Fatalf("schema_migration rows = %d", n)
		}
		if got := scalar[string](t, admin, "SELECT release FROM trove.schema_migration WHERE version = 1"); got != testVersion {
			t.Fatalf("release = %q", got)
		}
		if a := scalar[int](t, admin, "SELECT applied FROM trove.schema_state"); a != postgres.RequiredSchema() {
			t.Fatalf("schema_state.applied = %d", a)
		}
		if scalar[bool](t, admin, "SELECT transactional IS NOT TRUE FROM trove.schema_migration WHERE version = 1") {
			t.Fatal("step 1 must be recorded as transactional")
		}
	})

	t.Run("R-HA-019 a second run finds nothing to do", func(t *testing.T) {
		cfg, _, _ := migrated(t, st)
		res, err := postgres.Migrate(ctxT(t, 30*time.Second), cfg, opts(nil))
		if err != nil || !res.NothingToDo || len(res.Applied) != 0 || res.WaitedForLock {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
	})

	t.Run("R-BAK-004 system_state holds installation_id, keyspace_id and incarnation after the first migration", func(t *testing.T) {
		_, _, admin := migrated(t, st)
		rows, err := admin.Query(context.Background(), "SELECT key, value #>> '{}' FROM trove.system_state ORDER BY key")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		got := map[string]string{}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				t.Fatal(err)
			}
			got[k] = v
		}
		if len(got) != 3 {
			t.Fatalf("system_state = %v", got)
		}
		seen := map[string]bool{}
		for _, k := range []string{"installation_id", "keyspace_id", "incarnation"} {
			v, ok := got[k]
			if !ok || len(v) != 36 || strings.Count(v, "-") != 4 {
				t.Errorf("%s = %q, want a UUID", k, v)
			}
			if seen[v] {
				t.Errorf("%s repeats another key's UUID", k)
			}
			seen[v] = true
		}
	})

	t.Run("R-ARCH-026 status lists applied and pending steps without the lock", func(t *testing.T) {
		name, _ := st.newDatabase(t)
		cfg := st.config(t, name)
		rep, err := postgres.Status(ctxT(t, 30*time.Second), cfg, opts(nil))
		if err != nil {
			t.Fatal(err)
		}
		embedded, _ := postgres.EmbeddedSteps()
		if len(rep.Pending) != len(embedded) || len(rep.Applied) != 0 || rep.Servable {
			t.Fatalf("status of an empty database = %+v", rep)
		}
		if _, err := postgres.Migrate(ctxT(t, 60*time.Second), cfg, opts(nil)); err != nil {
			t.Fatal(err)
		}
		rep, err = postgres.Status(ctxT(t, 30*time.Second), cfg, opts(nil))
		if err != nil {
			t.Fatal(err)
		}
		if len(rep.Pending) != 0 || len(rep.Applied) != len(embedded) || !rep.Servable || rep.Schema.Applied != postgres.RequiredSchema() {
			t.Fatalf("status after migrate = %+v", rep)
		}
	})
}

// slowFirstStep delays step 1 so a second runner provably meets the lock.
func slowFirstStep(t testing.TB) map[string]string {
	t.Helper()
	steps, err := postgres.EmbeddedSteps()
	if err != nil {
		t.Fatal(err)
	}
	first := steps[0]
	sql := strings.Replace(first.SQL, "-- trove:phase expand\n", "-- trove:phase expand\nSELECT pg_sleep(1.5);\n", 1)
	return map[string]string{first.Name + ".sql": sql}
}

func TestMigrateConcurrentRunners(t *testing.T) {
	st := getStack(t)
	t.Run("R-HA-019 two runners at once: one applies, the other waits on the lock and finds nothing to do", func(t *testing.T) {
		name, admin := st.newDatabase(t)
		cfg := st.config(t, name)
		slow := stepsWith(t, slowFirstStep(t))

		type outcome struct {
			res postgres.Result
			err error
		}
		first := make(chan outcome, 1)
		go func() {
			res, err := postgres.Migrate(context.Background(), cfg, opts(slow))
			first <- outcome{res, err}
		}()

		// The second runner starts once the first provably holds the lock.
		deadline := time.Now().Add(20 * time.Second)
		for scalar[int](t, admin, `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory'
			AND classid = 3 AND objid = 0 AND granted AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`) == 0 {
			if time.Now().After(deadline) {
				t.Fatal("the first runner never took the migration lock")
			}
			time.Sleep(50 * time.Millisecond)
		}
		res2, err2 := postgres.Migrate(ctxT(t, 60*time.Second), cfg, opts(slow))
		o1 := <-first

		if o1.err != nil || err2 != nil {
			t.Fatalf("errors: %v / %v", o1.err, err2)
		}
		if len(o1.res.Applied) == 0 || o1.res.WaitedForLock {
			t.Fatalf("first runner: %+v", o1.res)
		}
		if !res2.WaitedForLock || !res2.NothingToDo || len(res2.Applied) != 0 {
			t.Fatalf("second runner must wait on the lock and apply nothing: %+v", res2)
		}
		embedded, _ := postgres.EmbeddedSteps()
		if n := scalar[int](t, admin, "SELECT count(*) FROM trove.schema_migration"); n != len(embedded) {
			t.Fatalf("schema_migration rows = %d", n)
		}
	})

	t.Run("R-HA-019 the lock wait is bounded by migrate.advisory_lock_wait and names the holder", func(t *testing.T) {
		name, admin := st.newDatabase(t)
		cfg := st.config(t, name)
		if _, err := postgres.Migrate(ctxT(t, 60*time.Second), cfg, opts(nil)); err != nil {
			t.Fatal(err)
		}
		// A backup's database phase holds the lock shared (R-ARCH-017).
		holder, err := pgx.Connect(context.Background(), admin.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Close(context.Background()) }()
		if _, err := holder.Exec(context.Background(), "SET application_name = 'trove:backup-pod:backup'"); err != nil {
			t.Fatal(err)
		}
		if _, err := holder.Exec(context.Background(), "SELECT pg_advisory_lock_shared(3, 0)"); err != nil {
			t.Fatal(err)
		}
		o := opts(nil)
		o.Migrate.AdvisoryLockWait = 1 * time.Second
		_, err = postgres.Migrate(ctxT(t, 30*time.Second), cfg, o)
		if err == nil || !strings.Contains(err.Error(), "trove:backup-pod:backup") || !strings.Contains(err.Error(), "migrate.advisory_lock_wait") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestMigrateFailure(t *testing.T) {
	st := getStack(t)
	t.Run("R-HA-019 a migration that fails on purpose rolls back and leaves the schema at the previous step", func(t *testing.T) {
		cfg, _, admin := migrated(t, st)
		before := scalar[int](t, admin, "SELECT applied FROM trove.schema_state")
		bad := stepsWith(t, map[string]string{
			"00006_fails_on_purpose.sql": "-- trove:phase expand\nCREATE TABLE trove.left_behind (id int);\nSELECT 1 / 0;\n",
		})
		res, err := postgres.Migrate(ctxT(t, 30*time.Second), cfg, opts(bad))
		if err == nil || !strings.Contains(err.Error(), "00006_fails_on_purpose") || !strings.Contains(err.Error(), "division by zero") {
			t.Fatalf("err = %v", err)
		}
		var ref *postgres.Refusal
		if errors.As(err, &ref) {
			t.Fatal("a failing step is an error (status 1), not a refusal (status 3)")
		}
		if len(res.Applied) != 0 {
			t.Fatalf("result = %+v", res)
		}
		if scalar[bool](t, admin, "SELECT to_regclass('trove.left_behind') IS NOT NULL") {
			t.Fatal("the failed step's table survived")
		}
		if n := scalar[int](t, admin, "SELECT count(*) FROM trove.schema_migration WHERE version = 6"); n != 0 {
			t.Fatal("the failed step left a schema_migration row")
		}
		if after := scalar[int](t, admin, "SELECT applied FROM trove.schema_state"); after != before {
			t.Fatalf("schema_state.applied moved from %d to %d", before, after)
		}
		// The migration lock is released: the next run proceeds.
		if res, err := postgres.Migrate(ctxT(t, 30*time.Second), cfg, opts(nil)); err != nil || !res.NothingToDo {
			t.Fatalf("after the failure: %+v, %v", res, err)
		}
	})

	t.Run("R-ARCH-026 a session killed inside a transactional step leaves neither its objects nor its row", func(t *testing.T) {
		name, admin := st.newDatabase(t)
		cfg := st.config(t, name)
		if _, err := postgres.Migrate(ctxT(t, 60*time.Second), cfg, opts(nil)); err != nil {
			t.Fatal(err)
		}
		slow := stepsWith(t, map[string]string{
			"00006_slow.sql": "-- trove:phase expand\nCREATE TABLE trove.killed_step (id int);\nSELECT pg_sleep(30);\n",
		})
		errc := make(chan error, 1)
		go func() {
			_, err := postgres.Migrate(context.Background(), cfg, opts(slow))
			errc <- err
		}()
		deadline := time.Now().Add(20 * time.Second)
		for {
			n := scalar[int](t, admin, "SELECT count(*) FROM pg_stat_activity WHERE application_name = 'trove:it-migrate:migrate' AND query LIKE '%pg_sleep(30)%' AND state = 'active'")
			if n > 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the slow step never started")
			}
			time.Sleep(50 * time.Millisecond)
		}
		scalar[bool](t, admin, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = 'trove:it-migrate:migrate' AND query LIKE '%pg_sleep(30)%'")
		if err := <-errc; err == nil {
			t.Fatal("a killed runner must report an error")
		}
		if scalar[bool](t, admin, "SELECT to_regclass('trove.killed_step') IS NOT NULL") ||
			scalar[int](t, admin, "SELECT count(*) FROM trove.schema_migration WHERE version = 6") != 0 {
			t.Fatal("the killed step left traces")
		}
		// The next run applies the step.
		fixed := stepsWith(t, map[string]string{
			"00006_slow.sql": "-- trove:phase expand\nCREATE TABLE trove.killed_step (id int);\n",
		})
		res, err := postgres.Migrate(ctxT(t, 30*time.Second), cfg, opts(fixed))
		if err != nil || len(res.Applied) != 1 {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
	})

	t.Run("R-ARCH-026 an edited applied migration stops the run, names the file and applies nothing", func(t *testing.T) {
		cfg, _, admin := migrated(t, st)
		steps, _ := postgres.EmbeddedSteps()
		edited := stepsWith(t, map[string]string{
			steps[1].Name + ".sql": steps[1].SQL + "\n-- edited after it was applied\n",
			"00006_new.sql":        "-- trove:phase expand\nCREATE TABLE trove.would_apply (id int);\n",
		})
		_, err := postgres.Migrate(ctxT(t, 30*time.Second), cfg, opts(edited))
		if err == nil || !strings.Contains(err.Error(), steps[1].Name+".sql") || !strings.Contains(err.Error(), "checksum") {
			t.Fatalf("err = %v", err)
		}
		if scalar[bool](t, admin, "SELECT to_regclass('trove.would_apply') IS NOT NULL") {
			t.Fatal("a step was applied after the checksum mismatch")
		}
	})

	t.Run("R-ARCH-026 a step that is not applied while a later one is stops the run", func(t *testing.T) {
		cfg, _, admin := migrated(t, st)
		scalar[int](t, admin, "WITH d AS (DELETE FROM trove.schema_migration WHERE version = 2 RETURNING 1) SELECT count(*) FROM d")
		_, err := postgres.Migrate(ctxT(t, 30*time.Second), cfg, opts(nil))
		if err == nil || !strings.Contains(err.Error(), "in order") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestMigrateNoTransactionStep(t *testing.T) {
	st := getStack(t)
	t.Run("R-HA-018 a no-transaction step builds an index concurrently and an invalid leftover is dropped first", func(t *testing.T) {
		cfg, _, admin := migrated(t, st)
		// A failed CONCURRENTLY build leaves an INVALID index of the same name.
		if _, err := admin.Exec(context.Background(), `INSERT INTO trove.repositories (key, format, class) VALUES ('r1', 'raw', 'local'), ('r1b', 'raw', 'local')`); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(context.Background(), `CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS repositories_format_idx ON trove.repositories (format)`); err == nil {
			t.Fatal("the unique build over duplicate values must fail, leaving an invalid index")
		}
		if n := scalar[int](t, admin, "SELECT count(*) FROM pg_index WHERE NOT indisvalid"); n != 1 {
			t.Fatalf("invalid indexes = %d", n)
		}
		steps := stepsWith(t, map[string]string{
			"00006_index.sql": "-- trove:phase expand\n-- trove:no-transaction\nCREATE INDEX CONCURRENTLY repositories_format_idx ON trove.repositories (format);\n",
		})
		res, err := postgres.Migrate(ctxT(t, 30*time.Second), cfg, opts(steps))
		if err != nil || len(res.Applied) != 1 {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
		if n := scalar[int](t, admin, "SELECT count(*) FROM pg_index WHERE NOT indisvalid"); n != 0 {
			t.Fatalf("invalid indexes after the step = %d", n)
		}
		if scalar[bool](t, admin, "SELECT transactional FROM trove.schema_migration WHERE version = 6") {
			t.Fatal("the step must be recorded as non-transactional")
		}
	})
}

func TestMigrateDDLLockRetry(t *testing.T) {
	st := getStack(t)
	t.Run("R-HA-018 a step that waits behind a long transaction gives up after ddl_lock_timeout and retries", func(t *testing.T) {
		cfg, name, admin := migrated(t, st)
		holder, err := pgx.Connect(context.Background(), admin.Config().ConnString())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Close(context.Background()) }()
		tx, err := holder.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(context.Background(), "LOCK TABLE trove.repositories IN ACCESS SHARE MODE"); err != nil {
			t.Fatal(err)
		}
		_ = name
		steps := stepsWith(t, map[string]string{
			"00006_alter.sql": "-- trove:phase expand\nALTER TABLE trove.repositories ADD COLUMN note text;\n",
		})
		// Release the table after the second attempt timed out.
		released := make(chan struct{})
		go func() {
			defer close(released)
			time.Sleep(3500 * time.Millisecond)
			_ = tx.Rollback(context.Background())
		}()
		defer func() { <-released }()
		o := opts(steps)
		o.Migrate.DDLLockTimeout = time.Second
		o.Migrate.DDLLockRetries = 10
		start := time.Now()
		res, err := postgres.Migrate(ctxT(t, 60*time.Second), cfg, o)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Applied) != 1 || res.Applied[0].Attempts < 2 {
			t.Fatalf("the step must have been retried: %+v (after %s)", res, time.Since(start))
		}
	})
}

func TestContractGuard(t *testing.T) {
	st := getStack(t)
	contract := func(t *testing.T) map[string]string {
		return map[string]string{
			"00006_scratch.sql":      "-- trove:phase expand\nCREATE TABLE trove.scratch (id int);\n",
			"00007_drop_scratch.sql": "-- trove:phase contract\n-- trove:requires-app >= 0.4.0\nDROP TABLE trove.scratch;\n",
		}
	}
	addReplica := func(t *testing.T, admin *pgx.Conn, name, version string, age time.Duration) {
		t.Helper()
		_, err := admin.Exec(context.Background(), `INSERT INTO trove.replica
			(instance_uid, name, version, revision, roles, state, secret_key_ids, active_secret_key_id, started_at, heartbeat_at)
			VALUES (gen_random_uuid(), $1, $2, 'abc', '{web}', 'ready', '{}', 'k1', now() - interval '1 hour', now() - $3::interval)`,
			name, version, age.String())
		if err != nil {
			t.Fatal(err)
		}
	}

	t.Run("R-ARCH-026 a contract step above a live replica's version exits 3, names the replica and applies nothing", func(t *testing.T) {
		cfg, _, admin := migrated(t, st)
		addReplica(t, admin, "trove-web-0.3-abc", "0.3.1", 2*time.Second)
		_, err := postgres.Migrate(ctxT(t, 30*time.Second), cfg, opts(stepsWith(t, contract(t))))
		var ref *postgres.Refusal
		if !errors.As(err, &ref) || ref.ExitCode() != 3 {
			t.Fatalf("err = %v, want a refusal with status 3", err)
		}
		if !strings.Contains(err.Error(), "trove-web-0.3-abc") || !strings.Contains(err.Error(), "0.3.1") ||
			!strings.Contains(err.Error(), "one minor release at a time") || !strings.Contains(err.Error(), "downtime") {
			t.Fatalf("the refusal must name the replica and both ways forward: %v", err)
		}
		if len(ref.Replicas) != 1 || ref.Step != "00007_drop_scratch" {
			t.Fatalf("refusal = %+v", ref)
		}
		// Nothing was applied, not even the expand step before the contract step.
		if scalar[bool](t, admin, "SELECT to_regclass('trove.scratch') IS NOT NULL") ||
			scalar[int](t, admin, "SELECT applied FROM trove.schema_state") != postgres.RequiredSchema() {
			t.Fatal("the guard must refuse before applying anything")
		}
	})

	t.Run("R-HA-018 a stale registry row does not block, and the applied step sets compat_floor", func(t *testing.T) {
		cfg, _, admin := migrated(t, st)
		addReplica(t, admin, "gone-pod", "0.3.1", 10*time.Minute)
		addReplica(t, admin, "new-pod", "0.4.0", time.Second)
		res, err := postgres.Migrate(ctxT(t, 30*time.Second), cfg, opts(stepsWith(t, contract(t))))
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Applied) != 2 || res.CompatFloor != "0.4.0" {
			t.Fatalf("res = %+v", res)
		}
		if got := scalar[string](t, admin, "SELECT compat_floor FROM trove.schema_state"); got != "0.4.0" {
			t.Fatalf("compat_floor = %q", got)
		}
		if got := scalar[string](t, admin, "SELECT requires_app FROM trove.schema_migration WHERE version = 7"); got != "0.4.0" {
			t.Fatalf("requires_app = %q", got)
		}
		// requiredSchema counts expand steps only: the binary with these files needs 6.
		steps, _ := postgres.ParseSteps(stepsWith(t, contract(t)))
		if len(steps) != 7 {
			t.Fatalf("steps = %d", len(steps))
		}
		rep, err := postgres.Status(ctxT(t, 30*time.Second), cfg, func() postgres.MigrateOptions { o := opts(stepsWith(t, contract(t))); o.Version = "0.3.9"; return o }())
		if err != nil {
			t.Fatal(err)
		}
		if rep.Servable || !strings.Contains(rep.Verdict, "schema requires trove >= 0.4.0") {
			t.Fatalf("an N-2 binary must be refused: %+v", rep)
		}
		rep, err = postgres.Status(ctxT(t, 30*time.Second), cfg, func() postgres.MigrateOptions { o := opts(stepsWith(t, contract(t))); o.Version = "0.4.0"; return o }())
		if err != nil || !rep.Servable || rep.Binary.RequiredSchema != 6 {
			t.Fatalf("0.4.0 must serve: %+v, %v", rep, err)
		}
	})

	t.Run("R-HA-019 with every replica stopped the registry is empty and the guard passes", func(t *testing.T) {
		cfg, _, _ := migrated(t, st)
		if _, err := postgres.Migrate(ctxT(t, 30*time.Second), cfg, opts(stepsWith(t, contract(t)))); err != nil {
			t.Fatal(err)
		}
	})
}

func TestHalfRestored(t *testing.T) {
	st := getStack(t)
	t.Run("R-ARCH-026 restore_in_progress in system_state: exit 3, the backup ID named, nothing applied", func(t *testing.T) {
		cfg, _, admin := migrated(t, st)
		if _, err := admin.Exec(context.Background(), `INSERT INTO trove.system_state (key, value) VALUES ('restore_in_progress', '"20261028T020000Z-7f3a"')`); err != nil {
			t.Fatal(err)
		}
		extra := stepsWith(t, map[string]string{"00006_new.sql": "-- trove:phase expand\nCREATE TABLE trove.must_not_exist (id int);\n"})
		_, err := postgres.Migrate(ctxT(t, 30*time.Second), cfg, opts(extra))
		var ref *postgres.Refusal
		if !errors.As(err, &ref) || ref.ExitCode() != 3 || ref.BackupID != "20261028T020000Z-7f3a" || !strings.Contains(err.Error(), "20261028T020000Z-7f3a") {
			t.Fatalf("err = %v", err)
		}
		if scalar[bool](t, admin, "SELECT to_regclass('trove.must_not_exist') IS NOT NULL") {
			t.Fatal("a step ran against a half-restored database")
		}
	})

	t.Run("R-ARCH-026 the restore anchor on the schema: exit 3, the backup ID named", func(t *testing.T) {
		name, admin := st.newDatabase(t)
		cfg := st.config(t, name)
		if _, err := admin.Exec(context.Background(), `CREATE SCHEMA trove; COMMENT ON SCHEMA trove IS 'trove-restore 20261028T020000Z-7f3a'`); err != nil {
			t.Fatal(err)
		}
		_, err := postgres.Migrate(ctxT(t, 30*time.Second), cfg, opts(nil))
		var ref *postgres.Refusal
		if !errors.As(err, &ref) || ref.BackupID != "20261028T020000Z-7f3a" {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestPreflight(t *testing.T) {
	st := getStack(t)
	t.Run("R-HA-014 a superuser role is refused unless database.preflight.allow_superuser is set", func(t *testing.T) {
		name, _ := st.newDatabase(t)
		cfg := st.config(t, name)
		cfg.Preflight.AllowSuperuser = false
		_, err := postgres.Migrate(ctxT(t, 30*time.Second), cfg, opts(nil))
		if err == nil || !strings.Contains(err.Error(), "database.preflight.allow_superuser") {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("R-HA-016 a connection budget above max_connections fails the migration naming both numbers", func(t *testing.T) {
		name, admin := st.newDatabase(t)
		cfg := st.config(t, name)
		cfg.Pool.MaxConns = 20
		cfg.Preflight.ExpectedWebReplicas, cfg.Preflight.ExpectedWorkerReplicas = 10, 5
		max := scalar[int](t, admin, "SELECT current_setting('max_connections')::int")
		sres := scalar[int](t, admin, "SELECT current_setting('superuser_reserved_connections')::int")
		want := postgres.ConnectionBudget(cfg, 10, 5, sres).Total
		if want <= max {
			t.Fatalf("test setup: budget %d fits max_connections %d", want, max)
		}
		_, err := postgres.Migrate(ctxT(t, 30*time.Second), cfg, opts(nil))
		if err == nil || !strings.Contains(err.Error(), "needs "+itoa(want)) || !strings.Contains(err.Error(), "allows "+itoa(max)) {
			t.Fatalf("err = %v, want it to name %d and %d", err, want, max)
		}
		if scalar[bool](t, admin, "SELECT to_regnamespace('trove') IS NOT NULL") {
			t.Fatal("preflight must fail before the schema is touched")
		}
		cfg.Preflight.ConnectionBudget = "warn"
		if _, err := postgres.Migrate(ctxT(t, 60*time.Second), cfg, opts(nil)); err != nil {
			t.Fatalf("warn must continue: %v", err)
		}
	})

	t.Run("R-ARCH-017 trove migrate behind a transaction-mode pooler exits with the session-state message", func(t *testing.T) {
		name, _ := st.newDatabase(t)
		cfg := st.config(t, name)
		cfg.Port = st.bouncerPort // the "session host" is the pooler
		stop := loadPooler(t, st, name)
		defer stop()
		_, err := postgres.Migrate(ctxT(t, 30*time.Second), cfg, opts(nil))
		if !errors.Is(err, postgres.ErrSessionState) ||
			err.Error() != "session connection must keep session state; set database.session_host to a direct or session-mode endpoint" {
			t.Fatalf("err = %v", err)
		}
	})
}

func itoa(n int) string { return strconv.Itoa(n) }
