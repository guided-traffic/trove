package authz

import (
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

// What an Owner without a policies capability can learn from the policy operations
// (R-AUTHZ-026, Q-AUTHZ-197, Q-AUTHZ-200).

func TestR_AUTHZ_026_OwnerFiltersTellNothingAboutOtherObjects(t *testing.T) {
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
		{"an unknown repository", ListFilter{Repository: "web/no-such-repo", Project: "web"}, errs.CodeUnknownRepository},
		{"a repository without its project", ListFilter{Repository: "web-npm", Project: "web"}, errs.CodeUnknownRepository},
	} {
		t.Run(tc.name+" is 422 for the holder of the capability", func(t *testing.T) {
			if _, err := s.Select(t.Context(), all, tc.f); err == nil {
				t.Fatal("no error")
			}
		})
		t.Run(tc.name+" answers an empty list for an Owner", func(t *testing.T) {
			tc.f.ProjectOwner = true
			got, err := s.Select(t.Context(), all, tc.f)
			if err != nil || len(got) != 0 {
				t.Fatalf("%v %v", got, err)
			}
		})
	}
	t.Run("a principal and a repository of another project answer the same", func(t *testing.T) {
		got, err := s.Select(t.Context(), all, ListFilter{Principal: "user:alice", Repository: "shop/shop-npm", Project: "web", ProjectOwner: true})
		if err != nil || len(got) != 0 {
			t.Fatalf("%v %v", got, err)
		}
	})
}

func TestR_AUTHZ_026_AnotherProjectsAccountAnswersAsAnUnknownPrincipalToAnOwner(t *testing.T) {
	s := projectService()
	in := projectPolicy("web-shopbot", "web", "web-npm", Entry{"sa:shop-bot", []string{"deploy"}})
	unknown := projectPolicy("web-nobot", "web", "web-npm", Entry{"sa:no-such-bot", []string{"deploy"}})
	r, err := s.Validate(t.Context(), in, AsProjectOwner("web"))
	if err != nil || len(r.Errors) != 1 {
		t.Fatal(err, r.Errors)
	}
	u, err := s.Validate(t.Context(), unknown, AsProjectOwner("web"))
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
		in := projectPolicy("p", "web", "it/npm-internal", Entry{"group:everyone", []string{"read"}})
		in.Name, in.Project = "p", nil
		if p, _ := firstProblem(t, s, in); p.Code != "" {
			t.Fatalf("%+v", p)
		}
	})
}

func TestR_AUTHZ_026_TheBuildSkipsAnAutoJoinGroupOfAProjectPolicy(t *testing.T) {
	// developers is no member of web, so only the policy could grant it.
	f := projectFixture().policy(policyDef{name: "web-all", selectors: []string{"web-*"}, entries: map[string][]string{idDevs: {"read"}}, project: pWeb})
	for i := range f.s.Entries {
		f.s.Entries[i].PrincipalAutoJoin = true
	}
	e := newTestEngine(t, f, nil)
	if allows(e, userSub("user-dave", "dave", idDevs), "web-npm", Read) {
		t.Fatal("a group that became auto-join still grants through a project policy")
	}
	g := newTestEngine(t, projectFixture().policy(policyDef{name: "web-all", selectors: []string{"web-*"}, entries: map[string][]string{idDevs: {"read"}}, project: pWeb}), nil)
	if !allows(g, userSub("user-dave", "dave", idDevs), "web-npm", Read) {
		t.Fatal("the control grants nothing")
	}
}

// TestR_AUTHZ_026_TheBuildSkipsAnAutoJoinGroupAsAMember: a group that became an auto-join group after it was made a
// member holds no role: every user is in it, so it would give every user the role (R-AUTHZ-026). The build skips
// the row (fail closed), as it skips such a group in a project policy.
func TestR_AUTHZ_026_TheBuildSkipsAnAutoJoinGroupAsAMember(t *testing.T) {
	f := projectFixture().member(pWeb, idDevs, "developer")
	f.s.ProjectMembers[len(f.s.ProjectMembers)-1].PrincipalAutoJoin = true
	e := newTestEngine(t, f, nil)
	if allows(e, userSub("user-dave", "dave", idDevs), "web-npm", Read) {
		t.Fatal("a member group that became auto-join still gives its role")
	}
	g := newTestEngine(t, projectFixture().member(pWeb, idDevs, "developer"), nil)
	if !allows(g, userSub("user-dave", "dave", idDevs), "web-npm", Deploy) {
		t.Fatal("the control holds no role")
	}
}
