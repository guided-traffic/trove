package authz

import (
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
)

// TestR_AUTHZ_013_AScopeOnAVirtualRepositoryCoversItsMembersWhenReadThroughIt: a token
// scoped to read on a virtual repository finds the content of the members through it
// (Q-AUTHZ-163), and only through it, and never more than the subject itself holds.
func TestR_AUTHZ_013_AScopeOnAVirtualRepositoryCoversItsMembersWhenReadThroughIt(t *testing.T) {
	e := newTestEngine(t, populateFixture(), nil)
	idx, err := e.Begin(t.Context(), bobSub)
	if err != nil {
		t.Fatal(err)
	}
	tok := func(sub Subject, g ...scope.Grant) Subject {
		return tokenScop(bearer(sub), scope.Scope{Repositories: g})
	}
	viaAll := scope.Grant{Repositories: []string{"npm-all"}, Actions: []string{"read"}}
	cases := []struct {
		name    string
		sub     Subject
		member  string
		through string
		want    bool
	}{
		{"a member is read through the virtual repository the scope names", tok(bobSub, viaAll), "npm-internal", "npm-all", true},
		{"a remote member is too", tok(bobSub, viaAll), "npmjs", "npm-all", true},
		{"the same member addressed directly is not covered", tok(bobSub, viaAll), "npm-internal", "npm-internal", false},
		{"a member read through no virtual repository is not covered", tok(bobSub, viaAll), "npm-internal", "", false},
		{"a repository that is no member is not covered", tok(bobSub, viaAll), "npm-odd", "npm-all", false},
		{"a scope on another repository covers nothing here", tok(bobSub, scope.Grant{Repositories: []string{"npm-other"}, Actions: []string{"read"}}), "npm-internal", "npm-all", false},
		{"a scope on the virtual repository with another action covers nothing", tok(bobSub, scope.Grant{Repositories: []string{"npm-all"}, Actions: []string{"deploy"}}), "npm-internal", "npm-all", false},
		{"the class selector of virtual repositories covers the members too", tok(bobSub, scope.Grant{Repositories: []string{"@any-virtual"}, Actions: []string{"read"}}), "npm-internal", "npm-all", true},
		{"the packages of the scope element still narrow", tok(bobSub, scope.Grant{Repositories: []string{"npm-all"}, Packages: scope.Patterns{Include: []string{"@web/*"}}, Actions: []string{"read"}}), "npm-internal", "npm-all", false},
		{"a scope never grants what the subject lacks", tok(carolSub, viaAll), "npm-internal", "npm-all", false},
	}
	for _, tc := range cases {
		t.Run("R-AUTHZ-013 Q-AUTHZ-163 "+tc.name, func(t *testing.T) {
			got, _ := e.MemberUsableOn(t.Context(), idx, tc.sub, tc.member, read(tc.through, "left-pad", ""))
			if got != tc.want {
				t.Fatalf("usable %v, want %v", got, tc.want)
			}
		})
	}
}
