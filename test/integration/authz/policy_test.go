//go:build integration

package authz

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

func (e *env) authzEpoch() int64 {
	return e.number("SELECT epoch FROM cluster_epoch WHERE area = 'authz'")
}

func (e *env) authzChanges() int64 {
	return e.number("SELECT count(*) FROM cluster_change WHERE area = 'authz'")
}

// problem asserts the status and code of an error. A 422 names the resource
// specific code of a violation in errors[]; its top-level code is validation_failed
// (R-API-009).
func problem(t *testing.T, err error, status int, code string) {
	t.Helper()
	ee, ok := errs.As(err)
	if ok && status == 422 && code != errs.CodeValidation {
		found := false
		for _, f := range ee.Fields {
			found = found || f.Code == code
		}
		if ee.Status() != 422 || ee.Code != errs.CodeValidation || !found {
			t.Fatalf("err = %v, want 422 validation_failed with %s in errors[]", err, code)
		}
		return
	}
	if !ok || ee.Status() != status || ee.Code != code {
		t.Fatalf("err = %v, want %d %s", err, status, code)
	}
}

func TestR_AUTHZ_021_PolicyLifecycle(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{})
	admin := sub(e.user("sec-alice"))
	e.user("lead-web")
	e.group("team-web", false)
	e.repo("npm-internal", "local", "npm", "")
	ctx := t.Context()

	in := authz.PolicyInput{
		Name: "team-web-publish", Description: "Team web publishes its own npm scope",
		Resources: authz.Resources{Repositories: []string{"npm-internal"}, Packages: authz.PatternLists{Include: []string{"@web/*"}}},
		Entries:   []authz.Entry{{Principal: "group:team-web", Actions: []string{"write"}}, {Principal: "user:lead-web", Actions: []string{"deploy", "manage", "read"}}},
	}

	before := e.authzEpoch()
	p, epoch1, err := a.svc.Create(ctx, admin, in)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("R-AUTHZ-021 create answers the new epoch and stores expanded actions", func(t *testing.T) {
		if epoch1 != before+1 || e.authzEpoch() != epoch1 {
			t.Errorf("epoch %d -> %d, row %d", before, epoch1, e.authzEpoch())
		}
		if p.Version != 1 || p.Entries[0].Principal != "group:team-web" || len(p.Entries[0].Actions) != 3 {
			t.Errorf("policy = %+v", p)
		}
		stored := e.text("SELECT actions::text FROM authz_policy_entry JOIN principal ON principal.id = principal_id WHERE name = 'team-web'")
		if stored != "{annotate,deploy,read}" {
			t.Errorf("stored actions = %s", stored)
		}
	})

	t.Run("R-AUTHZ-021 a name that exists answers 409 and stores nothing", func(t *testing.T) {
		n := e.authzEpoch()
		_, _, err := a.svc.Create(ctx, admin, in)
		problem(t, err, 409, errs.CodeAlreadyExists)
		if e.authzEpoch() != n {
			t.Error("a refused create advanced the epoch")
		}
	})

	t.Run("R-AUTHZ-021 get returns the policy", func(t *testing.T) {
		got, err := a.svc.Get(ctx, "team-web-publish")
		if err != nil || got.Version != 1 || got.Description != in.Description || got.Resources.Repositories[0] != "npm-internal" ||
			got.Resources.Packages.Include[0] != "@web/*" || got.CreatedBy != "user:sec-alice" || len(got.Entries) != 2 {
			t.Fatalf("%+v %v", got, err)
		}
		_, err = a.svc.Get(ctx, "no-such-policy")
		problem(t, err, 404, errs.CodeNotFound)
	})

	t.Run("R-AUTHZ-021 update needs the current version and replaces the policy as a whole", func(t *testing.T) {
		upd := in
		upd.Entries = []authz.Entry{{Principal: "group:team-web", Actions: []string{"read"}}}
		_, _, err := a.svc.Update(ctx, admin, "team-web-publish", 7, upd)
		problem(t, err, 412, errs.CodePreconditionFailed)
		if got, _ := a.svc.Get(ctx, "team-web-publish"); got.Version != 1 || len(got.Entries) != 2 {
			t.Fatalf("a refused update changed the policy: %+v", got)
		}
		n := e.authzEpoch()
		got, epoch, err := a.svc.Update(ctx, admin, "team-web-publish", 1, upd)
		if err != nil || got.Version != 2 || len(got.Entries) != 1 || epoch != n+1 {
			t.Fatalf("%+v %d %v", got, epoch, err)
		}
		// the deliberate blind write of If-Match: *
		got, _, err = a.svc.Update(ctx, admin, "team-web-publish", errs.AnyRevision, in)
		if err != nil || got.Version != 3 || len(got.Entries) != 2 {
			t.Fatalf("%+v %v", got, err)
		}
		_, _, err = a.svc.Update(ctx, admin, "missing", errs.AnyRevision, in)
		problem(t, err, 404, errs.CodeNotFound)
	})

	t.Run("R-AUTHZ-021 validation errors answer 422 with the code and store nothing", func(t *testing.T) {
		n := e.authzEpoch()
		bad := in
		bad.Name = "bad"
		bad.Entries = []authz.Entry{{Principal: "user:lead-web", Actions: []string{"publish"}}}
		_, _, err := a.svc.Create(ctx, admin, bad)
		problem(t, err, 422, errs.CodeInvalidAction)
		bad.Entries = []authz.Entry{{Principal: "user:ghost", Actions: []string{"read"}}}
		_, _, err = a.svc.Create(ctx, admin, bad)
		problem(t, err, 422, errs.CodeUnknownPrincipal)
		bad.Entries = nil
		bad.Resources.Repositories = []string{"no-such-repo"}
		_, _, err = a.svc.Create(ctx, admin, bad)
		problem(t, err, 422, errs.CodeUnknownRepository)
		if e.authzEpoch() != n || e.number("SELECT count(*) FROM authz_policy WHERE name = 'bad'") != 0 {
			t.Error("a refused create left traces")
		}
	})

	t.Run("R-AUTHZ-021 delete honours the version and answers 404 afterwards", func(t *testing.T) {
		_, err := a.svc.Delete(ctx, admin, "team-web-publish", 1)
		problem(t, err, 412, errs.CodePreconditionFailed)
		epoch, err := a.svc.Delete(ctx, admin, "team-web-publish", 3)
		if err != nil || epoch != e.authzEpoch() {
			t.Fatal(epoch, err)
		}
		_, err = a.svc.Delete(ctx, admin, "team-web-publish", errs.AnyRevision)
		problem(t, err, 404, errs.CodeNotFound)
		if e.number("SELECT count(*) FROM authz_policy_entry")+e.number("SELECT count(*) FROM authz_policy_selector")+e.number("SELECT count(*) FROM authz_policy_pattern") != 0 {
			t.Error("the children of a deleted policy remain")
		}
	})

	t.Run("R-AUTHZ-029 every change is one audit event, the removing ones carry a journal entry", func(t *testing.T) {
		if n := len(a.rec.ofType(authz.EventPolicyCreated)); n != 1 {
			t.Errorf("created events = %d", n)
		}
		upd, del := a.rec.ofType(authz.EventPolicyUpdated), a.rec.ofType(authz.EventPolicyDeleted)
		if len(upd) != 2 || len(del) != 1 {
			t.Fatalf("updated %d, deleted %d", len(upd), len(del))
		}
		for _, ev := range append(upd, del...) {
			if len(ev.Journal) == 0 {
				t.Errorf("%s carries no journal entry", ev.Type)
			}
		}
		if len(a.rec.ofType(authz.EventPolicyCreated)[0].Journal) != 0 {
			t.Error("a created policy grants and is not journaled")
		}
		for _, name := range []string{authz.EventPolicyUpdated, authz.EventPolicyDeleted, authz.EventRoleUnbound} {
			if et, ok := audit.Lookup(name); !ok || et.Journal != audit.JournalRevocation || et.Mode != audit.Transactional {
				t.Errorf("%s is not registered as a journaled transactional event: %+v", name, et)
			}
		}
	})
}

// failingRecorder refuses every transactional event, like an audit table that
// rejects inserts (R-AUTHZ-029).
type failingRecorder struct{ recorder }

func (*failingRecorder) RecordTx(context.Context, meta.Tx, audit.Event) error {
	return errors.New("the audit table rejects inserts")
}

func TestR_AUTHZ_029_NoChangeWithoutItsAuditRecord(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{})
	e.user("alice")
	e.repo("npm-internal", "local", "npm", "")
	svc := authz.NewService(authz.ServiceOptions{DB: a.db, Changes: a.changes, Principals: sqlPrincipals{e}, Repositories: sqlRepositories{e}, Recorder: &failingRecorder{}})
	n, changes := e.authzEpoch(), e.authzChanges()
	_, _, err := svc.Create(t.Context(), sub(person{}), authz.PolicyInput{
		Name: "p", Resources: authz.Resources{Repositories: []string{"npm-internal"}},
		Entries: []authz.Entry{{Principal: "user:alice", Actions: []string{"read"}}},
	})
	if err == nil {
		t.Fatal("the create succeeded without its audit event")
	}
	if e.number("SELECT count(*) FROM authz_policy") != 0 || e.authzEpoch() != n || e.authzChanges() != changes {
		t.Error("the rolled-back create left a policy, an epoch or a change")
	}
}

func TestR_AUTHZ_004_InertPoliciesAndDynamicSelectors(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{poll: 200 * time.Millisecond})
	b := e.newReplica("b", replicaOpts{poll: 200 * time.Millisecond})
	admin := sub(e.user("sec-alice"))
	bob, devs := e.user("bob"), e.group("developers", false)
	e.repo("npm-old", "local", "npm", "")
	ctx := t.Context()

	_, _, err := a.svc.Create(ctx, admin, authz.PolicyInput{
		Name: "legacy", Resources: authz.Resources{Repositories: []string{"npm-old"}},
		Entries: []authz.Entry{{Principal: "group:developers", Actions: []string{"read"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	epoch := func() int64 { return e.authzEpoch() }
	bobSub := sub(bob, devs)
	bobSub.MinEpoch = epoch()
	if d := b.engine.Decide(ctx, bobSub, read("npm-old", "x", "")); d.Outcome != authz.Allow {
		t.Fatalf("before the delete: %+v", d)
	}

	// The repository goes away and comes back under the same key, the way the
	// repository area does it: in a transaction that notifies repo_config.
	repoTx := func(f func(exec func(sql string, args ...any) error) error) {
		t.Helper()
		err := a.db.InTx(ctx, func(tx meta.Tx) error {
			ptx, _ := postgres.Unwrap(tx)
			if err := f(func(sql string, args ...any) error { _, err := ptx.Exec(ctx, sql, args...); return err }); err != nil {
				return err
			}
			return a.changes.Notify(tx, "repo_config", 1, map[string]string{"type": "repository"})
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	repoTx(func(exec func(string, ...any) error) error {
		return exec("DELETE FROM repositories WHERE key = 'npm-old'")
	})
	repoTx(func(exec func(string, ...any) error) error {
		return exec("INSERT INTO repositories (key, class, format, config) VALUES ('npm-old', 'local', 'npm', '{}')")
	})
	p, err := a.svc.Get(ctx, "legacy")
	if err != nil || !p.Inert || len(p.Resources.Repositories) != 0 {
		t.Fatalf("legacy = %+v, %v", p, err)
	}
	waitFor(t, "replica b refusing the re-created repository", 3*time.Second, func() bool {
		return b.engine.Decide(ctx, sub(bob, devs), read("npm-old", "x", "")).Outcome == authz.DenyHidden
	})

	t.Run("R-AUTHZ-004 a pattern selector covers a repository created later, on another replica, without a message", func(t *testing.T) {
		if _, _, err := a.svc.Create(ctx, admin, authz.PolicyInput{
			Name: "team-repos", Resources: authz.Resources{Repositories: []string{"npm-team-*"}},
			Entries: []authz.Entry{{Principal: "group:developers", Actions: []string{"read"}}},
		}); err != nil {
			t.Fatal(err)
		}
		repoTx(func(exec func(string, ...any) error) error {
			return exec("INSERT INTO repositories (key, class, format, config) VALUES ('npm-team-x', 'local', 'npm', '{}')")
		})
		start := time.Now()
		waitFor(t, "replica b reading the new repository", 2*time.Second+time.Second, func() bool {
			return b.engine.Decide(ctx, sub(bob, devs), read("npm-team-x", "x", "")).Outcome == authz.Allow
		})
		t.Logf("a repository created through replica a was readable through replica b after %s", time.Since(start))
	})
}

func TestR_AUTHZ_022_RoleBindings(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{})
	actor := sub(e.user("root-admin"))
	e.group("platform-admins", false)
	e.group("everyone", true)
	e.user("carol")
	e.serviceAccount("ci-bot")
	e.ciMapping("gitlab-internal/acme-publish")
	ctx := t.Context()

	t.Run("R-AUTHZ-022 bind is idempotent: 201 first, 200 afterwards, and only the first advances the epoch", func(t *testing.T) {
		n := e.authzEpoch()
		created, epoch, err := a.svc.BindRole(ctx, actor, authz.RoleAdmin, "group:platform-admins")
		if err != nil || !created || epoch != n+1 {
			t.Fatal(created, epoch, err)
		}
		created, epoch, err = a.svc.BindRole(ctx, actor, authz.RoleAdmin, "group:platform-admins")
		if err != nil || created || epoch != n+1 || e.authzEpoch() != n+1 {
			t.Fatalf("second bind: %v %d %v", created, epoch, err)
		}
	})

	t.Run("R-AUTHZ-010 R-AUTHZ-022 the refused bindings", func(t *testing.T) {
		for _, tc := range []struct {
			role authz.Role
			ref  string
			code string
		}{
			{authz.RoleAuditor, "anonymous", errs.CodeRoleNotAllowed},
			{authz.RoleAuditor, "authenticated", errs.CodeRoleNotAllowed},
			{authz.RoleAuditor, "ci:gitlab-internal/acme-publish", errs.CodeRoleNotAllowed},
			{authz.RoleAuditor, "user:ghost", errs.CodeUnknownPrincipal},
			{authz.Role("owner"), "user:carol", errs.CodeUnknownRole},
		} {
			_, _, err := a.svc.BindRole(ctx, actor, tc.role, tc.ref)
			problem(t, err, 422, tc.code)
		}
		_, _, err := a.svc.BindRole(ctx, actor, authz.RoleAdmin, "group:everyone")
		problem(t, err, 409, "auto_join_admin_conflict")
	})

	t.Run("R-AUTHZ-022 the last admin binding is kept while the break-glass administrator is disabled", func(t *testing.T) {
		_, err := a.svc.UnbindRole(ctx, actor, authz.RoleAdmin, "group:platform-admins")
		problem(t, err, 409, "last_admin")
		if e.number("SELECT count(*) FROM authz_role_binding WHERE role = 'admin'") != 1 {
			t.Fatal("the refused unbind removed the binding")
		}
		id := e.principalRow("user", "break-glass")
		e.exec("INSERT INTO app_user (principal_id, origin, is_break_glass) VALUES ($1::uuid, 'system', true)", id)
		if _, err := a.svc.UnbindRole(ctx, actor, authz.RoleAdmin, "group:platform-admins"); err != nil {
			t.Fatalf("with the break-glass administrator active: %v", err)
		}
		_, err = a.svc.UnbindRole(ctx, actor, authz.RoleAdmin, "group:platform-admins")
		problem(t, err, 404, errs.CodeNotFound)
	})

	t.Run("R-AUTHZ-022 list filters by role and principal", func(t *testing.T) {
		for _, b := range []struct {
			role authz.Role
			ref  string
		}{{authz.RoleAuditor, "user:carol"}, {authz.RoleAuditor, "sa:ci-bot"}, {authz.RoleSecurityAdmin, "user:carol"}} {
			if _, _, err := a.svc.BindRole(ctx, actor, b.role, b.ref); err != nil {
				t.Fatal(err)
			}
		}
		all, _ := a.svc.ListBindings(ctx, "", "")
		byRole, _ := a.svc.ListBindings(ctx, authz.RoleAuditor, "")
		byPrincipal, _ := a.svc.ListBindings(ctx, "", "user:carol")
		both, _ := a.svc.ListBindings(ctx, authz.RoleAuditor, "user:carol")
		unknown, err := a.svc.ListBindings(ctx, "", "user:ghost")
		if len(all) != 3 || len(byRole) != 2 || len(byPrincipal) != 2 || len(both) != 1 || err != nil || len(unknown) != 0 {
			t.Fatalf("all %d byRole %d byPrincipal %d both %d unknown %d %v", len(all), len(byRole), len(byPrincipal), len(both), len(unknown), err)
		}
		if both[0].Principal != "user:carol" || both[0].Role != authz.RoleAuditor || both[0].GrantedBy != "user:root-admin" {
			t.Fatalf("%+v", both[0])
		}
	})

	t.Run("R-AUTHZ-029 bindings are audited and an unbind is journaled", func(t *testing.T) {
		if len(a.rec.ofType(authz.EventRoleBound)) != 4 || len(a.rec.ofType(authz.EventRoleUnbound)) != 1 {
			t.Fatalf("bound %d unbound %d", len(a.rec.ofType(authz.EventRoleBound)), len(a.rec.ofType(authz.EventRoleUnbound)))
		}
		if len(a.rec.ofType(authz.EventRoleUnbound)[0].Journal) == 0 {
			t.Error("an unbind carries no journal entry")
		}
	})
}

// TestR_AUTHZ_028_EveryWriteAdvancesTheEpoch is the completeness test of
// R-AUTHZ-028 for the writes this area owns: each leaves the input tables and
// the epoch consistent, one epoch and one change row per write.
func TestR_AUTHZ_028_EveryWriteAdvancesTheEpoch(t *testing.T) {
	e := newEnv(t)
	a := e.newReplica("a", replicaOpts{})
	actor := sub(e.user("root-admin"))
	e.user("alice")
	e.group("team-web", false)
	e.repo("npm-internal", "local", "npm", "")
	ctx := t.Context()
	in := authz.PolicyInput{
		Name: "p", Resources: authz.Resources{Repositories: []string{"npm-internal"}},
		Entries: []authz.Entry{{Principal: "user:alice", Actions: []string{"read"}}},
	}
	steps := []struct {
		name string
		do   func() error
	}{
		{"create a policy", func() error { _, _, err := a.svc.Create(ctx, actor, in); return err }},
		{"update a policy", func() error { _, _, err := a.svc.Update(ctx, actor, "p", errs.AnyRevision, in); return err }},
		{"bind a role", func() error { _, _, err := a.svc.BindRole(ctx, actor, authz.RoleAuditor, "group:team-web"); return err }},
		{"unbind a role", func() error { _, err := a.svc.UnbindRole(ctx, actor, authz.RoleAuditor, "group:team-web"); return err }},
		{"delete a policy", func() error { _, err := a.svc.Delete(ctx, actor, "p", errs.AnyRevision); return err }},
	}
	for _, s := range steps {
		epoch, changes := e.authzEpoch(), e.authzChanges()
		inputs := e.number("SELECT (SELECT count(*) FROM authz_policy) + (SELECT count(*) FROM authz_policy_entry) + (SELECT count(*) FROM authz_role_binding)")
		if err := s.do(); err != nil {
			t.Fatalf("%s: %v", s.name, err)
		}
		after := e.number("SELECT (SELECT count(*) FROM authz_policy) + (SELECT count(*) FROM authz_policy_entry) + (SELECT count(*) FROM authz_role_binding)")
		if e.authzEpoch() != epoch+1 || e.authzChanges() != changes+1 {
			t.Errorf("R-AUTHZ-028 %s: epoch %d -> %d, changes %d -> %d (inputs %d -> %d)", s.name, epoch, e.authzEpoch(), changes, e.authzChanges(), inputs, after)
		}
	}
}
