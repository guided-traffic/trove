//go:build integration

package backup

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/backup/target"
	"github.com/guided-traffic/trove/internal/blob"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func TestR_BAK_020_Preconditions(t *testing.T) {
	src := newSource(t, 6)

	t.Run("R-BAK-020 --check passes on an empty installation and changes nothing", func(t *testing.T) {
		dst := src.destination()
		rep, err := dst.restore(backup.RestoreOptions{Check: true, SkipRevocationJournal: true})
		if err != nil || !rep.Checked {
			t.Fatalf("report %+v, err %v", rep, err)
		}
		if n := dst.relations(); n != 0 {
			t.Fatalf("--check created %d relations", n)
		}
		if c := dst.stack.Text("SELECT coalesce(obj_description('trove'::regnamespace, 'pg_namespace'), '')"); c != "" {
			t.Fatalf("--check set the anchor %q", c)
		}
	})

	t.Run("R-BAK-020 a database with one row in repositories exits 6 and names check 4", func(t *testing.T) {
		_, err := src.destinationOn(src.stack).restore(backup.RestoreOptions{Check: true, SkipRevocationJournal: true})
		wantExit(t, err, backup.ExitPrecondition, "check 4", "not empty")
	})

	t.Run("R-BAK-020 a key ring without the backup's key ID exits 6 and the database stays empty", func(t *testing.T) {
		other := src.destination("TROVE_SECRETS_ENCRYPTION_KEY_FILES=[\""+src.stack.KeyFile("2026-09.key", bytes.Repeat([]byte{1}, 32))+"\"]", "TROVE_SECRETS_ACTIVE_KEY_ID=2026-09")
		_, err := other.restore(backup.RestoreOptions{SkipRevocationJournal: true})
		wantExit(t, err, backup.ExitPrecondition, "check 3", "key ring does not match backup: missing key 2026-10")
		if n := other.relations(); n != 0 {
			t.Fatalf("the database holds %d relations", n)
		}
	})

	t.Run("R-BAK-020 a foreign object under the blob prefix exits 6 at check 5", func(t *testing.T) {
		dst := src.destination()
		c := dst.blobClient()
		key := dst.cfg.Storage.S3.Prefix + "/stray.txt"
		if err := c.Put(context.Background(), key, strings.NewReader("x"), 1, blob.Checksum{}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Delete(context.Background(), key) })
		_, err := dst.restore(backup.RestoreOptions{SkipRevocationJournal: true})
		wantExit(t, err, backup.ExitPrecondition, "check 5", "stray.txt")
		if n := dst.relations(); n != 0 {
			t.Fatalf("the database holds %d relations", n)
		}
	})

	t.Run("R-BAK-020 a database anchored by another backup exits 6 and names both IDs", func(t *testing.T) {
		dst := src.destination()
		dst.stack.Exec("COMMENT ON SCHEMA trove IS 'trove-restore 20250101T000000Z-aaaa'")
		_, err := dst.restore(backup.RestoreOptions{SkipRevocationJournal: true})
		wantExit(t, err, backup.ExitPrecondition, "check 4", "20250101T000000Z-aaaa", string(src.id))
	})

	t.Run("R-BAK-020 a schema that does not exist exits 6 at check 4", func(t *testing.T) {
		dst := src.destination()
		dst.stack.Exec("DROP SCHEMA trove CASCADE")
		_, err := dst.restore(backup.RestoreOptions{SkipRevocationJournal: true})
		wantExit(t, err, backup.ExitPrecondition, "check 4", "does not exist")
	})

	t.Run("R-BAK-020 a source journal that cannot be listed exits 6 at check 8; --skip-revocation-journal proceeds with H-37's warning", func(t *testing.T) {
		src.m.Source.RevocationJournal = true
		src.rewriteManifest()
		dst := src.destination()
		r, _ := dst.restorer(restorerOptions{jrn: testJournal{reach: errors.New("AccessDenied")}})
		_, err := r.Restore(context.Background(), backup.RestoreOptions{ID: src.id})
		wantExit(t, err, backup.ExitPrecondition, "check 8", "--skip-revocation-journal")
		if n := dst.relations(); n != 0 {
			t.Fatalf("the database holds %d relations", n)
		}

		r, out := dst.restorer(restorerOptions{jrn: testJournal{reach: errors.New("AccessDenied")}})
		rep, err := r.Restore(context.Background(), backup.RestoreOptions{ID: src.id, SkipRevocationJournal: true})
		if err != nil || rep.Journal != "skipped" {
			t.Fatalf("report %+v, err %v", rep, err)
		}
		if !strings.Contains(out.String(), "H-37") || !strings.Contains(out.String(), "NOT in effect") {
			t.Errorf("no warning in the output:\n%s", out.String())
		}
		if got := dst.stack.Text("SELECT details->>'journal' FROM audit_event WHERE action = 'system.restored'"); got != "skipped" {
			t.Errorf("system.restored journal = %q", got)
		}
		src.m.Source.RevocationJournal = false
		src.rewriteManifest()
	})

	t.Run("R-BAK-023 a binary with one migration file edited refuses with exit 6 and names the step", func(t *testing.T) {
		dst := src.destination()
		r, _ := dst.restorer(restorerOptions{steps: editedSteps(t, 10)})
		_, err := r.Restore(context.Background(), backup.RestoreOptions{ID: src.id, SkipRevocationJournal: true})
		wantExit(t, err, backup.ExitPrecondition, "check 2", "migration step 10")
		if n := dst.relations(); n != 0 {
			t.Fatalf("the database holds %d relations", n)
		}
	})

	t.Run("R-BAK-003 a manifest edited to format 2 exits 6 and names the format", func(t *testing.T) {
		src.m.Format = 2
		b, _ := src.m.Encode()
		src.m.Format = 1
		_, err := src.target.Put(context.Background(), src.layout.Manifest(src.id), bytes.Replace(b, []byte(`"format": 1`), []byte(`"format": 2`), 1), target.PutOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer src.rewriteManifest()
		_, err = src.destination().restore(backup.RestoreOptions{Check: true})
		wantExit(t, err, backup.ExitPrecondition, "format 2")
	})

	t.Run("R-BAK-021 a second restore pod waits on the migration lock, writes nothing and exits 75", func(t *testing.T) {
		dst := src.destination()
		holder, err := pgx.Connect(context.Background(), testenv.PgDSN(t, dst.stack.Database()))
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = holder.Close(context.Background()) }()
		if _, err := holder.Exec(context.Background(), "SELECT pg_advisory_lock(3, 0)"); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		_, err = dst.restore(backup.RestoreOptions{SkipRevocationJournal: true})
		wantExit(t, err, backup.ExitTemporary, "migration_wait")
		if time.Since(start) < 2*time.Second {
			t.Errorf("gave up after %s, backup.database.migration_wait is 2s", time.Since(start))
		}
		if n := dst.relations(); n != 0 {
			t.Fatalf("the database holds %d relations", n)
		}
	})
}

func TestR_BAK_021_Resumption(t *testing.T) {
	src := newSource(t, 24)
	tablesOf := exportedTables(src)
	ref := src.destination()
	if _, err := ref.restore(backup.RestoreOptions{SkipRevocationJournal: true}); err != nil {
		t.Fatal(err)
	}
	want := fingerprint(ref.stack, tablesOf)

	// Each point stops the restore the way a SIGTERM does (exit 75, rolled back and resumable); the Job's next pod, a fresh
	// process, finishes it. The kill -9 variants run the trove binary (restore_kill_test.go).
	for _, point := range []string{"step2:anchored", "step2:migrated:20", "step3:copied:5", "load:items", "load:blobs", "step5", "step6", "step8", "step9"} {
		t.Run("R-BAK-021 interrupted at "+point+" and resumed ends like an uninterrupted restore", func(t *testing.T) {
			dst := src.destination()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fired := false
			r, _ := dst.restorer(restorerOptions{hook: func(ev string) {
				if ev == point && !fired {
					fired = true
					cancel()
					time.Sleep(50 * time.Millisecond)
				}
			}})
			_, err := r.Restore(ctx, backup.RestoreOptions{ID: src.id, SkipRevocationJournal: true})
			if !fired {
				t.Fatalf("the restore never reached %s", point)
			}
			wantExit(t, err, backup.ExitTemporary)
			if c := dst.stack.Text("SELECT coalesce(obj_description('trove'::regnamespace, 'pg_namespace'), '')"); c != "trove-restore "+string(src.id) {
				t.Fatalf("anchor after the interruption = %q", c)
			}
			if _, err := dst.restore(backup.RestoreOptions{SkipRevocationJournal: true}); err != nil {
				t.Fatalf("resume: %v", err)
			}
			if got := fingerprint(dst.stack, tablesOf); got != want {
				t.Fatalf("resumed restore differs:\n%s\nwant\n%s", got, want)
			}
			if n := dst.stack.Number("SELECT count(*) FROM audit_event WHERE action = 'system.restored'"); n != 1 {
				t.Errorf("%d system.restored events", n)
			}
			if got := dst.stack.Number("SELECT rev_seq FROM packages WHERE name = 'pkg'"); got != 6+(1<<32) {
				t.Errorf("rev_seq = %d: the fix-ups ran more than once", got)
			}
		})
	}

	t.Run("R-ARCH-024 a replica's schema check waits while restore_in_progress is set, and passes after the restore", func(t *testing.T) {
		dst := src.destination()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		r, _ := dst.restorer(restorerOptions{hook: func(ev string) {
			if ev == "step6" {
				cancel()
				time.Sleep(50 * time.Millisecond)
			}
		}})
		_, _ = r.Restore(ctx, backup.RestoreOptions{ID: src.id, SkipRevocationJournal: true})
		if got := dst.stack.Text("SELECT value ->> 'phase' FROM system_state WHERE key = 'restore_in_progress'"); got != "loaded" {
			t.Fatalf("phase = %q", got)
		}
		db, err := postgres.Open(ctx2(t), dst.stack.DatabaseConfig(), "it-replica", meta.RoleWeb, testenv.Quiet())
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		// The schema is at the binary's step, only the marker keeps the replica away.
		if err := db.CheckSchema(ctx2(t), "0.1.0"); !errors.Is(err, postgres.ErrRestoreInProgress) {
			t.Fatalf("CheckSchema = %v, want ErrRestoreInProgress", err)
		}
		if n := dst.stack.Number("SELECT count(*) FROM replica"); n != 0 {
			t.Errorf("%d replicas registered", n)
		}
		if _, err := dst.restore(backup.RestoreOptions{SkipRevocationJournal: true}); err != nil {
			t.Fatal(err)
		}
		if err := db.CheckSchema(ctx2(t), "0.1.0"); err != nil {
			t.Fatalf("CheckSchema after the restore = %v", err)
		}
	})
}

func ctx2(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}
