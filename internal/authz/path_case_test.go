package authz

import (
	"strconv"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats/catalog"
	"github.com/guided-traffic/trove/internal/repository"
)

// TestR_AUTHZ_005_AnUpperCasePathPatternOnALowerCaseFormatIsRefused: NuGet stores lower-case paths (R-NUGET-005), and the
// decision point compares the path dimension of a NuGet repository in lower case, pattern and path alike, however and
// whenever the pattern reached it (Q-AUTHZ-254). A policy that covers such a repository refuses an upper-case pattern at
// the pattern, as feedback; the decision point answers the same question for a token scope and a CI inline grant
// (Q-AUTHZ-253). Formats whose paths may carry upper-case letters take the pattern and compare it as written, and every
// path pattern on a package format draws the warning that documents and files are decided on paths of their own.
func TestR_AUTHZ_005_AnUpperCasePathPatternOnALowerCaseFormatIsRefused(t *testing.T) {
	repos := fakeRepos{}
	for i, r := range []repository.Repository{
		{Name: qr("nuget-internal"), Class: repository.Local, Format: "nuget"},
		{Name: qr("npm-internal"), Class: repository.Local, Format: "npm"},
		{Name: qr("files-internal"), Class: repository.Local, Format: "generic"},
	} {
		r.ID = strconv.Itoa(i + 1)
		repos.list = append(repos.list, r)
	}
	s := NewService(ServiceOptions{Principals: fakePrincipals{}, Repositories: repos, Formats: testRegistry(), Projects: fakeProjects{}})
	policy := func(repo string, include, exclude []string) PolicyInput {
		return PolicyInput{Name: "p", Resources: Resources{Repositories: []string{inst(repo)}, Paths: PatternLists{Include: include, Exclude: exclude}}}
	}
	t.Run("R-AUTHZ-021 an upper-case exclude on a NuGet repository is refused at the pattern", func(t *testing.T) {
		r, err := s.Validate(t.Context(), policy("nuget-internal", nil, []string{"acme.open/**", "Acme.Secret/**"}))
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Errors) != 1 || r.Errors[0].Code != errs.CodeInvalidPattern || r.Errors[0].Pointer != "/resources/paths/exclude/1" {
			t.Fatalf("errors %+v", r.Errors)
		}
	})
	t.Run("R-AUTHZ-021 an upper-case include on a NuGet repository is refused", func(t *testing.T) {
		r, err := s.Validate(t.Context(), policy("nuget-internal", []string{"Acme.Open/**"}, nil))
		if err != nil || len(r.Errors) != 1 || r.Errors[0].Pointer != "/resources/paths/include/0" {
			t.Fatalf("%v %+v", err, r.Errors)
		}
	})
	t.Run("R-AUTHZ-021 a lower-case exclude on a package format is taken, with the warning", func(t *testing.T) {
		for _, repo := range []string{"nuget-internal", "npm-internal"} {
			r, err := s.Validate(t.Context(), policy(repo, nil, []string{"acme.secret/**"}))
			if err != nil || len(r.Errors) != 0 {
				t.Fatalf("%s: %v %+v", repo, err, r.Errors)
			}
			if len(r.Warnings) != 1 || r.Warnings[0] != WarnPathPatternsOnPackageFormat {
				t.Fatalf("%s: warnings %v", repo, r.Warnings)
			}
		}
	})
	t.Run("R-AUTHZ-021 formats whose paths may carry upper case take the pattern", func(t *testing.T) {
		for _, repo := range []string{"npm-internal", "files-internal"} {
			r, err := s.Validate(t.Context(), policy(repo, nil, []string{"Releases/**"}))
			if err != nil || len(r.Errors) != 0 {
				t.Fatalf("%s: %v %+v", repo, err, r.Errors)
			}
		}
	})

	t.Run("R-EP-012 an exposure rule's upper-case path pattern on a lower-case format is refused", func(t *testing.T) {
		reg := catalog.NewRegistry()
		reg.Register(catalog.Entry{
			Facts:    catalog.Facts{ID: "lowfmt", Classes: []catalog.Class{catalog.Local}, Layout: "lowfmt", PatternSubject: catalog.SubjectPath, LowerCasePaths: true},
			Exposure: catalog.Exposure{Exposable: true},
		})
		es := NewExposureService(ExposureServiceOptions{Ceiling: NewExposureCeiling(map[string]string{"customers": ExposureModeRules}, reg)})
		ps := es.validateForFormat(ExposureRuleInput{Name: "r", Paths: PatternLists{Include: []string{"open/**"}, Exclude: []string{"Secret/**"}}}, "lowfmt", "")
		if len(ps) != 1 || ps[0].Pointer != "/paths/exclude/0" || ps[0].Code != errs.CodeInvalidPattern {
			t.Fatalf("%+v", ps)
		}
	})

	f := newFixture().repo("nuget-local", "local", "nuget", "").repo("npm-local", "local", "npm", "")
	// A policy written before any NuGet repository existed, with a broad selector, as the write-time refusal cannot see it:
	// the patterns are folded when they are compiled for the NuGet repository (Q-AUTHZ-254).
	f.policy(policyDef{name: "later", selectors: []string{inst("nuget-*"), inst("npm-*")}, pathExc: []string{"Acme.Secret/**"}, entries: map[string][]string{idAlice: {"read"}}})
	f.policy(policyDef{name: "scoped", selectors: []string{"@any"}, entries: map[string][]string{idBob: {"read"}}})
	e := newTestEngine(t, f, nil)
	bob := userSub(idBob, "bob")
	bob.Scope = &scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"@any"}, Paths: scope.Patterns{Exclude: []string{"Acme.Secret/**"}}, Actions: []string{"read"}}}}
	read := func(repo, pkg, path string) Coordinate {
		return Coordinate{Repository: qr(repo), Package: pkg, Path: path, Kind: KindArtifact, Actions: []Action{Read}}
	}
	for _, tc := range []struct {
		name string
		sub  Subject
		c    Coordinate
		want bool
	}{
		{"a policy's upper-case exclude hides the NuGet package it names", userSub(idAlice, "alice"), read("nuget-local", "acme.secret", "acme.secret/index.json"), false},
		{"a policy's upper-case exclude hides the NuGet package's files", userSub(idAlice, "alice"), read("nuget-local", "acme.secret", "acme.secret/1.0.0/acme.secret.1.0.0.nupkg"), false},
		{"a policy's upper-case exclude leaves another NuGet package", userSub(idAlice, "alice"), read("nuget-local", "acme.open", "acme.open/index.json"), true},
		{"a token scope's upper-case exclude hides the NuGet package it names", bob, read("nuget-local", "acme.secret", "acme.secret/index.json"), false},
		{"a token scope's upper-case exclude leaves another NuGet package", bob, read("nuget-local", "acme.open", "acme.open/index.json"), true},
		{"npm paths keep their case: an upper-case exclude does not hide a lower-case npm path", userSub(idAlice, "alice"), read("npm-local", "acme.secret", "acme.secret"), true},
	} {
		t.Run("R-AUTHZ-005 folding: "+tc.name, func(t *testing.T) {
			if d := e.Decide(t.Context(), tc.sub, tc.c); d.Allowed() != tc.want {
				t.Fatalf("Decide = %+v, want allowed %v", d, tc.want)
			}
		})
	}
	t.Run("R-AUTHZ-005 folding: a folded path set compares the path in lower case as well", func(t *testing.T) {
		g, err := compileSet(dimPath, nil, []string{"Acme.Secret/**"}, caseFold{})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{"acme.secret/index.json", "acme.secret/1.0.0/.symbols/lib/Acme.Secret.pdb"} {
			if (resourceSet{paths: g}).matches(false, "acme.secret", p) {
				t.Errorf("%s is not excluded", p)
			}
		}
		if g, _ := compileSet(dimPath, nil, []string{"Acme.Secret/**"}, nil); !(resourceSet{paths: g}).matches(false, "acme.secret", "acme.secret/index.json") {
			t.Error("an unfolded path set folded")
		}
	})
	for _, tc := range []struct {
		name      string
		selectors []string
		patterns  []string
		want      bool
	}{
		{"an upper-case exclude on the NuGet repository", []string{inst("nuget-local")}, []string{"acme.open/**", "Acme.Secret/**"}, true},
		{"a selector by format covers the NuGet repository", []string{"@any-local:nuget"}, []string{"Acme.Secret/**"}, true},
		{"lower-case patterns on the NuGet repository", []string{inst("nuget-local")}, []string{"acme.secret/**"}, false},
		{"an upper-case pattern on an npm repository", []string{inst("npm-local")}, []string{"Acme/**"}, false},
		{"a template's letters are judged when it is rendered", []string{inst("nuget-local")}, []string{"{{Project}}/**"}, false},
		{"a literal upper-case letter beside a template", []string{inst("nuget-local")}, []string{"{{project}}/Lib/**"}, true},
	} {
		t.Run("R-AUTHZ-005 the decision point: "+tc.name, func(t *testing.T) {
			if _, _, got := e.UpperCasePath(instanceContext, tc.selectors, tc.patterns); got != tc.want {
				t.Fatalf("UpperCasePath = %v, want %v", got, tc.want)
			}
		})
	}
}
