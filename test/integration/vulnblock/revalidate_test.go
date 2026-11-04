//go:build integration

package vulnblock

import (
	"net/http"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/npmstub"
)

// R-VULN-015, R-ARCH-027: a cached tarball whose package document is expired is served only after the document was revalidated,
// and what the lookup of the item read before that (the item, its cache entry, the verdict of the scanning) is read again after
// it. A finding that blocks the version while the conditional request to the upstream runs refuses the download, on the remote
// repository's own route and on the tier of a virtual repository alike.
func TestR_VULN_015_AVersionBlockedDuringARevalidationIsRefused(t *testing.T) {
	e := newEnv(t, options{})
	r := e.replica("web-1")
	if a := e.do(r, "GET", "/api/npm/"+rRemote+"/"+e.pkg, e.tokReader); a.Status != 200 {
		t.Fatalf("document: %d %s", a.Status, a.Body)
	}
	if a := e.do(r, "GET", tgzPath(rRemote, e.pkg, e.ver), e.tokReader); a.Status != 200 {
		t.Fatalf("fill: %d %.200s", a.Status, a.Body)
	}
	e.requireLinked(rRemote, e.pkg, e.ver)
	id := e.finding(rRemote, "npm", e.pkg, e.ver, malA, "malicious", "dismissed")
	t.Cleanup(func() { _ = e.stub.SetMode(npmstub.Mode{}) })
	for name, path := range map[string]string{
		"the remote repository": tgzPath(rRemote, e.pkg, e.ver),
		"the virtual tier":      tgzPath(rVirtual, e.pkg, e.ver),
	} {
		t.Run(name, func(t *testing.T) {
			e.setStatus(id, "dismissed")
			e.st.Exec(`UPDATE remote_cache_entries c SET expires_at = now() - interval '1 minute' FROM repositories r
				WHERE r.id = c.repo_id AND r.key = $1 AND c.kind = 'metadata'`, rRemote)
			if err := e.stub.SetMode(npmstub.Mode{DelayMS: map[string]int{npmstub.KindDocument: 1500}}); err != nil {
				t.Fatal(err)
			}
			done := make(chan int, 1)
			go func() { done <- e.do(r, "GET", path, e.tokReader).Status }()
			time.Sleep(500 * time.Millisecond) // the gate waits for the upstream's answer
			e.setStatus(id, "needs_triage")
			select {
			case got := <-done:
				if got != http.StatusForbidden {
					t.Errorf("a version blocked during the revalidation: %d, want 403", got)
				}
			case <-time.After(30 * time.Second):
				t.Fatal("no answer")
			}
		})
	}
}
