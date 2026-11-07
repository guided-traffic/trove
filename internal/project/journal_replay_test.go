package project

import (
	"encoding/json"
	"testing"

	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/journal"
)

// TestR_AUTHZ_031_RepositoryEntriesAreReadStrictly: repository.disabled and repository.deleted name the repository by the
// decimal ID the producers write; anything else is skipped as malformed (Q-AUTHZ-238, Q-AUTHZ-239).
func TestR_AUTHZ_031_RepositoryEntriesAreReadStrictly(t *testing.T) {
	if id, text, ok := readRepositoryEntry(journal.Object{Entry: json.RawMessage(`{"repositoryId":"42","key":"web/npm-local"}`)}); !ok || id != 42 || text != "42" {
		t.Fatalf("the producer's entry reads as %d %q %v", id, text, ok)
	}
	for _, bad := range []string{`{"key":"web/npm-local"}`, `{"repositoryId":42}`, `{"repositoryId":"web/npm-local"}`, `{"repositoryId":"0"}`, `{"repositoryId":"-3"}`, `[]`} {
		if _, _, ok := readRepositoryEntry(journal.Object{Entry: json.RawMessage(bad)}); ok {
			t.Errorf("%s was read", bad)
		}
	}
}

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
