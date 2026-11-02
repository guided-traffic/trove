package authz

import (
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
)

// TestQ_AUTHZ_172_AuditorReadScope: the read scope of a listing (R-API-029) holds
// every package of every physical repository for the auditor, decides rows under
// a token's repository scope, and never lists anything to a token that does not
// carry the capability.
func TestQ_AUTHZ_172_AuditorReadScope(t *testing.T) {
	e := newTestEngine(t, metadataFixture(), nil)
	keys := []string{"npm-internal", "nuget-local", "npm-all"}
	shapes := func(t *testing.T, sub Subject) (ReadScope, map[string]ReadShape) {
		t.Helper()
		rs, err := e.ReadScopeOn(t.Context(), e.Index(), sub, keys)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]ReadShape{}
		for _, k := range keys {
			out[k] = rs.Shape(k)
		}
		return rs, out
	}
	t.Run("Q-AUTHZ-172 a session of the auditor lists every package of every physical repository", func(t *testing.T) {
		rs, got := shapes(t, userSub(idInGroup, "member", idAuditors))
		if got["npm-internal"] != ReadAll || got["nuget-local"] != ReadAll || got["npm-all"] != ReadNone {
			t.Fatalf("shapes %v", got)
		}
		if !rs.MayRead("npm-internal", "left-pad", "1.0.0") {
			t.Fatal("a row of the auditor is refused")
		}
	})
	t.Run("R-AUTHZ-013 a token scope narrows the listing row by row", func(t *testing.T) {
		sub := tokenScop(userSub(idAuditorDirect, "auditor"), scope.Scope{
			System:       []string{string(ContentMetadataRead)},
			Repositories: []scope.Grant{{Repositories: []string{"npm-internal"}, Actions: []string{"read"}}},
		})
		rs, got := shapes(t, sub)
		if got["npm-internal"] != ReadRows || !rs.MayRead("npm-internal", "left-pad", "") || rs.MayRead("nuget-local", "acme.widgets", "") {
			t.Fatalf("shapes %v", got)
		}
	})
	t.Run("R-AUTHZ-013 a token without the system item lists nothing", func(t *testing.T) {
		if _, got := shapes(t, tokenScop(userSub(idAuditorDirect, "auditor"), scope.Scope{Inherit: true})); got["npm-internal"] != ReadNone || got["nuget-local"] != ReadNone {
			t.Fatalf("shapes %v", got)
		}
	})
	t.Run("R-AUTHZ-001 a user without the role lists nothing", func(t *testing.T) {
		if _, got := shapes(t, carolSub); got["npm-internal"] != ReadNone {
			t.Fatalf("shapes %v", got)
		}
	})
}
