//go:build integration

package backup

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

func (in *install) migrateWithRole(t testing.TB, cfg config.Database, role string) {
	t.Helper()
	cfg.BackupRole = role
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := postgres.Migrate(ctx, cfg, postgres.MigrateOptions{
		Instance: "it-bak-grant", Version: "0.1.0", Log: quiet,
		Migrate: config.Migrate{AdvisoryLockWait: 20 * time.Second, DDLLockTimeout: 2 * time.Second, DDLLockRetries: 3},
	}); err != nil {
		t.Fatalf("migrate with backup role %q: %v", role, err)
	}
}

func createRole(t testing.TB, maint *pgx.Conn, prefix string) string {
	t.Helper()
	role := "trove_bak_" + prefix + "_" + runID()
	if _, err := maint.Exec(context.Background(), `CREATE ROLE `+role+` LOGIN PASSWORD 'pw'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = maint.Exec(context.Background(), `DROP ROLE IF EXISTS `+role) })
	return role
}

func (in *install) can(role, sql string, args ...any) bool {
	in.t.Helper()
	var ok bool
	if err := in.admin.QueryRow(context.Background(), sql, append([]any{role}, args...)...).Scan(&ok); err != nil {
		in.t.Fatalf("%s: %v", sql, err)
	}
	return ok
}

// TestR_BAK_015_TheBackupLoginWritesItsOwnAuditEventsAndNoOthers: the backup login has no INSERT on the audit trail; it calls a
// function that writes the four events of the lane as the actor system:backup.
func TestR_BAK_015_TheBackupLoginWritesItsOwnAuditEventsAndNoOthers(t *testing.T) {
	ctx := context.Background()
	maint := dialDB(t, "postgres")
	role := createRole(t, maint, "a")
	in := newInstall(t)
	in.exec(`GRANT CONNECT ON DATABASE ` + in.dbName + ` TO ` + role)
	in.exec(`GRANT USAGE ON SCHEMA trove TO ` + role)
	// A release that granted INSERT on the audit trail left it on the role: the next migration run takes it back.
	in.exec(`GRANT INSERT ON trove.audit_event TO ` + role)
	in.migrateWithRole(t, in.dbCfg, role)
	if in.can(role, `SELECT has_table_privilege($1, 'trove.audit_event', 'INSERT')`) {
		t.Fatal("the backup role still holds INSERT on audit_event")
	}

	c := dialAs(t, in, role)
	if _, err := c.Exec(ctx, `INSERT INTO trove.audit_event (id, occurred_at, action, category, outcome, actor_type) VALUES (gen_random_uuid(), now(), 'authz.role.bound', 'authz', 'success', 'user')`); err == nil || !strings.Contains(err.Error(), "42501") {
		t.Fatalf("a direct insert = %v, want 42501", err)
	}
	for _, bad := range [][]any{{"authz.role.bound", "success"}, {"system.restored", "success"}, {"system.backup.completed", "denied"}} {
		if _, err := c.Exec(ctx, `SELECT trove.backup_record_event($1, $2, 'pod', '', '{}')`, bad...); err == nil || !strings.Contains(err.Error(), "42501") {
			t.Fatalf("backup_record_event(%v) = %v, want 42501", bad, err)
		}
	}
	if _, err := c.Exec(ctx, `SELECT trove.backup_record_event('system.backup.completed', 'success', 'pod-1', '', '{"backupId": "x"}')`); err != nil {
		t.Fatal(err)
	}
	if got := scalarText(in, `SELECT actor_type || '/' || actor_name || '/' || category || '/' || replica FROM trove.audit_event WHERE action = 'system.backup.completed'`); got != "system/backup/system/pod-1" {
		t.Fatalf("event = %s", got)
	}
}

// TestR_BAK_015_ChangingTheBackupRoleTakesBackWhatTroveGranted: grants of a role that database.backup_role no longer names are
// revoked by the next migration run, down to none when the setting is emptied.
func TestR_BAK_015_ChangingTheBackupRoleTakesBackWhatTroveGranted(t *testing.T) {
	maint := dialDB(t, "postgres")
	first, second := createRole(t, maint, "f"), createRole(t, maint, "s")
	in := newInstall(t)
	holds := func(role string) bool {
		return in.can(role, `SELECT has_table_privilege($1, 'trove.items', 'SELECT') OR has_table_privilege($1, 'trove.backup_run', 'INSERT')
			OR has_function_privilege($1, 'trove.backup_record_event(text,text,text,text,jsonb)', 'EXECUTE')`)
	}
	in.migrateWithRole(t, in.dbCfg, first)
	if !holds(first) || scalarText(in, `SELECT value #>> '{}' FROM trove.system_state WHERE key = 'backup_role'`) != first {
		t.Fatal("the first role was not granted or not recorded")
	}
	in.migrateWithRole(t, in.dbCfg, second)
	if holds(first) {
		t.Fatal("the former backup role keeps its grants")
	}
	if !holds(second) || scalarText(in, `SELECT value #>> '{}' FROM trove.system_state WHERE key = 'backup_role'`) != second {
		t.Fatal("the new role was not granted or not recorded")
	}
	in.migrateWithRole(t, in.dbCfg, "")
	if holds(second) || in.number(`SELECT count(*) FROM trove.system_state WHERE key = 'backup_role'`) != 0 {
		t.Fatal("emptying the setting must revoke the last role and forget it")
	}
}

// TestR_BAK_015_DefaultPrivilegesReachTablesTheMigrationLoginCreatesLater: with a migration login that owns the schema but is no
// superuser, the tables it creates after the grants, and the audit partitions its SECURITY DEFINER function attaches, are
// readable by the backup role (ALTER DEFAULT PRIVILEGES without FOR ROLE applies to the login that ran it).
func TestR_BAK_015_DefaultPrivilegesReachTablesTheMigrationLoginCreatesLater(t *testing.T) {
	ctx := context.Background()
	maint := dialDB(t, "postgres")
	owner, bk := createRole(t, maint, "o"), createRole(t, maint, "b")
	name := "trove_bak_" + runID()
	if _, err := maint.Exec(ctx, `CREATE DATABASE `+name+` OWNER `+owner+` ENCODING 'UTF8' TEMPLATE template0`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = maint.Exec(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`) })
	in := &install{t: t, dbName: name}
	cfg := pgConfig(t, t.TempDir(), name)
	dir := t.TempDir()
	cfg.UsernameFile, cfg.PasswordFile = secretFile(t, dir, "u", owner), secretFile(t, dir, "p", "pw")
	cfg.Preflight.AllowSuperuser = false
	in.migrateWithRole(t, cfg, bk)

	admin := dialDB(t, name)
	in.admin = admin
	o := dialAs(t, in, owner)
	if _, err := o.Exec(ctx, `SET search_path TO trove`); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Exec(ctx, `CREATE TABLE later (a int)`); err != nil {
		t.Fatal(err)
	}
	if _, err := o.Exec(ctx, `SELECT audit_attach_partition('2031-01-01'::timestamptz)`); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"trove.later", "trove.audit_event_203101"} {
		if !in.can(bk, `SELECT has_table_privilege($1, '`+rel+`', 'SELECT')`) {
			t.Errorf("the backup role cannot read %s, created after the grants", rel)
		}
	}
}
