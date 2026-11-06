//go:build integration

package identity

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/qname"
)

// The project service accounts and the tokens an Owner of the project issues for them
// (R-AUTHZ-026, R-AUTH-020, R-AUTH-027, Q-AUTHZ-169, Q-AUTHZ-175 to Q-AUTHZ-177, Q-AUTHZ-209).

// project inserts a project row and returns its id. It runs before the replicas
// serve: the decision point builds its index from PostgreSQL when it starts.
func (e *env) project(key string) string {
	e.t.Helper()
	var id string
	err := e.admin.QueryRow(context.Background(),
		`INSERT INTO project (id, key, display_name) VALUES (gen_random_uuid(), $1, $1) RETURNING id::text`, key).Scan(&id)
	if err != nil {
		e.t.Fatalf("project %s: %v", key, err)
	}
	return id
}

// member gives a user or group a role in the project (Q-AUTHZ-208).
func (e *env) member(projectKey, principalID, role string) {
	e.t.Helper()
	e.exec(`INSERT INTO project_member (project_id, principal_id, role) SELECT id, $2::uuid, $3 FROM project WHERE key = $1`, projectKey, principalID, role)
}

// ownerToken is a token an Owner of a project uses: it exercises administration through admin: true
// (Q-AUTHZ-215) and holds no capability of its own.
func (r *replica) ownerToken(u principal.User) string {
	return r.pat(u, func(q *token.CreateRequest) { q.Scope = []byte(`{"inherit":true,"admin":true}`) }).Secret
}

func detailsOf(t testing.TB, ev audit.Event) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(ev.Details, &m); err != nil {
		t.Fatalf("details of %s: %v", ev.Type, err)
	}
	return m
}

func TestR_AUTH_020_ProjectServiceAccountsOfTheDomain(t *testing.T) {
	e := newEnv(t)
	e.project("web")
	e.project("mobile")
	a := e.newReplica("a", replicaOpts{tokens: func(c *config.AuthTokens) { c.SAT.AllowNonExpiring = true }})
	b := e.newReplica("b", replicaOpts{})
	ctx := context.Background()
	web := func(name string) qname.Object { return qname.Object{Project: "web", Name: name} }
	create := func(name, project string) (principal.ServiceAccount, error) {
		return a.manager.CreateProjectServiceAccount(ctx, admin, project, principal.NewServiceAccount{Name: name})
	}

	t.Run("R-AUTH-020 Q-AUTHZ-209 the name is unique within the project, the same name lives in every project and instance-wide", func(t *testing.T) {
		sa, err := create("partner", "web")
		if err != nil || sa.Project != "web" || sa.Name != "web/partner" || sa.Object() != web("partner") {
			t.Fatalf("%+v %v", sa, err)
		}
		_, err = create("partner", "web")
		wantCode(t, err, 409, errs.CodeAlreadyExists)
		if _, err := create("partner", "mobile"); err != nil {
			t.Fatalf("the same name in another project: %v", err)
		}
		if _, err := a.manager.CreateServiceAccount(ctx, admin, principal.NewServiceAccount{Name: "partner"}); err != nil {
			t.Fatalf("the same name instance-wide: %v", err)
		}
		_, err = create("xx", "nope")
		wantCode(t, err, 404, errs.CodeNotFound)
		_, err = a.manager.CreateProjectServiceAccount(ctx, admin, "web", principal.NewServiceAccount{Name: "ops", Admin: true})
		wantCode(t, err, 422, errs.CodeRoleNotAllowed)
		_, err = create("Bad_Name", "web")
		wantCode(t, err, 400, errs.CodeValidation)
		if d := detailsOf(t, a.rec.named(audit.EventSACreated)[0]); d["project"] != "web" {
			t.Errorf("serviceaccount.created details %v", d)
		}
	})
	t.Run("R-AUTH-020 each address reads its own account", func(t *testing.T) {
		got, err := a.manager.GetProjectServiceAccount(ctx, web("partner"))
		if err != nil || got.Project != "web" || got.Name != "web/partner" {
			t.Fatalf("%+v %v", got, err)
		}
		inst, err := a.manager.GetServiceAccount(ctx, "partner")
		if err != nil || inst.Project != "" || inst.Name != "partner" || inst.ID == got.ID {
			t.Fatalf("the bare name addresses the instance-wide account: %+v %v", inst, err)
		}
		for _, o := range []qname.Object{{Project: "web", Name: "ghost"}, {Project: "nope", Name: "partner"}, {Name: "partner"}} {
			_, err := a.manager.GetProjectServiceAccount(ctx, o)
			wantCode(t, err, 404, errs.CodeNotFound)
		}
		// An instance-wide method never reaches the account of a project.
		_, err = a.manager.GetServiceAccount(ctx, "web/partner")
		wantCode(t, err, 404, errs.CodeNotFound)
		if _, err = a.manager.UpdateServiceAccount(ctx, admin, "web/partner", "x", errs.AnyRevision); err == nil {
			t.Fatal("the instance-wide update reached a project account")
		}
		for _, ref := range []string{"web/partner", "partner"} {
			if _, err := a.manager.ServiceAccountByRef(ctx, ref); err != nil {
				t.Fatalf("sa:%s: %v", ref, err)
			}
		}
		_, err = a.manager.ServiceAccountByRef(ctx, "web/a/b")
		wantCode(t, err, 404, errs.CodeNotFound)
		upd, err := a.manager.UpdateProjectServiceAccount(ctx, admin, web("partner"), "builds", errs.AnyRevision)
		if err != nil || upd.Description != "builds" {
			t.Fatalf("%+v %v", upd, err)
		}
		if d := detailsOf(t, a.rec.named(audit.EventSAUpdated)[0]); d["project"] != "web" {
			t.Errorf("serviceaccount.updated details %v", d)
		}
	})
	t.Run("R-AUTH-020 the lists: by project, by text within the project, instance-wide with every account", func(t *testing.T) {
		if _, err := create("ci-bot", "web"); err != nil {
			t.Fatal(err)
		}
		got, err := a.manager.ListProjectServiceAccounts(ctx, "web", "", "", 50)
		if err != nil || len(got) != 2 || got[0].Name != "web/ci-bot" || got[1].Name != "web/partner" {
			t.Fatalf("%+v %v", got, err)
		}
		page, err := a.manager.ListProjectServiceAccounts(ctx, "web", "", got[0].Name, 50)
		if err != nil || len(page) != 1 || page[0].Name != "web/partner" {
			t.Fatalf("after the first: %+v %v", page, err)
		}
		q, err := a.manager.ListProjectServiceAccounts(ctx, "web", "ART", "", 50)
		if err != nil || len(q) != 1 || q[0].Name != "web/partner" {
			t.Fatalf("the text matches the name within the project: %+v %v", q, err)
		}
		if q, err = a.manager.ListProjectServiceAccounts(ctx, "web", "web", "", 50); err != nil || len(q) != 0 {
			t.Fatalf("the project key is no part of the name: %+v %v", q, err)
		}
		all, err := a.manager.ListServiceAccounts(ctx, "", "", "", 50)
		if err != nil || len(all) != 4 {
			t.Fatalf("%+v %v", all, err)
		}
		byProject, err := a.manager.ListServiceAccounts(ctx, "", "mobile", "", 50)
		if err != nil || len(byProject) != 1 || byProject[0].Project != "mobile" {
			t.Fatalf("%+v %v", byProject, err)
		}
	})
	t.Run("Q-AUTHZ-175 a project service account is no group member, an instance-wide one is", func(t *testing.T) {
		if _, err := a.manager.CreateGroup(ctx, admin, principal.NewGroup{Name: "devs"}); err != nil {
			t.Fatal(err)
		}
		_, err := a.manager.PatchMembers(ctx, admin, "devs", "", []string{"sa:web/partner"}, nil)
		wantCode(t, err, 422, errs.CodeProjectAccountNotGroupable)
		_, err = a.manager.CreateGroup(ctx, admin, principal.NewGroup{Name: "devs2", Members: []string{"sa:web/partner"}})
		wantCode(t, err, 422, errs.CodeProjectAccountNotGroupable)
		if _, err := a.manager.PatchMembers(ctx, admin, "devs", "", []string{"sa:partner"}, nil); err != nil {
			t.Fatalf("an instance-wide account joins a group: %v", err)
		}
		if n := e.scalar(`SELECT count(*) FROM group_membership gm JOIN principal p ON p.id = gm.member_id WHERE p.name = 'web/partner'`); n != 0 {
			t.Fatalf("%d memberships of the project account", n)
		}
	})
	t.Run("Q-AUTHZ-177 R-AUTH-027 a token an Owner issues names no system item, has admin false and expires", func(t *testing.T) {
		sa, err := a.manager.GetProjectServiceAccount(ctx, web("partner"))
		if err != nil {
			t.Fatal(err)
		}
		issue := func(scope string, ttl *int64) (token.Created, error) {
			req := token.CreateRequest{Caller: admin, Type: token.TypeSAT, OwnerID: sa.ID, Description: "ci", Scope: []byte(scope), Via: "api", Delegated: true}
			req.TTL.Seconds = ttl
			return a.tokens.Create(ctx, req)
		}
		_, err = issue(`{"inherit":true,"system":["identities:read"]}`, nil)
		wantCode(t, err, 422, errs.CodeInvalidScope)
		_, err = issue(`{"inherit":true,"admin":true}`, nil)
		wantCode(t, err, 422, errs.CodeInvalidScope)
		zero, big := int64(0), int64(8760*3600+1)
		if _, err = issue(`{"inherit":true}`, &zero); err == nil {
			t.Error("a non-expiring token was issued although the installation allows them for administrators")
		}
		_, err = issue(`{"inherit":true}`, &big)
		wantCode(t, err, 400, errs.CodeTTLExceedsMaximum)
		// R-AUTH-024: a scope names its repositories as an instance context does; a bare key is invalid_scope.
		_, err = issue(`{"repositories":[{"repositories":["npm-local"],"actions":["read"]}]}`, nil)
		wantCode(t, err, 400, errs.CodeInvalidScope)

		c, err := issue(`{"repositories":[{"repositories":["web/*"],"actions":["read"]}],"endpoints":["customers"]}`, nil)
		if err != nil {
			t.Fatalf("a repository grant with an endpoint binding: %v", err)
		}
		if c.Record.ExpiresAt == nil || len(c.Record.Scope.System) != 0 || c.Record.Scope.Admin {
			t.Fatalf("%+v", c.Record)
		}
		if c.Record.Owner.Ref() != "sa:web/partner" {
			t.Errorf("the owner of the token is %s", c.Record.Owner.Ref())
		}
		if d := detailsOf(t, a.rec.named(audit.EventTokenCreated)[0]); d["project"] != "web" || d["owner"] != "sa:web/partner" {
			t.Errorf("token.created details %v", d)
		}
		// The administrator's own request still takes a system item and no lifetime.
		if _, err := a.tokens.Create(ctx, token.CreateRequest{
			Caller: admin, Type: token.TypeSAT, OwnerID: sa.ID, Description: "x", Via: "admin-cli", Admin: true,
			Scope: []byte(`{"inherit":true,"system":["identities:read"]}`),
		}); err != nil {
			t.Fatalf("an administrator's token: %v", err)
		}
		if _, err := a.tokens.Revoke(ctx, admin, c.Record.ID, token.ReasonAdmin); err != nil {
			t.Fatal(err)
		}
		if d := detailsOf(t, a.rec.named(audit.EventTokenRevoked)[0]); d["project"] != "web" {
			t.Errorf("token.revoked details %v", d)
		}
	})
	t.Run("R-AUTH-020 disabling a project account fails its tokens on every replica, enabling restores them", func(t *testing.T) {
		sa, err := a.manager.GetProjectServiceAccount(ctx, web("ci-bot"))
		if err != nil {
			t.Fatal(err)
		}
		sat := a.sat(sa)
		if _, err := b.bearer(sat.Secret, "/x"); err != nil {
			t.Fatal(err)
		}
		if err := a.manager.DisableProjectServiceAccount(ctx, admin, web("ci-bot"), false); err != nil {
			t.Fatal(err)
		}
		if d := detailsOf(t, a.rec.named(audit.EventSADisabled)[0]); d["project"] != "web" {
			t.Errorf("serviceaccount.disabled details %v", d)
		}
		waitFor(t, "replica B", revocationBound, func() bool {
			_, err := b.bearer(sat.Secret, "/x")
			return errCode(err) == errs.CodePrincipalDisabled
		})
		if err := a.manager.EnableProjectServiceAccount(ctx, admin, web("ci-bot")); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "replica B", revocationBound, func() bool {
			_, err := b.bearer(sat.Secret, "/x")
			return err == nil
		})
		if err := a.manager.DisableProjectServiceAccount(ctx, admin, web("ci-bot"), true); err != nil {
			t.Fatal(err)
		}
		if err := a.manager.EnableProjectServiceAccount(ctx, admin, web("ci-bot")); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "replica B", revocationBound, func() bool {
			_, err := b.bearer(sat.Secret, "/x")
			return errCode(err) == errs.CodeInvalidToken
		})
	})
	t.Run("R-AUTH-020 deleting a project account revokes its tokens before the row goes, on every replica", func(t *testing.T) {
		sa, err := create("doomed", "web")
		if err != nil {
			t.Fatal(err)
		}
		sat := a.sat(sa)
		if _, err := b.bearer(sat.Secret, "/x"); err != nil {
			t.Fatal(err)
		}
		if err := a.manager.DeleteProjectServiceAccount(ctx, admin, web("doomed")); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "replica B", revocationBound, func() bool {
			_, err := b.bearer(sat.Secret, "/x")
			return errCode(err) == errs.CodeInvalidToken
		})
		if n := e.scalar(`SELECT count(*) FROM api_token WHERE id = $1::uuid`, sat.Record.ID); n != 0 {
			t.Fatalf("%d tokens of the deleted account remain", n)
		}
		evs := a.rec.named(audit.EventSADeleted)
		if d := detailsOf(t, evs[len(evs)-1]); d["project"] != "web" || d["tokensDeleted"] != float64(1) {
			t.Errorf("serviceaccount.deleted details %v", d)
		}
		_, err = a.manager.GetProjectServiceAccount(ctx, web("doomed"))
		wantCode(t, err, 404, errs.CodeNotFound)
		// The instance-wide account of the same name is a different principal and stays.
		if _, err := a.manager.GetServiceAccount(ctx, "partner"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-AUTH-020 an instance-wide method deletes no project account, a project method no instance-wide one", func(t *testing.T) {
		err := a.manager.DeleteServiceAccount(ctx, admin, "web/partner")
		wantCode(t, err, 404, errs.CodeNotFound)
		err = a.manager.DeleteProjectServiceAccount(ctx, admin, qname.Object{Name: "partner"})
		wantCode(t, err, 404, errs.CodeNotFound)
		err = a.manager.DeleteProjectServiceAccount(ctx, admin, qname.Object{Project: "mobile", Name: "ci-bot"})
		wantCode(t, err, 404, errs.CodeNotFound)
		if err := a.manager.DeleteProjectServiceAccount(ctx, admin, web("partner")); err != nil {
			t.Fatal(err)
		}
		if err := a.manager.DeleteServiceAccount(ctx, admin, "partner"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("Q-AUTHZ-175 the schema refuses a project service account in a group, whatever the code does", func(t *testing.T) {
		sa, err := create("two", "web")
		if err != nil {
			t.Fatal(err)
		}
		g, err := a.manager.GetGroup(ctx, "devs")
		if err != nil {
			t.Fatal(err)
		}
		_, err = e.admin.Exec(ctx, `INSERT INTO group_membership (group_id, member_id, source) VALUES ($1::uuid, $2::uuid, 'manual')`, g.ID, sa.ID)
		if err == nil {
			t.Fatal("the insert was accepted")
		}
	})
	t.Run("Q-AUTHZ-209 the schema keeps the qualified name and the project of an account together", func(t *testing.T) {
		own := func(name string) string {
			id := principal.NewID()
			e.exec(`INSERT INTO principal (id, kind, name) VALUES ($1::uuid, 'service_account', $2)`, id, name)
			return id
		}
		refused := func(id, project string) {
			t.Helper()
			var err error
			if project == "" {
				_, err = e.admin.Exec(ctx, `INSERT INTO service_account (principal_id, description) VALUES ($1::uuid, '')`, id)
			} else {
				_, err = e.admin.Exec(ctx, `INSERT INTO service_account (principal_id, description, project_id) SELECT $1::uuid, '', id FROM project WHERE key = $2`, id, project)
			}
			if err == nil || !strings.Contains(err.Error(), "AI003") {
				t.Fatalf("the insert answered %v", err)
			}
		}
		refused(own("bare-name"), "web")    // a project account named without its project
		refused(own("mobile/other"), "web") // another project's key
		refused(own("web/inst-wide"), "")   // an instance-wide account with a qualified name
	})
}

// TestR_AUTHZ_026_ProjectServiceAccountsOverHTTP drives what a holder of identities:write
// does with the routes of a project (Q-AUTHZ-209).
func TestR_AUTHZ_026_ProjectServiceAccountsOverHTTP(t *testing.T) {
	e := newEnv(t)
	e.project("web")
	e.project("mobile")
	a := e.newReplica("a", replicaOpts{})
	fx := e.newFixture(a)
	s := a.serve(e, nil)

	t.Run("R-AUTH-020 an administrator creates a project account on the project's route and reads its project", func(t *testing.T) {
		rp := s.call(post, "/api/v1/projects/web/service-accounts", fx.adminToken, map[string]any{"name": "partner"})
		want(t, rp, 201, "")
		m := rp.json(t)
		if str(m, "project") != "web" || str(m, "name") != "partner" || str(m, "principal") != "sa:web/partner" {
			t.Fatalf("%s", rp.Body)
		}
		want(t, s.call(post, "/api/v1/projects/mobile/service-accounts", fx.adminToken, map[string]any{"name": "partner"}), 201, "")
		want(t, s.call(post, "/api/v1/service-accounts", fx.adminToken, map[string]any{"name": "partner"}), 201, "")
		want(t, s.call(post, "/api/v1/projects/web/service-accounts", fx.adminToken, map[string]any{"name": "partner"}), 409, "already_exists")
		want(t, s.call(post, "/api/v1/projects/nope/service-accounts", fx.adminToken, map[string]any{"name": "partner"}), 404, "")
	})
	t.Run("R-AUTH-020 the routes address the account of their own place", func(t *testing.T) {
		if m := s.call(get, "/api/v1/service-accounts/partner", fx.adminToken, nil).json(t); m["project"] != nil || str(m, "principal") != "sa:partner" {
			t.Fatalf("the instance-wide route reads the instance-wide account: %v", m)
		}
		if m := s.call(get, "/api/v1/projects/web/service-accounts/partner", fx.adminToken, nil).json(t); str(m, "project") != "web" || str(m, "principal") != "sa:web/partner" {
			t.Fatalf("%v", m)
		}
		want(t, s.call(get, "/api/v1/projects/web/service-accounts/ghost", fx.adminToken, nil), 404, "")
		if got := list(t, s.call(get, "/api/v1/projects/web/service-accounts", fx.adminToken, nil)); len(got) != 1 || str(got[0], "name") != "partner" {
			t.Fatalf("%v", got)
		}
		if got := list(t, s.call(get, "/api/v1/service-accounts?project=web", fx.adminToken, nil)); len(got) != 1 || str(got[0], "project") != "web" {
			t.Fatalf("%v", got)
		}
		if got := list(t, s.call(get, "/api/v1/service-accounts", fx.adminToken, nil)); len(got) != 3 {
			t.Fatalf("%v", got)
		}
	})
	t.Run("Q-AUTHZ-175 a project account joins no group over HTTP", func(t *testing.T) {
		want(t, s.call(post, "/api/v1/groups", fx.adminToken, map[string]any{"name": "devs"}), 201, "")
		want(t, s.call(patch, "/api/v1/groups/devs/members", fx.adminToken, map[string]any{"add": []string{"sa:web/partner"}}), 422, "project_account_not_groupable")
		want(t, s.call(patch, "/api/v1/groups/devs/members", fx.adminToken, map[string]any{"add": []string{"sa:partner"}}), 200, "")
	})
	t.Run("R-AUTH-020 disabling, enabling and deleting on the project's route", func(t *testing.T) {
		want(t, s.call(post, "/api/v1/projects/mobile/service-accounts/partner/disable", fx.adminToken, nil), 204, "")
		want(t, s.call(post, "/api/v1/projects/mobile/service-accounts/partner/enable", fx.adminToken, nil), 204, "")
		want(t, s.call(del, "/api/v1/projects/mobile/service-accounts/partner", fx.adminToken, nil), 204, "")
		want(t, s.call(get, "/api/v1/projects/mobile/service-accounts/partner", fx.adminToken, nil), 404, "")
		want(t, s.call(get, "/api/v1/service-accounts/partner", fx.adminToken, nil), 200, "")
	})
}

// TestR_AUTHZ_026_OwnerServiceAccountsAndTokens is the acceptance of the delegated service accounts: an
// Owner of a project creates, disables and deletes the accounts of its project and issues their tokens,
// and reaches nothing outside it (Q-AUTHZ-169, Q-AUTHZ-177, Q-AUTHZ-208).
func TestR_AUTHZ_026_OwnerServiceAccountsAndTokens(t *testing.T) {
	e := newEnv(t)
	e.project("web")
	e.project("shop")
	a := e.newReplica("a", replicaOpts{tokens: func(c *config.AuthTokens) { c.SAT.AllowNonExpiring = true }})
	fx := e.newFixture(a)
	wanda, devon := a.user("wanda"), a.user("devon")
	e.member("web", wanda.ID, "owner")
	e.member("web", devon.ID, "maintainer")
	// An instance-wide account and an account of another project, which wanda must not reach.
	a.serviceAccount("ext-bot")
	if _, err := a.manager.CreateProjectServiceAccount(context.Background(), admin, "shop", principal.NewServiceAccount{Name: "shop-bot"}); err != nil {
		t.Fatal(err)
	}
	wandaTok, devonTok := a.ownerToken(wanda), a.ownerToken(devon)
	s := a.serve(e, nil)

	t.Run("R-AUTHZ-026 Q-AUTHZ-169 an Owner creates an account of its project and only of it", func(t *testing.T) {
		rp := s.call(post, "/api/v1/projects/web/service-accounts", wandaTok, map[string]any{"name": "web-ci", "description": "builds"})
		want(t, rp, 201, "")
		if str(rp.json(t), "project") != "web" {
			t.Fatalf("%s", rp.Body)
		}
		want(t, s.call(post, "/api/v1/service-accounts", wandaTok, map[string]any{"name": "web-x"}), 403, "forbidden")
		want(t, s.call(post, "/api/v1/projects/shop/service-accounts", wandaTok, map[string]any{"name": "shop-x"}), 404, "")
		want(t, s.call(post, "/api/v1/projects/web/service-accounts", devonTok, map[string]any{"name": "dev-x"}), 403, "forbidden")
	})
	t.Run("R-AUTHZ-026 the list and the read are scoped to the project", func(t *testing.T) {
		want(t, s.call(get, "/api/v1/service-accounts", wandaTok, nil), 403, "forbidden")
		want(t, s.call(get, "/api/v1/projects/shop/service-accounts", wandaTok, nil), 404, "")
		if got := list(t, s.call(get, "/api/v1/projects/web/service-accounts", wandaTok, nil)); len(got) != 1 || str(got[0], "name") != "web-ci" {
			t.Fatalf("%v", got)
		}
		want(t, s.call(get, "/api/v1/projects/web/service-accounts/web-ci", wandaTok, nil), 200, "")
		for _, name := range []string{"ext-bot", "shop-bot", "nobody"} {
			want(t, s.call(get, "/api/v1/projects/web/service-accounts/"+name, wandaTok, nil), 404, "")
		}
		want(t, s.call(get, "/api/v1/service-accounts/web-ci", fx.adminToken, nil), 404, "")
	})
	t.Run("Q-AUTHZ-177 the description changes inside the project only", func(t *testing.T) {
		// R-AUTH-020: the write names the revision it read; an account the caller cannot read has none to name.
		tag := s.call(get, "/api/v1/projects/web/service-accounts/web-ci", wandaTok, nil).Header.Get("ETag")
		if tag == "" {
			t.Fatal("no ETag on the account")
		}
		want(t, s.call(patch, "/api/v1/projects/web/service-accounts/web-ci", wandaTok, map[string]any{"description": "release builds"}, "If-Match", tag), 200, "")
		want(t, s.call(patch, "/api/v1/projects/web/service-accounts/ext-bot", wandaTok, map[string]any{"description": "x"}, "If-Match", "*"), 404, "")
		want(t, s.call(patch, "/api/v1/projects/web/service-accounts/shop-bot", wandaTok, map[string]any{"description": "x"}, "If-Match", "*"), 404, "")
	})
	t.Run("Q-AUTHZ-177 R-AUTH-027 a token is capped, carries no capability and may be bound to an endpoint", func(t *testing.T) {
		path := "/api/v1/projects/web/service-accounts/web-ci/tokens"
		rp := s.call(post, path, wandaTok, map[string]any{"description": "ci", "ttlSeconds": 3600, "scope": map[string]any{"inherit": true, "endpoints": []string{"customers"}}})
		want(t, rp, 201, "")
		m := rp.json(t)
		if m["expiresAt"] == nil || str(m, "token") == "" {
			t.Fatalf("%s", rp.Body)
		}
		if sc, _ := m["scope"].(map[string]any); sc["admin"] == true || len(asList(sc["system"])) != 0 {
			t.Fatalf("scope %v", sc)
		}
		if o, _ := m["owner"].(map[string]any); o["name"] != "web/web-ci" {
			t.Fatalf("the owner is the qualified principal: %s", rp.Body)
		}
		want(t, s.call(post, path, wandaTok, map[string]any{"description": "ci", "ttlSeconds": 0}), 403, "")
		want(t, s.call(post, path, wandaTok, map[string]any{"description": "ci", "ttlSeconds": 8760*3600 + 1}), 400, "ttl_exceeds_maximum")
		want(t, s.call(post, path, wandaTok, map[string]any{"description": "ci", "scope": map[string]any{"inherit": true, "system": []string{"identities:read"}}}), 422, "invalid_scope")
		want(t, s.call(post, path, wandaTok, map[string]any{"description": "ci", "scope": map[string]any{"inherit": true, "admin": true}}), 422, "invalid_scope")
		// R-AUTH-024: the selectors of a scope are instance selectors; a bare key is 400 invalid_scope.
		want(t, s.call(post, path, wandaTok, map[string]any{"description": "ci", "scope": map[string]any{"repositories": []any{map[string]any{"repositories": []string{"npm-local"}, "actions": []string{"read"}}}}}), 400, "invalid_scope")
		want(t, s.call(post, path, wandaTok, map[string]any{"description": "ci", "scope": map[string]any{"repositories": []any{map[string]any{"repositories": []string{"web/*"}, "actions": []string{"read"}}}}}), 201, "")
	})
	t.Run("Q-AUTH-192 a token an Owner issues is not self-rotatable, so it never outlives the cap", func(t *testing.T) {
		path := "/api/v1/projects/web/service-accounts/web-ci/tokens"
		rp := s.call(post, path, wandaTok, map[string]any{"description": "renews itself", "ttlSeconds": 3600, "selfRotatable": true})
		want(t, rp, 422, "validation_failed")
		if errs, _ := rp.json(t)["errors"].([]any); len(errs) != 1 || errs[0].(map[string]any)["pointer"] != "/selfRotatable" {
			t.Fatalf("%s", rp.Body)
		}
		want(t, s.call(post, path, wandaTok, map[string]any{"description": "fixed", "ttlSeconds": 3600, "selfRotatable": false}), 201, "")
		// An administrator of trove still issues one (R-AUTH-030).
		want(t, s.call(post, path, fx.adminToken, map[string]any{"description": "renews itself", "ttlSeconds": 3600, "selfRotatable": true}), 201, "")
	})
	t.Run("Q-AUTHZ-177 the tokens of the project's accounts are seen and revoked, no others", func(t *testing.T) {
		toks := list(t, s.call(get, "/api/v1/tokens?owner=sa:web/web-ci", wandaTok, nil))
		if len(toks) < 2 {
			t.Fatalf("%v", toks)
		}
		id := str(toks[0], "id")
		want(t, s.call(get, "/api/v1/tokens/"+id, wandaTok, nil), 200, "")
		want(t, s.call(del, "/api/v1/tokens/"+id, wandaTok, nil), 204, "")
		// Another account's tokens are no business of wanda's.
		for _, c := range []struct{ owner, path string }{{"ext-bot", "/api/v1/service-accounts/ext-bot"}, {"shop/shop-bot", "/api/v1/projects/shop/service-accounts/shop-bot"}} {
			sa, err := a.manager.ServiceAccountByRef(context.Background(), c.owner)
			if err != nil {
				t.Fatal(err)
			}
			other := a.sat(sa)
			want(t, s.call(get, "/api/v1/tokens/"+other.Record.ID, wandaTok, nil), 404, "")
			want(t, s.call(del, "/api/v1/tokens/"+other.Record.ID, wandaTok, nil), 404, "")
			for _, tk := range list(t, s.call(get, "/api/v1/tokens?owner=sa:"+c.owner, wandaTok, nil)) {
				if o, _ := tk["owner"].(map[string]any); o["kind"] != "user" || o["name"] != "wanda" || str(tk, "id") == other.Record.ID {
					t.Fatalf("%s: the list of another account shows %v", c.owner, tk)
				}
			}
			// A global administrator sees the account's own token, and only the account's.
			seen := false
			for _, tk := range list(t, s.call(get, "/api/v1/tokens?owner=sa:"+c.owner, fx.adminToken, nil)) {
				if o, _ := tk["owner"].(map[string]any); o["kind"] != "service_account" || o["name"] != c.owner {
					t.Fatalf("%s: the administrator's list shows %v", c.owner, tk)
				}
				seen = seen || str(tk, "id") == other.Record.ID
			}
			if !seen {
				t.Fatalf("%s: the administrator does not see the account's token", c.owner)
			}
			want(t, s.call(del, c.path+"/tokens", wandaTok, nil), 404, "")
			want(t, s.call(post, c.path+"/tokens", wandaTok, map[string]any{"description": "x"}), 403, "forbidden")
		}
		want(t, s.call(del, "/api/v1/projects/web/service-accounts/web-ci/tokens", wandaTok, nil), 204, "")
		if got := list(t, s.call(get, "/api/v1/tokens?owner=sa:web/web-ci", wandaTok, nil)); len(got) != 0 {
			t.Fatalf("%v", got)
		}
	})
	t.Run("R-AUTH-020 disabling, enabling and deleting an account of the project", func(t *testing.T) {
		want(t, s.call(post, "/api/v1/projects/web/service-accounts/web-ci/disable", wandaTok, nil), 204, "")
		want(t, s.call(post, "/api/v1/projects/web/service-accounts/web-ci/enable", wandaTok, nil), 204, "")
		for _, name := range []string{"ext-bot", "shop-bot"} {
			want(t, s.call(post, "/api/v1/projects/web/service-accounts/"+name+"/disable", wandaTok, nil), 404, "")
			want(t, s.call(del, "/api/v1/projects/web/service-accounts/"+name, wandaTok, nil), 404, "")
		}
		want(t, s.call(del, "/api/v1/projects/web/service-accounts/web-ci", wandaTok, nil), 204, "")
		want(t, s.call(get, "/api/v1/projects/web/service-accounts/web-ci", fx.adminToken, nil), 404, "")
	})
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}
