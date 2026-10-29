//go:build integration

package postgres

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// ensureRuntimeRole creates the two-role setup's runtime login (trove_app,
// R-HA-014) if the cluster has none. Roles are cluster-wide and the databases of
// other tests hold privileges on it, so the role is never dropped; creating it
// early only makes later migrations of other databases grant and revoke on it,
// which is what a two-role deployment does.
func ensureRuntimeRole(t *testing.T, st stack) {
	t.Helper()
	admin := st.admin(t, "postgres")
	ctx := ctxT(t, 10*time.Second)
	for range 5 {
		_, err := admin.Exec(ctx, `DO $$ BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'trove_app') THEN
				CREATE ROLE trove_app LOGIN PASSWORD 'trove-app-it';
			END IF;
		END $$`)
		if err == nil {
			_, err = admin.Exec(ctx, `ALTER ROLE trove_app LOGIN PASSWORD 'trove-app-it'`)
		}
		if err == nil {
			return
		}
		time.Sleep(100 * time.Millisecond) // a parallel package created it at the same moment
	}
	t.Fatal("could not create the runtime role trove_app")
}

func TestBookkeepingPrivileges(t *testing.T) {
	st := getStack(t)
	ensureRuntimeRole(t, st)
	cfg, _, admin := migrated(t, st)

	priv := func(table, p string) bool {
		return scalar[bool](t, admin, `SELECT has_table_privilege('trove_app', 'trove.`+table+`', '`+p+`')`)
	}
	t.Run("R-HA-018 the runtime role reads the migration bookkeeping and cannot write it", func(t *testing.T) {
		for _, table := range []string{"schema_migration", "schema_state"} {
			if !priv(table, "SELECT") {
				t.Errorf("trove_app cannot read %s", table)
			}
			for _, p := range []string{"INSERT", "UPDATE", "DELETE", "TRUNCATE"} {
				if priv(table, p) {
					t.Errorf("trove_app holds %s on %s", p, table)
				}
			}
		}
	})
	t.Run("R-HA-018 system_state and the data tables stay writable for the runtime role", func(t *testing.T) {
		for _, table := range []string{"system_state", "repositories", "job", "cluster_epoch"} {
			for _, p := range []string{"INSERT", "UPDATE", "DELETE"} {
				if !priv(table, p) {
					t.Errorf("trove_app lacks %s on %s", p, table)
				}
			}
		}
	})

	runtime := func(t *testing.T) error {
		t.Helper()
		dir := t.TempDir()
		user, pass := filepath.Join(dir, "username"), filepath.Join(dir, "password")
		if err := os.WriteFile(user, []byte("trove_app\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(pass, []byte("trove-app-it\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		c := cfg
		c.UsernameFile, c.PasswordFile = user, pass
		c.Preflight.AllowSuperuser = false
		db, err := postgres.Open(ctxT(t, 20*time.Second), c, "pod-runtime", meta.RoleWeb, quiet)
		if err == nil {
			db.Close()
		}
		return err
	}
	t.Run("R-HA-014 the preflight of the runtime role passes while the bookkeeping is read-only", func(t *testing.T) {
		if err := runtime(t); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-HA-014 the preflight refuses a runtime role that was given write access again, naming the table", func(t *testing.T) {
		scalarExec := func(sql string) {
			t.Helper()
			if _, err := admin.Exec(ctxT(t, 10*time.Second), sql); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
		}
		scalarExec(`GRANT UPDATE ON trove.schema_state TO trove_app`)
		defer scalarExec(`REVOKE UPDATE ON trove.schema_state FROM trove_app`)
		err := runtime(t)
		if err == nil || !strings.Contains(err.Error(), "schema_state") || !strings.Contains(err.Error(), "REVOKE") {
			t.Fatalf("err = %v", err)
		}
		if strings.Contains(err.Error(), "schema_migration") {
			t.Fatalf("the error names a table that is not writable: %v", err)
		}
	})
}
