package authz

import (
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/principal"
)

// TestR_BAS_004_ABastionRegistrationIsNeverPartOfAuthenticated: a subject of the kind bastion - the principal an
// interface request of a Bastion is admitted as - has an empty principal set. It is never `authenticated`, holds
// no role and no capability, and every read an `authenticated` grant opens to every user is refused to it. The
// kind is dormant (no route decides for it today); the rule keeps it so if one ever does.
func TestR_BAS_004_ABastionRegistrationIsNeverPartOfAuthenticated(t *testing.T) {
	const idBastion = "0192f1c4-0000-7000-8000-0000000000b1"
	f := newFixture().repo("generic-local", "local", "generic", "")
	f.policy(policyDef{name: "everyone-reads", selectors: []string{"generic-local"}, entries: map[string][]string{AuthenticatedID: {"read"}, idBastion: {"read"}}})
	f.bind(RoleAdmin, idBastion)
	e := newTestEngine(t, f, nil)
	bastion := Subject{Kind: principal.KindBastion, ID: idBastion, Name: "partners"}

	idx, err := e.Begin(t.Context(), bastion, "generic-local")
	if err != nil {
		t.Fatal(err)
	}
	if got := idx.principalIDs(bastion); len(got) != 0 {
		t.Fatalf("principal set of a Bastion registration = %v, want none", got)
	}
	if rs := idx.rolesOf(bastion); rs != 0 {
		t.Errorf("a Bastion registration holds roles %b", rs)
	}
	if d := e.DecideOn(t.Context(), idx, bastion, read("generic-local", "", "a.txt")); d.Allowed() {
		t.Errorf("a read the authenticated grant opens was allowed to a Bastion registration: %+v", d)
	}
	me, err := e.Effective(t.Context(), bastion)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(me.Principals, "authenticated") || len(me.Roles) != 0 || len(me.Capabilities) != 0 {
		t.Errorf("Me of a Bastion registration = %+v", me)
	}
	// The same policy reads for a user, through authenticated: the refusal is the kind's, not the fixture's.
	user := userSub("0192f1c4-0000-7000-8000-0000000000c1", "dana")
	if d := e.DecideOn(t.Context(), idx, user, read("generic-local", "", "a.txt")); !d.Allowed() {
		t.Fatalf("the fixture's authenticated grant does not read for a user: %+v", d)
	}
}
