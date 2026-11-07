package project

import (
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
)

// TestR_AUTHZ_031_RoleComparator: a replay never adds a member and never raises a role.
func TestR_AUTHZ_031_RoleComparator(t *testing.T) {
	roles := []authz.ProjectRole{authz.ProjectRoleReader, authz.ProjectRoleDeveloper, authz.ProjectRoleMaintainer, authz.ProjectRoleOwner}
	for i, cur := range roles {
		for j, aft := range roles {
			if got, want := (roleOrder{}).Narrows(cur, aft), j <= i; got != want {
				t.Errorf("Narrows(%d, %d) = %v, want %v", cur, aft, got, want)
			}
		}
		if (roleOrder{}).Narrows(authz.ProjectRoleNone, cur) {
			t.Errorf("an absent membership never narrows to %d: a replay does not add members", cur)
		}
		if (roleOrder{}).Narrows(cur, authz.ProjectRoleNone) {
			t.Errorf("an unreadable after-role never narrows from %d", cur)
		}
	}
}
