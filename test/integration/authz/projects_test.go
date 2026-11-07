//go:build integration

package authz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	"github.com/guided-traffic/trove/internal/project"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
	"github.com/guided-traffic/trove/test/integration/stack"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// Projects in the engine and over HTTP (R-AUTHZ-026, R-AUTHZ-028, Q-AUTHZ-207 to Q-AUTHZ-209, Q-AUTHZ-215 to
// Q-AUTHZ-217, R-REPO-060): the roles of members decide on every replica, project policies are named within their
// project and name its repositories by bare key, and a move takes a repository out of its project's roles.

// projectFixture is the setting of the acceptance criteria of R-AUTHZ-026 over HTTP: project web with the Owner
// group web-leads (alice is a member) and the Developer group web-devs (dave), repositories web/npm and web/extra;
// project mobile with its Owner bob and mobile/npm-internal; it/npm-internal in the project the stack seeds.
type projectFixture struct {
	*apiFixture
	leads, devs stack.Person
	dave        stack.Person
	webID       string
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
	pf := &projectFixture{apiFixture: f, leads: e.Group("web-leads"), devs: e.Group("web-devs"), dave: e.User("dave")}
	e.Join(pf.leads, f.alice)
	e.Join(pf.devs, pf.dave)
	e.RepoIn("web", "npm", "local", "npm")
	e.RepoIn("web", "extra", "local", "npm")
	e.RepoIn("mobile", "npm-internal", "local", "npm")
	e.Repo("npm-internal", "local", "npm")
	pf.webID = e.Project("web")
	e.Member("web", pf.leads, "owner")
	e.Member("web", pf.devs, "developer")
	e.Member("mobile", f.bob, "owner")
	o := stack.Options{Validator: f.val}
	f.a, f.b, f.c = e.NewReplica("web-a", o), e.NewReplica("web-b", o), e.NewReplica("web-c", o)
	f.cl = stack.NewCluster(f.a, f.b, f.c)
	return pf
}

// projectPolicyBody is a policy body of the project routes: the name within the project, the selectors bare keys of
// the project (R-AUTHZ-004).
func projectPolicyBody(name string, repositories []string, entries ...map[string]any) map[string]any {
	b := policyBody(name, entries...)
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

// TestR_AUTHZ_026_AnOwnerWritesThePoliciesOfItsProject: the acceptance criteria of R-AUTHZ-026 for project policies,
// over HTTP on three replicas.
func TestR_AUTHZ_026_AnOwnerWritesThePoliciesOfItsProject(t *testing.T) {
	f := newProjectAPI(t)
	alice, admin := f.alice.Name, f.admin.Name
	const web = "/api/v1/projects/web/policies"

	t.Run("R-AUTHZ-026 an Owner creates npm-read on the selector npm", func(t *testing.T) {
		r := f.cl.Do(alice, "POST", web, projectPolicyBody("npm-read", []string{"npm"}, entry("group:web-devs", "read")))
		wantStatus(t, r, 201, "")
		if body := r.JSON(t); body["name"] != "npm-read" || body["project"] != "web" || r.Epoch() == 0 {
			t.Fatalf("%v, epoch %d", body, r.Epoch())
		}
		if n := f.e.Number("SELECT count(*) FROM authz_policy WHERE name = 'web/npm-read'"); n != 1 {
			t.Fatalf("the stored name is not <project>/<name>: %d rows", n)
		}
		if n := f.e.Number("SELECT count(*) FROM authz_project_policy_entry e JOIN authz_policy p ON p.id = e.policy_id WHERE p.name = 'web/npm-read'"); n != 1 {
			t.Fatalf("%d rows in authz_project_policy_entry", n)
		}
		if n := f.e.Number("SELECT count(*) FROM audit_event WHERE action = 'authz.policy.created' AND project = 'web'"); n != 1 {
			t.Fatalf("%d audit events naming the project", n)
		}
	})
	t.Run("R-AUTHZ-004 a key of no repository of the project is unknown_repository, a selector with a slash invalid_pattern", func(t *testing.T) {
		wantStatus(t, f.cl.Do(alice, "POST", web, projectPolicyBody("x", []string{"npm-internal"})), 422, "unknown_repository")
		wantStatus(t, f.cl.Do(alice, "POST", web, projectPolicyBody("x", []string{"mobile/npm-internal"})), 422, "invalid_pattern")
	})
	t.Run("Q-AUTHZ-209 npm-read of mobile and a global npm-read exist beside it", func(t *testing.T) {
		wantStatus(t, f.cl.Do(admin, "POST", "/api/v1/projects/mobile/policies", projectPolicyBody("npm-read", []string{"npm-internal"}, entry("user:bob", "read"))), 201, "")
		global := policyBody("npm-read", entry("user:carol", "read"))
		global["resources"] = map[string]any{"repositories": []string{"it/npm-internal"}}
		wantStatus(t, f.cl.Do(admin, "POST", policies, global), 201, "")
		if n := f.e.Number("SELECT count(*) FROM authz_policy WHERE name IN ('npm-read', 'web/npm-read', 'mobile/npm-read')"); n != 3 {
			t.Fatalf("%d policies", n)
		}
	})
	t.Run("Q-AUTHZ-216 a bare key in a global policy is invalid_pattern", func(t *testing.T) {
		bare := policyBody("bare")
		bare["resources"] = map[string]any{"repositories": []string{"npm-internal"}}
		wantStatus(t, f.cl.Do(admin, "POST", policies, bare), 422, "invalid_pattern")
	})
	t.Run("R-AUTHZ-026 a Developer is refused with 403, a caller that is no member with 404", func(t *testing.T) {
		wantStatus(t, f.cl.Do(f.dave.Name, "POST", web, projectPolicyBody("y", []string{"npm"})), 403, "forbidden")
		wantStatus(t, f.cl.Do(f.carol.Name, "POST", web, projectPolicyBody("y", []string{"npm"})), 404, "")
	})
	t.Run("R-AUTHZ-021 the Owner reads, replaces, patches and deletes the policy of its project, each with the epoch", func(t *testing.T) {
		list := f.cl.Do(alice, "GET", web, nil)
		wantStatus(t, list, 200, "")
		if got := listNames(t, list); !slices.Equal(got, []string{"npm-read"}) {
			t.Fatalf("list %v", got)
		}
		g := f.cl.Do(alice, "GET", web+"/npm-read", nil)
		wantStatus(t, g, 200, "")
		r := f.cl.Do(alice, "PUT", web+"/npm-read", projectPolicyBody("npm-read", []string{"npm", "extra"}, entry("group:web-devs", "read")), "If-Match", g.Header.Get("ETag"))
		wantStatus(t, r, 200, "")
		p := f.cl.Do(alice, "PATCH", web+"/npm-read", map[string]any{"description": "web"}, "If-Match", r.Header.Get("ETag"), "Content-Type", "application/merge-patch+json")
		wantStatus(t, p, 200, "")
		d := f.cl.Do(alice, "DELETE", web+"/npm-read", nil)
		wantStatus(t, d, 204, "")
		if d.Epoch() == 0 {
			t.Fatal("no epoch")
		}
		if n := f.e.Number("SELECT count(*) FROM authz_policy WHERE name IN ('npm-read', 'mobile/npm-read')"); n != 2 {
			t.Fatalf("the delete reached another policy of the name: %d left", n)
		}
	})
}

// TestR_AUTHZ_022_AuthzMeListsTheMemberships: GET /api/v1/authz/me lists the caller's memberships with their roles,
// a group's role through the group (R-AUTHZ-022, Q-AUTHZ-207); an administrator of trove is no member.
func TestR_AUTHZ_022_AuthzMeListsTheMemberships(t *testing.T) {
	f := newProjectAPI(t)
	for name, want := range map[string]string{
		f.alice.Name: `[{"key":"web","role":"owner"}]`,
		f.dave.Name:  `[{"key":"web","role":"developer"}]`,
		f.bob.Name:   `[{"key":"mobile","role":"owner"}]`,
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
	if caps := f.cl.Do(f.alice.Name, "GET", "/api/v1/authz/me", nil).JSON(t)["capabilities"].([]any); len(caps) != 0 {
		t.Fatalf("a role is no capability: %v", caps)
	}
}

// A system role of a project service account is refused by the declaration of authz.role_bindings, like by the API
// (Q-AUTHZ-175).
func TestR_AUTHZ_026_ADeclaredRoleOfAProjectServiceAccountIsRefused(t *testing.T) {
	f := newProjectAPI(t)
	id := f.e.Text("INSERT INTO principal (id, kind, name) VALUES (gen_random_uuid(), 'service_account', 'web/bot') RETURNING id::text")
	f.e.Exec("INSERT INTO service_account (principal_id, project_id) VALUES ($1::uuid, $2::uuid)", id, f.webID)
	err := testenv.DeclareRoleBindings(f.e.Cfg, declared("auditor", "service_account", "web/bot"))
	if err == nil || !strings.Contains(err.Error(), "role_not_allowed") {
		t.Fatalf("the declaration was applied or refused for another reason: %v", err)
	}
	if n := f.e.Number("SELECT count(*) FROM authz_role_binding b JOIN principal p ON p.id = b.principal_id WHERE p.name = 'web/bot'"); n != 0 {
		t.Fatalf("%d bindings of the project account", n)
	}
}

// projectEnv is the engine-level setting: two replicas on one database, project web (web-npm, web-files, npm-web)
// whose Owner is the group web-leads (dave is in it) and whose Developer is the user alice; project shop (shop-npm)
// with its Owner bob; it/npm-internal.
type projectEnv struct {
	*env
	a, b          *replica
	webID, shopID string
	alice, bob    person
	leads, devs   person
	bot, dave     person
	carol, erin   person
	root          authz.Subject
	projects      project.Service
}

func newProjectEnv(t *testing.T) *projectEnv {
	t.Helper()
	e := newEnv(t)
	p := &projectEnv{env: e}
	p.root = sub(e.user("sec-alice"))
	p.alice, p.bob, p.dave = e.user("alice"), e.user("bob"), e.user("dave")
	p.carol, p.erin = e.user("carol"), e.user("erin")
	p.leads, p.devs = e.group("web-leads", false), e.group("developers", false)
	p.bot = e.serviceAccount("ci-bot")
	e.repo("web/web-npm", "local", "npm", "")
	e.repo("web/web-files", "local", "generic", "")
	e.repo("web/npm-web", "local", "npm", "")
	e.repo("shop/shop-npm", "local", "npm", "")
	e.repo("npm-internal", "local", "npm", "")
	p.webID, p.shopID = e.project("web"), e.project("shop")
	e.member("web", p.leads, "owner")
	e.member("web", p.alice, "developer")
	e.member("shop", p.bob, "owner")
	const poll = 500 * time.Millisecond
	p.a, p.b = e.newReplica("a", replicaOpts{poll: poll}), e.newReplica("b", replicaOpts{poll: poll})
	p.projects = project.New(project.Options{DB: p.a.db, Changes: p.a.changes, Recorder: p.a.rec, Policies: p.a.svc})
	return p
}

// changed notifies the authz area as the project service does in the transaction of a project write, for the rows
// a test writes with SQL.
func (p *projectEnv) changed() {
	p.t.Helper()
	err := p.a.db.InTx(context.Background(), func(tx meta.Tx) error {
		return p.a.changes.Notify(tx, authz.AreaAuthz, 1, map[string]string{"type": "project", "id": "test"})
	})
	if err != nil {
		p.t.Fatal(err)
	}
}

// may decides one action on a repository of the setting ("<project>/<key>" or a key of it) on a replica.
func (p *projectEnv) may(r *replica, s authz.Subject, repo string, a authz.Action) bool {
	return r.engine.Decide(context.Background(), s, write(repo, "x", "", a)).Outcome == authz.Allow
}

// TestR_AUTHZ_026_RolesDecideOnEveryReplica: a member's role grants its actions on every repository of the project
// and nothing outside it, through the user's own membership and through its groups, on every replica (R-AUTHZ-015,
// R-AUTHZ-028); a token's scope bounds it and only a session or an admin token administers (Q-AUTHZ-215); a service
// account is no member, also in a member group.
func TestR_AUTHZ_026_RolesDecideOnEveryReplica(t *testing.T) {
	p := newProjectEnv(t)
	owner := sub(p.dave, p.leads)
	dev := sub(p.alice)
	for _, r := range []*replica{p.a, p.b} {
		for _, repo := range []string{"web/web-npm", "web/web-files", "web/npm-web"} {
			for _, a := range []authz.Action{authz.Read, authz.Annotate, authz.Deploy, authz.Overwrite, authz.Delete, authz.Manage} {
				if !p.may(r, owner, repo, a) {
					t.Errorf("replica %s: the Owner group's member lacks %s on %s", r.name, a, repo)
				}
				if got, want := p.may(r, dev, repo, a), authz.ProjectRoleDeveloper.Actions().Has(a); got != want {
					t.Errorf("replica %s: the Developer %s on %s = %v", r.name, a, repo, got)
				}
			}
		}
		for _, repo := range []string{"shop/shop-npm", "npm-internal"} {
			if p.may(r, owner, repo, authz.Read) || p.may(r, dev, repo, authz.Read) {
				t.Errorf("replica %s: a role of web reads %s", r.name, repo)
			}
		}
		if p.may(r, sub(p.bob), "web/web-npm", authz.Read) {
			t.Errorf("replica %s: bob reads web", r.name)
		}
	}
	t.Run("Q-AUTHZ-215 the credentials of a member", func(t *testing.T) {
		idx, err := p.a.engine.Begin(context.Background(), owner)
		if err != nil {
			t.Fatal(err)
		}
		readOnly := owner
		readOnly.Scope = &scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"web/web-npm"}, Actions: []string{"read"}}}}
		if !p.may(p.a, readOnly, "web/web-npm", authz.Read) || p.may(p.a, readOnly, "web/web-npm", authz.Deploy) {
			t.Error("the token's scope does not bound the role")
		}
		if m := p.a.engine.MembershipsOn(idx, readOnly); len(m) != 1 || m[0].Key != "web" {
			t.Errorf("a token scoped to a repository of web is a member of %v", m)
		}
		elsewhere := owner
		elsewhere.Scope = &scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"shop/*"}, Actions: []string{"read"}}}}
		if m := p.a.engine.MembershipsOn(idx, elsewhere); len(m) != 0 || p.a.engine.RoleOn(idx, elsewhere, "web") != authz.ProjectRoleNone {
			t.Errorf("a token whose scope names no repository of web reaches the role there: %v", m)
		}
		inherit := owner
		inherit.Scope = &scope.Scope{Inherit: true}
		admin := owner
		admin.Scope = &scope.Scope{Inherit: true, Admin: true}
		if !p.may(p.a, inherit, "web/web-npm", authz.Delete) || p.a.engine.OwnsOn(idx, inherit, "web") || !p.a.engine.OwnsOn(idx, admin, "web") || !p.a.engine.OwnsOn(idx, owner, "web") {
			t.Error("the administration of an Owner follows the credential")
		}
		bot := sub(p.bot, p.leads)
		bot.Scope = &scope.Scope{Inherit: true, Admin: true}
		if p.may(p.a, bot, "web/web-npm", authz.Read) || len(p.a.engine.MembershipsOn(idx, bot)) != 0 {
			t.Error("a service account in the Owner group holds the role")
		}
	})
}

// TestR_AUTHZ_028_AMemberChangeReachesAnotherReplica: the project service adds, demotes and removes a member and the
// replica that did not write follows within the bound of R-AUTHZ-028; a removed Owner group takes the role from its
// members (R-AUTHZ-026, Q-AUTHZ-207).
func TestR_AUTHZ_028_AMemberChangeReachesAnotherReplica(t *testing.T) {
	p := newProjectEnv(t)
	ctx := context.Background()
	const poll = 500 * time.Millisecond
	bound := poll + time.Second
	carolSub := sub(p.carol)
	for trial := range 3 {
		if _, created, _, err := p.projects.AddMember(ctx, p.root, "web", "user:carol", project.RoleMaintainer, true); err != nil || !created {
			t.Fatalf("trial %d: add %v %v", trial, created, err)
		}
		waitFor(t, fmt.Sprintf("trial %d: replica b granting delete", trial), bound, func() bool { return p.may(p.b, carolSub, "web/web-npm", authz.Delete) })
		if _, _, err := p.projects.SetRole(ctx, p.root, "web", "user:carol", project.RoleReader, true); err != nil {
			t.Fatal(err)
		}
		waitFor(t, fmt.Sprintf("trial %d: replica b refusing delete after the demotion", trial), bound, func() bool {
			return !p.may(p.b, carolSub, "web/web-npm", authz.Delete) && p.may(p.b, carolSub, "web/web-npm", authz.Read)
		})
		if _, err := p.projects.RemoveMember(ctx, p.root, "web", "user:carol", true); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		waitFor(t, fmt.Sprintf("trial %d: replica b refusing the read", trial), bound, func() bool { return !p.may(p.b, carolSub, "web/web-npm", authz.Read) })
		if took := time.Since(start); took > bound {
			t.Errorf("trial %d: refused after %s, bound %s", trial, took, bound)
		}
	}
	t.Run("R-AUTHZ-031 the narrowing writes carry their journal entries", func(t *testing.T) {
		for _, name := range []string{"authz.project.member_changed", "authz.project.member_removed"} {
			evs := p.a.rec.ofType(name)
			if len(evs) != 3 {
				t.Errorf("%s: %d events, want 3", name, len(evs))
			}
			for _, ev := range evs {
				if len(ev.Journal) == 0 || ev.Project != "web" {
					t.Errorf("%s: journal %s, project %q", name, ev.Journal, ev.Project)
				}
			}
		}
		if evs := p.a.rec.ofType("authz.project.member_added"); len(evs) != 3 || evs[0].Journal != nil {
			t.Errorf("member_added: %d events", len(evs))
		}
	})
	t.Run("removing the Owner group takes the role from its members", func(t *testing.T) {
		member := sub(p.dave, p.leads)
		if !p.may(p.b, member, "web/web-files", authz.Manage) {
			t.Fatal("the group member holds no role first")
		}
		if _, err := p.projects.RemoveMember(ctx, p.root, "web", "group:web-leads", true); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "replica b refusing the group member", bound, func() bool { return !p.may(p.b, member, "web/web-files", authz.Read) })
	})
}

// TestR_REPO_060_AMoveTakesTheRepositoryOutOfItsProjectsRoles: a move refused while a policy of the source project
// names the repository, then done; the source project's roles stop reaching it on the other replica, the target's
// reach it, and a global key selector follows it (R-AUTHZ-004).
func TestR_REPO_060_AMoveTakesTheRepositoryOutOfItsProjectsRoles(t *testing.T) {
	p := newProjectEnv(t)
	ctx := context.Background()
	web := "web"
	if _, _, err := p.a.svc.Create(ctx, p.root, authz.PolicyInput{
		Name: "web/files-read", Project: &web, Resources: authz.Resources{Repositories: []string{"web-files"}},
		Entries: []authz.Entry{{Principal: "user:carol", Actions: []string{"read"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.a.svc.Create(ctx, p.root, authz.PolicyInput{
		Name: "files-global", Resources: authz.Resources{Repositories: []string{"web/web-files"}},
		Entries: []authz.Entry{{Principal: "user:carol", Actions: []string{"annotate"}}},
	}); err != nil {
		t.Fatal(err)
	}
	from := qname.Repo{Project: "web", Key: "web-files"}
	_, _, err := p.projects.MoveRepository(ctx, p.root, from, "shop", false)
	problem(t, err, 409, errs.CodeRepositoryInUse)
	if e, _ := errs.As(err); fmt.Sprint(e.Extensions["references"]) != fmt.Sprint([]project.Reference{{Kind: project.RefPolicy, Name: "files-read"}}) {
		t.Fatalf("references %v", e.Extensions["references"])
	}
	if _, err := p.a.svc.Delete(ctx, p.root, "web/files-read", errs.AnyRevision); err != nil {
		t.Fatal(err)
	}
	owner := sub(p.dave, p.leads)
	waitFor(t, "replica b granting the web Owner", 3*time.Second, func() bool { return p.may(p.b, owner, "web/web-files", authz.Read) })
	to, epoch, err := p.projects.MoveRepository(ctx, p.root, from, "shop", false)
	if err != nil || to != (qname.Repo{Project: "shop", Key: "web-files"}) || epoch == 0 {
		t.Fatalf("%v %d %v", to, epoch, err)
	}
	waitFor(t, "replica b following the move", 3*time.Second, func() bool {
		return !p.may(p.b, owner, "web/web-files", authz.Read) && !p.may(p.b, owner, "shop/web-files", authz.Read) &&
			p.may(p.b, sub(p.bob), "shop/web-files", authz.Delete)
	})
	if !p.may(p.b, sub(p.carol), "shop/web-files", authz.Annotate) {
		t.Error("the global key selector does not follow the repository")
	}
	got, err := p.a.svc.Get(ctx, "files-global")
	if err != nil || !slices.Equal(got.Resources.Repositories, []string{"shop/web-files"}) {
		t.Fatalf("the global policy reads back %v (%v)", got.Resources.Repositories, err)
	}
}

// Q-AUTHZ-174: the grants of a project policy are stored in their own table, so a build that reads the table of
// global grants only finds none of them, and the trigger AI004 refuses a row of a project policy there.
func TestR_AUTHZ_026_ProjectPolicyGrantsHaveTheirOwnTable(t *testing.T) {
	p := newProjectEnv(t)
	ctx := context.Background()
	web := "web"
	if _, _, err := p.a.svc.Create(ctx, p.root, authz.PolicyInput{
		Name: "web/readers", Project: &web, Resources: authz.Resources{Repositories: []string{"*"}},
		Entries: []authz.Entry{{Principal: "group:developers", Actions: []string{"read", "deploy"}}},
	}); err != nil {
		t.Fatal(err)
	}
	id := p.text("SELECT id::text FROM authz_policy WHERE name = 'web/readers'")
	if n := p.number("SELECT count(*) FROM authz_policy_entry WHERE policy_id = $1::uuid", id); n != 0 {
		t.Fatalf("%d rows in authz_policy_entry", n)
	}
	if n := p.number("SELECT count(*) FROM authz_project_policy_entry WHERE policy_id = $1::uuid", id); n != 1 {
		t.Fatalf("%d rows in authz_project_policy_entry", n)
	}
	p.mu.Lock()
	_, err := p.admin.Exec(ctx, "INSERT INTO authz_policy_entry (policy_id, principal_id, actions) VALUES ($1::uuid, $2::uuid, ARRAY['read'])", id, p.devs.ID)
	p.mu.Unlock()
	var pe *pgconn.PgError
	if !errors.As(err, &pe) || pe.Code != "AI004" {
		t.Fatalf("err = %v, want SQLSTATE AI004", err)
	}
	src := authz.NewPostgresSource(p.a.db, 10*time.Second)
	snap, err := src.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	devs := sub(p.erin, p.devs)
	if !p.decides(authz.BuildIndex(snap, nil, quiet), devs, "web/web-npm") {
		t.Fatal("the build does not grant the project policy")
	}
	var global []q.AuthzBuildEntriesRow
	for _, e := range snap.Entries {
		if e.PolicyID != id {
			global = append(global, e)
		}
	}
	snap.Entries = global
	if p.decides(authz.BuildIndex(snap, nil, quiet), devs, "web/web-npm") {
		t.Fatal("the table of global grants holds the project policy")
	}
}

func (p *projectEnv) decides(idx *authz.Index, s authz.Subject, repo string) bool {
	return p.a.engine.DecideOn(context.Background(), idx, s, read(repo, "x", "")).Outcome == authz.Allow
}

// A global policy cannot name a project service account, nor another project's policy, and a role cannot be bound to
// one; its own project's policy names it as sa:<project>/<name> (Q-AUTHZ-175, Q-AUTHZ-209).
func TestR_AUTHZ_026_ProjectServiceAccountIsGrantedByItsProjectOnly(t *testing.T) {
	p := newProjectEnv(t)
	ctx := context.Background()
	acct := person{ID: p.principalRow("service_account", "web/bot"), Name: "web/bot", Kind: principal.KindServiceAccount}
	p.exec("INSERT INTO service_account (principal_id, project_id) VALUES ($1::uuid, $2::uuid)", acct.ID, p.webID)
	web, shop := "web", "shop"
	t.Run("a global policy naming it is refused", func(t *testing.T) {
		_, _, err := p.a.svc.Create(ctx, p.root, authz.PolicyInput{
			Name: "g", Resources: authz.Resources{Repositories: []string{"it/npm-internal"}},
			Entries: []authz.Entry{{Principal: "sa:web/bot", Actions: []string{"read"}}},
		})
		problem(t, err, 422, errs.CodePrincipalOutsideProject)
	})
	t.Run("another project's policy naming it is refused", func(t *testing.T) {
		_, _, err := p.a.svc.Create(ctx, p.root, authz.PolicyInput{
			Name: "shop/g", Project: &shop, Entries: []authz.Entry{{Principal: "sa:web/bot", Actions: []string{"read"}}},
		})
		problem(t, err, 422, errs.CodePrincipalOutsideProject)
	})
	t.Run("its own project's policy may name it, and its token reads that repository and nothing else", func(t *testing.T) {
		pol, _, err := p.a.svc.Create(ctx, p.root, authz.PolicyInput{
			Name: "web/bot-read", Project: &web, Resources: authz.Resources{Repositories: []string{"web-npm"}},
			Entries: []authz.Entry{{Principal: "sa:web/bot", Actions: []string{"read"}}},
		})
		if err != nil || pol.Project == nil || *pol.Project != "web" || pol.Name != "web/bot-read" {
			t.Fatalf("%+v %v", pol, err)
		}
		bot := sub(acct)
		waitFor(t, "the policy is built", 3*time.Second, func() bool { return p.may(p.a, bot, "web/web-npm", authz.Read) })
		if p.may(p.a, bot, "web/web-files", authz.Read) || p.may(p.a, bot, "npm-internal", authz.Read) {
			t.Error("the account reads beyond its policy")
		}
	})
	t.Run("a system role is not bound to it", func(t *testing.T) {
		_, _, err := p.a.svc.BindRole(ctx, p.root, authz.RoleAuditor, "sa:web/bot")
		problem(t, err, 422, errs.CodeRoleNotAllowed)
	})
}

// An Owner's policy write is judged under the policy lock, on the project it writes for (Q-AUTHZ-199).
func TestR_AUTHZ_026_AnOwnersWriteChecksTheProjectUnderTheLock(t *testing.T) {
	p := newProjectEnv(t)
	ctx := context.Background()
	shop := "shop"
	if _, _, err := p.a.svc.Create(ctx, p.root, authz.PolicyInput{
		Name: "ops-readers", Resources: authz.Resources{Repositories: []string{"it/npm-internal"}},
		Entries: []authz.Entry{{Principal: "user:bob", Actions: []string{"read"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.a.svc.Create(ctx, p.root, authz.PolicyInput{Name: "shop/readers", Project: &shop}); err != nil {
		t.Fatal(err)
	}
	owner := authz.AsProjectOwner("web")
	webKey := "web"
	t.Run("a global policy is 403", func(t *testing.T) {
		_, _, err := p.a.svc.Update(ctx, sub(p.dave), "ops-readers", errs.AnyRevision, authz.PolicyInput{Description: "x", Resources: authz.Resources{Repositories: []string{"it/npm-internal"}}}, owner)
		problem(t, err, 403, errs.CodeForbidden)
		_, err = p.a.svc.Delete(ctx, sub(p.dave), "ops-readers", errs.AnyRevision, owner)
		problem(t, err, 403, errs.CodeForbidden)
	})
	t.Run("another project's policy is 404", func(t *testing.T) {
		_, _, err := p.a.svc.Update(ctx, sub(p.dave), "shop/readers", errs.AnyRevision, authz.PolicyInput{Project: &shop}, owner)
		problem(t, err, 404, errs.CodeNotFound)
		_, err = p.a.svc.Delete(ctx, sub(p.dave), "shop/readers", errs.AnyRevision, owner)
		problem(t, err, 404, errs.CodeNotFound)
	})
	t.Run("ExpectProject refuses a policy of another project or a global one", func(t *testing.T) {
		_, err := p.a.svc.Delete(ctx, p.root, "ops-readers", errs.AnyRevision, authz.ExpectProject(&webKey))
		problem(t, err, 404, errs.CodeNotFound)
		_, err = p.a.svc.Delete(ctx, p.root, "shop/readers", errs.AnyRevision, authz.ExpectProject(nil))
		problem(t, err, 404, errs.CodeNotFound)
		if n := p.number("SELECT count(*) FROM authz_policy WHERE name IN ('ops-readers', 'shop/readers')"); n != 2 {
			t.Fatalf("%d policies left", n)
		}
	})
	t.Run("the Owner's own project's policy is written and deleted", func(t *testing.T) {
		if _, _, err := p.a.svc.Create(ctx, sub(p.dave), authz.PolicyInput{Name: "web/readers", Project: &webKey, Resources: authz.Resources{Repositories: []string{"web-npm"}}}, owner); err != nil {
			t.Fatal(err)
		}
		if _, _, err := p.a.svc.Update(ctx, sub(p.dave), "web/readers", errs.AnyRevision, authz.PolicyInput{Project: &webKey, Description: "d", Resources: authz.Resources{Repositories: []string{"web-npm"}}}, owner); err != nil {
			t.Fatal(err)
		}
		if _, err := p.a.svc.Delete(ctx, sub(p.dave), "web/readers", errs.AnyRevision, owner); err != nil {
			t.Fatal(err)
		}
	})
}

// ghostProjects finds a project that a concurrent delete has just removed: the lookup answers an ID the table no
// longer holds.
type ghostProjects struct{ authz.ProjectLookup }

func (ghostProjects) ProjectID(context.Context, string) (string, error) {
	return "00000000-0000-4000-8000-00000000dead", nil
}

// A create that races a project delete is the field error unknown_project, not a foreign key violation (Q-AUTHZ-199).
func TestR_AUTHZ_026_ACreateRacingAProjectDeleteIsUnknownProject(t *testing.T) {
	p := newProjectEnv(t)
	ghost := "gone"
	svc := authz.NewService(authz.ServiceOptions{
		DB: p.a.db, Changes: p.a.changes, Principals: sqlPrincipals{p.env}, Repositories: sqlRepositories{p.env},
		Formats: integrationFormats(), Projects: ghostProjects{},
	})
	_, _, err := svc.Create(context.Background(), p.root, authz.PolicyInput{Name: "gone/readers", Project: &ghost})
	problem(t, err, 422, errs.CodeUnknownProject)
}

// An auto-join group is refused in a project policy, and a group that becomes one afterwards grants nothing through a
// project policy or a membership, against the tables (Q-AUTHZ-200, R-AUTHZ-026).
func TestR_AUTHZ_026_AnAutoJoinGroupGrantsNothingInAProject(t *testing.T) {
	p := newProjectEnv(t)
	ctx := context.Background()
	p.group("everyone", true)
	web := "web"
	_, _, err := p.a.svc.Create(ctx, p.root, authz.PolicyInput{
		Name: "web/everyone", Project: &web, Resources: authz.Resources{Repositories: []string{"web-npm"}},
		Entries: []authz.Entry{{Principal: "group:everyone", Actions: []string{"read"}}},
	})
	problem(t, err, 422, errs.CodePrincipalOutsideProject)
	_, _, _, err = p.projects.AddMember(ctx, p.root, "web", "group:everyone", project.RoleReader, true)
	problem(t, err, 422, errs.CodePrincipalNotAllowed)
	if _, _, err := p.a.svc.Create(ctx, p.root, authz.PolicyInput{
		Name: "web/devs", Project: &web, Resources: authz.Resources{Repositories: []string{"web-npm"}},
		Entries: []authz.Entry{{Principal: "group:developers", Actions: []string{"read"}}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := p.projects.AddMember(ctx, p.root, "shop", "group:developers", project.RoleReader, true); err != nil {
		t.Fatal(err)
	}
	member := sub(p.erin, p.devs)
	waitFor(t, "the policy and the membership are built", 3*time.Second, func() bool {
		return p.may(p.a, member, "web/web-npm", authz.Read) && p.may(p.a, member, "shop/shop-npm", authz.Read)
	})
	p.exec("UPDATE user_group SET auto_join = true WHERE principal_id = $1::uuid", p.devs.ID)
	p.changed()
	waitFor(t, "the new auto-join group is skipped", 3*time.Second, func() bool {
		return !p.may(p.a, member, "web/web-npm", authz.Read) && !p.may(p.a, member, "shop/shop-npm", authz.Read)
	})
}

// staleRepositories is the repository snapshot of a replica that has not caught up: the names it was given keep the
// repository they named when it was taken.
type staleRepositories struct {
	sqlRepositories
	frozen map[qname.Repo]repository.Repository
}

func (s staleRepositories) ByName(ctx context.Context, name qname.Repo) (repository.Repository, error) {
	if r, ok := s.frozen[name]; ok {
		return r, nil
	}
	return s.sqlRepositories.ByName(ctx, name)
}

// TestR_AUTHZ_004_AKeySelectorIsBoundInTheTransactionOfTheWrite: a policy write binds its key selectors to the
// repositories the database holds in its own transaction, under a share lock of the project and the repository rows,
// never through a snapshot of another moment. A replica whose snapshot still holds web/web-files after the repository
// moved to shop binds neither a project policy of web nor a global policy to it: the key names no repository (R-AUTHZ-004,
// R-REPO-060).
func TestR_AUTHZ_004_AKeySelectorIsBoundInTheTransactionOfTheWrite(t *testing.T) {
	p := newProjectEnv(t)
	ctx := context.Background()
	name := qname.Repo{Project: "web", Key: "web-files"}
	before, err := sqlRepositories{p.env}.ByName(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	svc := authz.NewService(authz.ServiceOptions{
		DB: p.a.db, Changes: p.a.changes, Principals: sqlPrincipals{p.env}, Formats: integrationFormats(),
		Repositories: staleRepositories{sqlRepositories{p.env}, map[qname.Repo]repository.Repository{name: before}},
	})
	if _, _, err := p.projects.MoveRepository(ctx, p.root, name, "shop", false); err != nil {
		t.Fatal(err)
	}
	web := "web"
	carol := []authz.Entry{{Principal: "user:carol", Actions: []string{"read"}}}
	_, _, err = svc.Create(ctx, p.root, authz.PolicyInput{Name: "web/files", Project: &web, Resources: authz.Resources{Repositories: []string{"web-files"}}, Entries: carol})
	problem(t, err, 422, errs.CodeUnknownRepository)
	_, _, err = svc.Create(ctx, p.root, authz.PolicyInput{Name: "files-global", Resources: authz.Resources{Repositories: []string{"web/web-files"}}, Entries: carol})
	problem(t, err, 422, errs.CodeUnknownRepository)
	// An update binds in its transaction too.
	if _, _, err := svc.Create(ctx, p.root, authz.PolicyInput{Name: "web/files", Project: &web, Resources: authz.Resources{Repositories: []string{"web-npm"}}, Entries: carol}); err != nil {
		t.Fatal(err)
	}
	_, _, err = svc.Update(ctx, p.root, "web/files", errs.AnyRevision, authz.PolicyInput{Project: &web, Resources: authz.Resources{Repositories: []string{"web-files"}}, Entries: carol})
	problem(t, err, 422, errs.CodeUnknownRepository)
	if n := p.number("SELECT count(*) FROM authz_policy_selector WHERE repository_id = $1::bigint", before.ID); n != 0 {
		t.Fatalf("%d selectors hold the moved repository", n)
	}
}

// TestR_REPO_060_TheDatabaseRefusesAMoveWhileItsProjectHoldsTheRepository: the guard of the move in the database (AI007)
// holds for every write, also one that raced the service's check: a repository whose project holds it by ID (a key
// selector of a project policy, a virtual repository's member list) does not change its project, and a project
// policy's key selector names a repository of the policy's project only. A global policy's key selector follows the
// repository (R-REPO-060, Q-REPO-217).
func TestR_REPO_060_TheDatabaseRefusesAMoveWhileItsProjectHoldsTheRepository(t *testing.T) {
	p := newProjectEnv(t)
	ctx := context.Background()
	web := "web"
	carol := []authz.Entry{{Principal: "user:carol", Actions: []string{"read"}}}
	if _, _, err := p.a.svc.Create(ctx, p.root, authz.PolicyInput{Name: "web/files", Project: &web, Resources: authz.Resources{Repositories: []string{"web-files"}}, Entries: carol}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.a.svc.Create(ctx, p.root, authz.PolicyInput{Name: "npm-global", Resources: authz.Resources{Repositories: []string{"web/web-npm"}}, Entries: carol}); err != nil {
		t.Fatal(err)
	}
	exec := func(sql string, args ...any) error {
		p.mu.Lock()
		defer p.mu.Unlock()
		_, err := p.admin.Exec(ctx, sql, args...)
		return err
	}
	state := func(err error) string {
		var pe *pgconn.PgError
		if errors.As(err, &pe) {
			return pe.Code
		}
		return fmt.Sprint(err)
	}
	move := `UPDATE repositories SET project_id = $1::uuid WHERE id = (SELECT r.id FROM repositories r JOIN project p ON p.id = r.project_id WHERE p.key = 'web' AND r.key = $2)`
	if got := state(exec(move, p.shopID, "web-files")); got != "AI007" {
		t.Errorf("a move of a repository a project policy names: %s, want AI007", got)
	}
	if err := exec(move, p.shopID, "web-npm"); err != nil {
		t.Errorf("a global policy's key selector does not hold a repository in its project: %v", err)
	}
	p.exec(`INSERT INTO repositories (key, class, format, config, project_id) VALUES ('web-all', 'virtual', 'npm', '{}'::jsonb, $1::uuid)`, p.webID)
	p.exec(`INSERT INTO virtual_members (virtual_id, position, member_id)
	          SELECT v.id, 0, m.id FROM repositories v, repositories m WHERE v.key = 'web-all' AND m.key = 'npm-web'`)
	for _, key := range []string{"npm-web", "web-all"} {
		if got := state(exec(move, p.shopID, key)); got != "AI007" {
			t.Errorf("a move of %s, which a member list holds: %s, want AI007", key, got)
		}
	}
	insert := `INSERT INTO authz_policy_selector (policy_id, ordinal, kind, repository_id)
	           SELECT a.id, 9, 'key', r.id FROM authz_policy a, repositories r JOIN project p ON p.id = r.project_id
	            WHERE a.name = 'web/files' AND p.key = 'shop' AND r.key = 'shop-npm'`
	if got := state(exec(insert)); got != "AI007" {
		t.Errorf("a project policy's key selector on a repository of another project: %s, want AI007", got)
	}
}
