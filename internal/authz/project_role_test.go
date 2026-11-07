package authz

import (
	"encoding/json"
	"testing"
)

func TestR_AUTHZ_026_ProjectRoleActions(t *testing.T) {
	all := SetOf(Read, Annotate, Deploy, Overwrite, Delete, Manage)
	for _, tc := range []struct {
		role ProjectRole
		want ActionSet
	}{
		{ProjectRoleNone, 0},
		{ProjectRoleReader, SetOf(Read)},
		{ProjectRoleDeveloper, SetOf(Read, Annotate, Deploy)},
		{ProjectRoleMaintainer, all},
		{ProjectRoleOwner, all},
		{ProjectRole(99), 0},
	} {
		if got := tc.role.Actions(); got != tc.want {
			t.Errorf("%q.Actions() = %v, want %v", tc.role, got.Strings(), tc.want.Strings())
		}
	}
	// Each role holds what the role before it holds (Q-AUTHZ-208).
	for r := ProjectRoleDeveloper; r <= ProjectRoleOwner; r++ {
		if !r.Actions().Contains((r - 1).Actions()) {
			t.Errorf("%s does not hold what %s holds", r, r-1)
		}
	}
}

func TestR_AUTHZ_026_ProjectRoleText(t *testing.T) {
	for _, s := range []string{"reader", "developer", "maintainer", "owner"} {
		r, ok := ParseProjectRole(s)
		if !ok || r.String() != s || !r.Valid() {
			t.Errorf("ParseProjectRole(%q) = %v, %v", s, r, ok)
		}
	}
	for _, s := range []string{"", "Owner", "admin", "guest"} {
		if _, ok := ParseProjectRole(s); ok {
			t.Errorf("ParseProjectRole(%q) accepted", s)
		}
	}
	if ProjectRoleNone.Valid() || ProjectRole(5).Valid() || ProjectRoleNone.String() != "" {
		t.Error("none and out-of-range values are no role")
	}
	if !ProjectRoleOwner.AtLeast(ProjectRoleReader) || ProjectRoleReader.AtLeast(ProjectRoleDeveloper) || ProjectRoleNone.AtLeast(ProjectRoleNone) {
		t.Error("AtLeast")
	}
	b, err := json.Marshal(Membership{Key: "web", Role: ProjectRoleMaintainer})
	if err != nil || string(b) != `{"key":"web","role":"maintainer"}` {
		t.Fatalf("Membership JSON = %s, %v", b, err)
	}
	var m Membership
	if err := json.Unmarshal(b, &m); err != nil || m.Role != ProjectRoleMaintainer {
		t.Errorf("unmarshal = %+v, %v", m, err)
	}
	if err := json.Unmarshal([]byte(`{"key":"web","role":"guest"}`), &m); err == nil {
		t.Error("an unknown role unmarshalled")
	}
	if _, err := json.Marshal(Membership{Key: "web"}); err == nil {
		t.Error("a membership without a role marshalled")
	}
}
