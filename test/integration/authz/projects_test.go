//go:build integration

package authz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/test/integration/stack"
)

// Projects and delegated administration in the engine and over HTTP
// (R-AUTHZ-026, R-AUTHZ-028, Q-AUTHZ-174, Q-AUTHZ-179, Q-AUTHZ-183). The project
// rows are written with SQL here: the project service is another package's work,
// and what these tests pin is what the grant index and the policy writes do with
// them.

// projectFixture is the setting of the acceptance criterion: project web, administered
// by the group web-leads (alice is a member), with web-npm in it and npm-internal not;
// project shop, administered by bob, with shop-npm.
type projectFixture struct {
	*apiFixture
	leads  stack.Person
	webID  string
	shopID string
}

func newProjectAPI(t *testing.T) *projectFixture {
	t.Helper()
	e := stack.New(t)
	f := &apiFixture{e: e, val: apitest.New(t, v1.EmbeddedYAML())}
	f.admin = e.User("sec-alice")
	e.Admin(f.admin)
	f.alice, f.bob, f.carol = e.User("alice"), e.User("bob"), e.User("carol")
	f.web = e.Group("team-web")
	e.Join(f.web, f.alice)
	f.bot = e.ServiceAccount("ci-bot")
	pf := &projectFixture{apiFixture: f, leads: e.Group("web-leads")}
	e.Join(pf.leads, f.alice)
	e.Repo("web-npm", "local", "npm")
	e.Repo("web-files", "local", "generic")
	e.Repo("npm-internal", "local", "npm")
	e.Repo("shop-npm", "local", "npm")
	pf.webID = e.Text("INSERT INTO project (id, key, display_name) VALUES (gen_random_uuid(), 'web', 'Web platform') RETURNING id::text")
	pf.shopID = e.Text("INSERT INTO project (id, key, display_name, manage_policies) VALUES (gen_random_uuid(), 'shop', 'Shop', true) RETURNING id::text")
	e.Exec("INSERT INTO project_admin (project_id, principal_id) VALUES ($1::uuid, $2::uuid)", pf.webID, pf.leads.ID)
	e.Exec("INSERT INTO project_admin (project_id, principal_id) VALUES ($1::uuid, $2::uuid)", pf.shopID, f.bob.ID)
	e.Exec("UPDATE repositories SET project_id = $1::uuid WHERE key IN ('web-npm', 'web-files')", pf.webID)
	e.Exec("UPDATE repositories SET project_id = $1::uuid WHERE key = 'shop-npm'", pf.shopID)
	o := stack.Options{Validator: f.val}
	f.a, f.b, f.c = e.NewReplica("web-a", o), e.NewReplica("web-b", o), e.NewReplica("web-c", o)
	f.cl = stack.NewCluster(f.a, f.b, f.c)
	return pf
}

// changed notifies the authz area as the project service does in the transaction of
// a project write, and returns the header that makes the next request see it.
func (f *projectFixture) changed(t *testing.T) []string {
	t.Helper()
	err := f.a.DB.InTx(context.Background(), func(tx meta.Tx) error {
		return f.a.Changes.Notify(tx, authz.AreaAuthz, 1, map[string]string{"type": "project", "id": "test"})
	})
	if err != nil {
		t.Fatal(err)
	}
	return f.current()
}

func projectPolicyBody(name, project string, repositories []string, entries ...map[string]any) map[string]any {
	b := policyBody(name, entries...)
	b["project"] = project
	b["resources"] = map[string]any{"repositories": repositories}
	return b
}

func listNames(t *testing.T, r stack.Response) []string {
	t.Helper()
	var out []string
	for _, it := range r.Items(t) {
		out = append(out, it["name"].(string))
	}
	return out
}

// The acceptance criteria of R-AUTHZ-026 for project policies, over HTTP on three
// replicas: a member of the administrator group writes policies for the project's
// repositories and nowhere else.
func TestR_AUTHZ_026_ProjectAdministratorWritesPolicies(t *testing.T) {
	f := newProjectAPI(t)
	alice, admin := f.alice.Name, f.admin.Name

	t.Run("a member of the administrator group creates a policy with project web on web-npm", func(t *testing.T) {
		r := f.cl.Do(alice, "POST", policies, projectPolicyBody("web-publish", "web", []string{"web-npm"}, entry("group:team-web", "read", "deploy")))
		wantStatus(t, r, 201, "")
		if r.Epoch() == 0 || r.Header.Get("ETag") != `"1"` {
			t.Fatalf("epoch %d, ETag %q", r.Epoch(), r.Header.Get("ETag"))
		}
		if got := r.JSON(t)["project"]; got != "web" {
			t.Fatalf("project = %v", got)
		}
		if n := f.e.Number("SELECT count(*) FROM audit_event WHERE action = 'authz.policy.created' AND resource_type = 'permission_policy' AND details->>'project' = 'web'"); n != 1 {
			t.Fatalf("%d audit events with the project", n)
		}
	})
	t.Run("the policy's entries are stored in authz_project_policy_entry, never in authz_policy_entry", func(t *testing.T) {
		if n := f.e.Number("SELECT count(*) FROM authz_policy_entry e JOIN authz_policy p ON p.id = e.policy_id WHERE p.name = 'web-publish'"); n != 0 {
			t.Fatalf("%d rows in authz_policy_entry", n)
		}
		if n := f.e.Number("SELECT count(*) FROM authz_project_policy_entry e JOIN authz_policy p ON p.id = e.policy_id WHERE p.name = 'web-publish'"); n != 1 {
			t.Fatalf("%d rows in authz_project_policy_entry", n)
		}
	})
	t.Run("a policy on npm-internal is refused: repository_outside_project", func(t *testing.T) {
		wantStatus(t, f.cl.Do(alice, "POST", policies, projectPolicyBody("web-out", "web", []string{"npm-internal"}, entry("group:team-web", "read"))), 422, "repository_outside_project")
	})
	t.Run("another project's repository and an unknown key answer the same", func(t *testing.T) {
		wantStatus(t, f.cl.Do(alice, "POST", policies, projectPolicyBody("web-shop", "web", []string{"shop-npm"})), 422, "repository_outside_project")
		wantStatus(t, f.cl.Do(alice, "POST", policies, projectPolicyBody("web-none", "web", []string{"no-such-repository"})), 422, "repository_outside_project")
	})
	t.Run("the name starts with the project key", func(t *testing.T) {
		wantStatus(t, f.cl.Do(alice, "POST", policies, projectPolicyBody("publish", "web", []string{"web-npm"})), 422, "project_prefix_required")
	})
	t.Run("a project the caller does not administer or that does not exist is unknown_project", func(t *testing.T) {
		wantStatus(t, f.cl.Do(alice, "POST", policies, projectPolicyBody("shop-read", "shop", []string{"shop-npm"})), 422, "unknown_project")
		wantStatus(t, f.cl.Do(alice, "POST", policies, projectPolicyBody("nope-read", "nope", []string{"web-npm"})), 422, "unknown_project")
		wantStatus(t, f.cl.Do(admin, "POST", policies, projectPolicyBody("nope-read", "nope", []string{"web-npm"})), 422, "unknown_project")
	})
	t.Run("a global policy needs policies:write", func(t *testing.T) {
		wantStatus(t, f.cl.Do(alice, "POST", policies, policyBody("global-one", entry("group:team-web", "read"))), 403, "forbidden")
	})
	t.Run("a project policy grants to no anonymous and no authenticated, and no CI mapping manage", func(t *testing.T) {
		wantStatus(t, f.cl.Do(alice, "POST", policies, projectPolicyBody("web-anon", "web", []string{"web-npm"}, entry("anonymous", "read"))), 422, "principal_outside_project")
		wantStatus(t, f.cl.Do(alice, "POST", policies, projectPolicyBody("web-auth", "web", []string{"web-npm"}, entry("authenticated", "read"))), 422, "principal_outside_project")
	})
	t.Run("a pattern selector * of a project policy covers only the project's repositories", func(t *testing.T) {
		r := f.cl.Do(alice, "POST", policies, projectPolicyBody("web-all", "web", []string{"*"}, entry("group:team-web", "read")))
		wantStatus(t, r, 201, "")
		v := f.cl.Do(alice, "POST", "/api/v1/authz/validate-policy", projectPolicyBody("web-all", "web", []string{"*"}, entry("group:team-web", "read")))
		wantStatus(t, v, 200, "")
		covers, _ := v.JSON(t)["covers"].([]any)
		if fmt.Sprint(covers) != "[web-files web-npm]" {
			t.Fatalf("covers %v", covers)
		}
	})
	t.Run("a project administrator cannot bind a system role or read the bindings", func(t *testing.T) {
		wantStatus(t, f.cl.Do(alice, "PUT", "/api/v1/role-bindings/auditor/user:carol", nil), 403, "forbidden")
		wantStatus(t, f.cl.Do(alice, "GET", "/api/v1/role-bindings", nil), 403, "forbidden")
	})

	// A global policy, and a policy of the other project, that alice must not see.
	wantStatus(t, f.cl.Do(admin, "POST", policies, policyBody("global-readers", entry("group:team-web", "read"))), 201, "")
	wantStatus(t, f.cl.Do(admin, "POST", policies, projectPolicyBody("shop-publish", "shop", []string{"shop-npm"}, entry("user:bob", "deploy"))), 201, "")

	t.Run("the list of a project administrator needs project=<key> and shows only that project's policies", func(t *testing.T) {
		wantStatus(t, f.cl.Do(alice, "GET", policies, nil), 403, "forbidden")
		r := f.cl.Do(alice, "GET", policies+"?project=web", nil)
		wantStatus(t, r, 200, "")
		if got := strings.Join(listNames(t, r), ","); got != "web-all,web-publish" {
			t.Fatalf("policies %s", got)
		}
		// A project the caller does not administer answers as one that does not exist.
		wantStatus(t, f.cl.Do(alice, "GET", policies+"?project=shop", nil), 403, "forbidden")
		wantStatus(t, f.cl.Do(alice, "GET", policies+"?project=nope", nil), 403, "forbidden")
	})
	t.Run("a holder of policies:read sees every policy and may filter by project", func(t *testing.T) {
		all := listNames(t, f.cl.Do(admin, "GET", policies, nil))
		if got := strings.Join(all, ","); got != "global-readers,shop-publish,web-all,web-publish" {
			t.Fatalf("policies %s", got)
		}
		r := f.cl.Do(admin, "GET", policies+"?project=shop", nil)
		wantStatus(t, r, 200, "")
		if got := strings.Join(listNames(t, r), ","); got != "shop-publish" {
			t.Fatalf("policies %s", got)
		}
		wantStatus(t, f.cl.Do(admin, "GET", policies+"?project=nope", nil), 422, "unknown_project")
	})
	t.Run("get: a project policy of its project, 404 for a global one and another project's", func(t *testing.T) {
		wantStatus(t, f.cl.Do(alice, "GET", policies+"/web-publish", nil), 200, "")
		wantStatus(t, f.cl.Do(alice, "GET", policies+"/global-readers", nil), 404, "not_found")
		wantStatus(t, f.cl.Do(alice, "GET", policies+"/shop-publish", nil), 404, "not_found")
	})
	t.Run("writes of a global policy are 403, of another project's policy 404", func(t *testing.T) {
		wantStatus(t, f.cl.Do(alice, "PUT", policies+"/global-readers", policyBody("global-readers"), "If-Match", "*"), 403, "forbidden")
		wantStatus(t, f.cl.Do(alice, "DELETE", policies+"/global-readers", nil), 403, "forbidden")
		wantStatus(t, f.cl.Do(alice, "PUT", policies+"/shop-publish", projectPolicyBody("shop-publish", "shop", []string{"shop-npm"}), "If-Match", "*"), 404, "not_found")
		wantStatus(t, f.cl.Do(alice, "DELETE", policies+"/shop-publish", nil), 404, "not_found")
		wantStatus(t, f.cl.Do(alice, "DELETE", policies+"/no-such-policy", nil), 404, "not_found")
	})
	t.Run("the project of a policy is fixed at creation", func(t *testing.T) {
		g := f.cl.Do(admin, "GET", policies+"/web-publish", nil)
		put := projectPolicyBody("web-publish", "shop", []string{"shop-npm"}, entry("group:team-web", "read"))
		wantStatus(t, f.cl.Do(admin, "PUT", policies+"/web-publish", put, "If-Match", g.Header.Get("ETag")), 422, "immutable")
		global := policyBody("web-publish", entry("group:team-web", "read"))
		wantStatus(t, f.cl.Do(admin, "PUT", policies+"/web-publish", global, "If-Match", g.Header.Get("ETag")), 422, "immutable")
		wantStatus(t, f.cl.Do(admin, "PATCH", policies+"/web-publish", map[string]any{"project": "shop"}, "If-Match", g.Header.Get("ETag"), "Content-Type", "application/merge-patch+json"), 422, "immutable")
	})
	t.Run("replace, patch and delete of a project policy by its administrator, each with the epoch", func(t *testing.T) {
		g := f.cl.Do(alice, "GET", policies+"/web-publish", nil)
		put := projectPolicyBody("web-publish", "web", []string{"web-npm"}, entry("group:team-web", "read"), entry("user:bob", "read"))
		r := f.cl.Do(alice, "PUT", policies+"/web-publish", put, "If-Match", g.Header.Get("ETag"))
		wantStatus(t, r, 200, "")
		if r.Epoch() == 0 {
			t.Fatal("no epoch")
		}
		p := f.cl.Do(alice, "PATCH", policies+"/web-publish", map[string]any{"description": "web"}, "If-Match", r.Header.Get("ETag"), "Content-Type", "application/merge-patch+json")
		wantStatus(t, p, 200, "")
		d := f.cl.Do(alice, "DELETE", policies+"/web-publish", nil)
		wantStatus(t, d, 204, "")
		if d.Epoch() == 0 {
			t.Fatal("no epoch")
		}
		if n := f.e.Number("SELECT count(*) FROM authz_project_policy_entry e WHERE NOT EXISTS (SELECT 1 FROM authz_policy p WHERE p.id = e.policy_id)"); n != 0 {
			t.Fatalf("%d orphaned entries", n)
		}
	})
	t.Run("a caller that administers nothing is refused at the declaration", func(t *testing.T) {
		wantStatus(t, f.cl.Do(f.carol.Name, "GET", policies+"?project=web", nil), 403, "forbidden")
		wantStatus(t, f.cl.Do(f.carol.Name, "POST", policies, projectPolicyBody("web-x", "web", []string{"web-npm"})), 403, "forbidden")
	})
}

// The switch managePolicies: off, a project administrator still reads and removes
// nothing it could not, but writes no policy (R-AUTHZ-026, Q-AUTHZ-180).
func TestR_AUTHZ_026_ProjectSwitchOffRefusesPolicyWrites(t *testing.T) {
	f := newProjectAPI(t)
	alice := f.alice.Name
	wantStatus(t, f.cl.Do(alice, "POST", policies, projectPolicyBody("web-publish", "web", []string{"web-npm"}, entry("group:team-web", "read"))), 201, "")

	f.e.Exec("UPDATE project SET manage_policies = false WHERE key = 'web'")
	hdr := f.changed(t)
	// The shared step admits a caller that administers any project; the handler names
	// the switch, whatever other projects the caller administers (Q-AUTHZ-180).
	wantStatus(t, f.cl.Do(alice, "POST", policies, projectPolicyBody("web-two", "web", []string{"web-npm"}), hdr...), 403, "project_switch_off")
	wantStatus(t, f.cl.Do(alice, "GET", policies+"/web-publish", nil, hdr...), 200, "")
	wantStatus(t, f.cl.Do(alice, "GET", policies+"?project=web", nil, hdr...), 200, "")

	// With a second project whose switch is on the answer for web does not change.
	f.e.Exec("INSERT INTO project_admin (project_id, principal_id) VALUES ($1::uuid, $2::uuid)", f.shopID, f.alice.ID)
	hdr = f.changed(t)
	wantStatus(t, f.cl.Do(alice, "POST", policies, projectPolicyBody("web-two", "web", []string{"web-npm"}), hdr...), 403, "project_switch_off")
	wantStatus(t, f.cl.Do(alice, "POST", "/api/v1/authz/validate-policy", projectPolicyBody("web-two", "web", []string{"web-npm"}), hdr...), 403, "project_switch_off")
	wantStatus(t, f.cl.Do(alice, "DELETE", policies+"/web-publish", nil, hdr...), 403, "project_switch_off")
	wantStatus(t, f.cl.Do(alice, "POST", policies, projectPolicyBody("shop-two", "shop", []string{"shop-npm"}), hdr...), 201, "")

	me := f.cl.Do(alice, "GET", "/api/v1/authz/me", nil, hdr...)
	wantStatus(t, me, 200, "")
	projects := me.JSON(t)["projects"].([]any)
	if len(projects) != 2 || projects[1].(map[string]any)["managePolicies"] != false {
		t.Fatalf("projects %v", projects)
	}
}

// GET /authz/me names the projects the caller administers with their switches,
// from the index that answers (Q-AUTHZ-181).
func TestR_AUTHZ_026_AuthzMeListsAdministeredProjects(t *testing.T) {
	f := newProjectAPI(t)
	for name, want := range map[string]string{
		f.alice.Name: `[{"key":"web","managePolicies":true,"manageRepositories":false}]`,
		f.bob.Name:   `[{"key":"shop","managePolicies":true,"manageRepositories":false}]`,
		f.carol.Name: `[]`,
		f.admin.Name: `[]`,
	} {
		r := f.cl.Do(name, "GET", "/api/v1/authz/me", nil)
		wantStatus(t, r, 200, "")
		raw, _ := json.Marshal(r.JSON(t)["projects"])
		if string(raw) != want {
			t.Errorf("%s: projects %s, want %s", name, raw, want)
		}
	}
	// A project administrator holds no capability through the project.
	if caps := f.cl.Do(f.alice.Name, "GET", "/api/v1/authz/me", nil).JSON(t)["capabilities"].([]any); len(caps) != 0 {
		t.Fatalf("capabilities %v", caps)
	}
}

// projectEnv is the engine-level setting: two replicas on one database.
type projectEnv struct {
	*env
	a, b           *replica
	webID, shopID  string
	alice, bob     person
	leads, devs    person
	bot, dave      person
	root           authz.Subject
	notifyReplicas []*replica
}

func newProjectEnv(t *testing.T) *projectEnv {
	t.Helper()
	e := newEnv(t)
	p := &projectEnv{env: e}
	p.root = sub(e.user("sec-alice"))
	p.alice, p.bob, p.dave = e.user("alice"), e.user("bob"), e.user("dave")
	p.leads, p.devs = e.group("web-leads", false), e.group("developers", false)
	p.bot = e.serviceAccount("ci-bot")
	e.repo("web-npm", "local", "npm", "")
	e.repo("web-files", "local", "generic", "")
	e.repo("npm-internal", "local", "npm", "")
	e.repo("npm-web", "local", "npm", "")
	p.webID = e.text("INSERT INTO project (id, key, display_name) VALUES (gen_random_uuid(), 'web', 'Web') RETURNING id::text")
	p.shopID = e.text("INSERT INTO project (id, key, display_name) VALUES (gen_random_uuid(), 'shop', 'Shop') RETURNING id::text")
	e.exec("UPDATE repositories SET project_id = $1::uuid WHERE key IN ('web-npm', 'web-files', 'npm-web')", p.webID)
	e.exec("INSERT INTO project_admin (project_id, principal_id) VALUES ($1::uuid, $2::uuid)", p.webID, p.alice.ID)
	e.exec("INSERT INTO project_admin (project_id, principal_id) VALUES ($1::uuid, $2::uuid)", p.webID, p.leads.ID)
	const poll = 500 * time.Millisecond
	p.a, p.b = e.newReplica("a", replicaOpts{poll: poll}), e.newReplica("b", replicaOpts{poll: poll})
	p.notifyReplicas = []*replica{p.a, p.b}
	return p
}

// changed notifies the authz area as the project service does in the transaction
// of a project write.
func (p *projectEnv) changed() {
	p.t.Helper()
	err := p.a.db.InTx(context.Background(), func(tx meta.Tx) error {
		return p.a.changes.Notify(tx, authz.AreaAuthz, 1, map[string]string{"type": "project", "id": "test"})
	})
	if err != nil {
		p.t.Fatal(err)
	}
}

func (p *projectEnv) manages(r *replica, s authz.Subject, repo string) bool {
	return r.engine.Decide(context.Background(), s, write(repo, "x", "", authz.Manage)).Outcome == authz.Allow
}

// A project administrator holds manage on every repository of the project and
// nothing outside it; a group administrator holds it through its members; a
// service account and a token without admin: true never administer.
func TestR_AUTHZ_026_ProjectAdministratorsHoldManageOnTheProject(t *testing.T) {
	p := newProjectEnv(t)
	aliceSub := sub(p.alice)
	daveSub := sub(p.dave, p.leads)
	bobSub := sub(p.bob)
	for _, r := range []*replica{p.a, p.b} {
		for repo, want := range map[string]bool{"web-npm": true, "web-files": true, "npm-web": true, "npm-internal": false} {
			if got := p.manages(r, aliceSub, repo); got != want {
				t.Errorf("replica %s: alice manage on %s = %v, want %v", r.name, repo, got, want)
			}
			if got := p.manages(r, daveSub, repo); got != want {
				t.Errorf("replica %s: group member manage on %s = %v, want %v", r.name, repo, got, want)
			}
			if p.manages(r, bobSub, repo) {
				t.Errorf("replica %s: bob holds manage on %s", r.name, repo)
			}
		}
	}
	t.Run("a token without admin: true and a service account never administer", func(t *testing.T) {
		inherit := aliceSub
		inherit.Scope = &scope.Scope{Inherit: true}
		admin := aliceSub
		admin.Scope = &scope.Scope{Inherit: true, Admin: true}
		bot := sub(p.bot)
		bot.Scope = &scope.Scope{Inherit: true, Admin: true}
		idx, err := p.a.engine.Begin(context.Background(), aliceSub)
		if err != nil {
			t.Fatal(err)
		}
		for name, tc := range map[string]struct {
			s    authz.Subject
			want int
		}{"session": {aliceSub, 1}, "admin token": {admin, 1}, "inherit token": {inherit, 0}, "service account": {bot, 0}} {
			if got := len(p.a.engine.ProjectsOn(idx, tc.s)); got != tc.want {
				t.Errorf("%s administers %d projects, want %d", name, got, tc.want)
			}
		}
		if !p.manages(p.a, admin, "web-npm") || p.manages(p.a, bot, "web-npm") {
			t.Error("manage through an admin token or a service account")
		}
		// manage on the strength of the project's grant needs the credential's repository scope too.
		if p.manages(p.a, inherit, "npm-internal") {
			t.Error("manage outside the project")
		}
	})
	t.Run("a project policy covers only its project (the two builders agree on every replica)", func(t *testing.T) {
		g := sub(p.dave)
		if _, _, err := p.a.svc.Create(context.Background(), p.root, authz.PolicyInput{
			Name: "a-global", Resources: authz.Resources{Repositories: []string{"npm-*"}},
			Entries: []authz.Entry{{Principal: "user:dave", Actions: []string{"read"}}},
		}); err != nil {
			t.Fatal(err)
		}
		web := "web"
		if _, _, err := p.a.svc.Create(context.Background(), p.root, authz.PolicyInput{
			Name: "web-npm-readers", Project: &web, Resources: authz.Resources{Repositories: []string{"npm-*"}},
			Entries: []authz.Entry{{Principal: "user:bob", Actions: []string{"read"}}},
		}); err != nil {
			t.Fatal(err)
		}
		for _, r := range []*replica{p.a, p.b} {
			waitFor(t, "replica "+r.name+" building both", 3*time.Second, func() bool {
				return r.engine.Decide(context.Background(), g, read("npm-internal", "x", "")).Outcome == authz.Allow &&
					r.engine.Decide(context.Background(), bobSub, read("npm-web", "x", "")).Outcome == authz.Allow
			})
			if r.engine.Decide(context.Background(), g, read("npm-web", "x", "")).Outcome != authz.Allow {
				t.Errorf("replica %s: the global policy lost npm-web", r.name)
			}
			if r.engine.Decide(context.Background(), bobSub, read("npm-internal", "x", "")).Outcome == authz.Allow {
				t.Errorf("replica %s: the project policy covers npm-internal", r.name)
			}
			if r.engine.Decide(context.Background(), bobSub, read("npm-web", "x", "")).Outcome != authz.Allow {
				t.Errorf("replica %s: the project policy does not cover npm-web", r.name)
			}
		}
	})
}

// Removing an administrator removes manage within the bound of R-AUTHZ-028 on a
// replica that did not write, and adding one grants it there.
func TestR_AUTHZ_028_ProjectAdministratorRemovalIsRefusedOnAnotherReplica(t *testing.T) {
	p := newProjectEnv(t)
	const poll = 500 * time.Millisecond
	bound := poll + time.Second
	ctx := context.Background()
	carol := p.user("carol")
	carolSub := sub(carol)
	member := sub(p.dave, p.leads)
	for trial := range 3 {
		p.exec("INSERT INTO project_admin (project_id, principal_id) VALUES ($1::uuid, $2::uuid) ON CONFLICT DO NOTHING", p.webID, carol.ID)
		p.changed()
		waitFor(t, fmt.Sprintf("trial %d: replica b granting manage", trial), bound, func() bool { return p.manages(p.b, carolSub, "web-npm") })
		p.exec("DELETE FROM project_admin WHERE project_id = $1::uuid AND principal_id = $2::uuid", p.webID, carol.ID)
		p.changed()
		start := time.Now()
		waitFor(t, fmt.Sprintf("trial %d: replica b refusing manage", trial), bound, func() bool { return !p.manages(p.b, carolSub, "web-npm") })
		if took := time.Since(start); took > bound {
			t.Errorf("trial %d: refused after %s, bound %s", trial, took, bound)
		}
	}
	t.Run("removing the administrator group takes manage from its members", func(t *testing.T) {
		if !p.manages(p.b, member, "web-files") {
			t.Fatal("the group member does not manage first")
		}
		p.exec("DELETE FROM project_admin WHERE project_id = $1::uuid AND principal_id = $2::uuid", p.webID, p.leads.ID)
		p.changed()
		waitFor(t, "replica b refusing the group member", bound, func() bool { return !p.manages(p.b, member, "web-files") })
	})
	t.Run("a repository that leaves the project takes the project's manage with it", func(t *testing.T) {
		aliceSub := sub(p.alice)
		if !p.manages(p.b, aliceSub, "web-files") {
			t.Fatal("alice does not manage first")
		}
		p.exec("UPDATE repositories SET project_id = NULL WHERE key = 'web-files'")
		if err := p.a.db.InTx(ctx, func(tx meta.Tx) error {
			return p.a.changes.Notify(tx, authz.AreaRepoConfig, 1, map[string]string{"type": "repository", "id": "web-files"})
		}); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "replica b refusing web-files", bound, func() bool { return !p.manages(p.b, aliceSub, "web-files") })
		if !p.manages(p.b, aliceSub, "web-npm") {
			t.Error("the project's other repository lost the grant")
		}
	})
}

// Q-AUTHZ-174: the grants of a project policy are stored where the previous
// release does not read them, so that release builds an index in which a project
// policy grants nobody.
func TestR_AUTHZ_026_ProjectPolicyIsInvisibleToTheOldBuild(t *testing.T) {
	p := newProjectEnv(t)
	ctx := context.Background()
	web := "web"
	if _, _, err := p.a.svc.Create(ctx, p.root, authz.PolicyInput{
		Name: "web-readers", Project: &web, Resources: authz.Resources{Repositories: []string{"*"}},
		Entries: []authz.Entry{{Principal: "group:developers", Actions: []string{"read", "deploy"}}},
	}); err != nil {
		t.Fatal(err)
	}
	id := p.text("SELECT id::text FROM authz_policy WHERE name = 'web-readers'")

	t.Run("the entries are in the project table only", func(t *testing.T) {
		if n := p.number("SELECT count(*) FROM authz_policy_entry WHERE policy_id = $1::uuid", id); n != 0 {
			t.Fatalf("%d rows in authz_policy_entry", n)
		}
		if n := p.number("SELECT count(*) FROM authz_project_policy_entry WHERE policy_id = $1::uuid", id); n != 1 {
			t.Fatalf("%d rows in authz_project_policy_entry", n)
		}
	})
	t.Run("the trigger AI004 refuses a row of authz_policy_entry for a project policy", func(t *testing.T) {
		p.mu.Lock()
		_, err := p.admin.Exec(ctx, "INSERT INTO authz_policy_entry (policy_id, principal_id, actions) VALUES ($1::uuid, $2::uuid, ARRAY['read'])", id, p.devs.ID)
		p.mu.Unlock()
		var pe *pgconn.PgError
		if !errors.As(err, &pe) || pe.Code != "AI004" {
			t.Fatalf("err = %v, want SQLSTATE AI004", err)
		}
	})
	t.Run("an index built from authz_policy_entry only grants nothing for the project policy", func(t *testing.T) {
		src := authz.NewPostgresSource(p.a.db, 10*time.Second)
		snap, err := src.Snapshot(ctx)
		if err != nil {
			t.Fatal(err)
		}
		devs := sub(p.dave, p.devs)
		current := authz.BuildIndex(snap, nil, quiet)
		if !p.decides(current, devs, "web-npm") {
			t.Fatal("the current build does not grant the project policy")
		}
		// What a 0.9.x replica reads: the rows of authz_policy_entry, none of which
		// belongs to the project policy, and no project.
		var old []q.AuthzBuildEntriesRow
		for _, e := range snap.Entries {
			if e.PolicyID != id {
				old = append(old, e)
			}
		}
		snap.Entries, snap.Projects, snap.ProjectAdmins = old, nil, nil
		if p.decides(authz.BuildIndex(snap, nil, quiet), devs, "web-npm") {
			t.Fatal("a build that reads authz_policy_entry only grants the project policy")
		}
	})
}

func (p *projectEnv) decides(idx *authz.Index, s authz.Subject, repo string) bool {
	return p.a.engine.DecideOn(context.Background(), idx, s, read(repo, "x", "")).Outcome == authz.Allow
}

// A global policy cannot name a project service account, and a role cannot be bound
// to one; the owner of a package in a repository outside its project is refused
// (Q-AUTHZ-175).
func TestR_AUTHZ_026_ProjectServiceAccountIsGrantedByItsProjectOnly(t *testing.T) {
	p := newProjectEnv(t)
	ctx := context.Background()
	// The project of a service account is fixed when the row is written (AI003).
	acct := person{ID: p.principalRow("service_account", "web-bot"), Name: "web-bot", Kind: principal.KindServiceAccount}
	p.exec("INSERT INTO service_account (principal_id, project_id) VALUES ($1::uuid, $2::uuid)", acct.ID, p.webID)
	web, shop := "web", "shop"
	t.Run("a global policy naming it is refused", func(t *testing.T) {
		_, _, err := p.a.svc.Create(ctx, p.root, authz.PolicyInput{
			Name: "g", Resources: authz.Resources{Repositories: []string{"npm-internal"}},
			Entries: []authz.Entry{{Principal: "sa:web-bot", Actions: []string{"read"}}},
		})
		problem(t, err, 422, errs.CodePrincipalOutsideProject)
	})
	t.Run("another project's policy naming it is refused", func(t *testing.T) {
		_, _, err := p.a.svc.Create(ctx, p.root, authz.PolicyInput{
			Name: "shop-g", Project: &shop, Resources: authz.Resources{},
			Entries: []authz.Entry{{Principal: "sa:web-bot", Actions: []string{"read"}}},
		})
		problem(t, err, 422, errs.CodePrincipalOutsideProject)
	})
	t.Run("its own project's policy may name it", func(t *testing.T) {
		pol, _, err := p.a.svc.Create(ctx, p.root, authz.PolicyInput{
			Name: "web-bot-deploy", Project: &web, Resources: authz.Resources{Repositories: []string{"web-npm"}},
			Entries: []authz.Entry{{Principal: "sa:web-bot", Actions: []string{"deploy"}}},
		})
		if err != nil || pol.Project == nil || *pol.Project != "web" {
			t.Fatalf("%+v %v", pol, err)
		}
	})
	t.Run("a system role is not bound to it", func(t *testing.T) {
		_, _, err := p.a.svc.BindRole(ctx, p.root, authz.RoleAuditor, "sa:web-bot")
		problem(t, err, 422, errs.CodeRoleNotAllowed)
	})
}

// The list of a policy carries the warning of Q-AUTHZ-179 when a key selector's
// repository left the project, and the policy stays.
func TestR_AUTHZ_026_PolicyOfARepositoryThatLeftTheProjectWarns(t *testing.T) {
	p := newProjectEnv(t)
	ctx := context.Background()
	web := "web"
	if _, _, err := p.a.svc.Create(ctx, p.root, authz.PolicyInput{
		Name: "web-files-read", Project: &web, Resources: authz.Resources{Repositories: []string{"web-files"}},
		Entries: []authz.Entry{{Principal: "group:developers", Actions: []string{"read"}}},
	}); err != nil {
		t.Fatal(err)
	}
	devs := sub(p.dave, p.devs)
	waitFor(t, "replica b granting", 3*time.Second, func() bool {
		return p.b.engine.Decide(ctx, devs, read("web-files", "x", "")).Outcome == authz.Allow
	})
	p.exec("UPDATE repositories SET project_id = NULL WHERE key = 'web-files'")
	if err := p.a.db.InTx(ctx, func(tx meta.Tx) error {
		return p.a.changes.Notify(tx, authz.AreaRepoConfig, 1, map[string]string{"type": "repository", "id": "web-files"})
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "replica b refusing the grant", 3*time.Second, func() bool {
		return p.b.engine.Decide(ctx, devs, read("web-files", "x", "")).Outcome != authz.Allow
	})
	got, err := p.a.svc.Get(ctx, "web-files-read")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Warnings) != 1 || got.Warnings[0] != authz.WarnSelectorOutsideProject {
		t.Fatalf("warnings %v", got.Warnings)
	}
	if got.Project == nil || *got.Project != "web" || len(got.Resources.Repositories) != 1 {
		t.Fatalf("the policy changed: %+v", got)
	}
	// Back in the project, the grant returns.
	p.exec("UPDATE repositories SET project_id = $1::uuid WHERE key = 'web-files'", p.webID)
	if err := p.a.db.InTx(ctx, func(tx meta.Tx) error {
		return p.a.changes.Notify(tx, authz.AreaRepoConfig, 1, map[string]string{"type": "repository", "id": "web-files"})
	}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "replica b granting again", 3*time.Second, func() bool {
		return p.b.engine.Decide(ctx, devs, read("web-files", "x", "")).Outcome == authz.Allow
	})
}
