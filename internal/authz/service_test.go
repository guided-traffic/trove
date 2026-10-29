package authz

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/repository"
)

type fakePrincipals map[string]principal.Principal

func (f fakePrincipals) ByID(_ context.Context, id string) (principal.Principal, error) {
	for _, p := range f {
		if p.ID == id {
			return p, nil
		}
	}
	return principal.Principal{}, principal.ErrNotFound
}

func (f fakePrincipals) ByName(_ context.Context, kind principal.Kind, name string) (principal.Principal, error) {
	if p, ok := f[string(kind)+"/"+name]; ok {
		return p, nil
	}
	return principal.Principal{}, principal.ErrNotFound
}

func (f fakePrincipals) Groups(context.Context, string) ([]principal.GroupRef, error) {
	return nil, nil
}

type fakeRepos struct{ list []repository.Repository }

func (f fakeRepos) ByKey(_ context.Context, key string) (repository.Repository, error) {
	for _, r := range f.list {
		if r.Key == key {
			return r, nil
		}
	}
	return repository.Repository{}, errs.E(errs.NotFound, errs.CodeRepositoryNotFound, "no such repository")
}

func (f fakeRepos) List(context.Context) ([]repository.Repository, error) { return f.list, nil }

func testService() *Service {
	p := fakePrincipals{}
	for kind, names := range map[principal.Kind][]string{
		principal.KindUser: {"alice", "lead-web"}, principal.KindGroup: {"team-web", "developers"},
		principal.KindServiceAccount: {"ci-bot"}, principal.KindCI: {"gitlab-internal/acme-publish"},
	} {
		for _, n := range names {
			p[string(kind)+"/"+n] = principal.Principal{ID: "id-" + n, Kind: kind, Name: n}
		}
	}
	repos := fakeRepos{}
	for i, r := range []repository.Repository{
		{Key: "npm-internal", Class: repository.Local, Format: "npm"},
		{Key: "npmjs", Class: repository.Remote, Format: "npm", Config: []byte(`{"remote":{"cacheFillAction":"read"}}`)},
		{Key: "npm-frozen", Class: repository.Remote, Format: "npm", Config: []byte(`{"remote":{"cacheFillAction":"deploy"}}`)},
		{Key: "npm-all", Class: repository.Virtual, Format: "npm"},
		{Key: "files-internal", Class: repository.Local, Format: "generic"},
	} {
		r.ID = strconv.Itoa(i + 1)
		repos.list = append(repos.list, r)
	}
	return NewService(ServiceOptions{Principals: p, Repositories: repos, KnownFormat: func(f string) bool { return f == "generic" || f == "npm" || f == "nuget" }})
}

func input(sel string, entries ...Entry) PolicyInput {
	return PolicyInput{Name: "p", Resources: Resources{Repositories: []string{sel}}, Entries: entries}
}

func firstProblem(t *testing.T, s *Service, in PolicyInput) (ProblemField, int) {
	t.Helper()
	r, err := s.Validate(t.Context(), in)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Errors) == 0 {
		return ProblemField{}, 0
	}
	return r.Errors[0], len(r.Errors)
}

func TestR_AUTHZ_021_PolicyValidation(t *testing.T) {
	s := testService()
	cases := []struct {
		name string
		in   PolicyInput
		code string // "" means valid
		ptr  string
	}{
		{"a valid policy", input("npm-internal", Entry{"group:team-web", []string{"write"}}), "", ""},
		{"a policy without entries is valid", input("npm-internal"), "", ""},
		{"R-AUTHZ-030 authenticated may only read", input("@any-virtual", Entry{"authenticated", []string{"read", "deploy"}}), errs.CodeAuthenticatedWriteAction, "/entries/0/actions"},
		{"R-AUTHZ-030 authenticated with only read is valid and may use pattern and class selectors", input("@any-virtual:npm", Entry{"authenticated", []string{"read"}}), "", ""},
		{"R-AUTHZ-030 authenticated with a preset beyond read is refused", input("npm-internal", Entry{"authenticated", []string{"write"}}), errs.CodeAuthenticatedWriteAction, "/entries/0/actions"},
		{"R-AUTHZ-003 anonymous may only read", input("npm-internal", Entry{"anonymous", []string{"read", "deploy"}}), errs.CodeAnonymousWriteAction, "/entries/0/actions"},
		{"R-AUTHZ-003 anonymous with a dynamic selector is refused", input("@any-remote", Entry{"anonymous", []string{"read"}}), errs.CodeAnonymousDynamicSelector, "/resources/repositories"},
		{"R-AUTHZ-003 anonymous with a key pattern is refused", input("npm-*", Entry{"anonymous", []string{"read"}}), errs.CodeAnonymousDynamicSelector, "/resources/repositories"},
		{"R-AUTHZ-003 anonymous read on a key is valid", input("npm-internal", Entry{"anonymous", []string{"read"}}), "", ""},
		{"R-AUTHZ-014 a CI mapping never holds manage", input("npm-internal", Entry{"ci:gitlab-internal/acme-publish", []string{"read", "manage"}}), errs.CodeActionNotAllowed, "/entries/0/actions"},
		{"R-AUTHZ-014 a CI mapping may hold deploy", input("npm-internal", Entry{"ci:gitlab-internal/acme-publish", []string{"read", "deploy"}}), "", ""},
		{"R-AUTHZ-004 a key selector on a virtual repository accepts only read and manage", input("npm-all", Entry{"group:team-web", []string{"read", "deploy"}}), errs.CodeVirtualActionNotApplicable, "/entries/0/actions"},
		{"R-AUTHZ-004 read and manage on a virtual repository", input("npm-all", Entry{"group:team-web", []string{"read", "manage"}}), "", ""},
		{"R-AUTHZ-004 a class selector may cover virtual repositories with any action", input("@any:npm", Entry{"group:team-web", []string{"write"}}), "", ""},
		{"R-AUTHZ-004 an unknown key selector", input("no-such-repo", Entry{"group:team-web", []string{"read"}}), errs.CodeUnknownRepository, "/resources/repositories/0"},
		{"R-AUTHZ-004 an unknown format filter", input("@any-local:rubygems", Entry{"group:team-web", []string{"read"}}), errs.CodeUnknownFormat, "/resources/repositories/0"},
		{"R-AUTHZ-004 a malformed selector", input("@any-everything", Entry{"group:team-web", []string{"read"}}), errs.CodeInvalidPattern, "/resources/repositories/0"},
		{"R-AUTHZ-005 an invalid package pattern", PolicyInput{Name: "p", Resources: Resources{Repositories: []string{"npm-internal"}, Packages: PatternLists{Include: []string{`x\`}}}}, errs.CodeInvalidPattern, "/resources/packages/include/0"},
		{"R-AUTHZ-005 an invalid path pattern", PolicyInput{Name: "p", Resources: Resources{Repositories: []string{"npm-internal"}, Paths: PatternLists{Exclude: []string{"a//b"}}}}, errs.CodeInvalidPattern, "/resources/paths/exclude/0"},
		{"R-AUTHZ-005 too many patterns", PolicyInput{Name: "p", Resources: Resources{Repositories: []string{"npm-internal"}, Packages: PatternLists{Include: make([]string, 257)}}}, errs.CodeInvalidPattern, "/resources/packages/include"},
		{"R-AUTHZ-002 an unknown principal", input("npm-internal", Entry{"user:nobody", []string{"read"}}), errs.CodeUnknownPrincipal, "/entries/0/principal"},
		{"R-AUTHZ-002 a malformed reference", input("npm-internal", Entry{"alice", []string{"read"}}), errs.CodeUnknownPrincipal, "/entries/0/principal"},
		{"R-AUTHZ-002 a reference of an unknown kind", input("npm-internal", Entry{"team:web", []string{"read"}}), errs.CodeUnknownPrincipal, "/entries/0/principal"},
		{"R-AUTHZ-021 a principal twice", input("npm-internal", Entry{"group:team-web", []string{"read"}}, Entry{"group:team-web", []string{"deploy"}}), errs.CodeDuplicatePrincipal, "/entries/1/principal"},
		{"R-AUTHZ-021 an unknown action", input("npm-internal", Entry{"group:team-web", []string{"publish"}}), errs.CodeInvalidAction, "/entries/0/actions"},
		{"R-AUTHZ-021 an entry without actions", input("npm-internal", Entry{"group:team-web", nil}), errs.CodeInvalidAction, "/entries/0/actions"},
		{"R-AUTHZ-021 an invalid name", PolicyInput{Name: "Not Valid", Resources: Resources{Repositories: []string{"npm-internal"}}}, "invalid_name", "/name"},
		{"R-AUTHZ-021 a build policy is not available in M1", PolicyInput{Name: "b", ResourceType: "build"}, "unsupported_resource_type", "/resourceType"},
		{"R-AUTHZ-021 a description over 1024 characters", PolicyInput{Name: "p", Description: strings.Repeat("x", 1025), Resources: Resources{Repositories: []string{"npm-internal"}}}, "invalid_description", "/description"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := firstProblem(t, s, tc.in)
			if p.Code != tc.code || (tc.ptr != "" && p.Pointer != tc.ptr) {
				t.Fatalf("problem = %+v, want %s at %s", p, tc.code, tc.ptr)
			}
		})
	}

	t.Run("R-AUTHZ-021 R-API-009 the answer is 422 validation_failed and every problem keeps its code in errors[]", func(t *testing.T) {
		err := problemError([]ProblemField{field("/entries/0/actions", errs.CodeAuthenticatedWriteAction, "x"), field("/name", "invalid_name", "y")})
		ee, _ := errs.As(err)
		if ee == nil || ee.Status() != 422 || ee.Code != errs.CodeValidation || len(ee.Fields) != 2 || ee.Fields[0].Code != errs.CodeAuthenticatedWriteAction {
			t.Fatalf("%#v", ee)
		}
	})

	t.Run("R-AUTHZ-001 a preset is expanded when the policy is written", func(t *testing.T) {
		rp, problems, err := s.resolve(t.Context(), input("npm-internal", Entry{"group:team-web", []string{"write"}}))
		if err != nil || len(problems) != 0 {
			t.Fatal(err, problems)
		}
		if got := rp.entries[0].actions.Strings(); strings.Join(got, ",") != "annotate,deploy,read" {
			t.Fatalf("stored actions = %v", got)
		}
	})
}

func TestR_AUTHZ_021_Warnings(t *testing.T) {
	s := testService()
	warn := func(in PolicyInput) []string {
		r, err := s.Validate(t.Context(), in)
		if err != nil || len(r.Errors) > 0 {
			t.Fatal(err, r.Errors)
		}
		return r.Warnings
	}
	t.Run("R-AUTHZ-005 path patterns on a package format are flagged", func(t *testing.T) {
		in := PolicyInput{Name: "p", Resources: Resources{Repositories: []string{"npm-internal"}, Paths: PatternLists{Include: []string{"a/**"}}}}
		if w := warn(in); len(w) != 1 || w[0] != WarnPathPatternsOnPackageFormat {
			t.Fatalf("%v", w)
		}
		in.Resources.Repositories = []string{"files-internal"}
		if w := warn(in); len(w) != 0 {
			t.Fatalf("a generic repository takes path patterns: %v", w)
		}
	})
	t.Run("R-AUTHZ-004 a pattern that matches nothing is flagged", func(t *testing.T) {
		if w := warn(input("zzz-*")); len(w) != 1 || w[0] != WarnSelectorMatchesNothing {
			t.Fatalf("%v", w)
		}
		if w := warn(input("npm-*")); len(w) != 0 {
			t.Fatalf("%v", w)
		}
	})
	t.Run("R-AUTHZ-008 anonymous read on a remote repository that fills on read is flagged", func(t *testing.T) {
		if w := warn(input("npmjs", Entry{"anonymous", []string{"read"}})); len(w) != 1 || w[0] != WarnAnonymousPopulatesRemote {
			t.Fatalf("%v", w)
		}
		if w := warn(input("npm-frozen", Entry{"anonymous", []string{"read"}})); len(w) != 0 {
			t.Fatalf("a frozen mirror is not flagged: %v", w)
		}
	})
	t.Run("R-AUTHZ-021 validate reports the repositories the selectors cover", func(t *testing.T) {
		r, err := s.Validate(t.Context(), input("npm-*"))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(r.Covers, ","); got != "npm-all,npm-frozen,npm-internal" {
			t.Fatalf("covers %s", got)
		}
	})
}

func TestR_AUTHZ_010_RoleBindingRules(t *testing.T) {
	s := testService()
	cases := []struct {
		name string
		role Role
		ref  string
		code string
	}{
		{"a role to a user", RoleAuditor, "user:alice", ""},
		{"a role to a group", RoleAdmin, "group:team-web", ""},
		{"a role to a service account", RoleSecurityAdmin, "sa:ci-bot", ""},
		{"anonymous holds no role", RoleAuditor, "anonymous", errs.CodeRoleNotAllowed},
		{"authenticated holds no role", RoleAuditor, "authenticated", errs.CodeRoleNotAllowed},
		{"a CI identity mapping holds no role", RoleAuditor, "ci:gitlab-internal/acme-publish", errs.CodeRoleNotAllowed},
		{"an unknown role", Role("owner"), "user:alice", errs.CodeUnknownRole},
		{"an unknown principal", RoleAuditor, "user:nobody", errs.CodeUnknownPrincipal},
		{"a malformed reference", RoleAuditor, "nobody", errs.CodeUnknownPrincipal},
	}
	for _, tc := range cases {
		t.Run("R-AUTHZ-022 "+tc.name, func(t *testing.T) {
			_, _, err := s.bindable(t.Context(), tc.role, tc.ref)
			if tc.code == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			ee, ok := errs.As(err)
			if !ok || ee.Code != errs.CodeValidation || len(ee.Fields) == 0 || ee.Fields[0].Code != tc.code || ee.Status() != 422 {
				t.Fatalf("err = %v", err)
			}
		})
	}
	t.Run("R-AUTHZ-010 the capability table", func(t *testing.T) {
		if len(RoleCapabilities(RoleAdmin)) != len(Capabilities()) {
			t.Error("admin holds every capability")
		}
		for _, r := range Roles() {
			if r != RoleAdmin && roleCaps(r).has(ContentBypass) {
				t.Errorf("%s holds content:bypass", r)
			}
		}
		if roleCaps(RoleWebhookAdmin).has(PoliciesRead) || !roleCaps(RoleRepositoryAdmin).has(RepositoriesWrite) || roleCaps(RoleAuditor).has(SettingsWrite) {
			t.Error("the table of R-AUTHZ-010 is wrong")
		}
	})
}
