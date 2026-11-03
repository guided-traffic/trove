package repository

import (
	"slices"
	"testing"
)

// R-REPO-023: MatchClaims, the claims of a caller's own transaction, orders and matches as the claim index does: the most specific
// first, equal specificity by repository id, a pattern that does not parse protects nothing.
func TestR_REPO_023_MatchClaimsOrdersAsTheIndex(t *testing.T) {
	claims := []StoredClaim{
		{RepoID: 9, Repository: "z", Pattern: "acme*"},
		{RepoID: 4, Repository: "d", Pattern: "acme**"},
		{RepoID: 7, Repository: "g", Pattern: "acme-ui"},
		{RepoID: 2, Repository: "b", Pattern: "acme-*"},
		{RepoID: 3, Repository: "c", Pattern: "acme-ui"},
		{RepoID: 5, Repository: "e", Pattern: "other*"},
		{RepoID: 6, Repository: "f", Pattern: "a?me"},
	}
	var got []string
	for _, m := range MatchClaims(claims, "acme-ui") {
		got = append(got, m.Repository)
	}
	if want := []string{"c", "g", "b", "z", "d"}; !slices.Equal(got, want) {
		t.Errorf("claims %v, want %v", got, want)
	}
	if m := MatchClaims(claims, ""); m != nil {
		t.Errorf("the empty name is covered by %v", m)
	}
	// a `*` stops at a slash: it covers no scope below the literal
	scoped := MatchClaims([]StoredClaim{{RepoID: 1, Repository: "a", Pattern: "@acme/*"}, {RepoID: 2, Repository: "b", Pattern: "@acme/**"}}, "@acme/ui/x")
	if len(scoped) != 1 || scoped[0].Repository != "b" {
		t.Errorf("scoped claims %v, want only the globstar", scoped)
	}
}
