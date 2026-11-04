//go:build integration

package querybudget

import (
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// R-ARCH-027, R-REPO-025: the cache entry is the record of a cached artifact. An entry whose item is gone is the 404 the request
// path answered before the hit was read in one statement, not a miss that fetches the artifact again, and the same request is
// served again once the item is back: the lookup that asks for the entry only when it finds no item changes neither answer.
func TestR_ARCH_027_AnEntryWhoseItemIsGoneIsRefusedAndServedAgainWithIt(t *testing.T) {
	e := newEnv(t)
	r := e.st.Start(t, testenv.ReplicaOptions{Name: "web-1"})
	if a := e.do(r, "/api/npm/"+rNPMRem+"/"+e.pkg); a.Status != 200 {
		t.Fatalf("npm document: %d %s", a.Status, a.Body)
	}
	path := npmTarballPath(rNPMRem, e.pkg, e.ver)
	if a := e.do(r, path); a.Status != 200 {
		t.Fatalf("npm fill: %d %.200s", a.Status, a.Body)
	}
	upstream := e.npmStub.Counts().Tarballs(e.pkg)
	if upstream != 1 {
		t.Fatalf("the fill asked the upstream for %d tarballs, want 1", upstream)
	}
	trashed := e.st.Number(`WITH t AS (UPDATE items i SET trashed_at = now() FROM repositories r
		WHERE r.id = i.repo_id AND r.key = $1 AND i.kind = 'file' AND i.path LIKE '%.tgz' AND i.trashed_at IS NULL RETURNING 1)
		SELECT count(*) FROM t`, rNPMRem)
	if trashed != 1 {
		t.Fatalf("%d items of %s were trashed, want 1", trashed, rNPMRem)
	}
	if a := e.do(r, path); a.Status != http.StatusNotFound {
		t.Fatalf("a cache entry whose item is gone: %d %.200s, want 404", a.Status, a.Body)
	}
	if got := e.npmStub.Counts().Tarballs(e.pkg); got != upstream {
		t.Errorf("the upstream was asked for the tarball again (%d requests, %d before)", got, upstream)
	}
	e.st.Exec(`UPDATE items i SET trashed_at = NULL FROM repositories r WHERE r.id = i.repo_id AND r.key = $1 AND i.path LIKE '%.tgz'`, rNPMRem)
	if a := e.do(r, path); a.Status != http.StatusOK {
		t.Fatalf("the cached artifact with its item back: %d %.200s, want 200", a.Status, a.Body)
	}
}
