//go:build integration

package backup

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/create"
	"github.com/guided-traffic/trove/internal/backup/format"
)

func fmtDigest(d [32]byte) format.Digest { return format.Digest(d) }

// TestR_BAK_001_ABackupOfARealInstallation: a create run against PostgreSQL and the S3 store writes the marker, the pool, a table
// file per exported table, the blob list and the manifest, and the manifest validates against its schema (R-BAK-002, R-BAK-003).
func TestR_BAK_001_ABackupOfARealInstallation(t *testing.T) {
	in := newInstall(t)
	a := in.mustUpload(in.repo, "a.bin", "alpha-content")
	b := in.mustUpload(in.repo, "b.bin", "beta-content")
	r := in.newRig(t)
	res, err := in.run(context.Background(), r, backup.CreateOptions{Trigger: backup.TriggerManual, NoPrune: true})
	if err != nil {
		t.Fatal(err)
	}
	m := manifestOf(t, r.tgt, r.cfg, res.BackupID)
	validateManifest(t, mustEncode(t, m))
	if m.Blobs.Referenced != 2 || m.Blobs.Copied != 2 || m.Schema.Step < 46 {
		t.Fatalf("manifest blobs %+v schema %d", m.Blobs, m.Schema.Step)
	}
	for _, d := range []format.Digest{fmtDigest(a), fmtDigest(b)} {
		if !poolHas(t, r.tgt, r.cfg, d) {
			t.Fatalf("pool object %s missing", d.Hex())
		}
	}
	if got := in.number(`SELECT count(*) FROM trove.backup_blob_pin`); got != 0 {
		t.Fatalf("pins left: %d", got)
	}
	if state := scalarText(in, `SELECT state FROM trove.backup_run`); state != "completed" {
		t.Fatalf("run state = %s", state)
	}
	if hold := scalarText(in, `SELECT hold FROM trove.backup_run`); hold != "none" {
		t.Fatalf("hold = %s", hold)
	}
	// A second run without new uploads copies nothing (R-BAK-002 criterion).
	res2, err := in.run(context.Background(), r, backup.CreateOptions{NoPrune: true})
	if err != nil || res2.BlobsCopied != 0 {
		t.Fatalf("second run %+v %v", res2, err)
	}
	if res2.BackupID == res.BackupID {
		t.Fatal("same ID")
	}
}

func scalarText(in *install, sql string, args ...any) string {
	in.t.Helper()
	var s string
	if err := in.admin.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		in.t.Fatalf("%s: %v", sql, err)
	}
	return s
}

// TestR_BAK_005_ConnectionsAndPartitionedAudit: a run uses at most 2 + parallel connections of class backup, and the partitioned
// audit_event is exported with every row of every partition.
func TestR_BAK_005_ConnectionsAndPartitionedAudit(t *testing.T) {
	in := newInstall(t)
	in.mustUpload(in.repo, "a.bin", "alpha")
	for i := 0; i < 50; i++ {
		in.exec(`INSERT INTO trove.audit_event (id, occurred_at, action, category, outcome, actor_type) VALUES (gen_random_uuid(), now() - make_interval(days => $1), 'x.y', 'system', 'success', 'system')`, i%3)
	}
	want := in.number(`SELECT count(*) FROM trove.audit_event`)
	r := in.newRig(t)
	r.cfg.Database.Parallel = 2

	stop := make(chan struct{})
	var wg sync.WaitGroup
	var maxSeen int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			var n int64
			if err := in.admin.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity WHERE application_name LIKE 'trove:%:backup' AND datname = current_database()`).Scan(&n); err == nil && n > maxSeen {
				maxSeen = n
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	res, err := in.run(context.Background(), r, backup.CreateOptions{NoPrune: true})
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if maxSeen > 4 || maxSeen < 3 {
		t.Fatalf("saw up to %d backup connections, want 3 or 4 (control, snapshot, up to 2 exporters)", maxSeen)
	}
	m := manifestOf(t, r.tgt, r.cfg, res.BackupID)
	tf, ok := m.Table("audit_event")
	if !ok || tf.Rows < want {
		t.Fatalf("audit_event = %+v, want at least %d rows (the run's own events come after the snapshot)", tf, want)
	}
}

// TestR_BAK_005_TableLockTimeoutIsExit75: a session holds ACCESS EXCLUSIVE on a table for longer than lock_timeout.
func TestR_BAK_005_TableLockTimeoutIsExit75(t *testing.T) {
	in := newInstall(t)
	r := in.newRig(t)
	r.cfg.Database.LockTimeout = time.Second
	ctx := context.Background()
	tx, err := in.admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `LOCK TABLE trove.items IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = in.run(ctx, r, backup.CreateOptions{NoPrune: true})
	if exit(err) != backup.ExitTemporary || backup.ErrorCode(err) != backup.CodeTableLockTimeout {
		t.Fatalf("err = %v (exit %d, %s)", err, exit(err), backup.ErrorCode(err))
	}
	if keys := listManifests(t, r); len(keys) != 0 {
		t.Fatalf("manifests = %v", keys)
	}
	if hold := scalarText(in, `SELECT hold FROM trove.backup_run`); hold != "none" {
		t.Fatalf("hold = %s", hold)
	}
}

// TestR_BAK_005_RewriteDuringExportFailsAndRowCountsHold: ALTER TABLE ... TYPE (a rewrite) during the export waits behind the
// snapshot's ACCESS SHARE lock and fails with 55P03; the exported row count of the table is the count at the snapshot.
func TestR_BAK_005_RewriteDuringExportFailsAndRowCountsHold(t *testing.T) {
	in := newInstall(t)
	for i := 0; i < 5; i++ {
		in.mustUpload(in.repo, "f"+string(rune('a'+i)), "content"+string(rune('a'+i)))
	}
	items := in.number(`SELECT count(*) FROM trove.items`)
	r := in.newRig(t)
	var alterErr error
	r.mutate = func(d *create.Deps) {
		d.Fault = func(ctx context.Context, point string) error {
			if point != create.PointSnapshot {
				return nil
			}
			c := dial(t, in)
			defer c.Close(ctx)
			if _, err := c.Exec(ctx, `SET lock_timeout = '1s'`); err != nil {
				return err
			}
			_, alterErr = c.Exec(ctx, `ALTER TABLE trove.items ALTER COLUMN size TYPE numeric`)
			return nil
		}
	}
	res, err := in.run(context.Background(), r, backup.CreateOptions{NoPrune: true})
	if err != nil {
		t.Fatal(err)
	}
	if alterErr == nil || !strings.Contains(alterErr.Error(), "55P03") {
		t.Fatalf("the rewrite must fail with 55P03, got %v", alterErr)
	}
	if tf, _ := manifestOf(t, r.tgt, r.cfg, res.BackupID).Table("items"); tf.Rows != items {
		t.Fatalf("items rows = %d, want %d", tf.Rows, items)
	}
}

// exportReleased waits, bounded, until no session of the installation's database holds the backup's shared migration lock
// (pg_advisory_lock_shared(3, 0)).
func exportReleased(c *pgx.Conn) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		var held int
		if err := c.QueryRow(context.Background(), `SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND classid = 3 AND objid = 0
			AND objsubid = 2 AND mode = 'ShareLock' AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`).Scan(&held); err != nil {
			return err
		}
		if held == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("the export still holds the shared migration lock after 30 s")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestR_BAK_005_MigrationLock: a migration holding the lock makes the run wait and exit 75; a migration started during the export
// waits and fails naming the backup; one started during the blob copy succeeds.
func TestR_BAK_005_MigrationLock(t *testing.T) {
	ctx := context.Background()
	t.Run("a held migration lock ends the run with exit 75 after migration_wait", func(t *testing.T) {
		in := newInstall(t)
		r := in.newRig(t)
		r.cfg.Database.MigrationWait = time.Second
		c := dial(t, in)
		defer c.Close(ctx)
		if _, err := c.Exec(ctx, `SELECT pg_advisory_lock(3, 0)`); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		_, err := in.run(ctx, r, backup.CreateOptions{NoPrune: true})
		if exit(err) != backup.ExitTemporary || backup.ErrorCode(err) != backup.CodeMigrationWait || time.Since(start) < time.Second {
			t.Fatalf("err = %v (exit %d) after %s", err, exit(err), time.Since(start))
		}
		if hold := scalarText(in, `SELECT hold FROM trove.backup_run`); hold != "none" {
			t.Fatalf("the wait for a migration must not hold back garbage collection: hold = %s", hold)
		}
	})
	t.Run("a migration during the export fails naming the backup, during the copy it succeeds", func(t *testing.T) {
		in := newInstall(t)
		in.mustUpload(in.repo, "a.bin", "alpha")
		r := in.newRig(t)
		var duringExport, duringCopy error
		migrate := func() error {
			return migrateOnce(in, time.Second)
		}
		locks := dial(t, in)
		r.mutate = func(d *create.Deps) {
			d.Fault = func(_ context.Context, point string) error {
				switch point {
				case create.PointSnapshot:
					duringExport = migrate()
				case create.PointCopied:
					// The export runs beside the blob copy and releases the migration lock when its last table is written,
					// which on a loaded host can be after the copy of the one blob (F-115): the copy phase starts for a
					// migration once the export has let go of the lock.
					if duringCopy = exportReleased(locks); duringCopy == nil {
						duringCopy = migrate()
					}
				}
				return nil
			}
		}
		if _, err := in.run(ctx, r, backup.CreateOptions{NoPrune: true}); err != nil {
			t.Fatal(err)
		}
		if duringExport == nil || !strings.Contains(duringExport.Error(), "trove:") {
			t.Fatalf("a migration during the export must fail naming the backup connection: %v", duringExport)
		}
		if duringCopy != nil {
			t.Fatalf("a migration after the export must succeed: %v", duringCopy)
		}
	})
}
