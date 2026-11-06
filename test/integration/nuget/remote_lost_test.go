//go:build integration

package nuget

import (
	"crypto/sha512"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/nugetstub"
)

// The objects of a remote's cached items are lost behind trove's back, as the object store of the dev profile lost its data
// (finding F-62): the rows stay. The remote fills every item again from its upstream, the service index it addresses the
// upstream by included, and while the upstream cannot serve them the answer is 503 with Retry-After, never a 500
// (storage.md R-STOR-027, ha-and-helm.md R-HA-024, Q-REPO-202, Q-REPO-203).

// cachedBlobs are the digests of the blobs every cache entry of a repository references, by path.
func (e *renv) cachedBlobs(repo string) map[string][]byte {
	e.t.Helper()
	out := map[string][]byte{}
	e.st.Query(`SELECT e.path, e.blob_sha256 FROM remote_cache_entries e JOIN repositories r ON r.id = e.repo_id WHERE r.key = $1`,
		func(rows pgx.Rows) error {
			var path string
			var d []byte
			if err := rows.Scan(&path, &d); err != nil {
				return err
			}
			out[path] = d
			return nil
		}, repo)
	return out
}

// loseEverything deletes the object of every cached item of a repository.
func (e *renv) loseEverything(repo string) map[string][]byte {
	e.t.Helper()
	blobs := e.cachedBlobs(repo)
	for _, d := range blobs {
		e.st.DeleteObject(e.t, d)
	}
	return blobs
}

func TestR_STOR_027_ARemoteWhoseObjectsAreAllMissingFillsItsNuGetItemsAgain(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{})
	rep := e.start(1)[0]
	pkg, _ := nugetstub.Default().Get(nugetstub.NewtonsoftID)
	sum := sha512.Sum512(nugetstub.Nupkg(pkg, pkg.Versions[1]))
	list := "v3/flatcontainer/newtonsoft.json/index.json"
	if a := e.get(rep, rRemote, nupkgPath("Newtonsoft.Json", "13.0.3")); a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if a := e.get(rep, rRemote, list); a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	blobs := e.loseEverything(rRemote)
	if len(blobs) < 3 {
		t.Fatalf("%d cache entries: the service index, the version list and the package were expected", len(blobs))
	}
	nupkgs, lists, index := e.stub.Counts().Nupkg(newtonsoft, "13.0.3"), e.stub.Counts().VersionLists(newtonsoft), e.stub.Counts().Requests("GET", "/v3/index.json")

	// The package, on the replica that filled it: fetched again once, with its bytes.
	a := e.get(rep, rRemote, nupkgPath("Newtonsoft.Json", "13.0.3"))
	if a.Status != 200 || sha512.Sum512(a.Body) != sum {
		t.Fatalf("package: %d %.300s", a.Status, a.Body)
	}
	if got := e.stub.Counts().Nupkg(newtonsoft, "13.0.3"); got != nupkgs+1 {
		t.Errorf("%d upstream requests for the package, want 1", got-nupkgs)
	}
	// The version list and the service index, on a replica that does not hold their bytes in memory.
	cold := e.start(1)[0]
	if b := e.get(cold, rRemote, list); b.Status != 200 || strings.Join(versionsOf(t, b), ",") != "13.0.1,13.0.3" {
		t.Fatalf("version list: %d %.300s", b.Status, b.Body)
	}
	if got := e.stub.Counts().VersionLists(newtonsoft); got != lists+1 {
		t.Errorf("%d upstream requests for the version list, want 1", got-lists)
	}
	if got := e.stub.Counts().Requests("GET", "/v3/index.json"); got != index+1 {
		t.Errorf("%d upstream requests for the service index, want 1", got-index)
	}
	for path, d := range blobs {
		if !e.st.ObjectExists(t, d) || e.blobStatus(d) != "ok" {
			t.Errorf("%s: the object was not written again (content_status %s)", path, e.blobStatus(d))
		}
	}
	// Everything is a hit again: with the upstream down, the package and the list are served from the cache.
	if err := e.stub.SetMode(nugetstub.Mode{Down: nugetstub.DownUnavailable}); err != nil {
		t.Fatal(err)
	}
	if a := e.get(e.start(1)[0], rRemote, nupkgPath("Newtonsoft.Json", "13.0.3")); a.Status != 200 || sha512.Sum512(a.Body) != sum {
		t.Fatalf("package from the cache: %d", a.Status)
	}
}

func TestR_STOR_027_ACachedNuGetPackageWhoseObjectIsMissingIs503WhileTheUpstreamIsDown(t *testing.T) {
	e := newRemoteEnv(t, remoteOptions{})
	rep := e.start(1)[0]
	pkg, _ := nugetstub.Default().Get(nugetstub.NewtonsoftID)
	sum := sha512.Sum512(nugetstub.Nupkg(pkg, pkg.Versions[1]))
	if a := e.get(rep, rRemote, nupkgPath("Newtonsoft.Json", "13.0.3")); a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	d := e.cachedBlobs(rRemote)["newtonsoft.json/13.0.3/newtonsoft.json.13.0.3.nupkg"]
	if d == nil {
		t.Fatalf("no cache entry of the package: %v", e.cachedBlobs(rRemote))
	}
	e.st.DeleteObject(t, d)
	if err := e.stub.SetMode(nugetstub.Mode{Down: nugetstub.DownUnavailable}); err != nil {
		t.Fatal(err)
	}
	for i, r := range []*testenv.Replica{rep, e.start(1)[0]} {
		a := e.get(r, rRemote, nupkgPath("Newtonsoft.Json", "13.0.3"))
		if a.Status != http.StatusServiceUnavailable || a.Header.Get("Retry-After") == "" || !strings.Contains(string(a.Body), "stored object") {
			t.Fatalf("request %d: %d Retry-After %q %.300s, want 503 naming the stored object", i+1, a.Status, a.Header.Get("Retry-After"), a.Body)
		}
	}
	if got := e.blobStatus(d); got != "missing" {
		t.Errorf("content_status %s, want missing: the first read marks it (R-HA-024)", got)
	}
	if err := e.stub.SetMode(nugetstub.Mode{}); err != nil {
		t.Fatal(err)
	}
	if a := e.get(rep, rRemote, nupkgPath("Newtonsoft.Json", "13.0.3")); a.Status != 200 || sha512.Sum512(a.Body) != sum {
		t.Fatalf("after the upstream returned: %d %.300s", a.Status, a.Body)
	}
	if !e.st.ObjectExists(t, d) || e.blobStatus(d) != "ok" {
		t.Fatal("the object was not written again")
	}
}

// blobStatus is the content_status of a blob.
func (e *renv) blobStatus(d []byte) string {
	e.t.Helper()
	return e.st.Text(`SELECT content_status FROM blobs WHERE sha256 = $1`, d)
}
