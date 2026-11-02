package authz

import "testing"

// A project service account is granted by the policies of its project only. The
// write refuses anything else; the build fails closed on a row that got there by
// another way (R-AUTHZ-026, Q-AUTHZ-175, Q-AUTHZ-198).

// accountOf marks the principal of every entry and binding row as a service account of
// the project.
func accountOf(f *fixture, principalID, project string) *fixture {
	for i := range f.s.Entries {
		if f.s.Entries[i].PrincipalID == principalID {
			f.s.Entries[i].PrincipalProject = project
		}
	}
	for i := range f.s.Bindings {
		if f.s.Bindings[i].PrincipalID == principalID {
			f.s.Bindings[i].PrincipalProject = project
		}
	}
	return f
}

func TestR_AUTHZ_026_TheBuildSkipsAProjectAccountOutsideItsProject(t *testing.T) {
	global := policyDef{name: "ci", selectors: []string{"npm-*"}, entries: map[string][]string{idBot: {"read"}}}
	inWeb := policyDef{name: "web-ci", selectors: []string{"web-*"}, entries: map[string][]string{idBot: {"read"}}, project: pWeb}
	inShop := policyDef{name: "shop-ci", selectors: []string{"shop-*"}, entries: map[string][]string{idBot: {"read"}}, project: pShop}
	t.Run("a global entry naming a project account grants nothing", func(t *testing.T) {
		e := newTestEngine(t, accountOf(projectFixture().policy(global), idBot, pWeb), nil)
		if allows(e, botSub, "npm-internal", Read) {
			t.Fatal("a project service account is granted by a global policy")
		}
	})
	t.Run("a global entry of a global account still grants", func(t *testing.T) {
		e := newTestEngine(t, projectFixture().policy(global), nil)
		if !allows(e, botSub, "npm-internal", Read) {
			t.Fatal("the control grants nothing")
		}
	})
	t.Run("an entry of the account's own project grants, another project's does not", func(t *testing.T) {
		e := newTestEngine(t, accountOf(projectFixture().policy(inWeb).policy(inShop), idBot, pWeb), nil)
		if !allows(e, botSub, "web-npm", Read) {
			t.Fatal("the account does not hold its own project's grant")
		}
		if allows(e, botSub, "shop-npm", Read) {
			t.Fatal("the account holds another project's grant")
		}
	})
	t.Run("a system role bound to a project account is not applied", func(t *testing.T) {
		e := newTestEngine(t, accountOf(projectFixture().bind(RoleAdmin, idBot), idBot, pWeb), nil)
		idx, err := e.Begin(t.Context(), botSub, "web-npm")
		if err != nil {
			t.Fatal(err)
		}
		if got := idx.rolesOf(botSub); got != 0 {
			t.Fatalf("roles of a project account: %v", got)
		}
	})
	t.Run("a role bound to a global account is applied", func(t *testing.T) {
		e := newTestEngine(t, projectFixture().bind(RoleAdmin, idBot), nil)
		idx, err := e.Begin(t.Context(), botSub, "web-npm")
		if err != nil {
			t.Fatal(err)
		}
		if got := idx.rolesOf(botSub); !got.has(RoleAdmin) {
			t.Fatal("the control binds nothing")
		}
	})
}
