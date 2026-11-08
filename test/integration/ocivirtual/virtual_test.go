//go:build integration

package ocivirtual

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// local pushes an image to docker-local itself, as the owner of the image does before others pull it through a virtual repository.
func (e *venv) local(name, tag string, im image) {
	e.t.Helper()
	e.push(rLocal, name, tag, im)
}

// The image of the stub's app:1: its digest as the first stub serves it.
func (e *venv) stubDigest(name, tag string) string {
	e.t.Helper()
	d, ok := e.stubA.Digest(name, tag)
	if !ok {
		e.t.Fatalf("the stub has no %s:%s", name, tag)
	}
	return d
}

func TestR_OCI_027_ALocalMembersTagIsPulledBeforeAnUpstreamsAndNamesTheMember(t *testing.T) {
	e := newEnv(t, options{})
	mine := newImage("mine", 1)
	e.local("app", "1", mine)
	e.stubA.Reset()

	for _, repo := range []string{rVirtual, rReverse} {
		a := e.pull(repo, "app", "1")
		if got := a.Header.Get("Docker-Content-Digest"); got != mine.digest() {
			t.Errorf("%s: pulled %s, want the local image %s", repo, got, mine.digest())
		}
		if got := a.Header.Get("Trove-Resolved-From"); got != rLocal {
			t.Errorf("%s: Trove-Resolved-From = %q, want %s (local members first, whatever the member order)", repo, got, rLocal)
		}
	}
	if n := e.stubA.Counts().ManifestGets("app", ""); n != 0 {
		t.Errorf("the stub saw %d manifest requests for app although a local member has the tag", n)
	}
	for _, m := range []string{"HEAD", "GET"} {
		if a := e.do(e.reader, "pull", m, rVirtual, "app/manifests/1", nil); a.Status != http.StatusOK || a.Header.Get("Trove-Resolved-From") != rLocal {
			t.Errorf("%s: %d from %q", m, a.Status, a.Header.Get("Trove-Resolved-From"))
		}
	}
}

// Mechanism of R-OCI-027, not a proof of its criteria: it checks the referrers union or a tag only an upstream holds; the
// criteria are the local-first pull, the claim, the tag union and the member order.
func TestATagOnlyAnUpstreamHasIsPulledAndCachedByTheRemoteMember(t *testing.T) {
	e := newEnv(t, options{})

	a := e.pull(rVirtual, "acme/tool", "1")
	if got := a.Header.Get("Trove-Resolved-From"); got != rHubA {
		t.Errorf("Trove-Resolved-From = %q, want %s", got, rHubA)
	}
	if got, want := a.Header.Get("Docker-Content-Digest"), e.stubDigest("acme/tool", "1"); got != want {
		t.Errorf("digest %s, want the upstream's %s", got, want)
	}
	if n := e.items(rLocal, "acme/tool/"); n != 0 {
		t.Errorf("the virtual repository wrote %d items into its local member", n)
	}
	if e.items(rHubA, "acme/tool/_manifests/") == 0 || e.items(rHubA, "acme/tool/_blobs/") == 0 {
		t.Error("the remote member's cache holds neither the manifest nor the blobs")
	}
	// A second pull is answered from the member's cache: the blobs and the manifest by digest come from the second tier.
	gets := e.stubA.Counts().ManifestGets("acme/tool", "")
	for _, d := range manifestBlobs(t, a.Body) {
		if n := e.stubA.Counts().BlobFetches(d); n != 1 {
			t.Errorf("blob %s was fetched %d times, want once", d, n)
		}
	}
	if b := e.get(rVirtual, "acme/tool/manifests/"+a.Header.Get("Docker-Content-Digest")); b.Status != http.StatusOK || b.Header.Get("Trove-Resolved-From") != rHubA {
		t.Errorf("the manifest by digest: %d from %q", b.Status, b.Header.Get("Trove-Resolved-From"))
	}
	if n := e.stubA.Counts().ManifestGets("acme/tool", ""); n != gets {
		t.Errorf("a pull by digest of a cached manifest asked the upstream again: %d requests after %d", n, gets)
	}
}

func TestR_OCI_027_AClaimKeepsARemoteMemberFromServingAClaimedName(t *testing.T) {
	e := newEnv(t, options{claim: "acme/*"})
	a := e.get(rVirtual, "acme/tool/manifests/1")
	if a.Status != http.StatusNotFound || !strings.Contains(string(a.Body), "MANIFEST_UNKNOWN") {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if n := e.stubA.Counts().RepositoryRequests("acme/tool"); n != 0 {
		t.Errorf("the stub recorded %d requests for a name claimed for a local member", n)
	}
	// A name the claim does not cover is served from the upstream.
	if b := e.get(rVirtual, "app/manifests/1"); b.Status != http.StatusOK || b.Header.Get("Trove-Resolved-From") != rHubA {
		t.Errorf("app:1: %d from %q", b.Status, b.Header.Get("Trove-Resolved-From"))
	}
}

func TestR_OCI_027_TheTagsListIsTheUnionOfTheMembersTagsPagedLikeADirectList(t *testing.T) {
	e := newEnv(t, options{})
	e.local("app", "2", newImage("two", 1))
	e.local("app", "latest", newImage("latest", 1))
	// The stub holds app:1 only.
	list := func(query string) (tags []string, link string) {
		a := e.get(rVirtual, "app/tags/list"+query)
		if a.Status != http.StatusOK {
			t.Fatalf("tags list%s: %d %s", query, a.Status, a.Body)
		}
		var doc struct {
			Name string   `json:"name"`
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(a.Body, &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Name != testenv.DefaultProject+"/"+rVirtual+"/app" {
			t.Errorf("name = %q", doc.Name)
		}
		return doc.Tags, a.Header.Get("Link")
	}
	tags, link := list("")
	if strings.Join(tags, ",") != "1,2,latest" || link != "" {
		t.Fatalf("tags %v, Link %q: want the union of docker-local's 2 and latest and the stub's 1, sorted", tags, link)
	}
	tags, link = list("?n=2")
	if strings.Join(tags, ",") != "1,2" || !strings.Contains(link, "last=2") {
		t.Fatalf("first page %v, Link %q", tags, link)
	}
	tags, link = list("?n=2&last=2")
	if strings.Join(tags, ",") != "latest" || link != "" {
		t.Fatalf("second page %v, Link %q", tags, link)
	}
	// A tag two members hold is listed once.
	e.local("app", "1", newImage("mine", 1))
	if tags, _ = list(""); strings.Join(tags, ",") != "1,2,latest" {
		t.Fatalf("tags after the local app:1: %v", tags)
	}
	if a := e.get(rVirtual, "nothing/tags/list"); a.Status != http.StatusNotFound {
		t.Errorf("an unknown name: %d %s", a.Status, a.Body)
	}
}

func TestR_OCI_027_ARemoteMemberBeforeAnotherIsAskedFirstEvenWhenTheLaterOneHasACachedCopy(t *testing.T) {
	e := newEnv(t, options{})
	// hub-b serves another image for app:1 and has it cached.
	viaB := e.pull(rHubB, "app", "1")
	if viaB.Header.Get("Docker-Content-Digest") == e.stubDigest("app", "1") {
		t.Fatal("the two stubs serve the same app:1")
	}
	e.stubA.Reset()
	e.stubB.Reset()

	a := e.get(rTwo, "app/manifests/1")
	if a.Status != http.StatusOK || a.Header.Get("Trove-Resolved-From") != rHubA {
		t.Fatalf("%d from %q: the first remote member in member order answers", a.Status, a.Header.Get("Trove-Resolved-From"))
	}
	if got := a.Header.Get("Docker-Content-Digest"); got != e.stubDigest("app", "1") {
		t.Errorf("digest %s, want the first member's %s", got, e.stubDigest("app", "1"))
	}
	if n := e.stubA.Counts().ManifestGets("app", "1"); n != 1 {
		t.Errorf("the first member's upstream saw %d requests, want one (a stored tag of a later member is not preferred over it)", n)
	}
}

func TestR_OCI_027_ADigestIsServedFromACacheBeforeAnUpstreamIsAsked(t *testing.T) {
	e := newEnv(t, options{})
	// hub-b caches acme/tool:1; hub-a has not seen it.
	viaB := e.pull(rHubB, "acme/tool", "1")
	digest := viaB.Header.Get("Docker-Content-Digest")
	e.stubA.Reset()

	a := e.get(rTwo, "acme/tool/manifests/"+digest)
	if a.Status != http.StatusOK || a.Header.Get("Trove-Resolved-From") != rHubB {
		t.Fatalf("%d from %q: the cache of the later member answers before the earlier member's upstream is asked", a.Status, a.Header.Get("Trove-Resolved-From"))
	}
	for _, d := range manifestBlobs(t, a.Body) {
		if b := e.get(rTwo, "acme/tool/blobs/"+d); b.Status != http.StatusOK || b.Header.Get("Trove-Resolved-From") != rHubB {
			t.Errorf("blob %s: %d from %q", d, b.Status, b.Header.Get("Trove-Resolved-From"))
		}
	}
	if n := e.stubA.Counts().Total(); n != 0 {
		t.Errorf("the first member's upstream saw %d requests for content a cache had", n)
	}
	// A digest nobody has is unknown, and only then are the upstreams asked.
	zero := "sha256:" + strings.Repeat("0", 64)
	if b := e.get(rTwo, "acme/tool/manifests/"+zero); b.Status != http.StatusNotFound || !strings.Contains(string(b.Body), "MANIFEST_UNKNOWN") {
		t.Errorf("an unknown digest: %d %s", b.Status, b.Body)
	}
}

func TestR_OCI_028_AChunkedPushThroughAVirtualRepositoryIsStoredInItsDeployTarget(t *testing.T) {
	e := newEnv(t, options{})
	im := newImage("pushed", 2)

	a := e.do(e.writer, "pull,push", http.MethodPost, rVirtual, "app2/blobs/uploads/", nil)
	if a.Status != http.StatusAccepted {
		t.Fatalf("start: %d %s", a.Status, a.Body)
	}
	// The session and its Location name the repository the client addressed (R-OCI-011), and the session records both.
	if rest := e.relative(a.Header.Get("Location"), rVirtual); !strings.HasPrefix(rest, "app2/blobs/uploads/") {
		t.Errorf("Location %q", a.Header.Get("Location"))
	}
	var addressed, target string
	e.st.Query(`SELECT a.key, t.key FROM upload_sessions s JOIN repositories a ON a.id = s.addressed_repo_id JOIN repositories t ON t.id = s.repo_id WHERE s.state = 'open'`,
		func(rows pgx.Rows) error { return rows.Scan(&addressed, &target) })
	if addressed != rVirtual || target != rLocal {
		t.Errorf("the session records addressed %q and target %q, want %q and %q", addressed, target, rVirtual, rLocal)
	}
	// Cancelling a session through the virtual repository acts on the session's target.
	if c := e.do(e.writer, "pull,push", http.MethodDelete, rVirtual, e.relative(a.Header.Get("Location"), rVirtual), nil); c.Status != http.StatusNoContent {
		t.Fatalf("cancel: %d %s", c.Status, c.Body)
	}
	if c := e.do(e.writer, "pull,push", http.MethodGet, rVirtual, e.relative(a.Header.Get("Location"), rVirtual), nil); c.Status != http.StatusNotFound {
		t.Fatalf("a cancelled session: %d %s", c.Status, c.Body)
	}

	if up := e.push(rVirtual, "app2", "2", im); len(up) != 3 {
		t.Fatalf("uploaded %d blobs, want the config and both layers", len(up))
	}
	m := e.putManifest(rVirtual, "app2", "2", im.manifest)
	if loc := m.Header.Get("Location"); !strings.Contains(loc, "/v2/"+testenv.DefaultProject+"/"+rVirtual+"/app2/manifests/") {
		t.Errorf("the manifest's Location %q names another repository than the one addressed", loc)
	}
	if e.items(rLocal, "app2/_manifests/") != 1 || e.items(rLocal, "app2/_blobs/") != 3 || e.items(rLocal, "app2/_tags/") != 1 {
		t.Errorf("docker-local holds %d manifests, %d blobs, %d tags of app2", e.items(rLocal, "app2/_manifests/"), e.items(rLocal, "app2/_blobs/"), e.items(rLocal, "app2/_tags/"))
	}
	if n := e.items(rVirtual, ""); n != 0 {
		t.Errorf("the virtual repository holds %d items itself", n)
	}
	for _, repo := range []string{rLocal, rVirtual} {
		if got := e.pull(repo, "app2", "2").Header.Get("Docker-Content-Digest"); got != im.digest() {
			t.Errorf("%s: pulled %s, want %s", repo, got, im.digest())
		}
	}
}

func TestR_OCI_028_ALayerARemoteMemberCachedIsLinkedIntoTheTargetAndNotUploadedAgain(t *testing.T) {
	e := newEnv(t, options{})
	// hub-a caches app:1; an image of the name app that shares its layers is pushed through the virtual repository.
	cached := e.pull(rVirtual, "app", "1")
	shared := manifestBlobs(t, cached.Body)
	for _, d := range shared {
		if e.items(rLocal, "app/_blobs/"+d) != 0 {
			t.Fatalf("docker-local already holds %s", d)
		}
	}
	var cfg []byte
	var layers [][]byte
	for _, d := range shared {
		b := e.get(rVirtual, "app/blobs/"+d)
		if b.Status != http.StatusOK {
			t.Fatalf("blob %s: %d", d, b.Status)
		}
		if cfg == nil {
			cfg = b.Body
		} else {
			layers = append(layers, b.Body)
		}
	}
	extra := []byte(strings.Repeat("a new layer ", 400))
	im := image{config: cfg, layers: append(layers, extra)}
	im.manifest = manifestOf(im.config, im.layers)

	uploaded := e.push(rVirtual, "app", "2", im)
	if len(uploaded) != 1 || uploaded[0] != digestOf(extra) {
		t.Fatalf("uploaded %v: only the new layer is sent, the HEAD of the others is answered 200 by the member that holds them", uploaded)
	}
	for _, d := range append(shared, digestOf(extra)) {
		if b := e.get(rLocal, "app/blobs/"+d); b.Status != http.StatusOK {
			t.Errorf("docker-local: blob %s: %d %s (the push linked the shared layers into the target)", d, b.Status, b.Body)
		}
	}
	if got := e.pull(rLocal, "app", "2").Header.Get("Docker-Content-Digest"); got != im.digest() {
		t.Errorf("docker-local app:2 = %s, want %s", got, im.digest())
	}
}

// Mechanism of R-OCI-028, not a proof of its criteria: it checks the shadowing decision, a missing blob or the mount; the
// criteria are the push to the target, the cached-layer link, the missing target and the 405 delete.
func TestAFirstPushOfANameARemoteMemberServesNeedsTheShadowingDecision(t *testing.T) {
	e := newEnv(t, options{plain: true})
	im := newImage("shadow", 1)
	for _, b := range append([][]byte{im.config}, im.layers...) {
		e.uploadBlob(rVirtual, "app", b)
	}
	// hub-a serves app, and a first push of the name to the target of the virtual repository that contains hub-a replaces it for the clients.
	a := e.putManifest(rVirtual, "app", "9", im.manifest)
	if a.Status != http.StatusForbidden || !strings.Contains(string(a.Body), "remote repository already serves") && !strings.Contains(string(a.Body), "would replace a package that a remote repository serves") {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if n := e.items(rLocal, "app/_manifests/"); n != 0 {
		t.Errorf("docker-local holds %d manifests of app", n)
	}
}

// Mechanism of R-OCI-028, not a proof of its criteria: it checks the shadowing decision, a missing blob or the mount; the
// criteria are the push to the target, the cached-layer link, the missing target and the 405 delete.
func TestALayerNobodyHoldsIsStillAMissingBlob(t *testing.T) {
	e := newEnv(t, options{})
	im := newImage("unpushed", 1)
	a := e.putManifest(rVirtual, "app4", "1", im.manifest)
	if a.Status != http.StatusBadRequest || !strings.Contains(string(a.Body), "MANIFEST_BLOB_UNKNOWN") {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
}

// Mechanism of R-OCI-028, not a proof of its criteria: it checks the shadowing decision, a missing blob or the mount; the
// criteria are the push to the target, the cached-layer link, the missing target and the 405 delete.
func TestAMountFromTheVirtualRepositoryLinksTheBlobOfAMember(t *testing.T) {
	e := newEnv(t, options{})
	cached := e.pull(rVirtual, "app", "1")
	d := manifestBlobs(t, cached.Body)[0]
	from := testenv.DefaultProject + "/" + rVirtual + "/app"
	a := e.st.Do(e.rep, http.MethodPost, "/v2/"+testenv.DefaultProject+"/"+rVirtual+"/app5/blobs/uploads/?mount="+d+"&from="+from,
		e.mountToken(rVirtual, "app5", from), nil)
	if a.Status != http.StatusCreated {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if loc := a.Header.Get("Location"); !strings.Contains(loc, "/v2/"+testenv.DefaultProject+"/"+rVirtual+"/app5/blobs/"+d) {
		t.Errorf("Location %q", loc)
	}
	if e.items(rLocal, "app5/_blobs/"+d) != 1 {
		t.Error("docker-local holds no blob of app5")
	}
}

func TestR_OCI_028_WithoutADeployTargetTheFirstUploadIsRefused(t *testing.T) {
	e := newEnv(t, options{})
	// The decision point decides a write to a virtual repository on its deployment target (R-AUTHZ-009 rule 5): without one nothing may be
	// written, and the refusal comes before any handler, so it is the 403 of every write and not the handler's 405 (Q-OCI-166).
	direct := func(method, path string, body []byte, hdr ...string) testenv.Answer {
		return e.st.Do(e.rep, method, "/v2/"+testenv.DefaultProject+"/"+rNoTarget+"/"+path, e.writer, body, hdr...)
	}
	a := direct(http.MethodPost, "app/blobs/uploads/", nil)
	if a.Status != http.StatusForbidden || !strings.Contains(string(a.Body), "DENIED") || !strings.Contains(string(a.Body), rNoTarget) {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	im := newImage("none", 1)
	if b := direct(http.MethodPut, "app/manifests/3", im.manifest, "Content-Type", ctManifest); b.Status != http.StatusForbidden {
		t.Fatalf("manifest: %d %s", b.Status, b.Body)
	}
	// The token service grants pull and withholds push, so a client with a registry token is refused before the handler.
	if b := e.do(e.writer, "pull,push", http.MethodPost, rNoTarget, "app/blobs/uploads/", nil); b.Status != http.StatusUnauthorized {
		t.Fatalf("with a registry token: %d %s", b.Status, b.Body)
	}
	// Reading through it still works.
	if b := e.get(rNoTarget, "app/manifests/1"); b.Status != http.StatusOK {
		t.Fatalf("pull: %d %s", b.Status, b.Body)
	}
}

func TestR_OCI_028_ADeleteThroughAVirtualRepositoryAnswers405AndDeletesNothing(t *testing.T) {
	e := newEnv(t, options{})
	im := newImage("keep", 1)
	e.local("keep", "1", im)
	for _, path := range []string{"keep/manifests/1", "keep/manifests/" + im.digest(), "keep/blobs/" + digestOf(im.layers[0])} {
		a := e.st.Do(e.rep, http.MethodDelete, "/v2/"+testenv.DefaultProject+"/"+rVirtual+"/"+path, e.writer, nil)
		if a.Status != http.StatusMethodNotAllowed || !strings.Contains(string(a.Body), "UNSUPPORTED") {
			t.Errorf("DELETE %s: %d %s", path, a.Status, a.Body)
		}
	}
	if got := e.pull(rLocal, "keep", "1").Header.Get("Docker-Content-Digest"); got != im.digest() {
		t.Errorf("the image is gone: %s", got)
	}
}
