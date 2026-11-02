package authz

import (
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

// What a project administrator can learn from the policy operations (R-AUTHZ-026,
// Q-AUTHZ-176, Q-AUTHZ-197, Q-AUTHZ-200).

func TestR_AUTHZ_026_ProjectAdministratorFiltersTellNothingAboutOtherObjects(t *testing.T) {
	s := projectService()
	web := "web"
	all := []Policy{{PolicyInput: PolicyInput{
		Name: "web-a", Project: &web, Resources: Resources{Repositories: []string{"*"}},
		Entries: []Entry{{Principal: "user:alice", Actions: []string{"read"}}},
	}}}
	for _, tc := range []struct {
		name string
		f    ListFilter
		code string
	}{
		{"an unknown principal", ListFilter{Principal: "user:nobody", Project: "web"}, errs.CodeUnknownPrincipal},
		{"an unknown repository", ListFilter{Repository: "no-such-repo", Project: "web"}, errs.CodeUnknownRepository},
	} {
		t.Run(tc.name+" is 422 for the holder of the capability", func(t *testing.T) {
			if _, err := s.Select(t.Context(), all, tc.f); err == nil {
				t.Fatal("no error")
			}
		})
		t.Run(tc.name+" answers an empty list for a project administrator", func(t *testing.T) {
			tc.f.Delegated = true
			got, err := s.Select(t.Context(), all, tc.f)
			if err != nil || len(got) != 0 {
				t.Fatalf("%v %v", got, err)
			}
		})
	}
	t.Run("a principal and a repository of another project answer the same", func(t *testing.T) {
		got, err := s.Select(t.Context(), all, ListFilter{Principal: "user:alice", Repository: "shop-npm", Project: "web", Delegated: true})
		if err != nil || len(got) != 0 {
			t.Fatalf("%v %v", got, err)
		}
	})
}

func TestR_AUTHZ_026_AnotherProjectsAccountAnswersAsAnUnknownPrincipalToADelegate(t *testing.T) {
	s := projectService()
	in := projectPolicy("web-shopbot", "web", "web-npm", Entry{"sa:shop-bot", []string{"deploy"}})
	unknown := projectPolicy("web-nobot", "web", "web-npm", Entry{"sa:no-such-bot", []string{"deploy"}})
	r, err := s.Validate(t.Context(), in, AsDelegate("web"))
	if err != nil || len(r.Errors) != 1 {
		t.Fatal(err, r.Errors)
	}
	u, err := s.Validate(t.Context(), unknown, AsDelegate("web"))
	if err != nil || len(u.Errors) != 1 {
		t.Fatal(err, u.Errors)
	}
	if r.Errors[0].Code != errs.CodeUnknownPrincipal || r.Errors[0].Code != u.Errors[0].Code || r.Errors[0].Pointer != u.Errors[0].Pointer {
		t.Fatalf("foreign %+v, unknown %+v", r.Errors[0], u.Errors[0])
	}
	t.Run("the holder of the capability still hears which project it belongs to", func(t *testing.T) {
		if p, _ := firstProblem(t, s, in); p.Code != errs.CodePrincipalOutsideProject {
			t.Fatalf("%+v", p)
		}
	})
}

func TestR_AUTHZ_026_AGlobalPolicyDoesNotTakeAProjectsPrefix(t *testing.T) {
	s := projectService()
	global := func(name string) PolicyInput {
		in := projectPolicy(name, "web", "npm-internal")
		in.Project = nil
		return in
	}
	for _, tc := range []struct {
		name, policy string
		code         string
	}{
		{"a name of the form <key>-… is the project's", "web-extra", errs.CodeNameReservedByProject},
		{"the key without the hyphen is free", "webinar", ""},
		{"another project's key is not a prefix of the name", "shopping", ""},
		{"a name of no project", "ops-read", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if p, _ := firstProblem(t, s, global(tc.policy)); p.Code != tc.code || (tc.code != "" && p.Pointer != "/name") {
				t.Fatalf("%+v, want %q", p, tc.code)
			}
		})
	}
	t.Run("a stored global policy of such a name keeps validating", func(t *testing.T) {
		in := global("web-legacy")
		rp, problems, err := s.resolve(t.Context(), in, true)
		if err != nil || len(problems) != 0 || rp == nil {
			t.Fatal(err, problems)
		}
	})
	t.Run("an update does not meet the reservation", func(t *testing.T) {
		in := global("web-legacy")
		if _, problems, err := s.resolve(t.Context(), in, false); err != nil || len(problems) != 0 {
			t.Fatal(err, problems)
		}
	})
}

func TestR_AUTHZ_026_AProjectPolicyNamesNoAutoJoinGroup(t *testing.T) {
	s := projectService()
	t.Run("an auto-join group is refused in a project policy, whatever the action", func(t *testing.T) {
		for _, act := range []string{"read", "deploy", "manage"} {
			in := projectPolicy("web-all", "web", "web-npm", Entry{"group:everyone", []string{act}})
			if p, _ := firstProblem(t, s, in); p.Code != errs.CodePrincipalOutsideProject || p.Pointer != "/entries/0/principal" {
				t.Fatalf("%s: %+v", act, p)
			}
		}
	})
	t.Run("another group is fine", func(t *testing.T) {
		if p, _ := firstProblem(t, s, projectPolicy("web-all", "web", "web-npm", Entry{"group:web-leads", []string{"deploy"}})); p.Code != "" {
			t.Fatalf("%+v", p)
		}
	})
	t.Run("a global policy may name it", func(t *testing.T) {
		in := projectPolicy("p", "web", "npm-internal", Entry{"group:everyone", []string{"read"}})
		in.Project = nil
		if p, _ := firstProblem(t, s, in); p.Code != "" {
			t.Fatalf("%+v", p)
		}
	})
}

func TestR_AUTHZ_026_TheBuildSkipsAnAutoJoinGroupOfAProjectPolicy(t *testing.T) {
	f := projectFixture().policy(policyDef{name: "web-all", selectors: []string{"web-*"}, entries: map[string][]string{idWeb: {"read"}}, project: pWeb})
	for i := range f.s.Entries {
		f.s.Entries[i].PrincipalAutoJoin = true
	}
	e := newTestEngine(t, f, nil)
	if allows(e, userSub("user-dave", "dave", idWeb), "web-npm", Read) {
		t.Fatal("a group that became auto-join still grants through a project policy")
	}
	g := newTestEngine(t, projectFixture().policy(policyDef{name: "web-all", selectors: []string{"web-*"}, entries: map[string][]string{idWeb: {"read"}}, project: pWeb}), nil)
	if !allows(g, userSub("user-dave", "dave", idWeb), "web-npm", Read) {
		t.Fatal("the control grants nothing")
	}
}
