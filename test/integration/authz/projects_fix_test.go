//go:build integration

package authz

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The fix wave of phase 5a on the real engine and the real tables (R-AUTHZ-026,
// Q-AUTHZ-196 to Q-AUTHZ-201).

// The manage of a project administrator follows the credential and the kind of
// the subject, through the group the administration is held by (Q-AUTHZ-196).
func TestR_AUTHZ_026_ManageOfAProjectAdministratorThroughAGroupFollowsTheCredential(t *testing.T) {
	p := newProjectEnv(t)
	// A global service account and a user of the administrator group.
	botInLeads := sub(p.bot, p.leads)
	botInLeads.Scope = &scope.Scope{Inherit: true, Admin: true}
	bot := sub(p.bot, p.leads)
	inherit := sub(p.dave, p.leads)
	inherit.Scope = &scope.Scope{Inherit: true}
	admin := sub(p.dave, p.leads)
	admin.Scope = &scope.Scope{Inherit: true, Admin: true}
	for _, r := range []*replica{p.a, p.b} {
		for name, tc := range map[string]struct {
			s    authz.Subject
			want bool
		}{
			"a global service account in the group": {bot, false},
			"its admin token":                       {botInLeads, false},
			"an inherit token of a group member":    {inherit, false},
			"an admin token of a group member":      {admin, true},
			"a session of a group member":           {sub(p.dave, p.leads), true},
		} {
			if got := p.manages(r, tc.s, "web-npm"); got != tc.want {
				t.Errorf("replica %s: %s: manage on web-npm = %v, want %v", r.name, name, got, tc.want)
			}
		}
	}
}

// A policy write of a delegate is judged under the policy lock, on the project it
// holds (Q-AUTHZ-199).
func TestR_AUTHZ_026_ADelegatedWriteChecksTheProjectUnderTheLock(t *testing.T) {
	p := newProjectEnv(t)
	ctx := context.Background()
	shop := "shop"
	if _, _, err := p.a.svc.Create(ctx, p.root, authz.PolicyInput{
		Name: "ops-readers", Resources: authz.Resources{Repositories: []string{"npm-internal"}},
		Entries: []authz.Entry{{Principal: "user:bob", Actions: []string{"read"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.a.svc.Create(ctx, p.root, authz.PolicyInput{Name: "shop-readers", Project: &shop}); err != nil {
		t.Fatal(err)
	}
	web := authz.AsDelegate("web")
	t.Run("a global policy is 403, whatever the handler saw before", func(t *testing.T) {
		_, _, err := p.a.svc.Update(ctx, sub(p.alice), "ops-readers", errs.AnyRevision, authz.PolicyInput{Description: "x", Resources: authz.Resources{Repositories: []string{"npm-internal"}}}, web)
		problem(t, err, 403, errs.CodeForbidden)
		_, err = p.a.svc.Delete(ctx, sub(p.alice), "ops-readers", errs.AnyRevision, web)
		problem(t, err, 403, errs.CodeForbidden)
	})
	t.Run("another project's policy is 404", func(t *testing.T) {
		_, _, err := p.a.svc.Update(ctx, sub(p.alice), "shop-readers", errs.AnyRevision, authz.PolicyInput{Project: &shop}, web)
		problem(t, err, 404, errs.CodeNotFound)
		_, err = p.a.svc.Delete(ctx, sub(p.alice), "shop-readers", errs.AnyRevision, web)
		problem(t, err, 404, errs.CodeNotFound)
	})
	t.Run("ExpectProject refuses a policy of another project or a global one, for a caller that is no delegate", func(t *testing.T) {
		webKey := "web"
		_, err := p.a.svc.Delete(ctx, p.root, "ops-readers", errs.AnyRevision, authz.ExpectProject(&webKey))
		problem(t, err, 404, errs.CodeNotFound)
		_, err = p.a.svc.Delete(ctx, p.root, "shop-readers", errs.AnyRevision, authz.ExpectProject(nil))
		problem(t, err, 404, errs.CodeNotFound)
		_, err = p.a.svc.Delete(ctx, p.root, "shop-readers", errs.AnyRevision, authz.ExpectProject(&webKey))
		problem(t, err, 404, errs.CodeNotFound)
	})
	t.Run("both still exist", func(t *testing.T) {
		if n := p.number("SELECT count(*) FROM authz_policy WHERE name IN ('ops-readers', 'shop-readers')"); n != 2 {
			t.Fatalf("%d policies left", n)
		}
	})
	t.Run("the delegate's own project's policy is written and deleted", func(t *testing.T) {
		webKey := "web"
		if _, _, err := p.a.svc.Create(ctx, sub(p.alice), authz.PolicyInput{Name: "web-readers", Project: &webKey, Resources: authz.Resources{Repositories: []string{"web-npm"}}}, web); err != nil {
			t.Fatal(err)
		}
		if _, _, err := p.a.svc.Update(ctx, sub(p.alice), "web-readers", errs.AnyRevision, authz.PolicyInput{Project: &webKey, Description: "d", Resources: authz.Resources{Repositories: []string{"web-npm"}}}, web); err != nil {
			t.Fatal(err)
		}
		if _, err := p.a.svc.Delete(ctx, sub(p.alice), "web-readers", errs.AnyRevision, web); err != nil {
			t.Fatal(err)
		}
	})
}

// ghostProjects finds a project that a concurrent delete has just removed: the lookup
// answers an ID the table no longer holds.
type ghostProjects struct{ authz.ProjectLookup }

func (ghostProjects) ProjectID(context.Context, string) (string, error) {
	return "00000000-0000-4000-8000-00000000dead", nil
}

// A create that races a project delete is the field error unknown_project, not a
// foreign key violation (Q-AUTHZ-199).
func TestR_AUTHZ_026_ACreateRacingAProjectDeleteIsUnknownProject(t *testing.T) {
	p := newProjectEnv(t)
	ghost := "gone"
	svc := authz.NewService(authz.ServiceOptions{
		DB: p.a.db, Changes: p.a.changes, Principals: sqlPrincipals{p.env}, Repositories: sqlRepositories{p.env},
		Formats: integrationFormats(), Projects: ghostProjects{},
	})
	_, _, err := svc.Create(context.Background(), p.root, authz.PolicyInput{Name: "gone-readers", Project: &ghost})
	problem(t, err, 422, errs.CodeUnknownProject)
}

// A group that is an auto-join group is refused in a project policy and skipped by
// the build when it became one afterwards (Q-AUTHZ-200).
func TestR_AUTHZ_026_AProjectPolicyNamesNoAutoJoinGroupAgainstTheTables(t *testing.T) {
	p := newProjectEnv(t)
	ctx := context.Background()
	everyone := p.group("everyone", true)
	web := "web"
	_, _, err := p.a.svc.Create(ctx, p.root, authz.PolicyInput{
		Name: "web-everyone", Project: &web, Resources: authz.Resources{Repositories: []string{"web-npm"}},
		Entries: []authz.Entry{{Principal: "group:everyone", Actions: []string{"read"}}},
	})
	problem(t, err, 422, errs.CodePrincipalOutsideProject)
	// A group that turns auto-join after the policy was written.
	if _, _, err := p.a.svc.Create(ctx, p.root, authz.PolicyInput{
		Name: "web-devs", Project: &web, Resources: authz.Resources{Repositories: []string{"web-npm"}},
		Entries: []authz.Entry{{Principal: "group:developers", Actions: []string{"read"}}},
	}); err != nil {
		t.Fatal(err)
	}
	member := sub(p.dave, p.devs)
	waitFor(t, "the policy is built", 3*time.Second, func() bool {
		return p.a.engine.Decide(ctx, member, read("web-npm", "x", "")).Outcome == authz.Allow
	})
	p.exec("UPDATE user_group SET auto_join = true WHERE principal_id = $1::uuid", p.devs.ID)
	p.changed()
	waitFor(t, "the policy of the new auto-join group is skipped", 3*time.Second, func() bool {
		return p.a.engine.Decide(ctx, member, read("web-npm", "x", "")).Outcome != authz.Allow
	})
	_ = everyone
}

// A system role of a project service account is refused by the declaration of
// authz.role_bindings, like by the API (Q-AUTHZ-198).
func TestR_AUTHZ_026_ADeclaredRoleOfAProjectServiceAccountIsRefused(t *testing.T) {
	f := newProjectAPI(t)
	id := f.e.Text("INSERT INTO principal (id, kind, name) VALUES (gen_random_uuid(), 'service_account', 'web-bot') RETURNING id::text")
	f.e.Exec("INSERT INTO service_account (principal_id, project_id) VALUES ($1::uuid, $2::uuid)", id, f.webID)
	err := testenv.DeclareRoleBindings(f.e.Cfg, declared("auditor", "service_account", "web-bot"))
	if err == nil || !strings.Contains(err.Error(), "role_not_allowed") {
		t.Fatalf("the declaration was applied or refused for another reason: %v", err)
	}
	if n := f.e.Number("SELECT count(*) FROM authz_role_binding b JOIN principal p ON p.id = b.principal_id WHERE p.name = 'web-bot'"); n != 0 {
		t.Fatalf("%d bindings of the project account", n)
	}
}
