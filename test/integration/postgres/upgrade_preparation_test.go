//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// The upgrade preparation of R-HA-021 at the runner, against PostgreSQL: step 4 (the report shows the state of a
// background migration that a contract step depends on, and the contract guard refuses the step for exactly that state,
// R-HA-022, Q-HA-114, Q-HA-115) and the guard and lock parts of its two acceptance criteria. The Helm Job around the runner
// (exit status 3 is FailJob, the old pods keep serving) is the chart test of the Job's podFailurePolicy and TestE04MigrationFailure.

// itBackfill is a background migration of the test: its job kind is data_migration_it_backfill, its completion the
// system_state row it_backfill_done.
var itBackfill = postgres.BackgroundMigration{Name: "it_backfill", Release: "0.1.0", DoneKey: "it_backfill_done"}

func execT(t *testing.T, c *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := c.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", strings.Join(strings.Fields(sql), " "), err)
	}
}

// liveReplica registers a replica of the version whose last heartbeat is now.
func liveReplica(t *testing.T, admin *pgx.Conn, name, version string) {
	t.Helper()
	execT(t, admin, `INSERT INTO trove.replica
		(instance_uid, name, version, revision, roles, state, secret_key_ids, active_secret_key_id, started_at, heartbeat_at)
		VALUES (gen_random_uuid(), $1, $2, 'abc', '{api}', 'ready', '{}', 'k1', now() - interval '1 hour', now())`, name, version)
}

func backgroundOf(t *testing.T, rep postgres.StatusReport, name string) postgres.BackgroundInfo {
	t.Helper()
	for _, b := range rep.Background {
		if b.Name == name {
			return b
		}
	}
	t.Fatalf("the report lists no background migration %s: %+v", name, rep.Background)
	return postgres.BackgroundInfo{}
}

func TestR_HA_021_UpgradePreparation(t *testing.T) {
	st := getStack(t)

	t.Run("R-HA-021 R-HA-022 step 4: the report shows the background migration a contract step depends on as not succeeded, and the guard refuses the step for that state", func(t *testing.T) {
		cfg, _, admin := migrated(t, st)
		scratch, drop := nextStep(1, "it_scratch"), nextStep(2, "it_drop_scratch")
		o := opts(stepsWith(t, map[string]string{
			scratch: "-- trove:phase expand\nCREATE TABLE trove.it_scratch (id int);\n",
			drop:    "-- trove:phase contract\n-- trove:requires-app >= 0.1.0\n-- trove:requires-background it_backfill\nDROP TABLE trove.it_scratch;\n",
		}))
		o.Background = []postgres.BackgroundMigration{itBackfill}
		before := scalar[int](t, admin, "SELECT applied FROM trove.schema_state")

		// report and guard agree on each state the records can be in
		check := func(t *testing.T, wantState string) postgres.StatusReport {
			t.Helper()
			rep, err := postgres.Status(ctxT(t, 30*time.Second), cfg, o)
			if err != nil {
				t.Fatal(err)
			}
			if got := backgroundOf(t, rep, "it_backfill"); got.State != wantState || got.Release != "0.1.0" {
				t.Fatalf("the report says %+v, want the state %s", got, wantState)
			}
			if wantState == postgres.BackgroundSucceeded {
				return rep
			}
			if len(rep.Pending) != 2 || rep.Pending[1].Name != strings.TrimSuffix(drop, ".sql") ||
				strings.Join(rep.Pending[1].RequiresBackground, ",") != "it_backfill" || rep.Pending[1].RequiresApp != "0.1.0" {
				t.Fatalf("the pending contract step does not show what the guard checks: %+v", rep.Pending)
			}
			_, err = postgres.Migrate(ctxT(t, 30*time.Second), cfg, o)
			var ref *postgres.Refusal
			if !errors.As(err, &ref) || ref.ExitCode() != postgres.ExitRefused {
				t.Fatalf("with it_backfill %s: err = %v, want a refusal with status 3", wantState, err)
			}
			if len(ref.Background) != 1 || ref.Background[0] != (postgres.BackgroundRef{Name: "it_backfill", State: wantState}) ||
				ref.Step != strings.TrimSuffix(drop, ".sql") || len(ref.Replicas) != 0 {
				t.Fatalf("refusal = %+v", ref)
			}
			for _, want := range []string{"it_backfill (" + wantState + ")", "nothing was applied", "GET /api/v1/system/migrations"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the refusal does not say %q: %v", want, err)
				}
			}
			// nothing was applied, not even the expand step before the contract step
			if scalar[bool](t, admin, "SELECT to_regclass('trove.it_scratch') IS NOT NULL") || scalar[int](t, admin, "SELECT applied FROM trove.schema_state") != before {
				t.Fatal("the guard must refuse before anything is applied")
			}
			return rep
		}

		// no job and no record: pending
		check(t, postgres.BackgroundPending)
		// a queued run: pending
		execT(t, admin, `INSERT INTO trove.job (id, kind, queue) VALUES (gen_random_uuid(), 'data_migration_it_backfill', 'maintenance')`)
		check(t, postgres.BackgroundPending)
		// the run holds it, with its progress
		execT(t, admin, `UPDATE trove.job SET state = 'running', started_at = now() - interval '1 minute', progress_done = 500, progress_total = 1000
			WHERE kind = 'data_migration_it_backfill'`)
		rep := check(t, postgres.BackgroundRunning)
		if b := backgroundOf(t, rep, "it_backfill"); b.Processed != 500 || b.EstimatedTotal == nil || *b.EstimatedTotal != 1000 || b.StartedAt == nil {
			t.Fatalf("a running migration's progress: %+v", b)
		}
		// the run failed
		execT(t, admin, `UPDATE trove.job SET state = 'failed', finished_at = now() WHERE kind = 'data_migration_it_backfill'`)
		check(t, postgres.BackgroundFailed)
		// a second run reaches the end and records the completion
		execT(t, admin, `INSERT INTO trove.job (id, kind, queue, state, started_at, finished_at, progress_done, progress_total, created_at)
			VALUES (gen_random_uuid(), 'data_migration_it_backfill', 'maintenance', 'succeeded', now() - interval '10 seconds', now(), 1000, 1000, now())`)
		execT(t, admin, `INSERT INTO trove.system_state (key, value) VALUES ('it_backfill_done', '{"rows": 1000}')`)
		rep = check(t, postgres.BackgroundSucceeded)
		first := scalar[time.Time](t, admin, "SELECT min(started_at) FROM trove.job WHERE kind = 'data_migration_it_backfill'")
		if b := backgroundOf(t, rep, "it_backfill"); b.Processed != 1000 || b.StartedAt == nil || !b.StartedAt.Equal(first) {
			t.Fatalf("a succeeded migration keeps the count of its last run and the start of its first: %+v (first start %s)", b, first)
		}

		// now the guard lets the step run, and the report has nothing pending
		res, err := postgres.Migrate(ctxT(t, 30*time.Second), cfg, o)
		if err != nil || len(res.Applied) != 2 {
			t.Fatalf("after it_backfill succeeded: res = %+v, err = %v", res, err)
		}
		rep, err = postgres.Status(ctxT(t, 30*time.Second), cfg, o)
		if err != nil || len(rep.Pending) != 0 || backgroundOf(t, rep, "it_backfill").State != postgres.BackgroundSucceeded {
			t.Fatalf("after the contract step: %+v, %v", rep, err)
		}
	})

	t.Run("R-HA-021 R-HA-018 a direct upgrade over a contract step is refused with status 3 while a replica of an older release is live, and succeeds without an override once the replicas are stopped", func(t *testing.T) {
		cfg, _, admin := migrated(t, st)
		o := opts(stepsWith(t, map[string]string{
			nextStep(1, "it_old_shape"):  "-- trove:phase expand\nCREATE TABLE trove.it_old_shape (id int);\n",
			nextStep(2, "it_drop_shape"): "-- trove:phase contract\n-- trove:requires-app >= 0.3.0\nDROP TABLE trove.it_old_shape;\n",
		}))
		o.Version = "0.4.0"
		// The replicas count as stopped once their heartbeat is older than system.heartbeat_stale_after; the test shortens it
		// from the 30 s of the criterion so that the wait is short.
		o.ReplicaStaleAfter = 4 * time.Second
		liveReplica(t, admin, "trove-api-0.2-abc", "0.2.7")

		_, err := postgres.Migrate(ctxT(t, 30*time.Second), cfg, o)
		var ref *postgres.Refusal
		if !errors.As(err, &ref) || ref.ExitCode() != postgres.ExitRefused {
			t.Fatalf("err = %v, want a refusal with status 3", err)
		}
		for _, want := range []string{"requires trove >= 0.3.0", "trove-api-0.2-abc (0.2.7)", "one minor release at a time", "stop all replicas and upgrade with downtime", "nothing was applied"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("the message of R-HA-018 does not say %q: %v", want, err)
			}
		}
		if scalar[bool](t, admin, "SELECT to_regclass('trove.it_old_shape') IS NOT NULL") {
			t.Fatal("the refused run applied a step")
		}

		// every replica stopped: the registry row goes stale, nothing is deleted or overridden
		time.Sleep(o.ReplicaStaleAfter + 500*time.Millisecond)
		res, err := postgres.Migrate(ctxT(t, 30*time.Second), cfg, o)
		if err != nil || len(res.Applied) != 2 || res.CompatFloor != "0.3.0" {
			t.Fatalf("after the replicas stopped: res = %+v, err = %v", res, err)
		}
		if n := scalar[int](t, admin, "SELECT count(*) FROM trove.replica WHERE name = 'trove-api-0.2-abc'"); n != 1 {
			t.Fatalf("the registry row of the stopped replica is gone (%d): the guard must pass on its age, not on a deletion", n)
		}
	})

	t.Run("R-HA-021 R-HA-019 a migration started during a backup's database phase waits for it, and fails naming the backup when the phase outlasts migrate.advisory_lock_wait", func(t *testing.T) {
		cfg, _, admin := migrated(t, st)
		backup := func(t *testing.T) *pgx.Conn {
			t.Helper()
			c, err := pgx.Connect(context.Background(), admin.Config().ConnString())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = c.Close(context.Background()) })
			execT(t, c, "SET application_name = 'trove:trove-backup-pre-upgrade-x2v4q:backup'")
			execT(t, c, "SELECT pg_advisory_lock_shared(3, 0)")
			return c
		}

		// the phase ends within the wait: the migration waits, then applies
		holder := backup(t)
		o := opts(stepsWith(t, map[string]string{nextStep(1, "it_after_backup"): "-- trove:phase expand\nCREATE TABLE trove.it_after_backup (id int);\n"}))
		o.Migrate.AdvisoryLockWait = 20 * time.Second
		go func() {
			time.Sleep(1500 * time.Millisecond)
			_, _ = holder.Exec(context.Background(), "SELECT pg_advisory_unlock_shared(3, 0)")
		}()
		res, err := postgres.Migrate(ctxT(t, 60*time.Second), cfg, o)
		if err != nil || !res.WaitedForLock || res.LockWaitMs < 1000 || len(res.Applied) != 1 {
			t.Fatalf("a migration during the backup's database phase: res = %+v, err = %v", res, err)
		}

		// the phase outlasts the wait: the migration fails and names the backup
		backup(t)
		o = opts(stepsWith(t, map[string]string{
			nextStep(1, "it_after_backup"): "-- trove:phase expand\nCREATE TABLE trove.it_after_backup (id int);\n",
			nextStep(2, "it_too_late"):     "-- trove:phase expand\nCREATE TABLE trove.it_too_late (id int);\n",
		}))
		o.Migrate.AdvisoryLockWait = 1500 * time.Millisecond
		_, err = postgres.Migrate(ctxT(t, 30*time.Second), cfg, o)
		if err == nil || !strings.Contains(err.Error(), "trove:trove-backup-pre-upgrade-x2v4q:backup") || !strings.Contains(err.Error(), "migrate.advisory_lock_wait") {
			t.Fatalf("err = %v, want the bounded wait that names the backup", err)
		}
		var ref *postgres.Refusal
		if errors.As(err, &ref) {
			t.Fatal("a lock wait that ran out is an error (status 1, the Job retries), not a refusal (status 3)")
		}
		if scalar[bool](t, admin, "SELECT to_regclass('trove.it_too_late') IS NOT NULL") {
			t.Fatal("a step was applied without the lock")
		}
	})
}
