//go:build integration

package postgres

import (
	"testing"

	"github.com/guided-traffic/trove/internal/backup/tables"
)

// TestVisibilityMigrations checks 00073_visibility, 00074_external_users and 00075_principal_builtins (R-AUTHZ-033, R-AUTH-044,
// Q-AUTHZ-276, Q-AUTHZ-279): the columns and the rules of the container in the database, the external flag, and the guards that
// keep a grant from naming the anonymous principal.
func TestVisibilityMigrations(t *testing.T) {
	st := getStack(t)
	ensureRuntimeRole(t, st)
	_, _, admin := migrated(t, st)

	t.Run("R-AUTHZ-033 everything is private after the step, and the default level is a table with its row", func(t *testing.T) {
		if got, ok := tables.Lookup("visibility_settings"); !ok || got.Class != tables.Data {
			t.Errorf("visibility_settings registered as %+v (%v)", got, ok)
		}
		if got := scalar[string](t, admin, "SELECT default_level FROM trove.visibility_settings WHERE singleton"); got != "private" {
			t.Errorf("default level = %q", got)
		}
		wantState(t, admin, "23505", "INSERT INTO trove.visibility_settings (singleton) VALUES (true)")
		wantState(t, admin, "23514", "UPDATE trove.visibility_settings SET default_level = 'open'")
		mustExec(t, admin, "INSERT INTO trove.repositories (key, format, class, project_id) VALUES ('vis-a', 'generic', 'local', (SELECT id FROM trove.project WHERE key = 'it'))")
		if got := scalar[string](t, admin, "SELECT project.visibility || '/' || r.visibility_artifacts || '/' || r.visibility_wiki FROM trove.repositories r JOIN trove.project ON project.id = r.project_id WHERE r.key = 'vis-a'"); got != "private/private/private" {
			t.Errorf("levels of a new project and repository = %q", got)
		}
	})

	t.Run("R-AUTHZ-033 a level is one of the three, in the order private, internal, public", func(t *testing.T) {
		for want, level := range []string{"private", "internal", "public"} {
			if got := scalar[int](t, admin, "SELECT trove.visibility_rank($1)", level); got != want {
				t.Errorf("rank(%s) = %d, want %d", level, got, want)
			}
		}
		wantState(t, admin, "23514", "UPDATE trove.project SET visibility = 'open' WHERE key = 'it'")
		wantState(t, admin, "23514", "UPDATE trove.repositories SET visibility_artifacts = 'open' WHERE key = 'vis-a'")
	})

	t.Run("R-AUTHZ-033 the wiki is never more open than the artifacts: a check constraint", func(t *testing.T) {
		wantState(t, admin, "23514", "UPDATE trove.repositories SET visibility_wiki = 'internal' WHERE key = 'vis-a'")
		mustExec(t, admin, "UPDATE trove.project SET visibility = 'public' WHERE key = 'it'")
		wantState(t, admin, "23514", "UPDATE trove.repositories SET visibility_artifacts = 'internal', visibility_wiki = 'public' WHERE key = 'vis-a'")
		mustExec(t, admin, "UPDATE trove.repositories SET visibility_artifacts = 'public', visibility_wiki = 'internal' WHERE key = 'vis-a'")
	})

	t.Run("R-AUTHZ-033 SQLSTATE AI008: a repository is never more open than its project, checked at commit", func(t *testing.T) {
		// The repository is public now and so is its project; lowering the project alone leaves the repository above it.
		wantState(t, admin, "AI008", "UPDATE trove.project SET visibility = 'internal' WHERE key = 'it'")
		mustExec(t, admin, "UPDATE trove.project SET visibility = 'public' WHERE key = 'it'")
		// Lowering both in one transaction is fine in either order, the rule being checked at commit.
		mustExec(t, admin, `BEGIN; UPDATE trove.project SET visibility = 'private' WHERE key = 'it';
			UPDATE trove.repositories SET visibility_artifacts = 'private', visibility_wiki = 'private' WHERE key = 'vis-a'; COMMIT`)
		wantState(t, admin, "AI008", "UPDATE trove.repositories SET visibility_artifacts = 'internal' WHERE key = 'vis-a'")
		wantState(t, admin, "AI008", `INSERT INTO trove.repositories (key, format, class, project_id, visibility_artifacts)
			VALUES ('vis-b', 'generic', 'local', (SELECT id FROM trove.project WHERE key = 'it'), 'public')`)
		// A move into a project less open than the repository is refused by the same rule.
		mustExec(t, admin, "INSERT INTO trove.project (id, key, display_name, visibility) VALUES (gen_random_uuid(), 'open-project', 'open', 'public')")
		mustExec(t, admin, "UPDATE trove.repositories SET visibility_artifacts = 'public' WHERE key = 'vis-a' AND false")
		mustExec(t, admin, "INSERT INTO trove.repositories (key, format, class, project_id, visibility_artifacts, visibility_wiki) VALUES ('vis-m', 'generic', 'local', (SELECT id FROM trove.project WHERE key = 'open-project'), 'public', 'public')")
		wantState(t, admin, "AI008", "UPDATE trove.repositories SET project_id = (SELECT id FROM trove.project WHERE key = 'it') WHERE key = 'vis-m'")
	})

	t.Run("R-REPO-004 the repository document loses requireAuthentication: the step removes the member from every stored document", func(t *testing.T) {
		if n := scalar[int](t, admin, "SELECT count(*) FROM trove.repositories WHERE config ? 'requireAuthentication'"); n != 0 {
			t.Errorf("%d documents still hold the member", n)
		}
	})

	t.Run("R-AUTH-044 the external flag of a user and the setting new users take", func(t *testing.T) {
		mustExec(t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES (gen_random_uuid(), 'user', 'erin')")
		mustExec(t, admin, "INSERT INTO trove.app_user (principal_id, origin) SELECT id, 'admin' FROM trove.principal WHERE name = 'erin'")
		if scalar[bool](t, admin, "SELECT external FROM trove.app_user u JOIN trove.principal p ON p.id = u.principal_id WHERE p.name = 'erin'") {
			t.Error("a user is external by default")
		}
		if scalar[bool](t, admin, "SELECT new_users_external FROM trove.auth_settings WHERE singleton") {
			t.Error("new users are external by default")
		}
		if scalar[bool](t, admin, "SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'trove' AND table_name = 'auth_settings' AND column_name = 'anonymous_access')") {
			t.Error("auth_settings still holds the anonymous switch")
		}
	})

	t.Run("R-AUTHZ-003 SQLSTATE AI009: no table that grants, assigns or owns names the anonymous principal", func(t *testing.T) {
		const anon = "'00000000-0000-0000-0000-000000000001'"
		mustExec(t, admin, "INSERT INTO trove.authz_policy (id, name, resource_type) VALUES (gen_random_uuid(), 'p-anon', 'repository')")
		wantState(t, admin, "AI009", "INSERT INTO trove.authz_policy_entry (policy_id, principal_id, actions) SELECT id, "+anon+"::uuid, ARRAY['read'] FROM trove.authz_policy WHERE name = 'p-anon'")
		wantState(t, admin, "AI009", "INSERT INTO trove.authz_role_binding (role, principal_id) VALUES ('auditor', "+anon+"::uuid)")
		wantState(t, admin, "AI009", "INSERT INTO trove.project_member (project_id, principal_id, role) SELECT id, "+anon+"::uuid, 'reader' FROM trove.project WHERE key = 'it'")
		mustExec(t, admin, "INSERT INTO trove.authz_policy (id, name, resource_type, project_id) SELECT gen_random_uuid(), 'it/p-anon', 'repository', id FROM trove.project WHERE key = 'it'")
		wantState(t, admin, "AI009", `INSERT INTO trove.authz_project_policy_entry (policy_id, project_id, principal_id, actions)
			SELECT id, project_id, `+anon+`::uuid, ARRAY['read'] FROM trove.authz_policy WHERE name = 'it/p-anon'`)
		wantState(t, admin, "AI009", `INSERT INTO trove.package_ownership (domain, format, package_name, state) VALUES ('domain:x', 'npm', '@x/y', 'claimed')`+
			`; INSERT INTO trove.package_owner (id, domain, format, package_name, owner_kind, principal_id, display, reason) VALUES (gen_random_uuid(), 'domain:x', 'npm', '@x/y', 'principal', `+anon+`::uuid, 'anonymous', 'added')`)
	})

	t.Run("R-AUTH-016 the kind authenticated is gone from the principal table", func(t *testing.T) {
		wantState(t, admin, "23514", "INSERT INTO trove.principal (id, kind, name) VALUES (gen_random_uuid(), 'authenticated', 'authenticated')")
	})
}
