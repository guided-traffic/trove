//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/format"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func openBackupStore(t *testing.T) (*backup.PGStore, *pgx.Conn) {
	t.Helper()
	st := getStack(t)
	cfg, _, admin := migrated(t, st)
	db, err := postgres.Open(ctxT(t, 30*time.Second), cfg, "it-backup", meta.RoleBackup, quiet)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(db.Close)
	return backup.NewPGStore(db), admin
}

func newRun(id string, op backup.Operation, lease time.Duration) backup.NewRun {
	return backup.NewRun{ID: id, Operation: op, Trigger: backup.TriggerManual, Target: "s3://b/p", Pod: "pod-" + id[:4], TroveVersion: "0.1.0", Lease: lease}
}

const (
	runA = "0192f0c4-7a1e-7c3b-9d2e-000000000001"
	runB = "0192f0c4-7a1e-7c3b-9d2e-000000000002"
	runC = "0192f0c4-7a1e-7c3b-9d2e-000000000003"
)

func TestBackupRunStore(t *testing.T) {
	store, admin := openBackupStore(t)
	ctx := ctxT(t, 60*time.Second)

	t.Run("R-BAK-010 one create or delete run is active; verify is not limited", func(t *testing.T) {
		a, err := store.Begin(ctx, newRun(runA, backup.OpCreate, time.Minute))
		if err != nil || a.State != backup.StateStarting || a.Hold != backup.HoldNone {
			t.Fatalf("Begin A = %+v, %v", a, err)
		}
		_, err = store.Begin(ctx, newRun(runB, backup.OpDelete, time.Minute))
		var taken *backup.ErrSlotTaken
		if !errors.As(err, &taken) || taken.Active.ID != runA {
			t.Fatalf("Begin B = %v, want the slot taken by A", err)
		}
		if v, err := store.Begin(ctx, newRun(runC, backup.OpVerify, time.Minute)); err != nil || v.Operation != backup.OpVerify {
			t.Fatalf("a verify run must start while a create run is active: %+v, %v", v, err)
		}
		active, ok, _, err := store.Active(ctx)
		if err != nil || !ok || active.ID != runA {
			t.Fatalf("Active = %+v, %v, %v", active, ok, err)
		}
		if err := store.Finish(ctx, runC, backup.StateCompleted, nil, nil); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("R-BAK-007 the hold protocol: all, then pinned, then none with the pins gone", func(t *testing.T) {
		if err := store.SetHold(ctx, runA, backup.HoldNone, backup.HoldAll); err != nil {
			t.Fatal(err)
		}
		if held, err := store.HoldValid(ctx, backup.HoldAll); err != nil || !held {
			t.Fatalf("HoldValid(all) = %v, %v", held, err)
		}
		var d1, d2 format.Digest
		d1[0], d2[0] = 1, 2
		if err := store.AddPins(ctx, runA, []format.BlobEntry{{Digest: d1, Size: 10}, {Digest: d2, Size: 20}}); err != nil {
			t.Fatal(err)
		}
		if err := store.SetHold(ctx, runA, backup.HoldAll, backup.HoldPinned); err != nil {
			t.Fatal(err)
		}
		if err := store.SetHold(ctx, runA, backup.HoldAll, backup.HoldPinned); !errors.Is(err, backup.ErrSuperseded) {
			t.Fatalf("a second compare-and-set must find the hold changed, err = %v", err)
		}
		held := func(d format.Digest) bool {
			t.Helper()
			h, err := store.BlobHeld(ctx, d)
			if err != nil {
				t.Fatal(err)
			}
			return h
		}
		var other format.Digest
		other[0] = 9
		if !held(d1) || !held(d2) || held(other) {
			t.Fatalf("pinned digests are held, others are not: %v %v %v", held(d1), held(d2), held(other))
		}
		if err := store.DeletePins(ctx, runA, []format.Digest{d1}); err != nil {
			t.Fatal(err)
		}
		if held(d1) || !held(d2) {
			t.Fatal("a confirmed pin stops holding its digest")
		}
	})

	t.Run("R-BAK-010 an expired lease ends the run abandoned, drops its hold and pins, and fences its heartbeat", func(t *testing.T) {
		// A's lease is a minute; expire it by hand, as a pod killed without SIGTERM handling leaves it.
		if err := store.Heartbeat(ctx, runA, backup.StateStarting, backup.Progress{Phase: "copying", BlobsCopied: 3}, nil, true, time.Minute); err != nil {
			t.Fatal(err)
		}
		mustExec(t, admin, "UPDATE trove.backup_run SET lease_expires_at = now() - interval '1 minute' WHERE id = $1", runA)
		b, err := store.Begin(ctx, newRun(runB, backup.OpCreate, time.Minute))
		if err != nil {
			t.Fatalf("Begin B after A's lease expired: %v", err)
		}
		a, ok, err := store.Get(ctx, runA)
		if err != nil || !ok || a.State != backup.StateAbandoned || a.Hold != backup.HoldNone || a.FinishedAt == nil {
			t.Fatalf("A = %+v, %v, %v", a, ok, err)
		}
		if held, _ := store.HoldValid(ctx, backup.HoldPinned); held {
			t.Fatal("an abandoned run still holds")
		}
		err = store.Heartbeat(ctx, runA, backup.StateStarting, backup.Progress{}, nil, true, time.Minute)
		if !errors.Is(err, backup.ErrSuperseded) {
			t.Fatalf("the heartbeat of an abandoned run must report ErrSuperseded, err = %v", err)
		}
		if b.State != backup.StateStarting {
			t.Fatalf("B = %+v", b)
		}
	})

	t.Run("R-BAK-010 the fenced finalize check needs lease for the write's own deadline", func(t *testing.T) {
		if ok, err := store.Fence(ctx, runB, backup.StateStarting, backup.StateFinalizing, time.Hour); err != nil || ok {
			t.Fatalf("Fence with a margin beyond the lease = %v, %v", ok, err)
		}
		if ok, err := store.Fence(ctx, runB, backup.StateStarting, backup.StateFinalizing, 10*time.Second); err != nil || !ok {
			t.Fatalf("Fence = %v, %v", ok, err)
		}
		if err := store.Finish(ctx, runB, backup.StateFailed, nil, []byte(`{"code":"hold_lost"}`)); err != nil {
			t.Fatal(err)
		}
		b, _, _ := store.Get(ctx, runB)
		if b.State != backup.StateFailed || b.Hold != backup.HoldNone || string(b.Error) == "" {
			t.Fatalf("B = %+v", b)
		}
	})

	t.Run("R-API-050 the list pages newest first and filters", func(t *testing.T) {
		all, err := store.List(ctx, backup.ListFilter{Limit: 10})
		if err != nil || len(all) != 3 {
			t.Fatalf("List = %d runs, %v", len(all), err)
		}
		page, err := store.List(ctx, backup.ListFilter{Limit: 1})
		if err != nil || len(page) != 1 {
			t.Fatal(err)
		}
		next, err := store.List(ctx, backup.ListFilter{Limit: 10, After: &backup.RunCursor{StartedAt: page[0].StartedAt, ID: page[0].ID}})
		if err != nil || len(next) != 2 {
			t.Fatalf("second page = %d, %v", len(next), err)
		}
		n, err := store.Count(ctx, backup.ListFilter{State: "failed"})
		if err != nil || n != 1 {
			t.Fatalf("Count(failed) = %d, %v", n, err)
		}
		last, ok, err := store.Last(ctx, backup.OpVerify, backup.StateCompleted)
		if err != nil || !ok || last.ID != runC {
			t.Fatalf("Last(verify) = %+v, %v, %v", last, ok, err)
		}
	})
}

func TestStorageUsageRecount(t *testing.T) {
	st := getStack(t)
	_, _, admin := migrated(t, st)
	blob := func(b byte, size int64, state string) []byte {
		sum := make([]byte, 32)
		for i := range sum {
			sum[i] = b
		}
		mustExec(t, admin, `INSERT INTO trove.blobs (sha256, sha1, md5, sha512, size, state) VALUES ($1, $2, $3, $4, $5, $6)`,
			sum, make([]byte, 20), make([]byte, 16), make([]byte, 64), size, state)
		return sum
	}
	mustExec(t, admin, `INSERT INTO trove.repositories (key, format, class, project_id) VALUES ('raw-local', 'raw', 'local', (SELECT id FROM trove.project WHERE key = 'it'))`)
	repo := scalar[int64](t, admin, `SELECT id FROM trove.repositories WHERE key = 'raw-local'`)
	live := blob(1, 100, "live")
	blob(2, 50, "live")
	mustExec(t, admin, `INSERT INTO trove.items (repo_id, path, parent_path, kind, blob_sha256, size) VALUES ($1, 'a', '', 'file', $2, 100)`, repo, live)
	mustExec(t, admin, `INSERT INTO trove.items (repo_id, path, parent_path, kind, explicit_folder) VALUES ($1, 'd', '', 'folder', true)`, repo)
	// A restore loads with the user triggers disabled and drops unreferenced blob rows, so the folded counters lie.
	mustExec(t, admin, `UPDATE trove.storage_usage_totals SET physical_bytes = 12345, blobs = 99`)
	mustExec(t, admin, `INSERT INTO trove.repository_usage (repo_id, logical_bytes, files, folders) VALUES ($1, 7, 7, 7) ON CONFLICT (repo_id) DO UPDATE SET logical_bytes = 7`, repo)

	t.Run("R-STOR-047 storage_usage_recount recomputes the folded counters and empties the deltas", func(t *testing.T) {
		mustExec(t, admin, `SELECT trove.storage_usage_recount()`)
		if n := scalar[int](t, admin, `SELECT count(*) FROM trove.storage_usage_deltas`); n != 0 {
			t.Fatalf("%d delta rows left", n)
		}
		l := scalar[int64](t, admin, `SELECT logical_bytes FROM trove.repository_usage WHERE repo_id = $1`, repo)
		f := scalar[int64](t, admin, `SELECT files FROM trove.repository_usage WHERE repo_id = $1`, repo)
		fo := scalar[int64](t, admin, `SELECT folders FROM trove.repository_usage WHERE repo_id = $1`, repo)
		if l != 100 || f != 1 || fo != 1 {
			t.Fatalf("repository_usage = %d bytes, %d files, %d folders", l, f, fo)
		}
		p := scalar[int64](t, admin, `SELECT physical_bytes FROM trove.storage_usage_totals`)
		b := scalar[int64](t, admin, `SELECT blobs FROM trove.storage_usage_totals`)
		if p != 150 || b != 2 {
			t.Fatalf("storage_usage_totals = %d bytes, %d blobs", p, b)
		}
	})
}

func TestBackupRoleGrants(t *testing.T) {
	st := getStack(t)
	name, admin := st.newDatabase(t)
	cfg := st.config(t, name)
	role := "trove_backup_" + testenv.RandHex(4)
	maint := st.admin(t, "postgres")
	mustExec(t, maint, "CREATE ROLE "+role+" LOGIN PASSWORD 'x'")
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP OWNED BY "+role)
		_, _ = maint.Exec(context.Background(), "DROP ROLE IF EXISTS "+role)
	})
	mustExec(t, admin, "CREATE SCHEMA IF NOT EXISTS trove")
	mustExec(t, admin, "GRANT USAGE ON SCHEMA trove TO "+role)

	t.Run("R-BAK-015 trove migrate grants the backup role what the backup pod needs, and nothing else", func(t *testing.T) {
		cfg.BackupRole = role
		if _, err := postgres.Migrate(ctxT(t, 60*time.Second), cfg, opts(nil)); err != nil {
			t.Fatalf("migrate: %v", err)
		}
		can := func(table, priv string) bool {
			return scalar[bool](t, admin, "SELECT has_table_privilege($1, $2, $3)", role, "trove."+table, priv)
		}
		for _, c := range []struct {
			table, priv string
			want        bool
		}{
			{"items", "SELECT", true},
			{"audit_event", "SELECT", true},
			{"audit_event", "INSERT", false},
			{"backup_run", "INSERT", true},
			{"backup_run", "UPDATE", true},
			{"backup_blob_pin", "DELETE", true},
			{"items", "DELETE", false},
			{"items", "INSERT", false},
			{"blobs", "UPDATE", false},
			{"audit_event", "DELETE", false},
		} {
			if got := can(c.table, c.priv); got != c.want {
				t.Errorf("%s on %s = %v, want %v", c.priv, c.table, got, c.want)
			}
		}
		// The backup writes its audit events through the function and not through INSERT (Q-BAK-50).
		if !scalar[bool](t, admin, "SELECT has_function_privilege($1, 'trove.backup_record_event(text, text, text, text, jsonb)', 'EXECUTE')", role) {
			t.Error("EXECUTE on backup_record_event() = false, want true")
		}
		// A table the owner creates later is readable at once (the monthly audit partitions, R-OPS-015).
		mustExec(t, admin, "CREATE TABLE trove.zz_later (id int)")
		if !can("zz_later", "SELECT") {
			t.Error("a table created after the run is not readable by the backup role")
		}
	})
	t.Run("R-BAK-015 a configured backup role that does not exist stops the run and names it", func(t *testing.T) {
		cfg.BackupRole = "trove_no_such_role"
		_, err := postgres.Migrate(ctxT(t, 60*time.Second), cfg, opts(nil))
		if err == nil || !strings.Contains(err.Error(), "trove_no_such_role") {
			t.Fatalf("err = %v", err)
		}
	})
}
