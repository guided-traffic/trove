//go:build integration

package postgres

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// TestProjectsMigration checks 00019_projects to 00025 on the project model (R-AUTHZ-026, Q-AUTHZ-206 to
// Q-AUTHZ-209, Q-AUTHZ-174, Q-AUTHZ-175, Q-REPO-212): the tables with their export class, the ETag column, and the
// guards that the database enforces whatever the API checked.
func TestProjectsMigration(t *testing.T) {
	st := getStack(t)
	ensureRuntimeRole(t, st)
	_, _, admin := migrated(t, st)

	t.Run("R-AUTHZ-026 the project tables exist with the export class data and project.version is a revision column", func(t *testing.T) {
		for _, name := range []string{"project", "project_member", "authz_project_policy_entry"} {
			if got, ok := tables.Lookup(name); !ok || got.Class != tables.Data {
				t.Errorf("%s registered as %+v (%v)", name, got, ok)
			}
			if !scalar[bool](t, admin, "SELECT to_regclass($1) IS NOT NULL", schemaName+"."+name) {
				t.Errorf("table %s is not created by the migrations", name)
			}
		}
		if scalar[bool](t, admin, "SELECT to_regclass($1) IS NOT NULL", schemaName+".project_admin") {
			t.Error("the table of project administrators of the old model exists")
		}
		if !meta.IsRevisionColumn("project", "version") {
			t.Error("project.version is not registered")
		}
	})

	t.Run("R-HA-018 the project steps are expand steps: indexes concurrently, keys validated, the unique constraints from their indexes", func(t *testing.T) {
		steps, err := postgres.EmbeddedSteps()
		if err != nil {
			t.Fatal(err)
		}
		seen := 0
		for _, s := range steps {
			if s.Version < 19 || s.Version > 25 {
				continue
			}
			seen++
			if s.Phase != "expand" || s.RequiresApp != "" {
				t.Errorf("%s: phase %q, requires-app %q", s.Name, s.Phase, s.RequiresApp)
			}
			if s.NoTx && !strings.Contains(strings.ToUpper(s.SQL), "CONCURRENTLY") {
				t.Errorf("%s is a no-transaction step without CONCURRENTLY", s.Name)
			}
		}
		if seen != 7 {
			t.Errorf("project steps = %d, want 7 (00019 to 00025)", seen)
		}
		if n := scalar[int](t, admin, "SELECT count(*) FROM pg_constraint WHERE contype = 'f' AND NOT convalidated AND connamespace = $1::regnamespace", schemaName); n != 0 {
			t.Errorf("%d foreign keys are still NOT VALID", n)
		}
		for _, idx := range []string{"repositories_project_key", "service_account_project", "authz_policy_project", "authz_policy_id_project"} {
			if !scalar[bool](t, admin, "SELECT indisvalid FROM pg_index WHERE indexrelid = to_regclass($1)", schemaName+"."+idx) {
				t.Errorf("index %s is missing or invalid", idx)
			}
		}
		for _, c := range []string{"authz_policy_id_project", "repositories_project_key"} {
			if got := scalar[string](t, admin, "SELECT contype::text FROM pg_constraint WHERE conname = $1", c); got != "u" {
				t.Errorf("%s is %q, want a unique constraint", c, got)
			}
		}
	})

	const web, mobile = "aaaaaaaa-0000-0000-0000-000000000001", "aaaaaaaa-0000-0000-0000-000000000002"
	const lead, leads, partner, global = "aaaaaaaa-0000-0000-0000-000000000011", "aaaaaaaa-0000-0000-0000-000000000012",
		"aaaaaaaa-0000-0000-0000-000000000013", "aaaaaaaa-0000-0000-0000-000000000014"
	mustExec(t, admin, "INSERT INTO trove.project (id, key, display_name) VALUES ($1, 'web', 'Web platform')", web)
	mustExec(t, admin, "INSERT INTO trove.project (id, key, display_name) VALUES ($1, 'mobile', 'Mobile')", mobile)
	mustExec(t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES ($1, 'user', 'lead')", lead)
	mustExec(t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES ($1, 'group', 'web-leads')", leads)
	mustExec(t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES ($1, 'service_account', 'web/partner')", partner)
	mustExec(t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES ($1, 'service_account', 'partner')", global)

	t.Run("R-AUTHZ-026 a project has a key of the rule, unique and immutable, and no switches", func(t *testing.T) {
		if got := scalar[string](t, admin, "SELECT version::text FROM trove.project WHERE key = 'web'"); got != "1" {
			t.Errorf("version = %q", got)
		}
		if scalar[bool](t, admin, "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = $1 AND table_name = 'project' AND column_name IN ('manage_policies', 'manage_repositories'))", schemaName) {
			t.Error("the switches of the old model exist")
		}
		for _, key := range []string{"w", "1web", "Web", "web_x", "web/x", "abcdefghijklmnopqrstuvwxyz0123456789"} {
			wantState(t, admin, "23514", "INSERT INTO trove.project (id, key, display_name) VALUES (gen_random_uuid(), $1, 'x')", key)
		}
		wantState(t, admin, "23505", "INSERT INTO trove.project (id, key, display_name) VALUES (gen_random_uuid(), 'web', 'again')")
		wantState(t, admin, "AI003", "UPDATE trove.project SET key = 'webs' WHERE key = 'web'")
	})

	t.Run("R-AUTHZ-026 Q-AUTHZ-207 a member is a user or a group with one of the four roles, once per project", func(t *testing.T) {
		mustExec(t, admin, "INSERT INTO trove.project_member (project_id, principal_id, role) VALUES ($1, $2, 'owner')", web, lead)
		mustExec(t, admin, "INSERT INTO trove.project_member (project_id, principal_id, role) VALUES ($1, $2, 'developer')", web, leads)
		mustExec(t, admin, "INSERT INTO trove.project_member (project_id, principal_id, role) VALUES ($1, $2, 'reader')", mobile, leads)
		wantState(t, admin, "23505", "INSERT INTO trove.project_member (project_id, principal_id, role) VALUES ($1, $2, 'reader')", web, lead)
		wantState(t, admin, "23514", "INSERT INTO trove.project_member (project_id, principal_id, role) VALUES ($1, $2, 'admin')", mobile, lead)
		wantState(t, admin, "AI001", "INSERT INTO trove.project_member (project_id, principal_id, role) VALUES ($1, $2, 'reader')", web, partner)
		wantState(t, admin, "AI001", "INSERT INTO trove.project_member (project_id, principal_id, role) VALUES ($1, '00000000-0000-0000-0000-000000000002', 'reader')", web)
	})

	t.Run("R-REPO-002 Q-AUTHZ-206 a repository belongs to a project and its key is unique within it", func(t *testing.T) {
		mustExec(t, admin, "INSERT INTO trove.repositories (key, format, class, project_id) VALUES ('npm', 'npm', 'local', $1)", web)
		mustExec(t, admin, "INSERT INTO trove.repositories (key, format, class, project_id) VALUES ('npm', 'npm', 'local', $1)", mobile)
		wantState(t, admin, "23505", "INSERT INTO trove.repositories (key, format, class, project_id) VALUES ('npm', 'npm', 'local', $1)", web)
		wantState(t, admin, "23502", "INSERT INTO trove.repositories (key, format, class) VALUES ('loose', 'npm', 'local')")
	})

	t.Run("R-AUTHZ-026 Q-AUTHZ-209 a project service account is named <project>/<name>, a global one has no slash, and the project never changes", func(t *testing.T) {
		wantState(t, admin, "AI003", "INSERT INTO trove.service_account (principal_id, project_id) VALUES ($1, $2)", partner, mobile)
		wantState(t, admin, "AI003", "INSERT INTO trove.service_account (principal_id) VALUES ($1)", partner)
		wantState(t, admin, "AI003", "INSERT INTO trove.service_account (principal_id, project_id) VALUES ($1, $2)", global, web)
		mustExec(t, admin, "INSERT INTO trove.service_account (principal_id, project_id) VALUES ($1, $2)", partner, web)
		mustExec(t, admin, "INSERT INTO trove.service_account (principal_id) VALUES ($1)", global)
		wantState(t, admin, "AI003", "UPDATE trove.service_account SET project_id = $2 WHERE principal_id = $1", partner, mobile)
	})

	t.Run("Q-AUTHZ-175 a project service account is no group member", func(t *testing.T) {
		mustExec(t, admin, "INSERT INTO trove.user_group (principal_id, origin) VALUES ($1, 'admin')", leads)
		wantState(t, admin, "AI002", "INSERT INTO trove.group_membership (group_id, member_id, source) VALUES ($1, $2, 'manual')", leads, partner)
		mustExec(t, admin, "INSERT INTO trove.group_membership (group_id, member_id, source) VALUES ($1, $2, 'manual')", leads, global)
	})

	const webRead, globalRead = "aaaaaaaa-0000-0000-0000-000000000021", "aaaaaaaa-0000-0000-0000-000000000022"
	t.Run("Q-AUTHZ-209 a project policy is named <project>/<name>; the same name exists in two projects and once instance-wide", func(t *testing.T) {
		mustExec(t, admin, "INSERT INTO trove.authz_policy (id, name, project_id) VALUES ($1, 'web/npm-read', $2)", webRead, web)
		mustExec(t, admin, "INSERT INTO trove.authz_policy (id, name, project_id) VALUES (gen_random_uuid(), 'mobile/npm-read', $1)", mobile)
		mustExec(t, admin, "INSERT INTO trove.authz_policy (id, name) VALUES ($1, 'npm-read')", globalRead)
		wantState(t, admin, "AI003", "INSERT INTO trove.authz_policy (id, name, project_id) VALUES (gen_random_uuid(), 'npm-write', $1)", web)
		wantState(t, admin, "AI003", "INSERT INTO trove.authz_policy (id, name, project_id) VALUES (gen_random_uuid(), 'mobile/npm-write', $1)", web)
		wantState(t, admin, "AI003", "INSERT INTO trove.authz_policy (id, name) VALUES (gen_random_uuid(), 'web/npm-write')")
		wantState(t, admin, "AI003", "UPDATE trove.authz_policy SET project_id = $2 WHERE id = $1", webRead, mobile)
	})

	t.Run("Q-AUTHZ-174 the grants of a project policy are in their own table and refused in the global one", func(t *testing.T) {
		mustExec(t, admin, `INSERT INTO trove.authz_project_policy_entry (policy_id, project_id, principal_id, actions) VALUES ($1, $2, $3, ARRAY['read'])`, webRead, web, partner)
		wantState(t, admin, "AI004", "INSERT INTO trove.authz_policy_entry (policy_id, principal_id, actions) VALUES ($1, $2, ARRAY['read'])", webRead, lead)
		mustExec(t, admin, "INSERT INTO trove.authz_policy_entry (policy_id, principal_id, actions) VALUES ($1, $2, ARRAY['read'])", globalRead, lead)
		// The table of project grants holds rows of project policies only, with the actions of R-AUTHZ-006.
		wantState(t, admin, "23503", `INSERT INTO trove.authz_project_policy_entry (policy_id, project_id, principal_id, actions) VALUES ($1, $2, $3, ARRAY['read'])`, globalRead, web, lead)
		wantState(t, admin, "23514", `INSERT INTO trove.authz_project_policy_entry (policy_id, project_id, principal_id, actions) VALUES ($1, $2, $3, ARRAY['write'])`, webRead, web, lead)
	})

	t.Run("Q-AUTHZ-175 a project service account is the principal of no global grant and of a project policy of its own project only", func(t *testing.T) {
		wantState(t, admin, "AI005", "INSERT INTO trove.authz_role_binding (role, principal_id) VALUES ('auditor', $1)", partner)
		wantState(t, admin, "AI005", "INSERT INTO trove.authz_policy_entry (policy_id, principal_id, actions) VALUES ($1, $2, ARRAY['read'])", globalRead, partner)
		mustExec(t, admin, "INSERT INTO trove.authz_role_binding (role, principal_id) VALUES ('auditor', $1)", global)
		mobileRead := scalar[string](t, admin, "SELECT id::text FROM trove.authz_policy WHERE name = 'mobile/npm-read'")
		wantState(t, admin, "AI005", `INSERT INTO trove.authz_project_policy_entry (policy_id, project_id, principal_id, actions) VALUES ($1, $2, $3, ARRAY['read'])`, mobileRead, mobile, partner)
		mustExec(t, admin, `INSERT INTO trove.authz_project_policy_entry (policy_id, project_id, principal_id, actions) VALUES ($1, $2, $3, ARRAY['read'])`, mobileRead, mobile, global)
		mustExec(t, admin, "DELETE FROM trove.authz_role_binding WHERE principal_id = $1", global)
	})

	t.Run("Q-AUTHZ-206 a virtual repository names repositories of its own project only", func(t *testing.T) {
		mustExec(t, admin, "INSERT INTO trove.repositories (key, format, class, project_id) VALUES ('npm-all', 'npm', 'virtual', $1)", web)
		wantState(t, admin, "AI006", `INSERT INTO trove.virtual_members (virtual_id, position, member_id)
			SELECT v.id, 0, m.id FROM trove.repositories v, trove.repositories m WHERE v.key = 'npm-all' AND m.key = 'npm' AND m.project_id = $1`, mobile)
		mustExec(t, admin, `INSERT INTO trove.virtual_members (virtual_id, position, member_id)
			SELECT v.id, 0, m.id FROM trove.repositories v, trove.repositories m WHERE v.key = 'npm-all' AND m.key = 'npm' AND m.project_id = $1`, web)
	})

	t.Run("R-REPO-060 a repository its project holds by ID keeps its project, and a project policy's key selector names its own project (AI007)", func(t *testing.T) {
		wantState(t, admin, "AI007", "UPDATE trove.repositories SET project_id = $1 WHERE key = 'npm' AND project_id = $2", mobile, web)
		wantState(t, admin, "AI007", "UPDATE trove.repositories SET project_id = $1 WHERE key = 'npm-all' AND project_id = $2", mobile, web)
		wantState(t, admin, "AI007", `INSERT INTO trove.authz_policy_selector (policy_id, ordinal, kind, repository_id)
			SELECT $1::uuid, 90, 'key', id FROM trove.repositories WHERE key = 'npm' AND project_id = $2`, webRead, mobile)
		mustExec(t, admin, `INSERT INTO trove.authz_policy_selector (policy_id, ordinal, kind, repository_id)
			SELECT $1::uuid, 90, 'key', id FROM trove.repositories WHERE key = 'npm' AND project_id = $2`, webRead, web)
		mustExec(t, admin, `INSERT INTO trove.authz_policy_selector (policy_id, ordinal, kind, repository_id)
			SELECT $1::uuid, 90, 'key', id FROM trove.repositories WHERE key = 'npm' AND project_id = $2`, globalRead, mobile)
		mustExec(t, admin, "DELETE FROM trove.authz_policy_selector WHERE ordinal = 90")
	})

	t.Run("R-AUTHZ-026 a project with a repository or a service account cannot be deleted; its policies and members go with it", func(t *testing.T) {
		wantState(t, admin, "23503", "DELETE FROM trove.project WHERE key = 'web'")
		mustExec(t, admin, "DELETE FROM trove.virtual_members")
		mustExec(t, admin, "DELETE FROM trove.repositories WHERE project_id = $1", web)
		wantState(t, admin, "23503", "DELETE FROM trove.project WHERE key = 'web'") // the service account remains
		mustExec(t, admin, "DELETE FROM trove.service_account WHERE principal_id = $1", partner)
		mustExec(t, admin, "DELETE FROM trove.project WHERE key = 'web'")
		for _, q := range []string{
			"SELECT count(*) FROM trove.authz_policy WHERE name = 'web/npm-read'",
			"SELECT count(*) FROM trove.authz_project_policy_entry WHERE project_id = 'aaaaaaaa-0000-0000-0000-000000000001'",
			"SELECT count(*) FROM trove.project_member WHERE project_id = 'aaaaaaaa-0000-0000-0000-000000000001'",
		} {
			if n := scalar[int](t, admin, q); n != 0 {
				t.Errorf("%s = %d after the project went", q, n)
			}
		}
		if n := scalar[int](t, admin, "SELECT count(*) FROM trove.project_member WHERE project_id = $1", mobile); n != 1 {
			t.Errorf("the members of another project went too: %d left", n)
		}
	})
}
