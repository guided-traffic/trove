//go:build integration

package backup

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/backup"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta/postgres"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_BAK_012_SIGTERMEndsTheRunInterrupted: a drain during copying ends the run as interrupted - pins gone, hold none, no
// manifest, exit 75, and no system.backup.failed event, because a drain is not a failure.
func TestR_BAK_012_SIGTERMEndsTheRunInterrupted(t *testing.T) {
	in := newInstall(t)
	for _, n := range []string{"a", "b"} {
		in.mustUpload(in.repo, n, randomBlob(300<<10))
	}
	r := in.newRig(t)
	cmd := exec.Command(troveBinary(t), "backup", "create", "--no-prune")
	cmd.Env = in.processEnv(t, r, "TROVE_BACKUP_COPY_SERVER_SIDE=never", "TROVE_BACKUP_COPY_BYTES_PER_SECOND=64KiB")
	var errb bytes.Buffer
	cmd.Stderr = &errb
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() { _ = cmd.Process.Kill() }()
	testenv.WaitFor(t, "the run is copying with pins left", 60*time.Second, func() bool {
		var n int64
		_ = in.admin.QueryRow(context.Background(), `SELECT count(*) FROM trove.backup_run r JOIN trove.backup_blob_pin p ON p.run_id = r.id WHERE r.state = 'copying'`).Scan(&n)
		return n > 0
	})
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	var ee *exec.ExitError
	select {
	case err := <-done:
		if !errors.As(err, &ee) || ee.ExitCode() != backup.ExitTemporary {
			t.Fatalf("exit = %v: %s", err, errb.String())
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the run did not end within the grace period")
	}
	if st := scalarText(in, `SELECT state FROM trove.backup_run`); st != "interrupted" {
		t.Fatalf("state = %s (%s)", st, errb.String())
	}
	if h := scalarText(in, `SELECT hold FROM trove.backup_run`); h != "none" {
		t.Fatalf("hold = %s", h)
	}
	if n := in.number(`SELECT count(*) FROM trove.backup_blob_pin`); n != 0 {
		t.Fatalf("pins left: %d", n)
	}
	if n := in.number(`SELECT count(*) FROM trove.audit_event WHERE action = 'system.backup.failed'`); n != 0 {
		t.Fatalf("failed events: %d", n)
	}
	if keys := listManifests(t, r); len(keys) != 0 {
		t.Fatalf("manifests: %v", keys)
	}
}

// TestR_BAK_015_TheBackupRole: with the grants of trove migrate the backup role creates a backup and cannot write the data
// tables; a role without them is refused at the start with exit 2 naming the privilege.
func TestR_BAK_015_TheBackupRole(t *testing.T) {
	ctx := context.Background()
	maint := dialDB(t, "postgres")
	granted, bare := "trove_bak_g_"+runID(), "trove_bak_b_"+runID()
	for _, role := range []string{granted, bare} {
		if _, err := maint.Exec(ctx, `CREATE ROLE `+role+` LOGIN PASSWORD 'pw'`); err != nil {
			t.Fatal(err)
		}
		role := role
		t.Cleanup(func() { _, _ = maint.Exec(context.Background(), `DROP ROLE IF EXISTS `+role) })
	}
	in := newInstall(t)
	in.mustUpload(in.repo, "a", "alpha-content")
	for _, role := range []string{granted, bare} {
		in.exec(`GRANT CONNECT ON DATABASE ` + in.dbName + ` TO ` + role)
		in.exec(`GRANT USAGE ON SCHEMA trove TO ` + role)
	}
	// The bare role can read but not write the run record: the privilege check names the first thing it lacks.
	in.exec(`GRANT SELECT ON ALL TABLES IN SCHEMA trove TO ` + bare)
	in.exec(`GRANT SELECT ON ALL SEQUENCES IN SCHEMA trove TO ` + bare)
	cfg := in.dbCfg
	cfg.BackupRole = granted
	if _, err := postgres.Migrate(ctx, cfg, postgres.MigrateOptions{
		Instance: "it-bak-grant", Version: "0.1.0", Log: quiet,
		Migrate: config.Migrate{AdvisoryLockWait: 20 * time.Second, DDLLockTimeout: 2 * time.Second, DDLLockRetries: 3},
	}); err != nil {
		t.Fatal(err)
	}
	as := func(role string) *config.Database {
		c := in.dbCfg
		dir := t.TempDir()
		c.UsernameFile, c.PasswordFile = secretFile(t, dir, "u", role), secretFile(t, dir, "p", "pw")
		c.Preflight.AllowSuperuser = false
		return &c
	}

	t.Run("the granted role creates a backup and cannot delete from a data table", func(t *testing.T) {
		r := in.newRig(t)
		r.dbCfg = as(granted)
		if _, err := in.run(ctx, r, backup.CreateOptions{NoPrune: true}); err != nil {
			t.Fatal(err)
		}
		c := dialAs(t, in, granted)
		if _, err := c.Exec(ctx, `DELETE FROM trove.items`); err == nil || !strings.Contains(err.Error(), "42501") {
			t.Fatalf("DELETE FROM items = %v, want 42501", err)
		}
	})
	t.Run("a role that cannot write the run record exits 2 and writes nothing", func(t *testing.T) {
		r := in.newRig(t)
		r.dbCfg = as(bare)
		_, err := in.run(ctx, r, backup.CreateOptions{NoPrune: true})
		if exit(err) != backup.ExitUsage || backup.ErrorCode(err) != backup.CodeBackupRoleGrant || !strings.Contains(err.Error(), "INSERT on backup_run") {
			t.Fatalf("err = %v (exit %d)", err, exit(err))
		}
		if keys := listManifests(t, r); len(keys) != 0 {
			t.Fatalf("manifests: %v", keys)
		}
	})
}
