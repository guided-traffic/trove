//go:build integration

package postgres

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_AUTHZ_010_MigrateCommandAppliesTheDeclaredBindings runs the binary: `trove
// migrate up` reads authz.role_bindings from its configuration file, as the chart's
// migration Job does, and applies it after the schema steps (R-AUTHZ-010,
// Q-AUTHZ-170).
func TestR_AUTHZ_010_MigrateCommandAppliesTheDeclaredBindings(t *testing.T) {
	st := getStack(t)
	bin := buildTrove(t)
	name, admin := st.newDatabase(t)
	cfg := st.config(t, name)
	env := []string{
		"TROVE_DATABASE_HOST=" + st.host, "TROVE_DATABASE_PORT=" + itoa(st.port), "TROVE_DATABASE_NAME=" + name,
		"TROVE_DATABASE_USERNAME_FILE=" + cfg.UsernameFile, "TROVE_DATABASE_PASSWORD_FILE=" + cfg.PasswordFile,
		"TROVE_DATABASE_SSL_MODE=verify-full", "TROVE_DATABASE_SSL_ROOT_CERT_FILE=" + testenv.CAFile(t),
		"TROVE_DATABASE_PREFLIGHT_ALLOW_SUPERUSER=true", "TROVE_POD_NAME=trove-migrate-abc",
	}
	dir := t.TempDir()
	file := func(t *testing.T, content string) string {
		t.Helper()
		p := filepath.Join(dir, strings.ReplaceAll(t.Name(), "/", "_")+".yaml")
		if err := writeFile(p, content); err != nil {
			t.Fatal(err)
		}
		return p
	}
	owner := `authz:
  role_bindings:
    - role: admin
      group: gh-wds18-k8s-main-admins
`

	t.Run("R-AUTHZ-010 trove migrate up creates the schema, the declared group and its binding", func(t *testing.T) {
		code, stdout, stderr := runTrove(t, bin, env, "migrate", "up", "--config", file(t, owner))
		if code != 0 {
			t.Fatalf("exit %d\nstdout %s\nstderr %s", code, stdout, stderr)
		}
		if !strings.Contains(stderr, `"msg":"role bindings of the configuration applied"`) || !strings.Contains(stderr, "gh-wds18-k8s-main-admins") {
			t.Fatalf("log lines: %s", stderr)
		}
		got := scalar[string](t, admin, `SELECT b.source FROM trove.authz_role_binding b JOIN trove.principal p ON p.id = b.principal_id
		                                 WHERE b.role = 'admin' AND p.kind = 'group' AND p.name = 'gh-wds18-k8s-main-admins'`)
		if got != "configuration" {
			t.Fatalf("source %q", got)
		}
		if n := scalar[int64](t, admin, `SELECT count(*) FROM trove.audit_event WHERE action = 'authz.role.bound'
		                                  AND actor_type = 'system' AND actor_name = 'configuration' AND replica = 'trove-migrate-abc'`); n != 1 {
			t.Fatalf("%d audit events", n)
		}
	})

	t.Run("R-AUTHZ-010 a second run with nothing to migrate applies the declaration again and changes nothing", func(t *testing.T) {
		epoch := scalar[int64](t, admin, "SELECT epoch FROM trove.cluster_epoch WHERE area = 'authz'")
		code, stdout, stderr := runTrove(t, bin, env, "migrate", "up", "--config", file(t, owner))
		if code != 0 || !strings.Contains(stdout, `"nothingToDo": true`) {
			t.Fatalf("exit %d stdout %s stderr %s", code, stdout, stderr)
		}
		if now := scalar[int64](t, admin, "SELECT epoch FROM trove.cluster_epoch WHERE area = 'authz'"); now != epoch {
			t.Fatalf("epoch %d -> %d", epoch, now)
		}
	})

	t.Run("R-AUTHZ-010 a declared user that does not exist makes trove migrate up fail and name it", func(t *testing.T) {
		code, _, stderr := runTrove(t, bin, env, "migrate", "up", "--config", file(t, owner+`    - role: auditor
      user: nobody
`))
		if code != 1 || !strings.Contains(stderr, "authz.role_bindings[1]: user:nobody does not exist") {
			t.Fatalf("exit %d stderr %s", code, stderr)
		}
	})

	t.Run("R-AUTHZ-010 an unknown role is refused at load, naming the key, before the database is touched", func(t *testing.T) {
		code, _, stderr := runTrove(t, bin, env, "migrate", "up", "--config", file(t, "authz:\n  role_bindings:\n    - {role: root, group: ops}\n"))
		if code != 1 || !strings.Contains(stderr, `authz.role_bindings[0].role "root" is not a system role`) {
			t.Fatalf("exit %d stderr %s", code, stderr)
		}
	})

	t.Run("R-AUTHZ-010 an empty declaration removes the configuration binding", func(t *testing.T) {
		// The installation has no other administrator; the break-glass administrator
		// is active, so the last_admin guard lets the binding go.
		bg := scalar[string](t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES (gen_random_uuid(), 'user', 'admin') RETURNING id::text")
		if _, err := admin.Exec(t.Context(), "INSERT INTO trove.app_user (principal_id, origin, is_break_glass) VALUES ($1::uuid, 'system', true)", bg); err != nil {
			t.Fatal(err)
		}
		code, _, stderr := runTrove(t, bin, env, "migrate", "up", "--config", file(t, ""))
		if code != 0 {
			t.Fatalf("exit %d stderr %s", code, stderr)
		}
		if n := scalar[int64](t, admin, "SELECT count(*) FROM trove.authz_role_binding"); n != 0 {
			t.Fatalf("%d bindings left", n)
		}
	})
}
