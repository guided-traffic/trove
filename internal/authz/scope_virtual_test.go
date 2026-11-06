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
	viaAll := scope.Grant{Repositories: []string{"it/npm-all"}, Actions: []string{"read"}}
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
		{"a scope on another repository covers nothing here", tok(bobSub, scope.Grant{Repositories: []string{"it/npm-other"}, Actions: []string{"read"}}), "npm-internal", "npm-all", false},
		{"a scope on the virtual repository with another action covers nothing", tok(bobSub, scope.Grant{Repositories: []string{"it/npm-all"}, Actions: []string{"deploy"}}), "npm-internal", "npm-all", false},
		{"the class selector of virtual repositories does not lift (Q-AUTHZ-164)", tok(bobSub, scope.Grant{Repositories: []string{"@any-virtual"}, Actions: []string{"read"}}), "npm-internal", "npm-all", false},
		{"a key glob does not lift either", tok(bobSub, scope.Grant{Repositories: []string{"*/npm-a*"}, Actions: []string{"read"}}), "npm-internal", "npm-all", false},
		{"an element that lists the key among others lifts", tok(bobSub, scope.Grant{Repositories: []string{"it/npm-x", "it/npm-all"}, Actions: []string{"read"}}), "npm-internal", "npm-all", true},
		{"the packages of the scope element still narrow", tok(bobSub, scope.Grant{Repositories: []string{"it/npm-all"}, Packages: scope.Patterns{Include: []string{"@web/*"}}, Actions: []string{"read"}}), "npm-internal", "npm-all", false},
		{"a scope never grants what the subject lacks", tok(carolSub, viaAll), "npm-internal", "npm-all", false},
	}
	for _, tc := range cases {
		t.Run("R-AUTHZ-013 Q-AUTHZ-163 "+tc.name, func(t *testing.T) {
			got, _ := e.MemberUsableOn(t.Context(), idx, tc.sub, qr(tc.member), read(tc.through, "left-pad", ""))
			if got != tc.want {
				t.Fatalf("usable %v, want %v", got, tc.want)
			}
		})
	}
}

// TestR_AUTHZ_013_ThePopulateCheckOfARemoteMemberFollowsTheAddressedVirtualRepository: a
// token scoped to the virtual repository may fill the cache of a remote member with
// cacheFillAction read for a request that went through it, and not for one that did not.
func TestR_AUTHZ_013_ThePopulateCheckOfARemoteMemberFollowsTheAddressedVirtualRepository(t *testing.T) {
	e := newTestEngine(t, populateFixture(), nil)
	idx, err := e.Begin(t.Context(), bobSub)
	if err != nil {
		t.Fatal(err)
	}
	sub := tokenScop(bearer(bobSub), scope.Scope{Repositories: []scope.Grant{{Repositories: []string{"it/npm-all"}, Actions: []string{"read"}}}})
	through := read("npmjs", "left-pad", "")
	through.Addressed = qr("npm-all")
	if d := e.MayPopulateOn(t.Context(), idx, sub, through); !d.Allowed() {
		t.Fatalf("through the virtual repository: %+v", d)
	}
	if d := e.MayPopulateOn(t.Context(), idx, sub, read("npmjs", "left-pad", "")); d.Allowed() {
		t.Fatalf("addressed directly: %+v", d)
	}
}
