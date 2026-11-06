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
)

// The project service accounts and the tokens a project administrator issues for them
// (R-AUTHZ-026, R-AUTH-020, R-AUTH-027, Q-AUTHZ-169, Q-AUTHZ-175 to Q-AUTHZ-177).

// project inserts a project row and returns its id. It runs before the replicas
// serve: the decision point builds its index from PostgreSQL when it starts.
func (e *env) project(key string, managePolicies, manageRepositories bool) string {
	e.t.Helper()
	var id string
	err := e.admin.QueryRow(context.Background(),
		`INSERT INTO project (id, key, display_name, manage_policies, manage_repositories)
		 VALUES (gen_random_uuid(), $1, $1, $2, $3) RETURNING id::text`, key, managePolicies, manageRepositories).Scan(&id)
	if err != nil {
		e.t.Fatalf("project %s: %v", key, err)
	}
	return id
}

// projectAdmin makes a user or group an administrator of the project.
func (e *env) projectAdmin(projectKey, principalID string) {
	e.t.Helper()
	e.exec(`INSERT INTO project_admin (project_id, principal_id) SELECT id, $2::uuid FROM project WHERE key = $1`, projectKey, principalID)
}

// projectAdminToken is a token a project administrator uses: it exercises
// administration through admin: true (Q-AUTHZ-183) and holds no capability of its own.
func (r *replica) projectAdminToken(u principal.User) string {
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
	e.project("web", true, false)
	a := e.newReplica("a", replicaOpts{tokens: func(c *config.AuthTokens) { c.SAT.AllowNonExpiring = true }})
	ctx := context.Background()
	create := func(name, project string) (principal.ServiceAccount, error) {
		return a.manager.CreateProjectServiceAccount(ctx, admin, project, principal.NewServiceAccount{Name: name})
	}

	t.Run("R-AUTH-020 R-AUTHZ-026 a project service account carries its project and its events name it", func(t *testing.T) {
		sa, err := create("web-ci", "web")
		if err != nil || sa.Project != "web" {
			t.Fatalf("%+v %v", sa, err)
		}
		got, err := a.manager.GetServiceAccount(ctx, "web-ci")
		if err != nil || got.Project != "web" {
			t.Fatalf("%+v %v", got, err)
		}
		if d := detailsOf(t, a.rec.named(audit.EventSACreated)[0]); d["project"] != "web" {
			t.Errorf("serviceaccount.created details %v", d)
		}
		if _, err := a.manager.UpdateServiceAccount(ctx, admin, "web-ci", "builds", errs.AnyRevision); err != nil {
			t.Fatal(err)
		}
		if d := detailsOf(t, a.rec.named(audit.EventSAUpdated)[0]); d["project"] != "web" {
			t.Errorf("serviceaccount.updated details %v", d)
		}
	})
	t.Run("Q-AUTHZ-176 the name starts with the key and a hyphen, and the project exists", func(t *testing.T) {
		_, err := create("ci-web", "web")
		wantCode(t, err, 422, errs.CodeProjectPrefixRequired)
		_, err = create("web-x", "nope")
		wantCode(t, err, 422, errs.CodeUnknownProject)
		_, err = a.manager.CreateProjectServiceAccount(ctx, admin, "web", principal.NewServiceAccount{Name: "web-adm", Admin: true})
		wantCode(t, err, 400, errs.CodeRoleNotAllowed)
		// The key and the hyphen alone name nothing: the trigger (AI003) refuses it, and the
		// code answers before it does (Q-AUTH-193).
		_, err = create("web-", "web")
		wantCode(t, err, 422, errs.CodeProjectPrefixRequired)
		if n := e.scalar(`SELECT count(*) FROM principal WHERE name = 'web-'`); n != 0 {
			t.Fatal("the refused create left a principal")
		}
	})
	t.Run("Q-AUTHZ-176 a global service account takes no name that a project reserves", func(t *testing.T) {
		_, err := create("web-global", "")
		wantCode(t, err, 422, errs.CodeNameReservedByProject)
		for _, name := range []string{"webby", "web", "shop-bot"} { // no hyphen after the key, or no such project
			if _, err := create(name, ""); err != nil {
				t.Errorf("%s: %v", name, err)
			}
		}
	})
	t.Run("R-AUTH-020 the list filters by project", func(t *testing.T) {
		got, err := a.manager.ListServiceAccounts(ctx, "", "web", "", 50)
		if err != nil || len(got) != 1 || got[0].Name != "web-ci" {
			t.Fatalf("%+v %v", got, err)
		}
		all, err := a.manager.ListServiceAccounts(ctx, "", "", "", 50)
		if err != nil || len(all) < 4 {
			t.Fatalf("%+v %v", all, err)
		}
	})
	t.Run("Q-AUTHZ-175 a project service account is no group member, a global one is", func(t *testing.T) {
		if _, err := a.manager.CreateGroup(ctx, admin, principal.NewGroup{Name: "devs"}); err != nil {
			t.Fatal(err)
		}
		_, err := a.manager.PatchMembers(ctx, admin, "devs", "", []string{"sa:web-ci"}, nil)
		wantCode(t, err, 422, errs.CodeProjectAccountNotGroupable)
		_, err = a.manager.CreateGroup(ctx, admin, principal.NewGroup{Name: "devs2", Members: []string{"sa:web-ci"}})
		wantCode(t, err, 422, errs.CodeProjectAccountNotGroupable)
		if _, err := a.manager.PatchMembers(ctx, admin, "devs", "", []string{"sa:webby"}, nil); err != nil {
			t.Fatalf("a global account joins a group: %v", err)
		}
		if n := e.scalar(`SELECT count(*) FROM group_membership gm JOIN principal p ON p.id = gm.member_id WHERE p.name = 'web-ci'`); n != 0 {
			t.Fatalf("%d memberships of the project account", n)
		}
	})
	t.Run("Q-AUTHZ-177 R-AUTH-027 a token issued for a project administrator names no system item, has admin false and expires", func(t *testing.T) {
		sa, err := a.manager.GetServiceAccount(ctx, "web-ci")
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

		c, err := issue(`{"repositories":[{"repositories":["web-*"],"actions":["read"]}],"endpoints":["customers"]}`, nil)
		if err != nil {
			t.Fatalf("a repository grant with an endpoint binding: %v", err)
		}
		if c.Record.ExpiresAt == nil || len(c.Record.Scope.System) != 0 || c.Record.Scope.Admin {
			t.Fatalf("%+v", c.Record)
		}
		if d := detailsOf(t, a.rec.named(audit.EventTokenCreated)[0]); d["project"] != "web" {
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
	t.Run("R-AUTH-020 disabling and deleting a project account, its events name the project and its tokens go", func(t *testing.T) {
		if err := a.manager.DisableServiceAccount(ctx, admin, "web-ci", false); err != nil {
			t.Fatal(err)
		}
		if d := detailsOf(t, a.rec.named(audit.EventSADisabled)[0]); d["project"] != "web" {
			t.Errorf("serviceaccount.disabled details %v", d)
		}
		if err := a.manager.EnableServiceAccount(ctx, admin, "web-ci"); err != nil {
			t.Fatal(err)
		}
		if n := e.scalar(`SELECT count(*) FROM api_token t JOIN principal p ON p.id = t.principal_id WHERE p.name = 'web-ci'`); n == 0 {
			t.Fatal("the account has no token to lose")
		}
		if err := a.manager.DeleteServiceAccount(ctx, admin, "web-ci"); err != nil {
			t.Fatal(err)
		}
		if d := detailsOf(t, a.rec.named(audit.EventSADeleted)[0]); d["project"] != "web" {
			t.Errorf("serviceaccount.deleted details %v", d)
		}
		if n := e.scalar(`SELECT count(*) FROM api_token WHERE description IN ('ci', 'x')`); n != 0 {
			t.Fatalf("%d tokens of the deleted account remain", n)
		}
	})
	t.Run("Q-AUTHZ-175 the schema refuses a project service account in a group, whatever the code does", func(t *testing.T) {
		sa, err := create("web-two", "web")
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
	t.Run("Q-AUTH-193 the schema refuses a project account named by the key and the hyphen alone", func(t *testing.T) {
		id := principal.NewID()
		e.exec(`INSERT INTO principal (id, kind, name) VALUES ($1::uuid, 'service_account', 'web-')`, id)
		_, err := e.admin.Exec(ctx, `INSERT INTO service_account (principal_id, description, project_id)
			SELECT $1::uuid, '', id FROM project WHERE key = 'web'`, id)
		if err == nil || !strings.Contains(err.Error(), "AI003") {
			t.Fatalf("the insert answered %v", err)
		}
	})
}

// TestR_AUTHZ_026_ProjectServiceAccountsOverHTTP drives what a holder of identities:write
// does with projects; no project administrator takes part.
func TestR_AUTHZ_026_ProjectServiceAccountsOverHTTP(t *testing.T) {
	e := newEnv(t)
	e.project("web", true, false)
	a := e.newReplica("a", replicaOpts{})
	fx := e.newFixture(a)
	s := a.serve(e, nil)

	t.Run("R-AUTHZ-026 an administrator creates a project account, lists it by project and reads its project", func(t *testing.T) {
		rp := s.call(post, "/api/v1/service-accounts", fx.adminToken, map[string]any{"name": "web-ci", "project": "web"})
		want(t, rp, 201, "")
		if str(rp.json(t), "project") != "web" {
			t.Fatalf("%s", rp.Body)
		}
		want(t, s.call(post, "/api/v1/service-accounts", fx.adminToken, map[string]any{"name": "plain-bot"}), 201, "")
		if got := list(t, s.call(get, "/api/v1/service-accounts?project=web", fx.adminToken, nil)); len(got) != 1 || str(got[0], "name") != "web-ci" {
			t.Fatalf("%v", got)
		}
		if got := list(t, s.call(get, "/api/v1/service-accounts", fx.adminToken, nil)); len(got) != 2 {
			t.Fatalf("%v", got)
		}
		if m := s.call(get, "/api/v1/service-accounts/plain-bot", fx.adminToken, nil).json(t); m["project"] != nil {
			t.Fatalf("a global account has project null: %v", m)
		}
	})
	t.Run("Q-AUTHZ-176 the name rules answer 422", func(t *testing.T) {
		want(t, s.call(post, "/api/v1/service-accounts", fx.adminToken, map[string]any{"name": "ci-web", "project": "web"}), 422, "project_prefix_required")
		want(t, s.call(post, "/api/v1/service-accounts", fx.adminToken, map[string]any{"name": "web-global"}), 422, "name_reserved_by_project")
		want(t, s.call(post, "/api/v1/service-accounts", fx.adminToken, map[string]any{"name": "x-bot", "project": "nope"}), 422, "unknown_project")
	})
	t.Run("Q-AUTHZ-175 a project account joins no group over HTTP", func(t *testing.T) {
		want(t, s.call(post, "/api/v1/groups", fx.adminToken, map[string]any{"name": "devs"}), 201, "")
		want(t, s.call(patch, "/api/v1/groups/devs/members", fx.adminToken, map[string]any{"add": []string{"sa:web-ci"}}), 422, "project_account_not_groupable")
		want(t, s.call(patch, "/api/v1/groups/devs/members", fx.adminToken, map[string]any{"add": []string{"sa:plain-bot"}}), 200, "")
	})
}

// TestR_AUTHZ_026_ProjectAdministratorServiceAccountsAndTokens is the acceptance of the
// delegated service accounts: a project administrator creates, disables and deletes the
// accounts of its project and issues their tokens, and reaches nothing outside it.
func TestR_AUTHZ_026_ProjectAdministratorServiceAccountsAndTokens(t *testing.T) {
	e := newEnv(t)
	e.project("web", true, false)
	e.project("shop", true, false)
	e.project("lab", false, false) // managePolicies is off
	a := e.newReplica("a", replicaOpts{tokens: func(c *config.AuthTokens) { c.SAT.AllowNonExpiring = true }})
	fx := e.newFixture(a)
	wanda, lena := a.user("wanda"), a.user("lena")
	e.projectAdmin("web", wanda.ID)
	e.projectAdmin("lab", lena.ID)
	// A global account and an account of another project, which wanda must not reach.
	a.serviceAccount("ext-bot")
	if _, err := a.manager.CreateProjectServiceAccount(context.Background(), admin, "shop", principal.NewServiceAccount{Name: "shop-bot"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.manager.CreateProjectServiceAccount(context.Background(), admin, "lab", principal.NewServiceAccount{Name: "lab-bot"}); err != nil {
		t.Fatal(err)
	}
	wandaTok, lenaTok := a.projectAdminToken(wanda), a.projectAdminToken(lena)
	s := a.serve(e, nil)

	t.Run("R-AUTHZ-026 Q-AUTHZ-169 a project administrator creates an account of its project and only of it", func(t *testing.T) {
		rp := s.call(post, "/api/v1/service-accounts", wandaTok, map[string]any{"name": "web-ci", "project": "web", "description": "builds"})
		want(t, rp, 201, "")
		if str(rp.json(t), "project") != "web" {
			t.Fatalf("%s", rp.Body)
		}
		want(t, s.call(post, "/api/v1/service-accounts", wandaTok, map[string]any{"name": "web-x"}), 403, "forbidden")
		want(t, s.call(post, "/api/v1/service-accounts", wandaTok, map[string]any{"name": "shop-x", "project": "shop"}), 404, "")
		want(t, s.call(post, "/api/v1/service-accounts", wandaTok, map[string]any{"name": "ci-web", "project": "web"}), 422, "project_prefix_required")
		want(t, s.call(post, "/api/v1/service-accounts", wandaTok, map[string]any{"name": "web-global"}), 403, "forbidden")
		want(t, s.call(post, "/api/v1/service-accounts", lenaTok, map[string]any{"name": "lab-ci", "project": "lab"}), 403, "project_switch_off")
	})
	t.Run("R-AUTHZ-026 the list and the read are scoped to the project", func(t *testing.T) {
		want(t, s.call(get, "/api/v1/service-accounts", wandaTok, nil), 403, "forbidden")
		want(t, s.call(get, "/api/v1/service-accounts?project=shop", wandaTok, nil), 404, "")
		if got := list(t, s.call(get, "/api/v1/service-accounts?project=web", wandaTok, nil)); len(got) != 1 || str(got[0], "name") != "web-ci" {
			t.Fatalf("%v", got)
		}
		want(t, s.call(get, "/api/v1/service-accounts/web-ci", wandaTok, nil), 200, "")
		for _, name := range []string{"ext-bot", "shop-bot", "nobody"} {
			want(t, s.call(get, "/api/v1/service-accounts/"+name, wandaTok, nil), 404, "")
		}
	})
	t.Run("Q-AUTHZ-177 the description changes inside the project only", func(t *testing.T) {
		// R-AUTH-020: the write names the revision it read; an account the caller cannot read has none to name.
		tag := s.call(get, "/api/v1/service-accounts/web-ci", wandaTok, nil).Header.Get("ETag")
		if tag == "" {
			t.Fatal("no ETag on the account")
		}
		want(t, s.call(patch, "/api/v1/service-accounts/web-ci", wandaTok, map[string]any{"description": "release builds"}, "If-Match", tag), 200, "")
		want(t, s.call(patch, "/api/v1/service-accounts/ext-bot", wandaTok, map[string]any{"description": "x"}, "If-Match", "*"), 404, "")
		want(t, s.call(patch, "/api/v1/service-accounts/shop-bot", wandaTok, map[string]any{"description": "x"}, "If-Match", "*"), 404, "")
	})
	t.Run("Q-AUTHZ-177 R-AUTH-027 a token is capped, carries no capability and may be bound to an endpoint", func(t *testing.T) {
		path := "/api/v1/service-accounts/web-ci/tokens"
		rp := s.call(post, path, wandaTok, map[string]any{"description": "ci", "ttlSeconds": 3600, "scope": map[string]any{"inherit": true, "endpoints": []string{"customers"}}})
		want(t, rp, 201, "")
		m := rp.json(t)
		if m["expiresAt"] == nil || str(m, "token") == "" {
			t.Fatalf("%s", rp.Body)
		}
		if sc, _ := m["scope"].(map[string]any); sc["admin"] == true || len(asList(sc["system"])) != 0 {
			t.Fatalf("scope %v", sc)
		}
		want(t, s.call(post, path, wandaTok, map[string]any{"description": "ci", "ttlSeconds": 0}), 403, "")
		want(t, s.call(post, path, wandaTok, map[string]any{"description": "ci", "ttlSeconds": 8760*3600 + 1}), 400, "ttl_exceeds_maximum")
		want(t, s.call(post, path, wandaTok, map[string]any{"description": "ci", "scope": map[string]any{"inherit": true, "system": []string{"identities:read"}}}), 422, "invalid_scope")
		want(t, s.call(post, path, wandaTok, map[string]any{"description": "ci", "scope": map[string]any{"inherit": true, "admin": true}}), 422, "invalid_scope")
		want(t, s.call(post, path, wandaTok, map[string]any{"description": "ci", "scope": map[string]any{"repositories": []any{map[string]any{"repositories": []string{"web-*"}, "actions": []string{"read"}}}}}), 201, "")
	})
	t.Run("Q-AUTH-192 a token a project administrator issues is not self-rotatable, so it never outlives the cap", func(t *testing.T) {
		path := "/api/v1/service-accounts/web-ci/tokens"
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
		toks := list(t, s.call(get, "/api/v1/tokens?owner=sa:web-ci", wandaTok, nil))
		if len(toks) < 2 {
			t.Fatalf("%v", toks)
		}
		id := str(toks[0], "id")
		want(t, s.call(get, "/api/v1/tokens/"+id, wandaTok, nil), 200, "")
		want(t, s.call(del, "/api/v1/tokens/"+id, wandaTok, nil), 204, "")
		// Another account's tokens are no business of wanda's: an owner it does not administer is
		// forced to the caller (R-AUTH-028, Q-AUTHZ-177), so the list is wanda's own tokens and never the account's.
		for _, owner := range []string{"ext-bot", "shop-bot"} {
			sa, err := a.manager.GetServiceAccount(context.Background(), owner)
			if err != nil {
				t.Fatal(err)
			}
			other := a.sat(sa)
			want(t, s.call(get, "/api/v1/tokens/"+other.Record.ID, wandaTok, nil), 404, "")
			want(t, s.call(del, "/api/v1/tokens/"+other.Record.ID, wandaTok, nil), 404, "")
			for _, tk := range list(t, s.call(get, "/api/v1/tokens?owner=sa:"+owner, wandaTok, nil)) {
				if o, _ := tk["owner"].(map[string]any); o["kind"] != "user" || o["name"] != "wanda" || str(tk, "id") == other.Record.ID {
					t.Fatalf("%s: the list of another account shows %v", owner, tk)
				}
			}
			// A global administrator sees the account's own token, and only the account's.
			seen := false
			for _, tk := range list(t, s.call(get, "/api/v1/tokens?owner=sa:"+owner, fx.adminToken, nil)) {
				if o, _ := tk["owner"].(map[string]any); o["kind"] != "service_account" || o["name"] != owner {
					t.Fatalf("%s: the administrator's list shows %v", owner, tk)
				}
				seen = seen || str(tk, "id") == other.Record.ID
			}
			if !seen {
				t.Fatalf("%s: the administrator does not see the account's token", owner)
			}
			want(t, s.call(del, "/api/v1/service-accounts/"+owner+"/tokens", wandaTok, nil), 404, "")
			want(t, s.call(post, "/api/v1/service-accounts/"+owner+"/tokens", wandaTok, map[string]any{"description": "x"}), 403, "forbidden")
		}
		want(t, s.call(del, "/api/v1/service-accounts/web-ci/tokens", wandaTok, nil), 204, "")
		if got := list(t, s.call(get, "/api/v1/tokens?owner=sa:web-ci", wandaTok, nil)); len(got) != 0 {
			t.Fatalf("%v", got)
		}
	})
	t.Run("Q-AUTHZ-177 a switch that is off blocks what widens and never what narrows", func(t *testing.T) {
		want(t, s.call(post, "/api/v1/service-accounts/lab-bot/tokens", lenaTok, map[string]any{"description": "x"}), 403, "project_switch_off")
		want(t, s.call(post, "/api/v1/service-accounts/lab-bot/disable", lenaTok, nil), 204, "")
		want(t, s.call(post, "/api/v1/service-accounts/lab-bot/enable", lenaTok, nil), 403, "project_switch_off")
		tag := s.call(get, "/api/v1/service-accounts/lab-bot", lenaTok, nil).Header.Get("ETag")
		want(t, s.call(patch, "/api/v1/service-accounts/lab-bot", lenaTok, map[string]any{"description": "idle"}, "If-Match", tag), 200, "")
		want(t, s.call(del, "/api/v1/service-accounts/lab-bot/tokens", lenaTok, nil), 204, "")
		want(t, s.call(del, "/api/v1/service-accounts/lab-bot", lenaTok, nil), 204, "")
	})
	t.Run("R-AUTH-020 disabling, enabling and deleting an account of the project", func(t *testing.T) {
		want(t, s.call(post, "/api/v1/service-accounts/web-ci/disable", wandaTok, nil), 204, "")
		want(t, s.call(post, "/api/v1/service-accounts/web-ci/enable", wandaTok, nil), 204, "")
		for _, name := range []string{"ext-bot", "shop-bot"} {
			want(t, s.call(post, "/api/v1/service-accounts/"+name+"/disable", wandaTok, nil), 404, "")
			want(t, s.call(del, "/api/v1/service-accounts/"+name, wandaTok, nil), 404, "")
		}
		want(t, s.call(del, "/api/v1/service-accounts/web-ci", wandaTok, nil), 204, "")
		want(t, s.call(get, "/api/v1/service-accounts/web-ci", fx.adminToken, nil), 404, "")
	})
}

func asList(v any) []any {
	l, _ := v.([]any)
	return l
}
