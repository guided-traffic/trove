//go:build integration

package authz

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/test/integration/stack"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// Role bindings declared in the system configuration (R-AUTHZ-010, Q-AUTHZ-170,
// Q-AUTHZ-171): trove migrate applies authz.role_bindings, three web replicas
// that follow the change log serve the management API, and every answer is
// validated against api/openapi.yaml.

const roleBindings = "/api/v1/role-bindings"

func declared(role, kind, name string) config.AuthzRoleBinding {
	b := config.AuthzRoleBinding{Role: role}
	switch kind {
	case "group":
		b.Group = name
	case "user":
		b.User = name
	default:
		b.ServiceAccount = name
	}
	return b
}

// bindingsBy returns role/principal -> source of every binding, read through the API.
func bindingsBy(t *testing.T, f *apiFixture) map[string]string {
	t.Helper()
	r := f.cl.Do(f.admin.Name, "GET", roleBindings+"?limit=500", nil, f.current()...)
	wantStatus(t, r, 200, "")
	out := map[string]string{}
	for _, it := range r.Items(t) {
		out[it["role"].(string)+"/"+it["principal"].(string)] = it["source"].(string)
	}
	return out
}

func (f *apiFixture) configurationEvents(action string) int64 {
	return f.e.Number("SELECT count(*) FROM audit_event WHERE action = $1 AND actor_type = 'system' AND actor_name = 'configuration'", action)
}

func TestR_AUTHZ_010_BindingsFromTheConfiguration(t *testing.T) {
	f := newAPI(t, stack.Options{})
	me := f.admin.Name
	ctx := context.Background()

	t.Run("R-AUTHZ-010 trove migrate creates the declared group as a local group and binds it", func(t *testing.T) {
		before := f.e.AuthzEpoch()
		if err := testenv.DeclareRoleBindings(f.e.Cfg, declared("admin", "group", "gh-ops"), declared("auditor", "user", "bob")); err != nil {
			t.Fatal(err)
		}
		if n := f.e.Number(`SELECT count(*) FROM user_group g JOIN principal p ON p.id = g.principal_id
		                     WHERE p.name = 'gh-ops' AND g.origin = 'admin' AND NOT g.auto_join`); n != 1 {
			t.Fatalf("%d local groups gh-ops", n)
		}
		got := bindingsBy(t, f)
		if got["admin/group:gh-ops"] != "configuration" || got["auditor/user:bob"] != "configuration" || got["admin/user:sec-alice"] != "api" {
			t.Fatalf("bindings %v", got)
		}
		if f.e.AuthzEpoch() <= before {
			t.Fatalf("R-AUTHZ-028 the epoch stayed at %d", before)
		}
		if n := f.configurationEvents("authz.role.bound"); n != 2 {
			t.Fatalf("%d authz.role.bound events by system:configuration", n)
		}
		if n := f.configurationEvents("group.created"); n != 1 {
			t.Fatalf("%d group.created events by system:configuration", n)
		}
		if n := f.e.Number(`SELECT count(*) FROM audit_event WHERE action = 'authz.role.bound' AND details->>'source' = 'configuration'
		                     AND details->>'principal' = 'group:gh-ops' AND (details->>'epoch')::bigint > 0`); n != 1 {
			t.Fatalf("the event of group:gh-ops lacks its source or epoch (%d)", n)
		}
		r := f.cl.Do(me, "GET", roleBindings+"?role=admin&principal=group%3Agh-ops", nil, f.current()...)
		wantStatus(t, r, 200, "")
		if items := r.Items(t); len(items) != 1 || items[0]["grantedBy"] != "" || items[0]["source"] != "configuration" {
			t.Fatalf("%v", items)
		}
	})

	t.Run("R-AUTHZ-010 applying the same declaration again changes nothing", func(t *testing.T) {
		epoch, events := f.e.AuthzEpoch(), f.e.Number("SELECT count(*) FROM audit_event")
		if err := testenv.DeclareRoleBindings(f.e.Cfg, declared("admin", "group", "gh-ops"), declared("auditor", "user", "bob")); err != nil {
			t.Fatal(err)
		}
		if f.e.AuthzEpoch() != epoch || f.e.Number("SELECT count(*) FROM audit_event") != events {
			t.Fatalf("epoch %d -> %d, events %d -> %d", epoch, f.e.AuthzEpoch(), events, f.e.Number("SELECT count(*) FROM audit_event"))
		}
	})

	t.Run("R-AUTHZ-010 DELETE of a configuration binding answers 409 and the binding stays", func(t *testing.T) {
		wantStatus(t, f.cl.Do(me, "DELETE", roleBindings+"/admin/group%3Agh-ops", nil, f.current()...), 409, "role_binding_managed_by_configuration")
		wantStatus(t, f.cl.Do(me, "DELETE", roleBindings+"/auditor/user%3Abob", nil, f.current()...), 409, "role_binding_managed_by_configuration")
		if got := bindingsBy(t, f); got["admin/group:gh-ops"] != "configuration" || got["auditor/user:bob"] != "configuration" {
			t.Fatalf("bindings %v", got)
		}
		wantStatus(t, f.cl.Do(me, "DELETE", roleBindings+"/auditor/user%3Acarol", nil, f.current()...), 404, "not_found")
	})

	t.Run("Q-AUTHZ-171 PUT of a configuration binding answers 200 and leaves it to the configuration", func(t *testing.T) {
		r := f.cl.Do(me, "PUT", roleBindings+"/admin/group%3Agh-ops", nil, f.current()...)
		wantStatus(t, r, 200, "")
		if r.JSON(t)["source"] != "configuration" {
			t.Fatalf("%v", r.JSON(t))
		}
	})

	t.Run("R-AUTH-008 R-AUTHZ-010 a member the login provider syncs into the declared group administers trove", func(t *testing.T) {
		prov := principal.NewProvisioner(principal.ProvisionerOptions{DB: f.a.DB, Notifier: f.a.Changes, Recorder: f.a.Audit, BreakGlassName: "admin"})
		// The provider's rules map the IdP group to gh-ops; create_groups is off, so
		// the sync needs the group trove migrate created.
		if _, err := prov.SignIn(ctx, principal.LoginClaims{
			Provider: "corp", Issuer: "https://idp.example.com", Subject: "sub-dave", Username: "dave",
			Email: "dave@example.com", EmailVerified: true, DisplayName: "Dave", GroupSync: true, JIT: true,
			Groups: []string{"gh-ops"},
		}); err != nil {
			t.Fatal(err)
		}
		if n := f.e.Number(`SELECT count(*) FROM group_membership m JOIN principal g ON g.id = m.group_id JOIN principal u ON u.id = m.member_id
		                     WHERE g.name = 'gh-ops' AND u.name = 'dave' AND m.source = 'oidc:corp'`); n != 1 {
			t.Fatalf("%d oidc:corp memberships of dave in gh-ops", n)
		}
		dave := f.e.Known(principal.KindUser, "dave")
		r := f.cl.Do(dave.Name, "GET", "/api/v1/authz/me", nil, f.current()...)
		wantStatus(t, r, 200, "")
		if roles, _ := r.JSON(t)["roles"].([]any); !slices.Contains(roles, any("admin")) {
			t.Fatalf("dave's roles %v", r.JSON(t)["roles"])
		}
		wantStatus(t, f.cl.Do(dave.Name, "POST", policies, policyBody("daves-policy", entry("group:team-web", "read")), f.current()...), 201, "")
	})

	t.Run("Q-AUTHZ-171 a binding the API made is taken over when it is declared", func(t *testing.T) {
		wantStatus(t, f.cl.Do(me, "PUT", roleBindings+"/auditor/sa%3Aci-bot", nil, f.current()...), 201, "")
		if err := testenv.DeclareRoleBindings(f.e.Cfg, declared("admin", "group", "gh-ops"), declared("auditor", "user", "bob"),
			declared("auditor", "service_account", "ci-bot")); err != nil {
			t.Fatal(err)
		}
		if got := bindingsBy(t, f); got["auditor/sa:ci-bot"] != "configuration" {
			t.Fatalf("bindings %v", got)
		}
		if n := f.e.Number(`SELECT count(*) FROM audit_event WHERE action = 'authz.role.bound' AND actor_type = 'system'
		                     AND details->>'principal' = 'sa:ci-bot' AND details->>'previousSource' = 'api'`); n != 1 {
			t.Fatalf("%d take-over events", n)
		}
		if n := f.e.Text("SELECT COALESCE(gb.name, '') FROM authz_role_binding b JOIN principal p ON p.id = b.principal_id LEFT JOIN principal gb ON gb.id = b.granted_by WHERE p.name = 'ci-bot'"); n != "sec-alice" {
			t.Fatalf("the take-over must keep the granting principal, got %q", n)
		}
	})

	t.Run("R-AUTHZ-010 R-AUTHZ-032 an entry removed from the declaration is unbound at the next run", func(t *testing.T) {
		unbound := f.configurationEvents("authz.role.unbound")
		if err := testenv.DeclareRoleBindings(f.e.Cfg, declared("auditor", "service_account", "ci-bot")); err != nil {
			t.Fatal(err)
		}
		got := bindingsBy(t, f)
		if _, ok := got["admin/group:gh-ops"]; ok {
			t.Fatalf("admin/group:gh-ops survived: %v", got)
		}
		if _, ok := got["auditor/user:bob"]; ok {
			t.Fatalf("auditor/user:bob survived: %v", got)
		}
		if got["admin/user:sec-alice"] != "api" || got["auditor/sa:ci-bot"] != "configuration" {
			t.Fatalf("bindings %v", got)
		}
		if n := f.configurationEvents("authz.role.unbound"); n != unbound+2 {
			t.Fatalf("%d authz.role.unbound events by system:configuration, want %d", n, unbound+2)
		}
		if n := f.e.Number("SELECT count(*) FROM revocation_outbox WHERE event = 'authz.role.unbound'"); n < 2 {
			t.Fatalf("R-AUTHZ-031 %d journal entries of the unbinds", n)
		}
		// The group stays; it only lost the role. dave's next request needs no new login.
		if n := f.e.Number("SELECT count(*) FROM principal WHERE kind = 'group' AND name = 'gh-ops'"); n != 1 {
			t.Fatal("the declared group was deleted")
		}
		wantStatus(t, f.cl.Do("dave", "GET", roleBindings, nil, f.current()...), 403, "forbidden")
	})

	t.Run("R-AUTHZ-010 a declared user or service account that does not exist stops trove migrate, naming each, and nothing is applied", func(t *testing.T) {
		epoch := f.e.AuthzEpoch()
		err := testenv.DeclareRoleBindings(f.e.Cfg, declared("admin", "group", "new-ops"), declared("auditor", "user", "nobody"),
			declared("auditor", "service_account", "ghost-bot"))
		if !errors.Is(err, authz.ErrDeclared) || !strings.Contains(err.Error(), "authz.role_bindings[1]: user:nobody does not exist") ||
			!strings.Contains(err.Error(), "authz.role_bindings[2]: sa:ghost-bot does not exist") {
			t.Fatalf("err = %v", err)
		}
		if n := f.e.Number("SELECT count(*) FROM principal WHERE name = 'new-ops'"); n != 0 {
			t.Fatal("the transaction left the group new-ops behind")
		}
		if got := bindingsBy(t, f); got["auditor/sa:ci-bot"] != "configuration" || f.e.AuthzEpoch() != epoch {
			t.Fatalf("a refused run changed the bindings: %v, epoch %d -> %d", got, epoch, f.e.AuthzEpoch())
		}
	})

	t.Run("R-AUTHZ-022 a declared auto-join group stops trove migrate", func(t *testing.T) {
		auto := f.e.Group("everyone")
		f.e.Exec("UPDATE user_group SET auto_join = true WHERE principal_id = $1::uuid", auto.ID)
		err := testenv.DeclareRoleBindings(f.e.Cfg, declared("auditor", "group", "everyone"))
		if !errors.Is(err, authz.ErrDeclared) || !strings.Contains(err.Error(), "group:everyone is an auto-join group") ||
			!strings.Contains(err.Error(), "auto_join_admin_conflict") {
			t.Fatalf("err = %v", err)
		}
	})
}

// TestR_AUTHZ_022_DeclaredRemovalKeepsAnAdministrator: trove migrate does not
// remove the last administrator while the break-glass administrator is disabled
// (Q-AUTHZ-171); the run stops and names the binding.
func TestR_AUTHZ_022_DeclaredRemovalKeepsAnAdministrator(t *testing.T) {
	e := stack.New(t)
	solo := e.User("solo")
	if err := testenv.DeclareRoleBindings(e.Cfg, declared("admin", "user", "solo")); err != nil {
		t.Fatal(err)
	}
	err := testenv.DeclareRoleBindings(e.Cfg)
	if !errors.Is(err, authz.ErrDeclared) || !strings.Contains(err.Error(), "admin/user:solo") || !strings.Contains(err.Error(), "last_admin") {
		t.Fatalf("err = %v", err)
	}
	if n := e.Number("SELECT count(*) FROM authz_role_binding WHERE role = 'admin' AND principal_id = $1::uuid", solo.ID); n != 1 {
		t.Fatal("the last administrator binding was removed")
	}
	// A declared group without members keeps nobody in charge either.
	err = testenv.DeclareRoleBindings(e.Cfg, declared("admin", "group", "empty-admins"))
	if !errors.Is(err, authz.ErrDeclared) || !strings.Contains(err.Error(), "last_admin") {
		t.Fatalf("err = %v", err)
	}
	// With the break-glass administrator active the binding goes.
	bg := e.Text("INSERT INTO principal (id, kind, name) VALUES (gen_random_uuid(), 'user', 'admin') RETURNING id::text")
	e.Exec("INSERT INTO app_user (principal_id, origin, is_break_glass) VALUES ($1::uuid, 'system', true)", bg)
	if err := testenv.DeclareRoleBindings(e.Cfg); err != nil {
		t.Fatal(err)
	}
	if n := e.Number("SELECT count(*) FROM authz_role_binding"); n != 0 {
		t.Fatalf("%d bindings left", n)
	}
}
