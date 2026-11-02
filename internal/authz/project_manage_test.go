package authz

import (
	"slices"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
)

// A project administrator's manage is decided from the administration of the project,
// not from a grant keyed by the administrator's principal ID: the credential and the
// kind of the subject count (R-AUTHZ-026, Q-AUTHZ-183, Q-AUTHZ-196).
func TestR_AUTHZ_026_ManageOfAProjectAdministratorFollowsTheCredential(t *testing.T) {
	e := newTestEngine(t, projectFixture(), nil)
	web := []principal.GroupRef{{ID: idWeb, Name: "team-web"}}
	sa := Subject{Kind: principal.KindServiceAccount, ID: idBot, Name: "ci-bot", Groups: web}
	for _, tc := range []struct {
		name string
		sub  Subject
		want bool
	}{
		{"a global service account in an administrator group holds no manage", sa, false},
		{"a token of that service account with admin: true holds none either", tokenScop(sa, scope.Scope{Admin: true, Inherit: true}), false},
		{"an inherit token of an administrator without admin: true holds none", tokenScop(aliceSub, scope.Scope{Inherit: true}), false},
		{"an inherit token of a group member without admin: true holds none", tokenScop(userSub("user-dave", "dave", idWeb), scope.Scope{Inherit: true}), false},
		{"a token with admin: true holds manage", tokenScop(aliceSub, scope.Scope{Admin: true, Inherit: true}), true},
		{"a session of a group member holds manage", userSub("user-dave", "dave", idWeb), true},
		{"a CI identity never does", ciSub, false},
		{"anonymous never does", anonSub, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := allows(e, tc.sub, "web-npm", Manage); got != tc.want {
				t.Fatalf("manage on web-npm = %v, want %v", got, tc.want)
			}
		})
	}
	t.Run("the decision names the project as its source", func(t *testing.T) {
		d := e.Decide(t.Context(), aliceSub, write("web-npm", "p", "", Manage))
		if d.Outcome != Allow || !slices.Equal(d.Sources, []string{"project:web"}) {
			t.Fatalf("%v %v", d.Outcome, d.Sources)
		}
	})
	t.Run("ProjectsOn and the decision agree for every subject", func(t *testing.T) {
		idx, err := e.Begin(t.Context(), aliceSub, "web-npm")
		if err != nil {
			t.Fatal(err)
		}
		for _, sub := range []Subject{aliceSub, sa, bobSub, tokenScop(aliceSub, scope.Scope{Inherit: true}), tokenScop(aliceSub, scope.Scope{Admin: true, Inherit: true})} {
			if has, adm := allows(e, sub, "web-npm", Manage), e.AdministersOn(idx, sub, "web", ProjectAny); has != adm {
				t.Errorf("%s: manage %v, administers %v", sub.Name, has, adm)
			}
		}
	})
	t.Run("the index holds no grant per project repository and administrator", func(t *testing.T) {
		idx, err := e.Begin(t.Context(), aliceSub, "web-npm")
		if err != nil {
			t.Fatal(err)
		}
		if n := len(idx.grants); n != 0 {
			t.Fatalf("%d repositories carry grants, want none", n)
		}
	})
}
