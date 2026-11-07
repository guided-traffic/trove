package authz

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/qname"
	"github.com/guided-traffic/trove/internal/repository"
)

// The roles of project members in the decision path (R-AUTHZ-015, R-AUTHZ-026, Q-AUTHZ-207, Q-AUTHZ-208,
// Q-AUTHZ-215): a member's role grants its actions on every repository of the project, to the member's session
// and personal tokens within their scope, and never to a service account or a CI token.

const (
	idDave  = "user-dave"
	idErin  = "user-erin"
	idLeads = "group-web-leads"
	idWDevs = "group-web-devs"
)

// roleFixture: project web holds web-npm (local), web-files (local generic) and npm-web (virtual over web-npm,
// deploying into it); shop holds shop-npm; it holds npm-internal. No policy grants anything: every allow below
// comes from a role.
func roleFixture() *fixture {
	return newFixture().
		repo("web-npm", "local", "npm", "").
		repo("web-files", "local", "generic", "").
		repo("npm-web", "virtual", "npm", `{"virtual":{"members":["web-npm"],"deployTarget":"web-npm"}}`).
		repo("shop-npm", "local", "npm", "").
		repo("npm-internal", "local", "npm", "").
		project(pWeb, "web").project(pShop, "shop")
}

func roleEngine(t testing.TB, f *fixture) *Engine { return newTestEngine(t, f, nil) }

func allActions() []Action { return []Action{Read, Annotate, Deploy, Overwrite, Delete, Manage} }

// TestR_AUTHZ_026_EachRoleGrantsItsActionsOnTheProjectsRepositories: Reader read; Developer read, annotate,
// deploy; Maintainer and Owner every action; on every repository of the project and on none of another project.
func TestR_AUTHZ_026_EachRoleGrantsItsActionsOnTheProjectsRepositories(t *testing.T) {
	for _, role := range []ProjectRole{ProjectRoleReader, ProjectRoleDeveloper, ProjectRoleMaintainer, ProjectRoleOwner} {
		e := roleEngine(t, roleFixture().member(pWeb, idDave, role.String()))
		dave := userSub(idDave, "dave")
		for _, a := range allActions() {
			want := role.Actions().Has(a)
			for _, repo := range []string{"web-npm", "web-files"} {
				d := e.Decide(t.Context(), dave, write(repo, "p", "", a))
				if got := d.Outcome == Allow; got != want {
					t.Errorf("%s %s on %s: %v (%s), want %v", role, a, repo, got, d.Reason, want)
				}
				if want && !slices.Contains(d.Sources, "role:web:"+role.String()) {
					t.Errorf("%s %s on %s: sources %v lack the role", role, a, repo, d.Sources)
				}
			}
			if allows(e, dave, "shop-npm", a) || allows(e, dave, "npm-internal", a) {
				t.Errorf("%s holds %s outside its project", role, a)
			}
		}
	}
}

// TestR_AUTHZ_026_TheHighestRoleOverTheUserAndItsGroups: a user's role is the highest among its own membership
// and those of its groups (R-AUTHZ-002); a group's role reaches its members through the subject's groups.
func TestR_AUTHZ_026_TheHighestRoleOverTheUserAndItsGroups(t *testing.T) {
	e := roleEngine(t, roleFixture().
		member(pWeb, idDave, "reader").member(pWeb, idWDevs, "developer").
		member(pWeb, idErin, "maintainer").member(pWeb, idLeads, "reader"))
	dave := userSub(idDave, "dave", idWDevs)
	erin := userSub(idErin, "erin", idLeads)
	if !allows(e, dave, "web-npm", Deploy) || allows(e, dave, "web-npm", Delete) {
		t.Error("dave holds developer through his group, not more")
	}
	if !allows(e, erin, "web-npm", Delete) {
		t.Error("erin's own maintainer counts over her group's reader")
	}
	if !allows(e, userSub("user-frank", "frank", idWDevs), "web-npm", Deploy) {
		t.Error("a member of the group holds the group's role")
	}
	if allows(e, userSub("user-frank", "frank"), "web-npm", Read) {
		t.Error("a user in no member group holds a role")
	}
	if got := e.RoleOn(e.Index(), dave, "web"); got != ProjectRoleDeveloper {
		t.Errorf("RoleOn = %s", got)
	}
}

// TestR_AUTHZ_013_ATokenScopeIntersectsTheRole: a role reaches the personal access tokens of the member, under
// the token's scope like a grant (Q-AUTHZ-215); the refusal of what the role holds and the scope does not is
// deny.scope.
func TestR_AUTHZ_013_ATokenScopeIntersectsTheRole(t *testing.T) {
	e := roleEngine(t, roleFixture().member(pWeb, idDave, "owner"))
	dave := userSub(idDave, "dave")
	readOnly := tokenScop(dave, scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"web/web-npm"}, Actions: []string{"read"}}}})
	if !allows(e, readOnly, "web-npm", Read) {
		t.Error("the scope removed the read the role holds")
	}
	d := e.Decide(t.Context(), readOnly, write("web-npm", "p", "", Deploy))
	if d.Outcome == Allow || d.Reason != ReasonScope {
		t.Errorf("deploy through a read scope: %+v", d)
	}
	if allows(e, readOnly, "web-files", Read) {
		t.Error("the scope admits a repository it does not name")
	}
	inherit := tokenScop(dave, scope.Scope{Inherit: true})
	for _, a := range allActions() {
		if !allows(e, inherit, "web-npm", a) {
			t.Errorf("an Owner's inheriting token holds the content actions, %s refused", a)
		}
	}
	t.Run("Q-AUTHZ-215 the administration needs a session or a token with admin: true", func(t *testing.T) {
		idx := e.Index()
		if e.OwnsOn(idx, inherit, "web") || e.RoleOn(idx, inherit, "web") != ProjectRoleMaintainer {
			t.Error("a token without admin administers the project")
		}
		if !e.OwnsOn(idx, tokenScop(dave, scope.Scope{Inherit: true, Admin: true}), "web") || !e.OwnsOn(idx, dave, "web") {
			t.Error("a session or an admin token of the Owner does not administer")
		}
	})
}

// TestQ_AUTHZ_215_AMembershipReachesATokenWithinItsScope: the projects a personal access token is a member of, and its
// role in each, are the ones its scope reaches (R-AUTHZ-013): a session, an inheriting token, a token with admin: true
// or with the system item projects:read reach every membership; a restricted token reaches a project one of its
// repository elements covers, whatever the element's actions, and no other. A token scoped to one repository's
// deploy does not read the other projects of its owner and their members.
func TestQ_AUTHZ_215_AMembershipReachesATokenWithinItsScope(t *testing.T) {
	e := roleEngine(t, roleFixture().member(pWeb, idDave, "reader").member(pShop, idDave, "owner"))
	idx := e.Index()
	dave := userSub(idDave, "dave")
	el := func(sel string, actions ...string) scope.Grant {
		return scope.Grant{Repositories: []string{sel}, Actions: actions}
	}
	for _, c := range []struct {
		name string
		sub  Subject
		want []string
	}{
		{"a session", dave, []string{"shop", "web"}},
		{"an inheriting token", tokenScop(dave, scope.Scope{Inherit: true}), []string{"shop", "web"}},
		{"an admin token", tokenScop(dave, scope.Scope{Admin: true}), []string{"shop", "web"}},
		{"the system item projects:read", tokenScop(dave, scope.Scope{System: []string{string(ProjectsRead)}}), []string{"shop", "web"}},
		{"one repository's deploy", tokenScop(dave, scope.Scope{Repositories: []scope.Grant{el("web/web-npm", "deploy")}}), []string{"web"}},
		{"a key glob of one project", tokenScop(dave, scope.Scope{Repositories: []scope.Grant{el("shop/*", "read")}}), []string{"shop"}},
		{"a project glob", tokenScop(dave, scope.Scope{Repositories: []scope.Grant{el("*/npm-*", "read")}}), []string{"shop", "web"}},
		{"a class covers every project", tokenScop(dave, scope.Scope{Repositories: []scope.Grant{el("@any-local:npm", "read")}}), []string{"shop", "web"}},
		{"a project of no membership", tokenScop(dave, scope.Scope{Repositories: []scope.Grant{el("it/npm-internal", "read")}}), nil},
		{"another system item", tokenScop(dave, scope.Scope{System: []string{string(PoliciesRead)}}), nil},
		{"an empty scope", tokenScop(dave, scope.Scope{}), nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			var got []string
			for _, m := range e.MembershipsOn(idx, c.sub) {
				got = append(got, m.Key)
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("memberships %v, want %v", got, c.want)
			}
			for _, p := range []string{"web", "shop"} {
				if role := e.RoleOn(idx, c.sub, p); (role != ProjectRoleNone) != slices.Contains(c.want, p) {
					t.Errorf("RoleOn(%s) = %s against memberships %v", p, role, c.want)
				}
			}
		})
	}
}

// TestQ_AUTHZ_215_OnlyUsersCarryARole: a service account, a CI token, a Bastion registration and anonymous are
// never members; a membership row of a principal that is neither a user nor a group is skipped by the build.
func TestQ_AUTHZ_215_OnlyUsersCarryARole(t *testing.T) {
	f := roleFixture().member(pWeb, idBot, "owner")
	f.s.ProjectMembers[len(f.s.ProjectMembers)-1].PrincipalKind = dbServiceAcct
	f.member(pWeb, AuthenticatedID, "reader")
	f.s.ProjectMembers[len(f.s.ProjectMembers)-1].PrincipalKind = dbAuthenticated
	e := roleEngine(t, f)
	bastion := Subject{Kind: principal.KindBastion, ID: idDave, Name: "partners"}
	for name, sub := range map[string]Subject{
		"service account": botSub, "ci": ciSub, "anonymous": anonSub, "bastion": bastion, "any user through authenticated": carolSub,
	} {
		if allows(e, sub, "web-npm", Read) {
			t.Errorf("%s reads through a role", name)
		}
		if len(e.MembershipsOn(e.Index(), sub)) != 0 || e.RoleOn(e.Index(), sub, "web") != ProjectRoleNone {
			t.Errorf("%s is a member", name)
		}
	}
	t.Run("a service account member row with the ID of a user subject grants that user nothing either", func(t *testing.T) {
		if allows(e, userSub(idBot, "imposter"), "web-npm", Read) {
			t.Error("the skipped row grants")
		}
	})
}

// TestR_AUTHZ_026_AdministratorsAndAuditorsSeeEveryProject: admin and auditor hold projects:read and see every
// project without a membership; only admin holds projects:write. A member holds neither.
func TestR_AUTHZ_026_AdministratorsAndAuditorsSeeEveryProject(t *testing.T) {
	f := roleFixture().member(pWeb, idDave, "owner")
	f.bind(RoleAdmin, idOps).bind(RoleAuditor, idCarol)
	e := roleEngine(t, f)
	idx := e.Index()
	admin, auditor, owner := adminSub, carolSub, userSub(idDave, "dave")
	for _, tc := range []struct {
		name        string
		sub         Subject
		read, write bool
	}{
		{"admin", admin, true, true},
		{"auditor", auditor, true, false},
		{"an Owner", owner, false, false},
	} {
		if got := e.HoldsOn(idx, tc.sub, string(ProjectsRead)); got != tc.read {
			t.Errorf("%s projects:read = %v", tc.name, got)
		}
		if got := e.HoldsOn(idx, tc.sub, string(ProjectsWrite)); got != tc.write {
			t.Errorf("%s projects:write = %v", tc.name, got)
		}
	}
	if len(e.MembershipsOn(idx, admin)) != 0 || e.RoleOn(idx, admin, "web") != ProjectRoleNone {
		t.Error("the capability is no membership")
	}
	if allows(e, auditor, "web-npm", Deploy) {
		t.Error("the auditor writes")
	}
}

// TestR_AUTHZ_009_ARoleOnAVirtualRepository: a role opens the gate of a virtual repository of the project with
// read, and a write through it is decided on its deployment target, where the role holds the action; on the
// virtual repository itself only read and manage have an effect.
func TestR_AUTHZ_009_ARoleOnAVirtualRepository(t *testing.T) {
	e := roleEngine(t, roleFixture().member(pWeb, idDave, "developer"))
	dave := userSub(idDave, "dave")
	if d := e.Decide(t.Context(), dave, read("npm-web", "p", "")); d.Outcome != Allow || !slices.Contains(d.Sources, "role:web:developer") {
		t.Errorf("read through the virtual repository: %+v", d)
	}
	if !allows(e, dave, "npm-web", Deploy) {
		t.Error("a deploy through the virtual repository lands on the deployment target, where the developer deploys")
	}
	if allows(e, dave, "npm-web", Manage) {
		t.Error("a developer manages the virtual repository")
	}
	idx := e.Index()
	if got := roleActions(ProjectRoleOwner, idx.repo(qr("npm-web"))); got != virtualActions {
		t.Errorf("an Owner's actions on a virtual repository: %v", got.Strings())
	}
}

// TestR_API_029_AListingReadsWhatARoleReads: a listing classifies a repository of the member's project as read in
// full, and one of another project as none (R-API-029 on the decision of R-AUTHZ-015).
func TestR_API_029_AListingReadsWhatARoleReads(t *testing.T) {
	e := roleEngine(t, roleFixture().member(pWeb, idDave, "reader"))
	dave := userSub(idDave, "dave")
	keys := qrs([]string{"web-npm", "shop-npm"})
	idx, err := e.Begin(t.Context(), dave, keys...)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := e.ReadScopeOn(t.Context(), idx, dave, qname.Repo{}, keys)
	if err != nil {
		t.Fatal(err)
	}
	if rs.Shape(qr("web-npm")) != ReadAll || rs.Shape(qr("shop-npm")) != ReadNone {
		t.Errorf("shapes %s %s", rs.Shape(qr("web-npm")), rs.Shape(qr("shop-npm")))
	}
	if !rs.MayRead(qr("web-npm"), "left-pad", "1.0.0") || rs.MayRead(qr("shop-npm"), "left-pad", "1.0.0") {
		t.Error("MayRead disagrees with the role")
	}
	if got, _ := e.ReadableOn(t.Context(), idx, dave, keys); len(got) != 1 || got[0] != qr("web-npm") {
		t.Errorf("readable %v", got)
	}
}

// TestR_AUTHZ_018_ADeployThroughARoleClaimsForTheMember: a project role that allowed deploy makes the member
// through which it came the owner of a new name: the group whose membership carries it, or the user for its own
// membership; a role is no owner identity of its own (rule 2).
func TestR_AUTHZ_018_ADeployThroughARoleClaimsForTheMember(t *testing.T) {
	f := roleFixture().member(pWeb, idWDevs, "developer").member(pWeb, idErin, "maintainer").member(pWeb, idLeads, "reader")
	e := roleEngine(t, f)
	o := &Ownership{e: e}
	idx := e.Index()
	r := idx.repo(qr("web-npm"))
	k := ownKey{repo: r, domain: domainOf(r), format: r.Format, name: "@web/button"}
	claimers := func(sub Subject) []string {
		var out []string
		for _, c := range o.factsOf(idx, sub, k, publish("web-npm", "@web/button")).claimers {
			out = append(out, c.ref.String())
		}
		slices.Sort(out)
		return out
	}
	if got := claimers(userSub("user-frank", "frank", idWDevs)); !slices.Equal(got, []string{"group:web-devs"}) {
		t.Errorf("through the group: %v", got)
	}
	if got := claimers(userSub(idErin, "erin", idLeads)); !slices.Equal(got, []string{"user:erin"}) {
		t.Errorf("through her own membership (her group's reader holds no deploy): %v", got)
	}
	if got := claimers(userSub("user-gina", "gina")); len(got) != 0 {
		t.Errorf("no role, no claim: %v", got)
	}
}

// TestR_AUTHZ_015_ARoleDecisionDoesNotGrowWithTheProjects: the roles of a decision are derived from the subject's
// user and group IDs once, through the index's memberships by principal, and looked up by the repository's project
// (R-AUTHZ-015 performance target, R-ARCH-033): no query, and no work per project of the installation. A decision
// of a subject in 200 groups allocates the same with 10 projects and with 100, 20 member groups each, and stays
// within the 1 ms target (the p99 is enforced with TROVE_PERF_GATE=1, Q-ARCH-250). What grows is the subject's own
// memberships, which are few (Q-AUTHZ-207).
func TestR_AUTHZ_015_ARoleDecisionDoesNotGrowWithTheProjects(t *testing.T) {
	allocs := map[int]float64{}
	for _, projects := range []int{10, 100} {
		e, sub, c := manyProjects(t, projects)
		ctx := context.Background()
		if d := e.Decide(ctx, sub, c); d.Outcome != Allow {
			t.Fatalf("%d projects: %+v", projects, d)
		}
		allocs[projects] = testing.AllocsPerRun(50, func() { e.Decide(ctx, sub, c) })
		if projects != 100 || raceDetector {
			continue
		}
		const n = 2000
		cpu, release := threadCPU()
		onCPU := make([]time.Duration, 0, n)
		for range n {
			c0 := cpu()
			e.Decide(ctx, sub, c)
			onCPU = append(onCPU, cpu()-c0)
		}
		release()
		slices.Sort(onCPU)
		p50, p99 := onCPU[n/2], onCPU[n*99/100]
		t.Logf("one Decide through a role, %d projects, 200 groups, %s: p50 %s, p99 %s", projects, cpuClock, p50, p99)
		if p50 > time.Millisecond {
			t.Errorf("median %s is above the 1 ms target of R-AUTHZ-015", p50)
		}
		if p99 > time.Millisecond && os.Getenv("TROVE_PERF_GATE") == "1" {
			t.Errorf("p99 %s (%s) is above the 1 ms target of R-AUTHZ-015", p99, cpuClock)
		}
	}
	if allocs[10] != allocs[100] {
		t.Errorf("a decision allocates %v with 10 projects and %v with 100", allocs[10], allocs[100])
	}
}

// manyProjects builds projects p000… with 10 repositories and 20 member groups each, a user in 200 groups of which
// five are readers of the first three projects, the user's own developer role in the last project, and returns a
// deploy in that project.
func manyProjects(t testing.TB, projects int) (*Engine, Subject, Coordinate) {
	t.Helper()
	f := newFixture()
	var groups []string
	for g := range 200 {
		groups = append(groups, fmt.Sprintf("group-g%03d", g))
	}
	var last qname.Repo
	for p := range projects {
		key := fmt.Sprintf("p%03d", p)
		id := "project-" + key
		f.project(id, key)
		for r := range 10 {
			f.next++
			name := qname.Repo{Project: key, Key: fmt.Sprintf("npm-%02d", r)}
			f.s.Repositories = append(f.s.Repositories, repoRow(f.next, name, id))
			last = name
		}
		for g := range 20 {
			f.member(id, fmt.Sprintf("group-%s-%02d", key, g), "reader")
		}
		if p < 3 {
			for _, g := range groups[:5] {
				f.member(id, g, "reader")
			}
		}
	}
	f.member("project-"+last.Project, idDave, "developer")
	e := newTestEngine(t, f, func(o *Options) { o.Config.Cache.MaxStaleness = time.Hour })
	return e, userSub(idDave, "dave", groups...), Coordinate{Repository: last, Package: "p", Kind: KindWrite, Actions: []Action{Deploy}}
}

func BenchmarkRoleDecide(b *testing.B) {
	e, sub, c := manyProjects(b, 100)
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		if d := e.Decide(ctx, sub, c); d.Outcome != Allow {
			b.Fatal(d)
		}
	}
}

// repoRow is the snapshot row of a local npm repository.
func repoRow(id int64, name qname.Repo, projectID string) q.AuthzBuildRepositoriesRow {
	return q.AuthzBuildRepositoriesRow{ID: id, Key: name.Key, ProjectKey: name.Project, ProjectID: projectID, Class: string(repository.Local), Format: "npm", Config: []byte("{}")}
}

// TestR_AUTHZ_026_ManyMembershipsMergeToTheHighestRole: the roles of a subject whose groups are members of more
// projects than a scan serves are merged and looked up through the index of the list, with the same answers.
func TestR_AUTHZ_026_ManyMembershipsMergeToTheHighestRole(t *testing.T) {
	f := newFixture()
	for p := range 3 * smallRoles {
		key := fmt.Sprintf("q%03d", p)
		f.project("project-"+key, key).member("project-"+key, "group-a", "reader").member("project-"+key, "group-b", "developer")
	}
	f.member("project-q020", idDave, "owner")
	e := roleEngine(t, f)
	sub := userSub(idDave, "dave", "group-a", "group-b")
	roles := e.Index().projectRoles(sub)
	if len(roles.list) != 3*smallRoles || roles.at == nil {
		t.Fatalf("%d roles, indexed %v", len(roles.list), roles.at != nil)
	}
	if got := e.RoleOn(e.Index(), sub, "q020"); got != ProjectRoleOwner {
		t.Errorf("q020: %s", got)
	}
	ms := e.MembershipsOn(e.Index(), sub)
	if len(ms) != 3*smallRoles || ms[0] != (Membership{Key: "q000", Role: ProjectRoleDeveloper}) {
		t.Errorf("%+v", ms[:2])
	}
	for _, m := range roles.list {
		if got := roles.in(m.p); got != m.role || (m.p.key != "q020" && m.role != ProjectRoleDeveloper) {
			t.Errorf("%s: %s", m.p.key, m.role)
		}
	}
}

// TestR_AUTHZ_026_TheIndexHoldsEveryProject: a project is on the index with or without members and repositories, and
// an unknown key is not; the management API answers a missing project on it (R-REPO-012).
func TestR_AUTHZ_026_TheIndexHoldsEveryProject(t *testing.T) {
	idx := roleEngine(t, roleFixture().project("p-empty", "empty")).Index()
	for key, want := range map[string]bool{"web": true, "shop": true, "empty": true, "nothing": false, "": false} {
		if got := idx.HasProject(key); got != want {
			t.Errorf("HasProject(%q) = %v, want %v", key, got, want)
		}
	}
	if (*Index)(nil).HasProject("web") {
		t.Error("a nil index holds a project")
	}
}
