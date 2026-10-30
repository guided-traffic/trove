//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/guided-traffic/trove/internal/backup/tables"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// m1Tables are the tables of the M1 migrations (00006_audit to 00012_endpoints)
// with the export class the specification gives them (R-BAK-004).
var m1Tables = map[string]tables.Class{
	"audit_event": tables.Data, "audit_policy": tables.Data, "revocation_outbox": tables.Data,
	"principal": tables.Data, "app_user": tables.Data, "user_group": tables.Data, "group_membership": tables.Data,
	"service_account": tables.Data, "api_token": tables.Data, "auth_settings": tables.Data,
	"user_identity": tables.Data, "ui_session": tables.Transient, "oidc_login_flow": tables.Transient,
	"cli_login": tables.Transient, "auth_lockout": tables.Data,
	"api_idempotency_keys": tables.Transient, "user_preferences": tables.Data, "secret_key_canary": tables.Data,
	"authz_policy": tables.Data, "authz_policy_selector": tables.Data, "authz_policy_pattern": tables.Data,
	"authz_policy_entry": tables.Data, "authz_role_binding": tables.Data,
	"endpoint_exposure_rule": tables.Data,
}

func sqlState(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func mustExec(t testing.TB, c *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := c.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", strings.Join(strings.Fields(sql), " "), err)
	}
}

// wantState runs sql and requires it to fail with the SQLSTATE. A failed
// statement outside a transaction leaves the connection usable.
func wantState(t testing.TB, c *pgx.Conn, state, sql string, args ...any) {
	t.Helper()
	_, err := c.Exec(context.Background(), sql, args...)
	if got := sqlState(err); got != state {
		t.Errorf("%s: SQLSTATE %q (%v), want %s", strings.Join(strings.Fields(sql), " "), got, err, state)
	}
}

// wantRestrict runs a delete that an ON DELETE RESTRICT foreign key refuses.
// PostgreSQL 18 reports it as restrict_violation (23001); 16 and 17 report
// foreign_key_violation (23503). The expected code follows the server, so the
// test stays exact on every supported version (R-HA-013).
func wantRestrict(t testing.TB, c *pgx.Conn, sql string, args ...any) {
	t.Helper()
	var num int
	if err := c.QueryRow(context.Background(), "SELECT current_setting('server_version_num')::int").Scan(&num); err != nil {
		t.Fatal(err)
	}
	state := "23503"
	if num >= 180000 {
		state = "23001"
	}
	wantState(t, c, state, sql, args...)
}

// TestM1Migrations checks the tables of milestone M1: that each exists with its
// export class, the rows the migrations seed, the integrity rules in the database
// and the audit partitions with their privileges.
func TestM1Migrations(t *testing.T) {
	st := getStack(t)
	ensureRuntimeRole(t, st)
	_, _, admin := migrated(t, st)

	t.Run("R-BAK-004 every M1 table exists and carries the export class of the specification", func(t *testing.T) {
		for name, class := range m1Tables {
			got, ok := tables.Lookup(name)
			if !ok || got.Class != class {
				t.Errorf("%s registered as %+v (%v), want %s", name, got, ok, class)
			}
			if !scalar[bool](t, admin, "SELECT to_regclass($1) IS NOT NULL", schemaName+"."+name) {
				t.Errorf("table %s is not created by the migrations", name)
			}
		}
		if p, _ := tables.Lookup("principal"); p.CycleBreak != "created_by" {
			t.Errorf("principal CycleBreak = %q", p.CycleBreak)
		}
	})

	t.Run("R-API-012 the revision columns of the M1 tables are registered", func(t *testing.T) {
		for _, c := range [][2]string{
			{"repositories", "config_version"},
			{"authz_policy", "version"},
			{"endpoint_exposure_rule", "revision"},
			{"user_preferences", "revision"},
		} {
			if !meta.IsRevisionColumn(c[0], c[1]) {
				t.Errorf("%s.%s is not registered", c[0], c[1])
			}
		}
	})

	t.Run("R-ARCH-018 the migrations seed the rows of the change areas auth, authz and repo_config", func(t *testing.T) {
		for _, area := range []string{"auth", "authz", "repo_config"} {
			if !scalar[bool](t, admin, "SELECT EXISTS (SELECT 1 FROM trove.cluster_epoch WHERE area = $1 AND epoch = 0)", area) {
				t.Errorf("no cluster_epoch row for %s", area)
			}
		}
	})

	t.Run("R-AUTH-037 the built-in principals exist once and cannot be changed", func(t *testing.T) {
		if n := scalar[int](t, admin, "SELECT count(*) FROM trove.principal WHERE kind IN ('anonymous', 'authenticated') AND name = kind"); n != 2 {
			t.Fatalf("built-in principals = %d", n)
		}
		wantState(t, admin, "23505", "INSERT INTO trove.principal (id, kind, name) VALUES (gen_random_uuid(), 'anonymous', 'anonymous')")
		wantState(t, admin, "AI001", "DELETE FROM trove.principal WHERE kind = 'anonymous'")
		wantState(t, admin, "AI001", "UPDATE trove.principal SET status = 'disabled' WHERE kind = 'authenticated'")
		wantState(t, admin, "23514", "INSERT INTO trove.principal (id, kind, name) VALUES (gen_random_uuid(), 'anonymous', 'other')")
	})

	t.Run("R-AUTH-007 reserved and malformed names are refused per kind", func(t *testing.T) {
		for _, n := range []string{"me", "system", "anonymous", "authenticated", "Alice", "-x"} {
			wantState(t, admin, "23514", "INSERT INTO trove.principal (id, kind, name) VALUES (gen_random_uuid(), 'user', $1)", n)
		}
		mustExec(t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES (gen_random_uuid(), 'user', 'alice')")
		mustExec(t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES (gen_random_uuid(), 'group', 'alice')")
		mustExec(t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES (gen_random_uuid(), 'ci_mapping', 'gitlab-com/acme-release')")
		wantState(t, admin, "23514", "INSERT INTO trove.principal (id, kind, name) VALUES (gen_random_uuid(), 'ci_mapping', 'no-slash')")
		wantState(t, admin, "23505", "INSERT INTO trove.principal (id, kind, name) VALUES (gen_random_uuid(), 'user', 'alice')")
	})

	t.Run("R-AUTH-018 a membership, token, session or role binding names a principal of the right kind", func(t *testing.T) {
		mustExec(t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES ('11111111-1111-1111-1111-111111111111', 'group', 'team-web')")
		mustExec(t, admin, "INSERT INTO trove.user_group (principal_id, origin) VALUES ('11111111-1111-1111-1111-111111111111', 'admin')")
		mustExec(t, admin, "INSERT INTO trove.principal (id, kind, name) VALUES ('22222222-2222-2222-2222-222222222222', 'user', 'bob')")
		mustExec(t, admin, "INSERT INTO trove.app_user (principal_id, origin) VALUES ('22222222-2222-2222-2222-222222222222', 'jit')")
		mustExec(t, admin, "INSERT INTO trove.group_membership (group_id, member_id, source) VALUES ('11111111-1111-1111-1111-111111111111', '22222222-2222-2222-2222-222222222222', 'oidc:corp')")
		wantState(t, admin, "23514", "INSERT INTO trove.group_membership (group_id, member_id, source) VALUES ('11111111-1111-1111-1111-111111111111', '22222222-2222-2222-2222-222222222222', 'oidc:Corp')")
		wantState(t, admin, "AI001", "INSERT INTO trove.group_membership (group_id, member_id, source) VALUES ('11111111-1111-1111-1111-111111111111', '00000000-0000-0000-0000-000000000002', 'manual')")
		wantState(t, admin, "AI001", "INSERT INTO trove.app_user (principal_id, origin) VALUES ('11111111-1111-1111-1111-111111111111', 'admin')")
		wantState(t, admin, "AI001", `INSERT INTO trove.api_token (id, type, secret_sha256, hint, principal_id, description, scope, created_via, expires_at)
			VALUES (gen_random_uuid(), 'pat', decode(repeat('ab', 32), 'hex'), 'trv_pat_…', '00000000-0000-0000-0000-000000000001', 'x', '{}', 'api', now() + interval '1 day')`)
		wantState(t, admin, "AI001", `INSERT INTO trove.ui_session (id, secret_sha256, csrf_sha256, principal_id, auth_method, authenticated_at, idle_timeout, absolute_expires_at)
			VALUES (gen_random_uuid(), decode(repeat('01', 32), 'hex'), decode(repeat('02', 32), 'hex'), '11111111-1111-1111-1111-111111111111', 'oidc', now(), '1 hour', now() + interval '12 hours')`)
		mustExec(t, admin, "INSERT INTO trove.authz_role_binding (role, principal_id) VALUES ('admin', '11111111-1111-1111-1111-111111111111')")
		wantState(t, admin, "AI001", "INSERT INTO trove.authz_role_binding (role, principal_id) VALUES ('auditor', '00000000-0000-0000-0000-000000000002')")
	})

	t.Run("R-AUTH-023 a token row stores a 32-byte hash and every non-service-account token expires", func(t *testing.T) {
		ins := `INSERT INTO trove.api_token (id, type, secret_sha256, hint, principal_id, description, scope, created_via, expires_at)
			VALUES (gen_random_uuid(), $1, decode($2, 'hex'), 'h', '22222222-2222-2222-2222-222222222222', 'x', '{}', 'api', $3)`
		mustExec(t, admin, ins, "pat", strings.Repeat("cd", 32), time.Now().Add(time.Hour))
		wantState(t, admin, "23505", ins, "pat", strings.Repeat("cd", 32), time.Now().Add(time.Hour))
		wantState(t, admin, "23514", ins, "pat", strings.Repeat("ce", 31), time.Now().Add(time.Hour))
		wantState(t, admin, "23514", ins, "pat", strings.Repeat("cf", 32), nil)
	})

	t.Run("R-AUTHZ-021 a policy entry holds only the six actions and a selector the columns of its kind", func(t *testing.T) {
		mustExec(t, admin, "INSERT INTO trove.authz_policy (id, name) VALUES ('33333333-3333-3333-3333-333333333333', 'p')")
		mustExec(t, admin, "INSERT INTO trove.authz_policy_entry (policy_id, principal_id, actions) VALUES ('33333333-3333-3333-3333-333333333333', '22222222-2222-2222-2222-222222222222', ARRAY['deploy', 'read'])")
		wantState(t, admin, "23514", "INSERT INTO trove.authz_policy_entry (policy_id, principal_id, actions) VALUES ('33333333-3333-3333-3333-333333333333', '11111111-1111-1111-1111-111111111111', ARRAY['write'])")
		wantState(t, admin, "23514", "INSERT INTO trove.authz_policy_entry (policy_id, principal_id, actions) VALUES ('33333333-3333-3333-3333-333333333333', '11111111-1111-1111-1111-111111111111', ARRAY[]::text[])")
		mustExec(t, admin, "INSERT INTO trove.authz_policy_selector (policy_id, ordinal, kind, repo_class, format) VALUES ('33333333-3333-3333-3333-333333333333', 0, 'class', 'local', 'npm')")
		wantState(t, admin, "23514", "INSERT INTO trove.authz_policy_selector (policy_id, ordinal, kind, key_pattern) VALUES ('33333333-3333-3333-3333-333333333333', 1, 'class', 'npm-*')")
		// Deleting a repository removes its key selectors and leaves the policy.
		mustExec(t, admin, "INSERT INTO trove.repositories (key, format, class) VALUES ('npm-old', 'npm', 'local')")
		mustExec(t, admin, "INSERT INTO trove.authz_policy_selector (policy_id, ordinal, kind, repository_id) SELECT '33333333-3333-3333-3333-333333333333', 2, 'key', id FROM trove.repositories WHERE key = 'npm-old'")
		mustExec(t, admin, "DELETE FROM trove.repositories WHERE key = 'npm-old'")
		if n := scalar[int](t, admin, "SELECT count(*) FROM trove.authz_policy_selector WHERE kind = 'key'"); n != 0 {
			t.Errorf("key selectors left after the repository went = %d", n)
		}
	})

	t.Run("R-REPO-001 repositories gain a lifecycle and a configuration version; keys with dots are refused", func(t *testing.T) {
		mustExec(t, admin, "INSERT INTO trove.repositories (key, format, class) VALUES ('generic-local', 'generic', 'local')")
		if got := scalar[string](t, admin, "SELECT lifecycle || ':' || config_version FROM trove.repositories WHERE key = 'generic-local'"); got != "active:1" {
			t.Errorf("lifecycle and version = %q", got)
		}
		wantState(t, admin, "23514", "INSERT INTO trove.repositories (key, format, class) VALUES ('a..b', 'generic', 'local')")
		wantState(t, admin, "23514", "INSERT INTO trove.repositories (key, format, class) VALUES ('x.', 'generic', 'local')")
		wantState(t, admin, "23514", "UPDATE trove.repositories SET lifecycle = 'gone' WHERE key = 'generic-local'")
	})

	t.Run("R-AUTH-040 the settings row exists from the start and there is only one", func(t *testing.T) {
		if n := scalar[int](t, admin, "SELECT count(*) FROM trove.auth_settings WHERE singleton AND NOT anonymous_access"); n != 1 {
			t.Fatalf("settings rows = %d", n)
		}
		wantState(t, admin, "23505", "INSERT INTO trove.auth_settings (singleton) VALUES (true)")
	})

	t.Run("R-OPS-015 the partitions of the current and the next three months exist", func(t *testing.T) {
		got := scalar[[]string](t, admin, `SELECT array_agg(c.relname::text ORDER BY c.relname)
			FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid WHERE i.inhparent = 'trove.audit_event'::regclass`)
		want := scalar[[]string](t, admin, `SELECT array_agg('audit_event_' || to_char(date_trunc('month', now() AT TIME ZONE 'UTC') + make_interval(months => k), 'YYYYMM') ORDER BY k)
			FROM generate_series(0, 3) k`)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("partitions = %v, want %v", got, want)
		}
		mustExec(t, admin, `INSERT INTO trove.audit_event (id, occurred_at, action, category, outcome, actor_type) VALUES (gen_random_uuid(), now(), 'repository.created', 'repository', 'success', 'user')`)
		wantState(t, admin, "23514", `INSERT INTO trove.audit_event (id, occurred_at, action, category, outcome, actor_type) VALUES (gen_random_uuid(), now(), 'x', 'x', 'maybe', 'user')`)
		if n := scalar[int](t, admin, `SELECT count(*) FROM trove.audit_event_`+scalar[string](t, admin, `SELECT to_char(now() AT TIME ZONE 'UTC', 'YYYYMM')`)); n != 1 {
			t.Errorf("rows in the current partition = %d", n)
		}
	})

	t.Run("R-OPS-015 the runtime role appends and reads audit events, and can neither change them nor the retention", func(t *testing.T) {
		part := "trove.audit_event_" + scalar[string](t, admin, `SELECT to_char(now() AT TIME ZONE 'UTC', 'YYYYMM')`)
		has := func(obj, priv string) bool {
			return scalar[bool](t, admin, "SELECT has_table_privilege('trove_app', $1, $2)", obj, priv)
		}
		for _, p := range []string{"INSERT", "SELECT"} {
			if !has("trove.audit_event", p) {
				t.Errorf("trove_app lacks %s on audit_event", p)
			}
		}
		for _, p := range []string{"UPDATE", "DELETE", "TRUNCATE"} {
			if has("trove.audit_event", p) {
				t.Errorf("trove_app holds %s on audit_event", p)
			}
		}
		for _, p := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE"} {
			if has(part, p) {
				t.Errorf("trove_app holds %s on the partition", p)
			}
		}
		if !has("trove.audit_policy", "SELECT") {
			t.Error("trove_app cannot read audit_policy")
		}
		for _, p := range []string{"INSERT", "UPDATE", "DELETE", "TRUNCATE"} {
			if has("trove.audit_policy", p) {
				t.Errorf("trove_app holds %s on audit_policy", p)
			}
		}
		mustExec(t, admin, "SET ROLE trove_app")
		defer mustExec(t, admin, "RESET ROLE")
		mustExec(t, admin, `INSERT INTO trove.audit_event (id, occurred_at, action, category, outcome, actor_type) VALUES (gen_random_uuid(), now(), 'token.revoked', 'token', 'success', 'user')`)
		wantState(t, admin, "42501", "DELETE FROM trove.audit_event")
		wantState(t, admin, "42501", "UPDATE trove.audit_event SET outcome = 'success'")
		wantState(t, admin, "42501", "DELETE FROM "+part)
		wantState(t, admin, "42501", "UPDATE trove.audit_policy SET retention_days = 1")
		wantState(t, admin, "42501", "SELECT trove.audit_create_partitions('2025-01-15Z', '2025-04-02Z')")
		wantState(t, admin, "42501", "SELECT trove.audit_attach_partition(now())")
		mustExec(t, admin, "SELECT trove.audit_maintain_partitions()")
	})

	t.Run("R-OPS-015 audit_create_partitions adds the months of a range and drops nothing; maintenance applies the retention", func(t *testing.T) {
		mustExec(t, admin, "SELECT trove.audit_create_partitions('2025-01-15Z', '2025-04-02Z')")
		old := scalar[[]string](t, admin, `SELECT array_agg(c.relname::text ORDER BY c.relname)
			FROM pg_inherits i JOIN pg_class c ON c.oid = i.inhrelid
			WHERE i.inhparent = 'trove.audit_event'::regclass AND c.relname < 'audit_event_202601'`)
		if strings.Join(old, ",") != "audit_event_202501,audit_event_202502,audit_event_202503,audit_event_202504" {
			t.Fatalf("partitions created for the range = %v", old)
		}
		mustExec(t, admin, "SELECT trove.audit_create_partitions('2025-02-01Z', '2025-02-10Z')") // idempotent
		wantState(t, admin, "P0001", "SELECT trove.audit_create_partitions('2025-04-02Z', '2025-01-15Z')")
		wantState(t, admin, "P0001", "SELECT trove.audit_create_partitions('1900-01-01Z', '2100-01-01Z')")

		// Retention 0 keeps every partition; 30 days drops the whole year 2025.
		mustExec(t, admin, "UPDATE trove.audit_policy SET retention_days = 0")
		mustExec(t, admin, "SELECT trove.audit_maintain_partitions()")
		if n := scalar[int](t, admin, "SELECT count(*) FROM pg_inherits WHERE inhparent = 'trove.audit_event'::regclass"); n != 8 {
			t.Fatalf("retention 0 left %d partitions, want 8", n)
		}
		mustExec(t, admin, "UPDATE trove.audit_policy SET retention_days = 30")
		mustExec(t, admin, "SELECT trove.audit_maintain_partitions()")
		if n := scalar[int](t, admin, "SELECT count(*) FROM pg_inherits WHERE inhparent = 'trove.audit_event'::regclass"); n != 4 {
			t.Fatalf("retention 30 left %d partitions, want the 4 of the window", n)
		}
	})

	t.Run("R-OPS-015 a call that waits for a long reader of audit_event gives up within the lock timeout and drops nothing", func(t *testing.T) {
		mustExec(t, admin, "UPDATE trove.audit_policy SET retention_days = 30")
		mustExec(t, admin, "SELECT trove.audit_create_partitions('2025-01-15Z', '2025-01-20Z')")
		reader, err := pgx.ConnectConfig(context.Background(), admin.Config().Copy())
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close(context.Background())
		mustExec(t, reader, "BEGIN")
		mustExec(t, reader, "SELECT count(*) FROM trove.audit_event_202501") // ACCESS SHARE on the partition, as a backup's snapshot holds
		begin := time.Now()
		mustExec(t, admin, "SELECT trove.audit_maintain_partitions()")
		if took := time.Since(begin); took > 8*time.Second {
			t.Errorf("the call took %v", took)
		}
		if !scalar[bool](t, admin, "SELECT to_regclass('trove.audit_event_202501') IS NOT NULL") {
			t.Error("the partition was dropped under a reader")
		}
		mustExec(t, reader, "COMMIT")
		mustExec(t, admin, "SELECT trove.audit_maintain_partitions()")
		if scalar[bool](t, admin, "SELECT to_regclass('trove.audit_event_202501') IS NOT NULL") {
			t.Error("the next call did not drop the expired partition")
		}
	})

	t.Run("R-HA-054 the revocation outbox keeps the id of the event it belongs to", func(t *testing.T) {
		mustExec(t, admin, `INSERT INTO trove.revocation_outbox (id, at, event, entry) VALUES (gen_random_uuid(), now(), 'token.revoked', '{"v": 1}')`)
		wantState(t, admin, "23502", `INSERT INTO trove.revocation_outbox (id, at, event, entry) VALUES (gen_random_uuid(), now(), 'token.revoked', NULL)`)
	})
}

// TestM1AuditFunctionsFollowTheSchema runs the migrations into a schema that is
// not called trove: the audit functions resolve names through the search_path the
// migration fixed, so they must work wherever the schema is.
func TestM1AuditFunctionsFollowTheSchema(t *testing.T) {
	st := getStack(t)
	ensureRuntimeRole(t, st)
	name, admin := st.newDatabase(t)
	cfg := st.config(t, name)
	cfg.Schema = "access_core"
	if _, err := postgres.Migrate(ctxT(t, 60*time.Second), cfg, opts(nil)); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Run("R-OPS-015 the partitions and the functions live in the configured schema", func(t *testing.T) {
		if n := scalar[int](t, admin, "SELECT count(*) FROM pg_inherits WHERE inhparent = 'access_core.audit_event'::regclass"); n != 4 {
			t.Fatalf("partitions = %d, want 4", n)
		}
		mustExec(t, admin, "SELECT access_core.audit_create_partitions('2025-01-15Z', '2025-02-02Z')")
		mustExec(t, admin, "UPDATE access_core.audit_policy SET retention_days = 30")
		mustExec(t, admin, "SELECT access_core.audit_maintain_partitions()")
		if n := scalar[int](t, admin, "SELECT count(*) FROM pg_inherits WHERE inhparent = 'access_core.audit_event'::regclass"); n != 4 {
			t.Fatalf("partitions after the retention = %d, want 4", n)
		}
		if n := scalar[int](t, admin, "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.relname LIKE 'audit_event_2%' AND n.nspname <> 'access_core'"); n != 0 {
			t.Fatalf("%d partitions outside the schema", n)
		}
	})
}
