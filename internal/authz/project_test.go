package authz

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/qname"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/repository"
)

// The tests of projects in the engine (R-AUTHZ-026, R-AUTHZ-028, Q-AUTHZ-207 to
// Q-AUTHZ-209, Q-AUTHZ-215, Q-AUTHZ-216).

const (
	pWeb  = "project-web"
	pShop = "project-shop"
)

// fakeProjects answers the project lookups of the validation.
type fakeProjects struct {
	ids      map[string]string // key -> ID
	accounts map[string]string // service account principal ID -> project key
	autoJoin map[string]bool   // group principal ID -> auto-join
}

func (f fakeProjects) KeyPrefixing(_ context.Context, name string) (string, error) {
	for key := range f.ids {
		if strings.HasPrefix(name, key+"-") {
			return key, nil
		}
	}
	return "", nil
}

func (f fakeProjects) AutoJoinGroup(_ context.Context, id string) (bool, error) {
	return f.autoJoin[id], nil
}

func (f fakeProjects) ProjectID(_ context.Context, key string) (string, error) {
	return f.ids[key], nil
}

func (f fakeProjects) AccountProject(_ context.Context, id string) (string, error) {
	return f.accounts[id], nil
}

// project adds a project (once) and its Owners (users and groups) to the snapshot.
func (f *fixture) project(id, key string, owners ...string) *fixture {
	known := false
	for _, p := range f.s.Projects {
		known = known || p.ID == id
	}
	if !known {
		f.s.Projects = append(f.s.Projects, q.AuthzBuildProjectsRow{ID: id, Key: key})
	}
	for _, o := range owners {
		f.member(id, o, "owner")
	}
	return f
}

// member adds a member with a role to a project of the snapshot.
func (f *fixture) member(project, principal, role string) *fixture {
	kind := dbGroup
	if strings.HasPrefix(principal, "user-") {
		kind = dbUser
	}
	f.s.ProjectMembers = append(f.s.ProjectMembers, q.AuthzBuildProjectMembersRow{ProjectID: project, PrincipalID: principal, PrincipalKind: kind, PrincipalName: principal, Role: role})
	return f
}

// assign puts a repository into a project.
func (f *fixture) assign(repo, project string) *fixture {
	key := ""
	for _, p := range f.s.Projects {
		if p.ID == project {
			key = p.Key
		}
	}
	for i := range f.s.Repositories {
		if f.s.Repositories[i].Key == repo {
			f.s.Repositories[i].ProjectID, f.s.Repositories[i].ProjectKey = project, key
			return f
		}
	}
	panic("unknown repository " + repo)
}

// projectFixture is the acceptance setting: project web with web-npm and web-files,
// project shop with shop-npm, and npm-internal in the project it (testProject).
func projectFixture() *fixture {
	return newFixture().
		repo("web-npm", "local", "npm", "").
		repo("web-files", "local", "generic", "").
		repo("shop-npm", "local", "npm", "").
		repo("npm-internal", "local", "npm", "").
		project(pWeb, "web", idAlice, idWeb).
		project(pShop, "shop", idBob).
		assign("web-npm", pWeb).assign("web-files", pWeb).assign("shop-npm", pShop)
}

func allows(e *Engine, sub Subject, repo string, a ...Action) bool {
	return e.Decide(context.Background(), sub, write(repo, "p", "", a...)).Outcome == Allow
}

func TestR_AUTHZ_207_MembershipsOnAnswersForUsersAndGroupsThroughSessionsAndAdminTokens(t *testing.T) {
	e := newTestEngine(t, projectFixture().project("project-api", "api").member("project-api", idAlice, "maintainer"), nil)
	idx, err := e.Begin(t.Context(), aliceSub)
	if err != nil {
		t.Fatal(err)
	}
	keys := func(sub Subject) string {
		var out []string
		for _, m := range e.MembershipsOn(idx, sub) {
			out = append(out, m.Key+":"+m.Role.String())
		}
		return strings.Join(out, ",")
	}
	t.Run("a session lists its projects sorted by key, with the role", func(t *testing.T) {
		got := e.MembershipsOn(idx, aliceSub)
		want := []Membership{{Key: "api", Role: ProjectRoleMaintainer}, {Key: "web", Role: ProjectRoleOwner}}
		if !slices.Equal(got, want) {
			t.Fatalf("%+v, want %+v", got, want)
		}
	})
	t.Run("a member of a group is a member through the group", func(t *testing.T) {
		if got := keys(userSub("user-dave", "dave", idWeb)); got != "web:owner" {
			t.Fatal(got)
		}
	})
	t.Run("a token with admin: true keeps the Owner role, one without holds what an Owner holds beyond the administration", func(t *testing.T) {
		if got := keys(tokenScop(aliceSub, scope.Scope{Admin: true})); got != "api:maintainer,web:owner" {
			t.Fatalf("admin token: %q", got)
		}
		if got := keys(tokenScop(aliceSub, scope.Scope{Inherit: true})); got != "api:maintainer,web:maintainer" {
			t.Fatalf("inherit token without admin: %q", got)
		}
	})
	t.Run("a service account, a CI identity and anonymous are no members", func(t *testing.T) {
		sa := botSub
		sa.Scope = &scope.Scope{Admin: true}
		for name, sub := range map[string]Subject{"service account": sa, "ci": ciSub, "anonymous": anonSub} {
			if got := keys(sub); got != "" {
				t.Errorf("%s: %q", name, got)
			}
		}
		// Even a service account whose own ID is listed as a member row.
		e2 := newTestEngine(t, projectFixture().project("project-x", "x", idBot), nil)
		idx2, _ := e2.Begin(t.Context(), botSub)
		if got := e2.MembershipsOn(idx2, botSub); len(got) != 0 {
			t.Fatalf("a service account is a member: %+v", got)
		}
	})
	t.Run("OwnsOn needs an Owner role through a session or an admin token, and does not tell an unknown project from a foreign one", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			sub     Subject
			project string
			want    bool
		}{
			{"session of an Owner", aliceSub, "web", true},
			{"maintainer is no Owner", aliceSub, "api", false},
			{"another project", aliceSub, "shop", false},
			{"unknown project", aliceSub, "nothing", false},
			{"token without admin", tokenScop(aliceSub, scope.Scope{Inherit: true}), "web", false},
			{"token with admin", tokenScop(aliceSub, scope.Scope{Admin: true}), "web", true},
			{"owner through a group", userSub("user-dave", "dave", idWeb), "web", true},
		} {
			if got := e.OwnsOn(idx, tc.sub, tc.project); got != tc.want {
				t.Errorf("%s: %s = %v, want %v", tc.name, tc.project, got, tc.want)
			}
		}
	})
	t.Run("ProjectOf names the project of the repository", func(t *testing.T) {
		if idx.ProjectOf(qname.Repo{Project: "shop", Key: "shop-npm"}) != "shop" || idx.ProjectOf(qname.Repo{Project: "it", Key: "unknown"}) != "" {
			t.Fatal("ProjectOf")
		}
	})
	t.Run("Effective lists the memberships", func(t *testing.T) {
		me, err := e.Effective(t.Context(), aliceSub)
		if err != nil {
			t.Fatal(err)
		}
		if len(me.Memberships) != 2 || me.Memberships[1] != (Membership{Key: "web", Role: ProjectRoleOwner}) {
			t.Fatalf("%+v", me.Memberships)
		}
		if me, _ = e.Effective(t.Context(), botSub); len(me.Memberships) != 0 {
			t.Fatalf("%+v", me.Memberships)
		}
	})
}

func TestR_AUTHZ_026_ProjectPolicyIsScopedToItsProject(t *testing.T) {
	proj := func(name string, selectors []string, entries map[string][]string) policyDef {
		return policyDef{name: name, selectors: selectors, entries: entries, project: pWeb}
	}
	t.Run("a pattern selector covers only the project's repositories", func(t *testing.T) {
		f := projectFixture().policy(proj("web-all", []string{"*"}, map[string][]string{idCarol: {"read"}}))
		e := newTestEngine(t, f, nil)
		for repo, want := range map[string]bool{"web-npm": true, "web-files": true, "shop-npm": false, "npm-internal": false} {
			if got := allows(e, carolSub, repo, Read); got != want {
				t.Errorf("read on %s = %v, want %v", repo, got, want)
			}
		}
	})
	t.Run("a class selector covers only the project's repositories", func(t *testing.T) {
		f := projectFixture().policy(proj("web-local", []string{"@any-local"}, map[string][]string{idCarol: {"read"}}))
		e := newTestEngine(t, f, nil)
		if !allows(e, carolSub, "web-files", Read) || allows(e, carolSub, "npm-internal", Read) {
			t.Fatal("class selector escaped the project")
		}
	})
	t.Run("a stored key selector of another project's repository covers nothing (fail closed)", func(t *testing.T) {
		f := projectFixture().policy(proj("web-out", []string{"npm-internal", "shop-npm", "web-npm"}, map[string][]string{idCarol: {"read"}}))
		e := newTestEngine(t, f, nil)
		if allows(e, carolSub, "npm-internal", Read) || allows(e, carolSub, "shop-npm", Read) {
			t.Fatal("a key selector outside the project granted")
		}
		if !allows(e, carolSub, "web-npm", Read) {
			t.Fatal("the key selector inside the project does not grant")
		}
	})
	t.Run("R-REPO-060 a repository in another project is no longer covered by the source project's policies", func(t *testing.T) {
		f := projectFixture().policy(proj("web-npm-read", []string{"web-npm"}, map[string][]string{idCarol: {"read"}}))
		e := newTestEngine(t, f, nil)
		if !allows(e, carolSub, "web-npm", Read) {
			t.Fatal("not granted in the project")
		}
		moved := projectFixture().policy(proj("web-npm-read", []string{"web-npm"}, map[string][]string{idCarol: {"read"}})).assign("web-npm", pShop)
		e2 := newTestEngine(t, moved, nil)
		if e2.Decide(t.Context(), carolSub, Coordinate{Repository: qname.Repo{Project: "shop", Key: "web-npm"}, Actions: []Action{Read}}).Outcome == Allow {
			t.Fatal("granted after the repository moved to another project")
		}
	})
	t.Run("a policy of a project the snapshot does not hold covers nothing", func(t *testing.T) {
		p := proj("ghost-all", []string{"*"}, map[string][]string{idCarol: {"read"}})
		p.project = "project-ghost"
		e := newTestEngine(t, projectFixture().policy(p), nil)
		if allows(e, carolSub, "web-npm", Read) || allows(e, carolSub, "npm-internal", Read) {
			t.Fatal("a policy of an unknown project granted")
		}
	})
	t.Run("a global policy keeps applying to project repositories; the effect is the union", func(t *testing.T) {
		f := projectFixture().
			policy(policyDef{name: "all-read", selectors: []string{"*"}, entries: map[string][]string{idBob: {"read"}}}).
			policy(proj("web-deploy", []string{"*"}, map[string][]string{idBob: {"deploy"}}))
		e := newTestEngine(t, f, nil)
		if !allows(e, bobSub, "web-npm", Read) || !allows(e, bobSub, "web-npm", Deploy) {
			t.Fatal("union on a project repository")
		}
		if !allows(e, bobSub, "npm-internal", Read) || allows(e, bobSub, "npm-internal", Deploy) {
			t.Fatal("a project policy leaked deploy")
		}
	})
}

// A global policy and a project policy with the same selector text must not share a
// match result, whatever the order in which the builder meets them.
func TestR_AUTHZ_026_MatchCacheKeyIncludesTheProject(t *testing.T) {
	for _, tc := range []struct{ name, global, project string }{
		{"global first", "a-global", "z-project"},
		{"project first", "z-global", "a-project"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture().
				repo("npm-internal", "local", "npm", "").
				repo("npm-web", "local", "npm", "").
				project(pWeb, "web", idAlice).
				assign("npm-web", pWeb).
				policy(policyDef{name: tc.global, selectors: []string{"npm-*"}, entries: map[string][]string{idCarol: {"read"}}}).
				policy(policyDef{name: tc.project, selectors: []string{"npm-*"}, entries: map[string][]string{idBob: {"read"}}, project: pWeb})
			e := newTestEngine(t, f, nil)
			if !allows(e, carolSub, "npm-internal", Read) || !allows(e, carolSub, "npm-web", Read) {
				t.Error("the global policy lost a repository")
			}
			if allows(e, bobSub, "npm-internal", Read) {
				t.Error("the project policy took the global result and covers a repository outside the project")
			}
			if !allows(e, bobSub, "npm-web", Read) {
				t.Error("the project policy does not cover its repository")
			}
		})
	}
}

func projectService() *Service {
	p := fakePrincipals{}
	for kind, names := range map[principal.Kind][]string{
		principal.KindUser: {"alice", "lead-web"}, principal.KindGroup: {"web-leads", "team-web", "everyone"},
		principal.KindServiceAccount: {"ci-bot", "web-bot", "shop-bot"}, principal.KindCI: {"gitlab-internal/acme-publish"},
	} {
		for _, n := range names {
			p[string(kind)+"/"+n] = principal.Principal{ID: "id-" + n, Kind: kind, Name: n}
		}
	}
	repos := fakeRepos{}
	for i, r := range []repository.Repository{
		{Name: qr("web-npm"), Class: repository.Local, Format: "npm", ProjectID: pWeb},
		{Name: qr("web-files"), Class: repository.Local, Format: "generic", ProjectID: pWeb},
		{Name: qr("shop-npm"), Class: repository.Local, Format: "npm", ProjectID: pShop},
		{Name: qr("npm-internal"), Class: repository.Local, Format: "npm", ProjectID: "project-it"},
	} {
		r.ID = strconv.Itoa(i + 1)
		repos.list = append(repos.list, r)
	}
	return NewService(ServiceOptions{
		Principals: p, Repositories: repos, Formats: testRegistry(),
		Projects: fakeProjects{
			ids:      map[string]string{"web": pWeb, "shop": pShop, "it": "project-it"},
			accounts: map[string]string{"id-web-bot": "web", "id-shop-bot": "shop"},
			autoJoin: map[string]bool{"id-everyone": true},
		},
	})
}

func projectPolicy(name, project string, selector string, entries ...Entry) PolicyInput {
	return PolicyInput{Name: name, Project: &project, Resources: Resources{Repositories: []string{selector}}, Entries: entries}
}

func TestR_AUTHZ_026_ProjectPolicyValidation(t *testing.T) {
	s := projectService()
	global := func(in PolicyInput) PolicyInput { in.Project = nil; return in }
	for _, tc := range []struct {
		name string
		in   PolicyInput
		code string // "" means valid
		ptr  string
	}{
		{"a project policy on a project repository", projectPolicy("web-read", "web", "web-npm", Entry{"group:web-leads", []string{"write"}}), "", ""},
		{"a pattern selector is valid", projectPolicy("web-all", "web", "*", Entry{"user:alice", []string{"manage"}}), "", ""},
		{"a project that does not exist", projectPolicy("nope-read", "nope", "web-npm"), errs.CodeUnknownProject, "/project"},
		{"R-AUTHZ-004 a key of no repository of the project", projectPolicy("web-out", "web", "npm-internal"), errs.CodeUnknownRepository, "/resources/repositories/0"},
		{"R-AUTHZ-004 a key another project has", projectPolicy("web-shop", "web", "shop-npm"), errs.CodeUnknownRepository, "/resources/repositories/0"},
		{"R-AUTHZ-004 a key that exists nowhere", projectPolicy("web-none", "web", "no-such-repo"), errs.CodeUnknownRepository, "/resources/repositories/0"},
		{"R-AUTHZ-004 Q-AUTHZ-216 a selector with a slash names another project and is refused", projectPolicy("web-q", "web", "shop/shop-npm"), errs.CodeInvalidPattern, "/resources/repositories/0"},
		{"R-AUTHZ-004 Q-AUTHZ-216 its own project qualified is refused too", projectPolicy("web-q", "web", "web/web-npm"), errs.CodeInvalidPattern, "/resources/repositories/0"},
		{"R-AUTHZ-004 a class of the project", projectPolicy("web-c", "web", "@any-local:npm"), "", ""},
		{"anonymous is refused", projectPolicy("web-anon", "web", "web-npm", Entry{"anonymous", []string{"read"}}), errs.CodePrincipalOutsideProject, "/entries/0/principal"},
		{"authenticated is refused", projectPolicy("web-auth", "web", "web-npm", Entry{"authenticated", []string{"read"}}), errs.CodePrincipalOutsideProject, "/entries/0/principal"},
		{"a CI mapping never holds manage", projectPolicy("web-ci", "web", "web-npm", Entry{"ci:gitlab-internal/acme-publish", []string{"manage"}}), errs.CodeActionNotAllowed, "/entries/0/actions"},
		{"a CI mapping may deploy", projectPolicy("web-ci", "web", "web-npm", Entry{"ci:gitlab-internal/acme-publish", []string{"deploy"}}), "", ""},
		{"a service account of the project", projectPolicy("web-bot", "web", "web-npm", Entry{"sa:web-bot", []string{"deploy"}}), "", ""},
		{"a global service account", projectPolicy("web-ci-bot", "web", "web-npm", Entry{"sa:ci-bot", []string{"deploy"}}), "", ""},
		{"a service account of another project", projectPolicy("web-shopbot", "web", "web-npm", Entry{"sa:shop-bot", []string{"deploy"}}), errs.CodePrincipalOutsideProject, "/entries/0/principal"},
		{"a global policy naming a project service account", global(projectPolicy("p", "web", "it/npm-internal", Entry{"sa:web-bot", []string{"read"}})), errs.CodePrincipalOutsideProject, "/entries/0/principal"},
		{"a global policy may name a global service account", global(projectPolicy("p", "web", "it/npm-internal", Entry{"sa:ci-bot", []string{"read"}})), "", ""},
		{"Q-AUTHZ-209 a project policy's name is its name within the project", projectPolicy("web/read", "web", "web-npm"), "invalid_name", "/name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := firstProblem(t, s, tc.in)
			if p.Code != tc.code || (tc.ptr != "" && p.Pointer != tc.ptr) {
				t.Fatalf("problem = %+v, want %q at %s", p, tc.code, tc.ptr)
			}
		})
	}
	t.Run("validate reports only the project's repositories as covered", func(t *testing.T) {
		r, err := s.Validate(t.Context(), projectPolicy("web-all", "web", "*"))
		if err != nil || len(r.Errors) != 0 {
			t.Fatal(err, r.Errors)
		}
		if got := strings.Join(r.Covers, ","); got != "web/web-files,web/web-npm" {
			t.Fatalf("covers %s", got)
		}
		if len(r.Warnings) != 0 {
			t.Fatalf("%v", r.Warnings)
		}
	})
	t.Run("a pattern that matches nothing in the project warns although the installation has matches", func(t *testing.T) {
		r, err := s.Validate(t.Context(), projectPolicy("web-x", "web", "npm-*"))
		if err != nil || len(r.Errors) != 0 {
			t.Fatal(err, r.Errors)
		}
		if len(r.Warnings) != 1 || r.Warnings[0] != WarnSelectorMatchesNothing || len(r.Covers) != 0 {
			t.Fatalf("%v %v", r.Warnings, r.Covers)
		}
	})
	t.Run("Q-AUTHZ-209 the same name in two projects and once instance-wide are three policies", func(t *testing.T) {
		web, mobile := "web", "mobile"
		names := map[string]bool{}
		for _, in := range []PolicyInput{{Name: "npm-read", Project: &web}, {Name: "npm-read", Project: &mobile}, {Name: "npm-read"}} {
			names[in.StoredName()] = true
			project, name := splitPolicyName(in.StoredName())
			if name != "npm-read" || !sameProject(project, in.Project) {
				t.Errorf("%s reads back as %v %q", in.StoredName(), project, name)
			}
		}
		if len(names) != 3 || !names["web/npm-read"] || !names["npm-read"] {
			t.Fatalf("%v", names)
		}
	})
}

func TestR_AUTHZ_026_ProjectServiceAccountTakesNoRole(t *testing.T) {
	s := projectService()
	for ref, code := range map[string]string{
		"sa:web-bot": errs.CodeRoleNotAllowed,
		"sa:ci-bot":  "",
	} {
		_, _, err := s.bindable(t.Context(), RoleAuditor, ref)
		if code == "" {
			if err != nil {
				t.Errorf("%s: %v", ref, err)
			}
			continue
		}
		ee, ok := errs.As(err)
		if !ok || len(ee.Fields) == 0 || ee.Fields[0].Code != code || ee.Status() != 422 {
			t.Errorf("%s: %v", ref, err)
		}
	}
}

func TestR_AUTHZ_026_ListFilterByProject(t *testing.T) {
	s := projectService()
	web, shop := "web", "shop"
	all := []Policy{
		{PolicyInput: PolicyInput{Name: "global"}},
		{PolicyInput: PolicyInput{Name: "a", Project: &web, Resources: Resources{Repositories: []string{"*"}}}},
		{PolicyInput: PolicyInput{Name: "a", Project: &shop, Resources: Resources{Repositories: []string{"*"}}}},
	}
	got, err := s.Select(t.Context(), all, ListFilter{Project: "web"})
	if err != nil || len(got) != 1 || got[0].StoredName() != "web/a" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := s.Select(t.Context(), all, ListFilter{Project: "nope"}); err == nil {
		t.Fatal("an unknown project is 422")
	}
	got, err = s.Select(t.Context(), all, ListFilter{Repository: "web/web-npm"})
	// The global policy has no selector, so only the web policy covers web-npm.
	if err != nil || len(got) != 1 || got[0].StoredName() != "web/a" {
		t.Fatalf("a repository filter must keep a project policy only for its project's repositories: %v %v", got, err)
	}
	if _, err := s.Select(t.Context(), all, ListFilter{Repository: "web-npm"}); err == nil {
		t.Fatal("a repository filter names the project (422 unknown_repository)")
	}
	got, err = s.Select(t.Context(), all, ListFilter{Repository: "it/npm-internal"})
	if err != nil || len(got) != 0 {
		t.Fatalf("the project policies cover nothing outside their projects: %v %v", got, err)
	}
}
