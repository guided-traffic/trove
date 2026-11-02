package authz

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta/postgres/q"
	"github.com/guided-traffic/trove/internal/repository"
)

// The tests of projects in the engine (R-AUTHZ-026, R-AUTHZ-028, Q-AUTHZ-179,
// Q-AUTHZ-183).

const (
	pWeb  = "project-web"
	pShop = "project-shop"
)

// fakeProjects answers the project lookups of the validation.
type fakeProjects struct {
	ids      map[string]string // key -> ID
	accounts map[string]string // service account principal ID -> project key
}

func (f fakeProjects) ProjectID(_ context.Context, key string) (string, error) {
	return f.ids[key], nil
}

func (f fakeProjects) AccountProject(_ context.Context, id string) (string, error) {
	return f.accounts[id], nil
}

// project adds a project and its administrators to the snapshot.
func (f *fixture) project(id, key string, managePolicies, manageRepositories bool, admins ...string) *fixture {
	f.s.Projects = append(f.s.Projects, q.AuthzBuildProjectsRow{ID: id, Key: key, ManagePolicies: managePolicies, ManageRepositories: manageRepositories})
	for _, a := range admins {
		kind := dbGroup
		if strings.HasPrefix(a, "user-") {
			kind = dbUser
		}
		f.s.ProjectAdmins = append(f.s.ProjectAdmins, q.AuthzBuildProjectAdminsRow{ProjectID: id, PrincipalID: a, PrincipalKind: kind, PrincipalName: a})
	}
	return f
}

// assign puts a repository into a project.
func (f *fixture) assign(repo, project string) *fixture {
	for i := range f.s.Repositories {
		if f.s.Repositories[i].Key == repo {
			f.s.Repositories[i].ProjectID = project
			return f
		}
	}
	panic("unknown repository " + repo)
}

// projectFixture is the acceptance setting: project web with web-npm and web-files,
// project shop with shop-npm, and npm-internal in none.
func projectFixture() *fixture {
	return newFixture().
		repo("web-npm", "local", "npm", "").
		repo("web-files", "local", "generic", "").
		repo("shop-npm", "local", "npm", "").
		repo("npm-internal", "local", "npm", "").
		project(pWeb, "web", true, false, idAlice, idWeb).
		project(pShop, "shop", false, true, idBob).
		assign("web-npm", pWeb).assign("web-files", pWeb).assign("shop-npm", pShop)
}

func allows(e *Engine, sub Subject, repo string, a ...Action) bool {
	return e.Decide(context.Background(), sub, write(repo, "p", "", a...)).Outcome == Allow
}

func TestR_AUTHZ_026_ProjectAdministratorsHoldManageOnTheirRepositories(t *testing.T) {
	e := newTestEngine(t, projectFixture(), nil)
	for _, tc := range []struct {
		name string
		sub  Subject
		repo string
		want bool
	}{
		{"a user administrator holds manage on a project repository", aliceSub, "web-npm", true},
		{"and on the other repository of the project", aliceSub, "web-files", true},
		{"and nothing on another project's repository", aliceSub, "shop-npm", false},
		{"and nothing on a repository without a project", aliceSub, "npm-internal", false},
		{"a member of an administrator group holds manage", userSub("user-dave", "dave", idWeb), "web-npm", true},
		{"a user that is neither listed nor in a listed group holds none", carolSub, "web-npm", false},
		{"another project's administrator holds none here", bobSub, "web-npm", false},
		{"the other project's administrator holds manage there", bobSub, "shop-npm", true},
		{"a service account never administers", botSub, "web-npm", false},
		{"a session of the administrator is a session", tokenScop(aliceSub, scope.Scope{Inherit: true, Admin: true}), "web-npm", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := allows(e, tc.sub, tc.repo, Manage); got != tc.want {
				t.Fatalf("manage on %s = %v, want %v", tc.repo, got, tc.want)
			}
		})
	}
	t.Run("the grant is manage only: no deploy", func(t *testing.T) {
		if allows(e, aliceSub, "web-npm", Deploy) {
			t.Fatal("an administrator holds deploy without a policy")
		}
	})
	t.Run("the grant names the project", func(t *testing.T) {
		idx, err := e.Begin(t.Context(), aliceSub, "web-npm")
		if err != nil {
			t.Fatal(err)
		}
		r := idx.repo("web-npm")
		var names []string
		for _, g := range idx.grantsFor(r.ID, idAlice) {
			names = append(names, g.policy)
		}
		if !slices.Equal(names, []string{"project:web"}) {
			t.Fatalf("grants of alice: %v", names)
		}
	})
}

func TestR_AUTHZ_026_ProjectsOnAnswersForUsersAndGroupsThroughSessionsAndAdminTokens(t *testing.T) {
	e := newTestEngine(t, projectFixture().project("project-api", "api", false, false, idAlice), nil)
	idx, err := e.Begin(t.Context(), aliceSub)
	if err != nil {
		t.Fatal(err)
	}
	keys := func(sub Subject) string {
		var out []string
		for _, p := range e.ProjectsOn(idx, sub) {
			out = append(out, p.Key)
		}
		return strings.Join(out, ",")
	}
	t.Run("a session lists its projects sorted by key, with the switches", func(t *testing.T) {
		got := e.ProjectsOn(idx, aliceSub)
		want := []AdministeredProject{{Key: "api"}, {Key: "web", ManagePolicies: true}}
		if !slices.Equal(got, want) {
			t.Fatalf("%+v, want %+v", got, want)
		}
	})
	t.Run("a group administrator administers through its group", func(t *testing.T) {
		if got := keys(userSub("user-dave", "dave", idWeb)); got != "web" {
			t.Fatal(got)
		}
	})
	t.Run("a token with admin: true exercises the right, one without does not", func(t *testing.T) {
		if got := keys(tokenScop(aliceSub, scope.Scope{Admin: true})); got != "api,web" {
			t.Fatalf("admin token: %q", got)
		}
		if got := keys(tokenScop(aliceSub, scope.Scope{Inherit: true})); got != "" {
			t.Fatalf("inherit token without admin: %q", got)
		}
	})
	t.Run("a service account, a CI identity and anonymous never administer", func(t *testing.T) {
		sa := botSub
		sa.Scope = &scope.Scope{Admin: true}
		for name, sub := range map[string]Subject{"service account": sa, "ci": ciSub, "anonymous": anonSub} {
			if got := keys(sub); got != "" {
				t.Errorf("%s: %q", name, got)
			}
		}
		// Even a service account whose own ID is listed as an administrator row.
		e2 := newTestEngine(t, projectFixture().project("project-x", "x", true, true, idBot), nil)
		idx2, _ := e2.Begin(t.Context(), botSub)
		if got := e2.ProjectsOn(idx2, botSub); len(got) != 0 {
			t.Fatalf("a service account administers: %+v", got)
		}
	})
	t.Run("AdministersOn follows the switches and does not tell an unknown project from a foreign one", func(t *testing.T) {
		for _, tc := range []struct {
			project string
			right   ProjectRight
			want    bool
		}{
			{"web", ProjectAny, true},
			{"web", ProjectManagePolicies, true},
			{"web", ProjectManageRepositories, false},
			{"api", ProjectManagePolicies, false},
			{"shop", ProjectAny, false},
			{"nothing", ProjectAny, false},
			{"web", ProjectRight("bogus"), false},
		} {
			if got := e.AdministersOn(idx, aliceSub, tc.project, tc.right); got != tc.want {
				t.Errorf("%s/%s = %v, want %v", tc.project, tc.right, got, tc.want)
			}
		}
	})
	t.Run("AdministersRepositoryOn names the project of the repository", func(t *testing.T) {
		if p, ok := e.AdministersRepositoryOn(idx, aliceSub, "web-npm", ProjectManagePolicies); !ok || p != "web" {
			t.Fatalf("%q %v", p, ok)
		}
		for _, repo := range []string{"web-npm:manageRepositories", "shop-npm", "npm-internal", "unknown"} {
			right := ProjectAny
			if strings.HasSuffix(repo, ":manageRepositories") {
				repo, right = "web-npm", ProjectManageRepositories
			}
			if p, ok := e.AdministersRepositoryOn(idx, aliceSub, repo, right); ok || p != "" {
				t.Errorf("%s: %q %v", repo, p, ok)
			}
		}
		if idx.ProjectOf("shop-npm") != "shop" || idx.ProjectOf("npm-internal") != "" || idx.ProjectOf("unknown") != "" {
			t.Fatal("ProjectOf")
		}
	})
	t.Run("Effective lists the projects with their switches", func(t *testing.T) {
		me, err := e.Effective(t.Context(), aliceSub)
		if err != nil {
			t.Fatal(err)
		}
		if len(me.Projects) != 2 || me.Projects[1] != (AdministeredProject{Key: "web", ManagePolicies: true}) {
			t.Fatalf("%+v", me.Projects)
		}
		if me, _ = e.Effective(t.Context(), botSub); len(me.Projects) != 0 {
			t.Fatalf("%+v", me.Projects)
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
	t.Run("a key selector outside the project covers nothing (Q-AUTHZ-179)", func(t *testing.T) {
		f := projectFixture().policy(proj("web-out", []string{"npm-internal", "shop-npm", "web-npm"}, map[string][]string{idCarol: {"read"}}))
		e := newTestEngine(t, f, nil)
		if allows(e, carolSub, "npm-internal", Read) || allows(e, carolSub, "shop-npm", Read) {
			t.Fatal("a key selector outside the project granted")
		}
		if !allows(e, carolSub, "web-npm", Read) {
			t.Fatal("the key selector inside the project does not grant")
		}
	})
	t.Run("a repository that leaves the project leaves the policy inert, and returns with it", func(t *testing.T) {
		f := projectFixture().policy(proj("web-npm-read", []string{"web-npm"}, map[string][]string{idCarol: {"read"}}))
		e := newTestEngine(t, f, nil)
		if !allows(e, carolSub, "web-npm", Read) {
			t.Fatal("not granted in the project")
		}
		moved := projectFixture().policy(proj("web-npm-read", []string{"web-npm"}, map[string][]string{idCarol: {"read"}})).assign("web-npm", "")
		e2 := newTestEngine(t, moved, nil)
		if allows(e2, carolSub, "web-npm", Read) {
			t.Fatal("granted after the repository left the project")
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
				project(pWeb, "web", true, false, idAlice).
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
		principal.KindUser: {"alice", "lead-web"}, principal.KindGroup: {"web-leads", "team-web"},
		principal.KindServiceAccount: {"ci-bot", "web-bot", "shop-bot"}, principal.KindCI: {"gitlab-internal/acme-publish"},
	} {
		for _, n := range names {
			p[string(kind)+"/"+n] = principal.Principal{ID: "id-" + n, Kind: kind, Name: n}
		}
	}
	repos := fakeRepos{}
	for i, r := range []repository.Repository{
		{Key: "web-npm", Class: repository.Local, Format: "npm", ProjectID: pWeb},
		{Key: "web-files", Class: repository.Local, Format: "generic", ProjectID: pWeb},
		{Key: "shop-npm", Class: repository.Local, Format: "npm", ProjectID: pShop},
		{Key: "npm-internal", Class: repository.Local, Format: "npm"},
		{Key: "web-moved", Class: repository.Local, Format: "npm"},
	} {
		r.ID = strconv.Itoa(i + 1)
		repos.list = append(repos.list, r)
	}
	return NewService(ServiceOptions{
		Principals: p, Repositories: repos, Formats: testRegistry(),
		Projects: fakeProjects{
			ids:      map[string]string{"web": pWeb, "shop": pShop},
			accounts: map[string]string{"id-web-bot": "web", "id-shop-bot": "shop"},
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
		{"a name without the project prefix", projectPolicy("read-web", "web", "web-npm"), errs.CodeProjectPrefixRequired, "/name"},
		{"a name with the prefix of another project", projectPolicy("shop-read", "web", "web-npm"), errs.CodeProjectPrefixRequired, "/name"},
		{"a key selector of a repository outside the project", projectPolicy("web-out", "web", "npm-internal"), errs.CodeRepositoryOutsideProject, "/resources/repositories/0"},
		{"a key selector of another project's repository", projectPolicy("web-shop", "web", "shop-npm"), errs.CodeRepositoryOutsideProject, "/resources/repositories/0"},
		{"an unknown key answers like one outside the project", projectPolicy("web-none", "web", "no-such-repo"), errs.CodeRepositoryOutsideProject, "/resources/repositories/0"},
		{"anonymous is refused", projectPolicy("web-anon", "web", "web-npm", Entry{"anonymous", []string{"read"}}), errs.CodePrincipalOutsideProject, "/entries/0/principal"},
		{"authenticated is refused", projectPolicy("web-auth", "web", "web-npm", Entry{"authenticated", []string{"read"}}), errs.CodePrincipalOutsideProject, "/entries/0/principal"},
		{"a CI mapping never holds manage", projectPolicy("web-ci", "web", "web-npm", Entry{"ci:gitlab-internal/acme-publish", []string{"manage"}}), errs.CodeActionNotAllowed, "/entries/0/actions"},
		{"a CI mapping may deploy", projectPolicy("web-ci", "web", "web-npm", Entry{"ci:gitlab-internal/acme-publish", []string{"deploy"}}), "", ""},
		{"a service account of the project", projectPolicy("web-bot", "web", "web-npm", Entry{"sa:web-bot", []string{"deploy"}}), "", ""},
		{"a global service account", projectPolicy("web-ci-bot", "web", "web-npm", Entry{"sa:ci-bot", []string{"deploy"}}), "", ""},
		{"a service account of another project", projectPolicy("web-shopbot", "web", "web-npm", Entry{"sa:shop-bot", []string{"deploy"}}), errs.CodePrincipalOutsideProject, "/entries/0/principal"},
		{"a global policy naming a project service account", global(projectPolicy("p", "web", "npm-internal", Entry{"sa:web-bot", []string{"read"}})), errs.CodePrincipalOutsideProject, "/entries/0/principal"},
		{"a global policy may name a global service account", global(projectPolicy("p", "web", "npm-internal", Entry{"sa:ci-bot", []string{"read"}})), "", ""},
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
		if got := strings.Join(r.Covers, ","); got != "web-files,web-npm" {
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
	t.Run("a stored policy whose repository left the project keeps the selector and warns (Q-AUTHZ-179)", func(t *testing.T) {
		in := projectPolicy("web-read", "web", "web-moved")
		if _, n := firstProblem(t, s, in); n == 0 {
			t.Fatal("a write must refuse the selector")
		}
		rp, problems, err := s.resolve(t.Context(), in, true)
		if err != nil || len(problems) != 0 {
			t.Fatal(err, problems)
		}
		if !slices.Equal(rp.warnings, []string{WarnSelectorOutsideProject}) || len(rp.covers) != 0 {
			t.Fatalf("%v %v", rp.warnings, rp.covers)
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
		{PolicyInput: PolicyInput{Name: "web-a", Project: &web, Resources: Resources{Repositories: []string{"*"}}}},
		{PolicyInput: PolicyInput{Name: "shop-a", Project: &shop, Resources: Resources{Repositories: []string{"*"}}}},
	}
	got, err := s.Select(t.Context(), all, ListFilter{Project: "web"})
	if err != nil || len(got) != 1 || got[0].Name != "web-a" {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := s.Select(t.Context(), all, ListFilter{Project: "nope"}); err == nil {
		t.Fatal("an unknown project is 422")
	}
	got, err = s.Select(t.Context(), all, ListFilter{Repository: "web-npm"})
	// The global policy has no selector, so only the web policy covers web-npm.
	if err != nil || len(got) != 1 || got[0].Name != "web-a" {
		t.Fatalf("a repository filter must keep a project policy only for its project's repositories: %v %v", got, err)
	}
	got, err = s.Select(t.Context(), all, ListFilter{Repository: "npm-internal"})
	if err != nil || len(got) != 0 {
		t.Fatalf("the project policies cover nothing outside their projects: %v %v", got, err)
	}
}
