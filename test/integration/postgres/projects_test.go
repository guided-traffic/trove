//go:build integration

package postgres

import (
	"testing"

	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/meta"
)

// TestProjectsMigration checks 00019_projects: the tables with their export class, the
// ETag column, and the guards that the database enforces whatever the API checked
// (R-AUTHZ-026, Q-AUTHZ-174 to Q-AUTHZ-176, Q-AUTHZ-183).
func TestProjectsMigration(t *testing.T) {
	st := getStack(t)
	ensureRuntimeRole(t, st)
	_, _, admin := migrated(t, st)

	t.Run("R-AUTHZ-026 the project tables exist with the export class data and project.version is a revision column", func(t *testing.T) {
		for _, name := range []string{"project", "project_admin", "authz_project_policy_entry"} {
			if got, ok := tables.Lookup(name); !ok || got.Class != tables.Data {
				t.Errorf("%s registered as %+v (%v)", name, got, ok)
			}
			if !scalar[bool](t, admin, "SELECT to_regclass($1) IS NOT NULL", schemaName+"."+name) {
				t.Errorf("table %s is not created by the migrations", name)
			}
		}
		if !meta.IsRevisionColumn("project", "version") {
			t.Error("project.version is not registered")
		}
	})

	mustExec(t, admin, "INSERT INTO trove.project (id, key, display_name) VALUES ('aaaaaaaa-0000-0000-0000-000000000001', 'web', 'Web platform')")
	mustExec(t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES ('aaaaaaaa-0000-0000-0000-000000000011', 'user', 'lead')")
	mustExec(t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES ('aaaaaaaa-0000-0000-0000-000000000012', 'group', 'web-leads')")
	mustExec(t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES ('aaaaaaaa-0000-0000-0000-000000000013', 'service_account', 'web-partner')")
	mustExec(t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES ('aaaaaaaa-0000-0000-0000-000000000014', 'service_account', 'other-bot')")

	t.Run("R-AUTHZ-026 a project has a key of the rule, defaults to managePolicies on and manageRepositories off", func(t *testing.T) {
		if got := scalar[string](t, admin, "SELECT manage_policies::text || ':' || manage_repositories::text || ':' || version FROM trove.project WHERE key = 'web'"); got != "true:false:1" {
			t.Errorf("defaults = %q", got)
		}
		for _, key := range []string{"w", "1web", "Web", "web_x", "abcdefghijklmnopqrstuvwxyz0123456789"} {
			wantState(t, admin, "23514", "INSERT INTO trove.project (id, key, display_name) VALUES (gen_random_uuid(), $1, 'x')", key)
		}
		wantState(t, admin, "23505", "INSERT INTO trove.project (id, key, display_name) VALUES (gen_random_uuid(), 'web', 'again')")
	})

	t.Run("R-AUTHZ-026 a project administrator is a user or a group", func(t *testing.T) {
		mustExec(t, admin, "INSERT INTO trove.project_admin (project_id, principal_id) VALUES ('aaaaaaaa-0000-0000-0000-000000000001', 'aaaaaaaa-0000-0000-0000-000000000011')")
		mustExec(t, admin, "INSERT INTO trove.project_admin (project_id, principal_id) VALUES ('aaaaaaaa-0000-0000-0000-000000000001', 'aaaaaaaa-0000-0000-0000-000000000012')")
		wantState(t, admin, "AI001", "INSERT INTO trove.project_admin (project_id, principal_id) VALUES ('aaaaaaaa-0000-0000-0000-000000000001', 'aaaaaaaa-0000-0000-0000-000000000013')")
		wantState(t, admin, "AI001", "INSERT INTO trove.project_admin (project_id, principal_id) VALUES ('aaaaaaaa-0000-0000-0000-000000000001', '00000000-0000-0000-0000-000000000002')")
	})

	t.Run("R-AUTHZ-026 a project service account's name starts with the project key and its project never changes", func(t *testing.T) {
		wantState(t, admin, "AI003", "INSERT INTO trove.service_account (principal_id, project_id) VALUES ('aaaaaaaa-0000-0000-0000-000000000014', 'aaaaaaaa-0000-0000-0000-000000000001')")
		mustExec(t, admin, "INSERT INTO trove.service_account (principal_id, project_id) VALUES ('aaaaaaaa-0000-0000-0000-000000000013', 'aaaaaaaa-0000-0000-0000-000000000001')")
		wantState(t, admin, "AI003", "UPDATE trove.service_account SET project_id = NULL WHERE principal_id = 'aaaaaaaa-0000-0000-0000-000000000013'")
		// A global service account stays global.
		mustExec(t, admin, "INSERT INTO trove.service_account (principal_id) VALUES ('aaaaaaaa-0000-0000-0000-000000000014')")
	})

	t.Run("R-AUTHZ-026 a project service account is no group member", func(t *testing.T) {
		mustExec(t, admin, "INSERT INTO trove.user_group (principal_id, origin) VALUES ('aaaaaaaa-0000-0000-0000-000000000012', 'admin')")
		wantState(t, admin, "AI002", "INSERT INTO trove.group_membership (group_id, member_id, source) VALUES ('aaaaaaaa-0000-0000-0000-000000000012', 'aaaaaaaa-0000-0000-0000-000000000013', 'manual')")
		mustExec(t, admin, "INSERT INTO trove.group_membership (group_id, member_id, source) VALUES ('aaaaaaaa-0000-0000-0000-000000000012', 'aaaaaaaa-0000-0000-0000-000000000014', 'manual')")
	})

	t.Run("R-AUTHZ-026 the grants of a project policy are in their own table and refused in the old one", func(t *testing.T) {
		mustExec(t, admin, "INSERT INTO trove.authz_policy (id, name, project_id) VALUES ('aaaaaaaa-0000-0000-0000-000000000021', 'web-npm-read', 'aaaaaaaa-0000-0000-0000-000000000001')")
		mustExec(t, admin, "INSERT INTO trove.authz_policy (id, name) VALUES ('aaaaaaaa-0000-0000-0000-000000000022', 'global-read')")
		mustExec(t, admin, `INSERT INTO trove.authz_project_policy_entry (policy_id, project_id, principal_id, actions)
			VALUES ('aaaaaaaa-0000-0000-0000-000000000021', 'aaaaaaaa-0000-0000-0000-000000000001', 'aaaaaaaa-0000-0000-0000-000000000013', ARRAY['read'])`)
		// What the release before projects does: it reads authz_policy_entry only and finds nothing; a write of
		// the entry of a project policy there fails instead of making the policy global (Q-AUTHZ-174).
		if n := scalar[int](t, admin, "SELECT count(*) FROM trove.authz_policy_entry WHERE policy_id = 'aaaaaaaa-0000-0000-0000-000000000021'"); n != 0 {
			t.Errorf("entries of the project policy where the previous release reads = %d", n)
		}
		wantState(t, admin, "AI004", "INSERT INTO trove.authz_policy_entry (policy_id, principal_id, actions) VALUES ('aaaaaaaa-0000-0000-0000-000000000021', 'aaaaaaaa-0000-0000-0000-000000000011', ARRAY['read'])")
		mustExec(t, admin, "INSERT INTO trove.authz_policy_entry (policy_id, principal_id, actions) VALUES ('aaaaaaaa-0000-0000-0000-000000000022', 'aaaaaaaa-0000-0000-0000-000000000011', ARRAY['read'])")
		// The table of project grants holds rows of project policies only.
		wantState(t, admin, "23503", `INSERT INTO trove.authz_project_policy_entry (policy_id, project_id, principal_id, actions)
			VALUES ('aaaaaaaa-0000-0000-0000-000000000022', 'aaaaaaaa-0000-0000-0000-000000000001', 'aaaaaaaa-0000-0000-0000-000000000011', ARRAY['read'])`)
		wantState(t, admin, "23514", `INSERT INTO trove.authz_project_policy_entry (policy_id, project_id, principal_id, actions)
			VALUES ('aaaaaaaa-0000-0000-0000-000000000021', 'aaaaaaaa-0000-0000-0000-000000000001', 'aaaaaaaa-0000-0000-0000-000000000011', ARRAY['write'])`)
	})

	t.Run("R-AUTHZ-026 a project with a repository or a service account cannot be deleted, and its policies go with it", func(t *testing.T) {
		mustExec(t, admin, "INSERT INTO trove.repositories (key, format, class, project_id) VALUES ('web-npm', 'npm', 'local', 'aaaaaaaa-0000-0000-0000-000000000001')")
		wantState(t, admin, "23503", "DELETE FROM trove.project WHERE key = 'web'")
		mustExec(t, admin, "DELETE FROM trove.repositories WHERE key = 'web-npm'")
		wantState(t, admin, "23503", "DELETE FROM trove.project WHERE key = 'web'") // the service account remains
		mustExec(t, admin, "DELETE FROM trove.service_account WHERE principal_id = 'aaaaaaaa-0000-0000-0000-000000000013'")
		mustExec(t, admin, "DELETE FROM trove.project WHERE key = 'web'")
		for _, q := range []string{
			"SELECT count(*) FROM trove.authz_policy WHERE name = 'web-npm-read'",
			"SELECT count(*) FROM trove.authz_project_policy_entry",
			"SELECT count(*) FROM trove.project_admin",
		} {
			if n := scalar[int](t, admin, q); n != 0 {
				t.Errorf("%s = %d after the project went", q, n)
			}
		}
	})
}
