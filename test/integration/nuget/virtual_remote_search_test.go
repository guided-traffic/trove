//go:build integration

package nuget

import (
	"slices"
	"testing"
)

// TestR_REPO_005_TheCacheSearchOfARemoteMemberAppliesTheVirtualRepositorysPatterns: a remote member
// that answers from its cache (forwardSearch false) scans with its own and the virtual repository's
// patterns, as a local member does (Q-NUGET-77), so the merged count holds no ID the virtual
// repository's exclude keeps out, also beyond the page the merge sees (take=1 here). Without it the
// member reported every cached match and the merge could subtract only the ones in its window
// (H-346).
func TestR_REPO_005_TheCacheSearchOfARemoteMemberAppliesTheVirtualRepositorysPatterns(t *testing.T) {
	const pv = "nuget-fpv" // virtual over nuget-noforward, excludes Acme.Hidden*
	e := newForwardEnv(t)
	e.st.Repository(pv, "virtual", "nuget", `{"excludePatterns":["Acme.Hidden*"]}`)
	e.st.Exec(`INSERT INTO virtual_members (virtual_id, position, member_id)
		SELECT v.id, 0, m.id FROM repositories v, repositories m WHERE v.key = $1 AND m.key = $2`, pv, fNoForward)
	for _, id := range []string{"Acme.Able", "Acme.Hidden.One", "Acme.Hidden.Two", "Acme.Hidden.Three"} {
		insert(e.st, fNoForward, seedPackage{display: id, title: id, description: "cached " + id, versions: vs("1.0.0")})
	}
	a := e.start(1)[0]
	before := e.searches.total()

	for _, take := range []string{"1", "20"} {
		r := e.search(t, a, pv, q("acme")+"&take="+take)
		if !slices.Equal(r.ids(), []string{"Acme.Able"}) || r.TotalHits != 1 {
			t.Errorf("take=%s through %s: %v, totalHits %d, want Acme.Able and 1", take, pv, r.ids(), r.TotalHits)
		}
	}
	// The member on its own has no pattern and lists all four.
	if r := e.search(t, a, fNoForward, q("acme")+"&take=1"); r.TotalHits != 4 {
		t.Errorf("%s on its own: totalHits %d, want 4", fNoForward, r.TotalHits)
	}
	if got := e.searches.total(); got != before {
		t.Errorf("a remote with forwardSearch false asked the upstream (%d searches)", got-before)
	}
}
