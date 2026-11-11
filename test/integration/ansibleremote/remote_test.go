//go:build integration

package ansibleremote

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/ansiblestub"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func list(v any) []any { l, _ := v.([]any); return l }
func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func eventually(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	testenv.WaitFor(t, what, within, ok)
}

func TestR_ANS_016_TheRootDocumentFollowsTheApiRootOfTheUpstream(t *testing.T) {
	e := newEnv(t, options{})
	r := e.start(1)[0]

	repo := e.fresh()
	a := e.get(r, repo, "")
	if a.Status != http.StatusOK {
		t.Fatalf("root: %d %s", a.Status, a.Body)
	}
	av := obj(a.JSON(t)["available_versions"])
	if av["v3"] != "v3/" || av["v1"] != "v1/" {
		t.Fatalf("available_versions = %v", av)
	}
	e.get(r, repo, "")
	if got := e.requests("/api/"); got != 1 {
		t.Errorf("the stub saw %d requests for the API root of two requests, want 1", got)
	}

	// An upstream without v1 is a remote without the role API: the root does not list it and the role routes answer 404 in the v1 shape.
	if err := e.stub.SetMode(ansiblestub.Mode{NoV1: true}); err != nil {
		t.Fatal(err)
	}
	hub := e.fresh()
	av = obj(e.get(r, hub, "").JSON(t)["available_versions"])
	if _, ok := av["v1"]; ok || av["v3"] != "v3/" {
		t.Errorf("a remote of an upstream without v1 lists %v", av)
	}
	role := e.get(r, hub, "v1/roles/?owner__username=geerlingguy&name=pip")
	if role.Status != http.StatusNotFound || role.JSON(t)["default"] == nil {
		t.Errorf("role lookup without v1: %d %s", role.Status, role.Body)
	}
	if err := e.stub.SetMode(ansiblestub.Mode{}); err != nil {
		t.Fatal(err)
	}

	// An upstream URL that answers something else than an API root is reported as an unusable upstream.
	bad := e.get(r, rNotGalaxy, "")
	if bad.Status != http.StatusBadGateway {
		t.Fatalf("a URL that is no API root: %d %s", bad.Status, bad.Body)
	}
	if code := obj(list(bad.JSON(t)["errors"])[0])["code"]; code != "upstream_unavailable" {
		t.Errorf("error code %v", code)
	}
}

func TestR_ANS_017_AListOf250VersionsIsStoredInPagesAndServedWithTheClientsLinks(t *testing.T) {
	e := newEnv(t, options{})
	r := e.start(1)[0]
	repo := e.fresh()

	walk := func() (versions []string, pages int) {
		next := "v3/collections/many/versions/versions/?limit=100"
		for next != "" {
			a := e.get(r, repo, next)
			if a.Status != http.StatusOK {
				t.Fatalf("%s: %d %s", next, a.Status, a.Body)
			}
			doc := a.JSON(t)
			if got := obj(doc["meta"])["count"]; got != float64(250) {
				t.Fatalf("meta.count = %v", got)
			}
			for _, d := range list(doc["data"]) {
				versions = append(versions, obj(d)["version"].(string))
			}
			pages++
			next = ""
			if n, ok := obj(doc["links"])["next"].(string); ok {
				next = e.follow(repo, n)
			}
		}
		return versions, pages
	}
	versions, pages := walk()
	if pages != 3 || len(versions) != 250 || versions[0] != "1.249.0" || versions[249] != "1.0.0" {
		t.Fatalf("%d pages, %d versions, first %q", pages, len(versions), versions[0])
	}
	path := "/api/v3/collections/many/versions/versions/"
	if got := e.stub.Counts().Prefixed(path, http.MethodGet); got != 3 {
		t.Errorf("the stub served %d list pages for 250 versions, want 3", got)
	}
	// A second install, and a window across the border of two stored pages, make no request to the stub.
	walk()
	a := e.get(r, repo, "v3/collections/many/versions/versions/?limit=10&offset=95")
	doc := a.JSON(t)
	data := list(doc["data"])
	if len(data) != 10 || obj(data[0])["version"] != "1.154.0" || obj(data[9])["version"] != "1.145.0" {
		t.Errorf("window 95..104: %d entries, first %v", len(data), obj(data[0])["version"])
	}
	if got := e.stub.Counts().Prefixed(path, http.MethodGet); got != 3 {
		t.Errorf("the stub served %d list pages after the second install, want 3", got)
	}
	// An offset beyond the count is an empty page.
	if empty := list(e.get(r, repo, "v3/collections/many/versions/versions/?limit=10&offset=999").JSON(t)["data"]); len(empty) != 0 {
		t.Errorf("offset 999: %d entries", len(empty))
	}
}

func TestR_ANS_017_TheDocumentsCarryTheRepositorysAddressesAndTheStoredBytesAreTheUpstreams(t *testing.T) {
	e := newEnv(t, options{})
	r := e.start(1)[0]
	repo := e.fresh()

	col := e.get(r, repo, "v3/collections/acme/tools/")
	if col.Status != http.StatusOK {
		t.Fatalf("collection: %d %s", col.Status, col.Body)
	}
	cd := col.JSON(t)
	if hv := obj(cd["highest_version"])["version"]; hv != "2.0.0" || !strings.HasPrefix(cd["versions_url"].(string), baseURL+base(repo)+"v3/collections/acme/tools/versions/") {
		t.Errorf("collection document: %v", cd)
	}

	ver := e.get(r, repo, "v3/collections/acme/tools/versions/2.0.0/")
	if ver.Status != http.StatusOK {
		t.Fatalf("version: %d %s", ver.Status, ver.Body)
	}
	etag := ver.Header.Get("ETag")
	vd := ver.JSON(t)
	wantSum, wantSize := e.digest("acme.tools", "2.0.0")
	art := obj(vd["artifact"])
	if vd["download_url"] != baseURL+base(repo)+"v3/artifacts/collections/acme-tools-2.0.0.tar.gz" ||
		art["sha256"] != wantSum || art["size"] != float64(wantSize) || vd["requires_ansible"] != ">=2.15.0" {
		t.Errorf("version document: %v", vd)
	}
	meta := obj(vd["metadata"])
	for _, k := range []string{"contents", "docs_blob", "manifest", "files"} {
		if _, ok := meta[k]; ok {
			t.Errorf("metadata.%s is served: the plugin listing is dropped", k)
		}
	}
	if obj(meta["dependencies"])["acme.base"] != ">=1.0.0" {
		t.Errorf("dependencies = %v", meta["dependencies"])
	}
	if strings.Contains(string(ver.Body), e.run.URL) {
		t.Errorf("the version document still names the upstream: %s", ver.Body)
	}

	// The stored document is the upstream's, byte for byte.
	upstream := e.direct(e.run.URL + "/api/v3/collections/acme/tools/versions/2.0.0/")
	got := e.st.Text(`SELECT encode(blob_sha256, 'hex') FROM remote_cache_entries WHERE repo_id = $1 AND path = $2 AND variant = 'doc'`,
		e.repoID(repo), "collections/acme/tools/2.0.0.json")
	if got != sum(upstream) {
		t.Errorf("the stored version document is %s, the upstream's is %s", got, sum(upstream))
	}

	// A conditional request is answered 304 without a request to the stub.
	before := e.stub.Counts().Total()
	if a := e.get(r, repo, "v3/collections/acme/tools/versions/2.0.0/", "If-None-Match", etag); a.Status != http.StatusNotModified {
		t.Errorf("If-None-Match: %d", a.Status)
	}
	if after := e.stub.Counts().Total(); after != before {
		t.Errorf("a 304 made %d requests to the stub", after-before)
	}

	// A signature passes unchanged.
	sig := list(e.get(r, repo, "v3/collections/acme/signed/versions/1.0.0/").JSON(t)["signatures"])
	if len(sig) != 1 || !strings.Contains(obj(sig[0])["signature"].(string), "opaque-signature-of-acme.signed-1.0.0") {
		t.Errorf("signatures = %v", sig)
	}

	// An unknown collection is a 404 in the Galaxy shape and a negative entry: the stub is asked once.
	for range 2 {
		a := e.get(r, repo, "v3/collections/no/such/")
		if a.Status != http.StatusNotFound || obj(list(a.JSON(t)["errors"])[0])["code"] != "not_found" {
			t.Fatalf("unknown collection: %d %s", a.Status, a.Body)
		}
	}
	if got := e.requests("/api/v3/collections/no/such/"); got != 1 {
		t.Errorf("the stub saw %d requests for an unknown collection, want 1", got)
	}
}

func TestR_ANS_018_AnArchiveIsFetchedOnceVerifiedAndServedFromTheCache(t *testing.T) {
	e := newEnv(t, options{valkey: true})
	reps := e.start(3)
	repo := e.fresh()
	wantSum, _ := e.digest("acme.tools", "2.0.0")
	const file = "v3/artifacts/collections/acme-tools-2.0.0.tar.gz"

	var wg sync.WaitGroup
	bodies := make([][]byte, 6)
	for i := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a := e.get(reps[i%3], repo, file)
			if a.Status != http.StatusOK {
				t.Errorf("client %d: %d %s", i, a.Status, a.Body)
				return
			}
			bodies[i] = a.Body
		}()
	}
	wg.Wait()
	for i, b := range bodies {
		if b != nil && sum(b) != wantSum {
			t.Errorf("client %d got %s, the stub's archive is %s", i, sum(b), wantSum)
		}
	}
	versionDoc := "/api/v3/collections/acme/tools/versions/2.0.0/"
	archive := "/api/v3/plugin/ansible/content/published/collections/artifacts/acme-tools-2.0.0.tar.gz"
	if v, a := e.requests(versionDoc), e.requests(archive); v != 1 || a != 1 {
		t.Errorf("six clients on three replicas made %d requests for the version document and %d for the archive, want 1 and 1", v, a)
	}
	id := e.repoID(repo)
	if got := e.st.Text(`SELECT encode(sha256, 'hex') FROM ansible_upstream_versions WHERE repo_id = $1 AND name = 'acme.tools' AND version = '2.0.0'`, id); got != wantSum {
		t.Errorf("the recorded digest is %q, want %s", got, wantSum)
	}
	if n := e.st.Number(`SELECT count(*) FROM items WHERE repo_id = $1 AND path = 'collections/acme/tools/acme-tools-2.0.0.tar.gz' AND trashed_at IS NULL`, id); n != 1 {
		t.Errorf("%d items for the archive, want 1", n)
	}
	// The row of the cached version holds its manifest for the package screens (Q-ANS-76); nothing else is written for a remote.
	if n := e.st.Number(`SELECT count(*) FROM ansible_collection_versions WHERE repo_id = $1 AND name = 'acme.tools' AND version = '2.0.0'`, id); n != 1 {
		t.Errorf("a remote repository wrote %d rows of ansible_collection_versions for the cached version, want 1", n)
	}
	if n := e.st.Number(`SELECT count(*) FROM ansible_collection_versions WHERE repo_id = $1`, id); n != 1 {
		t.Errorf("a remote repository wrote %d rows of ansible_collection_versions, want the one of the cached version", n)
	}
	if n := e.st.Number(`SELECT count(*) FROM ansible_tombstones WHERE repo_id = $1`, id); n != 0 {
		t.Errorf("a remote repository wrote %d tombstones", n)
	}

	// The cached archive is served without a request to the stub, and a range of it as a range.
	total := e.stub.Counts().Total()
	a := e.get(reps[1], repo, file, "Range", "bytes=0-9")
	if a.Status != http.StatusPartialContent || len(a.Body) != 10 || !bytes.HasPrefix(a.Body, []byte{0x1f, 0x8b}) {
		t.Errorf("range: %d, %d bytes", a.Status, len(a.Body))
	}
	if sum(e.get(reps[2], repo, file).Body) != wantSum {
		t.Error("the second read of the archive differs")
	}
	if after := e.stub.Counts().Total(); after != total {
		t.Errorf("reading the cached archive made %d requests to the stub", after-total)
	}

	// An archive of a version nobody has asked about is fetched with its document first, and a version the upstream does not have is 404.
	fresh := e.fresh()
	if a := e.get(reps[0], fresh, "v3/artifacts/collections/acme-base-1.2.0.tar.gz"); a.Status != http.StatusOK {
		t.Errorf("an archive without a stored document: %d %s", a.Status, a.Body)
	}
	if a := e.get(reps[0], fresh, "v3/artifacts/collections/acme-base-9.9.9.tar.gz"); a.Status != http.StatusNotFound {
		t.Errorf("an archive of a version the upstream does not have: %d", a.Status)
	}
}

func TestR_ANS_018_AnArchiveThatDoesNotMatchItsDocumentOrItsPathIsRefusedAndStoresNothing(t *testing.T) {
	e := newEnv(t, options{})
	r := e.start(1)[0]
	const file = "v3/artifacts/collections/acme-base-1.0.0.tar.gz"
	stored := func(repo string) int64 {
		return e.st.Number(`SELECT count(*) FROM items WHERE repo_id = $1 AND path LIKE 'collections/%.tar.gz' AND trashed_at IS NULL`, e.repoID(repo))
	}

	// The bytes are not the ones the document announced.
	wrong := e.fresh()
	if err := e.stub.SetMode(ansiblestub.Mode{WrongDigest: []string{ansiblestub.AllCollections}}); err != nil {
		t.Fatal(err)
	}
	a := e.get(r, wrong, file)
	if a.Status != http.StatusBadGateway || !strings.Contains(string(a.Body), "integrity") {
		t.Errorf("wrong digest: %d %s", a.Status, a.Body)
	}
	if n := stored(wrong); n != 0 {
		t.Errorf("%d archives stored after a digest mismatch", n)
	}

	// The digest matches and the archive declares another collection than the path names.
	swapped := e.fresh()
	if err := e.stub.SetMode(ansiblestub.Mode{WrongIdentity: []string{ansiblestub.AllCollections}}); err != nil {
		t.Fatal(err)
	}
	a = e.get(r, swapped, file)
	if a.Status != http.StatusBadGateway || !strings.Contains(string(a.Body), "identity_mismatch") {
		t.Errorf("wrong identity: %d %s", a.Status, a.Body)
	}
	if n := stored(swapped); n != 0 {
		t.Errorf("%d archives stored after an identity mismatch", n)
	}
	if err := e.stub.SetMode(ansiblestub.Mode{}); err != nil {
		t.Fatal(err)
	}

	// A repository that requires digests takes an archive that announces one.
	if a := e.get(r, rRequire, file); a.Status != http.StatusOK {
		t.Errorf("checksumPolicy require: %d %s", a.Status, a.Body)
	}
}

func TestR_ANS_018_TheFirstDigestOfACachedVersionStaysWhenTheUpstreamAnnouncesAnother(t *testing.T) {
	e := newEnv(t, options{})
	r := e.start(1)[0]
	repo := e.fresh()
	const (
		file = "v3/artifacts/collections/acme-base-1.0.0.tar.gz"
		doc  = "v3/collections/acme/base/versions/1.0.0/"
	)
	first, _ := e.digest("acme.base", "1.0.0")
	cached := e.get(r, repo, file)
	if cached.Status != http.StatusOK || sum(cached.Body) != first {
		t.Fatalf("first read: %d", cached.Status)
	}

	// The upstream republishes the version: another archive, announced with its own digest.
	if err := e.stub.SetMode(ansiblestub.Mode{DigestChanged: []string{"acme.base"}}); err != nil {
		t.Fatal(err)
	}
	e.expire(repo)
	before := e.requests("/api/v3/collections/acme/base/versions/1.0.0/")
	served := e.get(r, repo, doc)
	if served.Status != http.StatusOK {
		t.Fatalf("version document: %d %s", served.Status, served.Body)
	}
	if after := e.requests("/api/v3/collections/acme/base/versions/1.0.0/"); after != before+1 {
		t.Fatalf("the document was not revalidated: %d requests, had %d", after, before)
	}
	// The document the client checks the archive against still announces the digest of the cached bytes.
	if got := obj(served.JSON(t)["artifact"])["sha256"]; got != first {
		t.Errorf("the served document announces %v, the cached archive is %s", got, first)
	}
	if got := e.st.Text(`SELECT encode(sha256, 'hex') FROM ansible_upstream_versions WHERE repo_id = $1 AND name = 'acme.base' AND version = '1.0.0'`, e.repoID(repo)); got != first {
		t.Errorf("the recorded digest is %s, the first one was %s", got, first)
	}
	again := e.get(r, repo, file)
	if again.Status != http.StatusOK || sum(again.Body) != first {
		t.Errorf("the cached archive: %d %s", again.Status, sum(again.Body))
	}
	eventually(t, 15*time.Second, "the event proxy.upstream_digest_changed", func() bool {
		return e.st.Number(`SELECT count(*) FROM audit_event WHERE action = 'proxy.upstream_digest_changed'`) >= 1
	})
}

func TestR_ANS_017_ARemovedVersionLeavesTheListAndItsCachedArchiveStays(t *testing.T) {
	e := newEnv(t, options{})
	r := e.start(1)[0]
	repo := e.fresh()
	names := func() []string {
		var out []string
		for _, d := range list(e.get(r, repo, "v3/collections/acme/tools/versions/").JSON(t)["data"]) {
			out = append(out, obj(d)["version"].(string))
		}
		return out
	}
	if got := strings.Join(names(), " "); got != "2.1.0-rc.1 2.0.0 1.1.0 1.0.0" {
		t.Fatalf("versions = %s", got)
	}
	want, _ := e.digest("acme.tools", "1.0.0")
	if a := e.get(r, repo, "v3/artifacts/collections/acme-tools-1.0.0.tar.gz"); a.Status != http.StatusOK || sum(a.Body) != want {
		t.Fatalf("cache the archive: %d", a.Status)
	}

	if err := e.stub.DropVersion("acme.tools", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	e.expire(repo)
	if got := strings.Join(names(), " "); got != "2.1.0-rc.1 2.0.0 1.1.0" {
		t.Errorf("versions after the upstream dropped 1.0.0 = %s", got)
	}
	total := e.stub.Counts().Total()
	if a := e.get(r, repo, "v3/artifacts/collections/acme-tools-1.0.0.tar.gz"); a.Status != http.StatusOK || sum(a.Body) != want {
		t.Errorf("the cached archive of the dropped version: %d", a.Status)
	}
	if after := e.stub.Counts().Total(); after != total {
		t.Errorf("serving the cached archive made %d requests to the stub", after-total)
	}
}

func TestR_ANS_017_ARedirectingUpstreamIsFollowed(t *testing.T) {
	e := newEnv(t, options{})
	r := e.start(1)[0]
	repo := e.fresh()
	if err := e.stub.SetMode(ansiblestub.Mode{Redirect: true}); err != nil {
		t.Fatal(err)
	}
	want, _ := e.digest("acme.base", "1.2.0")
	if a := e.get(r, repo, "v3/collections/acme/base/versions/1.2.0/"); a.Status != http.StatusOK {
		t.Fatalf("version through a redirect: %d %s", a.Status, a.Body)
	}
	a := e.get(r, repo, "v3/artifacts/collections/acme-base-1.2.0.tar.gz")
	if a.Status != http.StatusOK || sum(a.Body) != want {
		t.Fatalf("archive through the signed address: %d", a.Status)
	}
	if got := e.stub.Counts().Prefixed("/api/pulp/content/published/", http.MethodGet); got != 1 {
		t.Errorf("the stub served %d signed downloads, want 1", got)
	}
}
