//go:build integration

package ociremote

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/ocistub"
)

// host is the Host header the stub counts a request of one of its names under.
func (e *renv) host(name string) string { return name + ":" + portOf(e.run.Addr) }

func portOf(addr string) string { return addr[strings.LastIndex(addr, ":")+1:] }

func TestR_OCI_021_ARealmOnACredentialHostGetsTheCredentialAndTheRegistryOnlyTheToken(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	if err := e.stub.SetMode(ocistub.Mode{Auth: ocistub.AuthBasic}); err != nil {
		t.Fatal(err)
	}
	e.pull(rep, rBasic, "library/alpine", "3.19")

	c := e.stub.Counts()
	if got := c.AuthSchemes(e.host(ocistub.LoopbackRealmHost)); strings.Join(got, ",") != "basic" {
		t.Errorf("the realm saw the credential schemes %v, want basic only", got)
	}
	// The registry is the origin of the repository, so its first request carries the credential, as the challenge needs it; after the
	// 401 it sees the token only, and the remembered challenge keeps every later request from being challenged again.
	registry := e.host(ocistub.LoopbackRegistryHost)
	if got := c.AuthSchemes(registry); strings.Join(got, ",") != "basic,bearer" {
		t.Errorf("the registry saw %v, want the credential once and then the token", got)
	}
	basic := 0
	for _, en := range c.Entries {
		if en.Host == registry && en.Auth == "basic" {
			basic += en.Count
		}
	}
	if basic != 1 {
		t.Errorf("the registry saw the credential %d times, want the first request only", basic)
	}
	if got := c.AuthSchemes(e.host(ocistub.LoopbackCDNHost)); strings.Join(got, ",") != "none" {
		t.Errorf("the CDN saw %v: no credential or token goes to a redirect target", got)
	}
	if c.CDNRequests("") == 0 {
		t.Error("no blob was fetched from the CDN: the redirect was not followed")
	}
}

func TestR_OCI_021_ARealmThatIsNoExtraHostIsRefusedAndGetsNothing(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	a := e.get(rep, rNoHost, "library/alpine/manifests/3.19")
	if a.Status != http.StatusBadGateway {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if !strings.Contains(string(a.Body), "UNAVAILABLE") {
		t.Errorf("answer: %s", a.Body)
	}
	if n := e.stub.Counts().TokenRequests(""); n != 0 {
		t.Errorf("the foreign realm got %d requests", n)
	}
}

func TestR_OCI_021_OnlyTheFirstRequestOfAReplicaIsChallengedAndATokenIsReusedWithinItsLifetime(t *testing.T) {
	e := newEnv(t, options{})
	reps := e.start(2)
	e.pull(reps[0], rAnon, "library/alpine", "3.19")
	tokens := e.stub.Counts().TokenRequests("")
	if tokens == 0 {
		t.Fatal("no token request")
	}
	if got := e.stub.Counts().WithStatus(http.StatusUnauthorized); got != 1 {
		t.Errorf("%d challenged requests for a pull of a whole image, want the first request only (the registry's challenge is remembered)", got)
	}
	// Another image of the same registry: its scope is another one, the token is asked for before the request, so no request is challenged.
	e.pull(reps[0], rAnon, "acme/tool", "1")
	if got := e.stub.Counts().WithStatus(http.StatusUnauthorized); got != 1 {
		t.Errorf("%d challenged requests after a second image, want the first one only", got)
	}
	// Within the token's lifetime a pull of another tag of the first image asks the realm for nothing.
	before := e.stub.Counts().TokenRequests("")
	e.pull(reps[0], rAnon, "library/alpine", "3")
	if got := e.stub.Counts().TokenRequests("") - before; got != 0 {
		t.Errorf("%d token requests for another tag of an image with a token", got)
	}
	// Another replica keeps no token of this one: for an image the cache does not hold yet it asks the realm.
	before = e.stub.Counts().TokenRequests("")
	e.pull(reps[1], rAnon, "acme/b", "1.0")
	if got := e.stub.Counts().TokenRequests("") - before; got == 0 {
		t.Error("a second replica sent no token request: tokens are shared between replicas")
	}
}

func TestR_OCI_022_ATagPullIsOneManifestRequestAfterTheHandshakeWithTheFourTypesInAccept(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	e.get(rep, rAnon, "library/alpine/manifests/3")
	e.get(rep, rAnon, "library/alpine/manifests/3")
	c := e.stub.Counts()
	if got := c.ManifestGets("library/alpine", "3"); got != 1 {
		t.Errorf("%d manifest GET requests for two pulls inside the period, want 1", got)
	}
	entries := c.ManifestEntries("library/alpine", "3", "GET")
	if len(entries) == 0 {
		t.Fatal("no manifest entry")
	}
	for _, en := range entries {
		got := strings.Join(en.Accept, ",")
		for _, mt := range []string{
			"application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json",
			"application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json",
		} {
			if !strings.Contains(got, mt) {
				t.Errorf("a manifest request carried Accept %q, which lacks %s", got, mt)
			}
		}
	}
}

func TestR_OCI_022_AManifestFetchedByTagIsServedByItsDigestWithoutARequest(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	a := e.get(rep, rAnon, "library/alpine/manifests/3")
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	digest := a.Header.Get("Docker-Content-Digest")
	want, _ := e.stub.Digest("library/alpine", "3")
	if digest != want {
		t.Fatalf("Docker-Content-Digest %q, want the stub's %q", digest, want)
	}
	if ct := a.Header.Get("Content-Type"); ct != "application/vnd.oci.image.index.v1+json" {
		t.Errorf("Content-Type %q: the stored type of the upstream's answer", ct)
	}
	before := e.stub.Counts().ManifestGets("library/alpine", digest)
	b := e.get(rep, rAnon, "library/alpine/manifests/"+digest)
	if b.Status != 200 || string(b.Body) != string(a.Body) || b.Header.Get("Docker-Content-Digest") != digest {
		t.Fatalf("%d %.200s", b.Status, b.Body)
	}
	if got := e.stub.Counts().ManifestGets("library/alpine", digest) - before; got != 0 {
		t.Errorf("a pull by the digest of a fetched tag sent %d upstream requests", got)
	}
	if e.manifestRows(rAnon, "library/alpine") != 1 {
		t.Errorf("manifest rows = %d, want the row of the fetched manifest", e.manifestRows(rAnon, "library/alpine"))
	}
	if e.itemCount(rAnon, "library/alpine/_manifests/") != 1 {
		t.Errorf("the alias item of the digest is missing")
	}
}

func TestR_OCI_022_AnExpiredTagIsRevalidatedByHeadAndOnlyAMovedTagCostsAGet(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	a := e.get(rep, rFast, "acme/a/manifests/latest")
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	first := a.Header.Get("Docker-Content-Digest")
	if got := e.stub.Counts().ManifestGets("acme/a", "latest"); got != 1 {
		t.Fatalf("%d GET requests for the first pull", got)
	}
	// After the period, with the tag unchanged: one HEAD and no GET.
	eventually(t, 15*time.Second, "the unchanged tag to be revalidated by HEAD", func() bool {
		b := e.get(rep, rFast, "acme/a/manifests/latest")
		return b.Status == 200 && e.stub.Counts().ManifestHeads("acme/a", "latest") == 1
	})
	if got := e.stub.Counts().ManifestGets("acme/a", "latest"); got != 1 {
		t.Errorf("%d GET requests after the revalidation of an unchanged tag, want 1", got)
	}
	// The tag moves: the next revalidation finds another digest and fetches the manifest.
	moved, err := e.stub.MoveTag("acme/a", "latest")
	if err != nil {
		t.Fatal(err)
	}
	eventually(t, 15*time.Second, "the moved tag to be fetched", func() bool {
		b := e.get(rep, rFast, "acme/a/manifests/latest")
		return b.Status == 200 && b.Header.Get("Docker-Content-Digest") == moved
	})
	if moved == first {
		t.Fatal("the stub did not move the tag")
	}
	if got := e.stub.Counts().ManifestGets("acme/a", "latest"); got != 2 {
		t.Errorf("%d GET requests after a moved tag, want 2", got)
	}
	// The revalidation request is the GET's own: same path, same Accept, no body.
	for _, en := range e.stub.Counts().ManifestEntries("acme/a", "latest", "HEAD") {
		if len(en.Accept) < 4 {
			t.Errorf("a revalidating HEAD carried Accept %v, want the four types of the GET", en.Accept)
		}
	}
}

func TestR_OCI_022_AManifestWhoseBytesAreNotTheirAnnouncedDigestIsNotStored(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	if err := e.stub.SetMode(ocistub.Mode{WrongDigest: []string{"acme/a:1.0"}}); err != nil {
		t.Fatal(err)
	}
	a := e.get(rep, rAnon, "acme/a/manifests/1.0")
	if a.Status != http.StatusBadGateway {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if n := e.cacheEntries(rAnon, "acme/a/"); n != 0 {
		t.Errorf("%d cache entries after a refused manifest", n)
	}
	if n := e.itemCount(rAnon, "acme/a/"); n != 0 {
		t.Errorf("%d items after a refused manifest", n)
	}
	if n := e.manifestRows(rAnon, "acme/a"); n != 0 {
		t.Errorf("%d manifest rows after a refused manifest", n)
	}
}

// Mechanism of R-OCI-022, not a proof of its criteria: it checks a request shape, a verification step or the memory of a miss;
// the criteria are the 300 s revalidation, the read by digest without a request and the mismatched digest.
func TestASchemaOneManifestIsNeverStored(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	if err := e.stub.SetMode(ocistub.Mode{Schema1: []string{"acme/a:1.0"}}); err != nil {
		t.Fatal(err)
	}
	a := e.get(rep, rAnon, "acme/a/manifests/1.0")
	if a.Status != http.StatusBadGateway || !strings.Contains(string(a.Body), "MANIFEST_INVALID") {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	if n := e.cacheEntries(rAnon, "acme/a/") + e.itemCount(rAnon, "acme/a/"); n != 0 {
		t.Errorf("%d entries or items after a schema 1 manifest", n)
	}
}

// Mechanism of R-OCI-022, not a proof of its criteria: it checks a request shape, a verification step or the memory of a miss;
// the criteria are the 300 s revalidation, the read by digest without a request and the mismatched digest.
func TestATagTheUpstreamDoesNotHaveIsRememberedForTheNotFoundPeriod(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	for range 2 {
		a := e.get(rep, rAnon, "library/alpine/manifests/nosuchtag")
		if a.Status != http.StatusNotFound || !strings.Contains(string(a.Body), "MANIFEST_UNKNOWN") {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	}
	if got := e.stub.Counts().ManifestGets("library/alpine", "nosuchtag"); got != 1 {
		t.Errorf("%d upstream requests for two pulls of a missing tag, want 1", got)
	}
}

// Mechanism of R-OCI-023, not a proof of its criteria: it checks a redirect, a response type or a HEAD; the criteria are one
// fetch per layer across three replicas and the CDN host receiving no credential.
func TestAMultiPlatformImageIsPulledThroughWithItsChildrenAndEveryLayerOnce(t *testing.T) {
	e := newEnv(t, options{})
	reps := e.start(1)
	e.pull(reps[0], rAnon, "library/alpine", "3")
	img, err := e.stub.Image("library/alpine", "3")
	if err != nil {
		t.Fatal(err)
	}
	c := e.stub.Counts()
	for _, d := range img.Blobs() {
		if got := c.BlobFetches(d); got != 1 {
			t.Errorf("blob %s fetched %d times", d, got)
		}
	}
	// A second pull of the whole image asks the upstream for no blob.
	e.pull(reps[0], rAnon, "library/alpine", "3")
	if v := e.stub.Counts().Violations(1, 1); len(v) != 0 {
		t.Errorf("a second pull fetched again: %v", v)
	}
	// The rows of the children are those of the format's tables.
	if got := e.manifestRows(rAnon, "library/alpine"); got < 3 {
		t.Errorf("%d manifest rows for an index and its children", got)
	}
	// A blob is content under the image name; its bytes verify against the digest in the path.
	if got := e.itemCount(rAnon, "library/alpine/_blobs/"); got != int64(len(img.Blobs())) {
		t.Errorf("%d blob items, want %d", got, len(img.Blobs()))
	}
}

func TestR_OCI_023_ABlobFromTheCDNIsFetchedWithoutACredentialAndABlobWithTheWrongBytesIsNotStored(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	img, err := e.stub.Image("acme/a", "1.0")
	if err != nil {
		t.Fatal(err)
	}
	// The CDN refuses a request that carries an Authorization header, so a fetched layer proves none was sent.
	a := e.get(rep, rAnon, "acme/a/manifests/1.0")
	if a.Status != 200 {
		t.Fatalf("%d", a.Status)
	}
	bad := img.Layers[0].Digest
	if err := e.stub.SetMode(ocistub.Mode{WrongBlob: []string{"acme/a@" + bad}}); err != nil {
		t.Fatal(err)
	}
	b := e.get(rep, rAnon, "acme/a/blobs/"+bad)
	if b.Status != http.StatusBadGateway {
		t.Fatalf("%d %.200s", b.Status, b.Body)
	}
	if n := e.itemCount(rAnon, "acme/a/_blobs/"); n != 0 {
		t.Errorf("%d blob items after a refused blob", n)
	}
	if err := e.stub.SetMode(ocistub.Mode{}); err != nil {
		t.Fatal(err)
	}
	if c := e.get(rep, rAnon, "acme/a/blobs/"+bad); c.Status != 200 {
		t.Fatalf("%d %.200s", c.Status, c.Body)
	}
	if e.stub.Counts().AuthSchemes(e.host(ocistub.LoopbackCDNHost))[0] != "none" {
		t.Error("the CDN saw a credential")
	}
}

// Mechanism of R-OCI-023, not a proof of its criteria: it checks a redirect, a response type or a HEAD; the criteria are one
// fetch per layer across three replicas and the CDN host receiving no credential.
func TestAHeadOfABlobIsAnsweredWithoutAFill(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	img, _ := e.stub.Image("acme/a", "1.0")
	d := img.Layers[0].Digest
	a := e.do(rep, http.MethodHead, rAnon, "acme/a/blobs/"+d)
	if a.Status != 200 {
		t.Fatalf("%d", a.Status)
	}
	if n := e.itemCount(rAnon, "acme/a/_blobs/"); n != 0 {
		t.Errorf("a HEAD stored %d blobs", n)
	}
	if got := e.stub.Counts().BlobFetches(d); got != 0 {
		t.Errorf("a HEAD fetched the blob %d times", got)
	}
}

func TestR_OCI_024_TheTagsListIsOneStoredDocumentAndAWindowBeyondItIsPassedThrough(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	type page struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	read := func(path string) (page, string) {
		a := e.get(rep, rAnon, path)
		if a.Status != 200 {
			t.Fatalf("%s: %d %s", path, a.Status, a.Body)
		}
		var p page
		if err := json.Unmarshal(a.Body, &p); err != nil {
			t.Fatal(err)
		}
		return p, a.Header.Get("Link")
	}
	p, link := read(`acme/a/tags/list?n=3`)
	if strings.Join(p.Tags, ",") != "1.0,1.1,1.2" || !strings.Contains(link, "last=1.2") || !strings.Contains(link, "/v2/it/oci-anon/acme/a/tags/list") {
		t.Errorf("first page = %v, link %q", p.Tags, link)
	}
	if p.Name != "it/oci-anon/acme/a" && !strings.HasSuffix(p.Name, "acme/a") {
		t.Errorf("name = %q", p.Name)
	}
	read(`acme/a/tags/list?n=2&last=1.1`)
	if got := tagListGets(e.stub.Counts(), "acme/a"); got != 1 {
		t.Errorf("%d tags list requests for two pages inside the period, want 1", got)
	}
	// The upstream cuts its answer at 700 tags; the stored document is marked, and a window beyond it is the upstream's own page.
	big, _ := read(`bulk/tags/tags/list?n=5`)
	if len(big.Tags) != 5 {
		t.Fatalf("%d tags", len(big.Tags))
	}
	if got := tagListGets(e.stub.Counts(), "bulk/tags"); got != 1 {
		t.Errorf("%d tags list requests after one page of a long list", got)
	}
	far, _ := read(`bulk/tags/tags/list?n=5&last=t0900`)
	if len(far.Tags) != 5 || far.Tags[0] != "t0901" {
		t.Errorf("window beyond the stored document = %v", far.Tags)
	}
	if got := tagListGets(e.stub.Counts(), "bulk/tags"); got != 2 {
		t.Errorf("%d tags list requests, want the document and the passed-through window", got)
	}
	read(`bulk/tags/tags/list?n=5&last=t0900`)
	if got := tagListGets(e.stub.Counts(), "bulk/tags"); got != 2 {
		t.Errorf("the passed-through window was not kept for the metadata period: %d requests", got)
	}
	if a := e.get(rep, rAnon, "nosuch/name/tags/list"); a.Status != http.StatusNotFound || !strings.Contains(string(a.Body), "NAME_UNKNOWN") {
		t.Errorf("%d %s", a.Status, a.Body)
	}
}

func TestR_OCI_024_ReferrersArePassedThroughAndAnUpstreamWithoutTheAPIAnswers404(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	subject, _ := e.stub.Digest("acme/signed", "1.0")
	if a := e.get(rep, rAnon, "acme/signed/referrers/"+subject); a.Status != http.StatusNotFound {
		t.Fatalf("an upstream without the referrers API: %d %s", a.Status, a.Body)
	}
	if err := e.stub.SetMode(ocistub.Mode{Referrers: true}); err != nil {
		t.Fatal(err)
	}
	a := e.get(rep, rFast, "acme/signed/referrers/"+subject)
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	var idx struct {
		Manifests []struct {
			Digest       string `json:"digest"`
			ArtifactType string `json:"artifactType"`
		} `json:"manifests"`
	}
	if err := json.Unmarshal(a.Body, &idx); err != nil || len(idx.Manifests) != 2 {
		t.Fatalf("%v %s", err, a.Body)
	}
	if a.Header.Get("Content-Type") != "application/vnd.oci.image.index.v1+json" {
		t.Errorf("Content-Type %q", a.Header.Get("Content-Type"))
	}
	f := e.get(rep, rFast, "acme/signed/referrers/"+subject+"?artifactType="+url.QueryEscape(idx.Manifests[0].ArtifactType))
	if f.Status != 200 || f.Header.Get("OCI-Filters-Applied") != "artifactType" {
		t.Fatalf("%d %v", f.Status, f.Header)
	}
	if err := json.Unmarshal(f.Body, &idx); err != nil || len(idx.Manifests) != 1 {
		t.Fatalf("%v %s", err, f.Body)
	}
	// The manifests a client then fetches by digest are cached like every manifest.
	m := e.get(rep, rFast, "acme/signed/manifests/"+idx.Manifests[0].Digest)
	if m.Status != 200 {
		t.Fatalf("%d %.200s", m.Status, m.Body)
	}
}

func TestR_OCI_025_ANameWithoutANamespaceIsBelowLibraryForDockerHubOnly(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	a := e.get(rep, rHub, "nginx/manifests/1")
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	b := e.get(rep, rHub, "library/nginx/manifests/1")
	if b.Status != 200 || string(b.Body) != string(a.Body) {
		t.Fatalf("%d %.200s", b.Status, b.Body)
	}
	if got := e.hub.Counts().ManifestGets("library/nginx", "1"); got != 1 {
		t.Errorf("%d upstream requests for both spellings, want 1", got)
	}
	if got := e.hub.Counts().ManifestGets("nginx", "1"); got != 0 {
		t.Errorf("the name was requested without library/: %d times", got)
	}
	if n := e.cacheEntries(rHub, "library/nginx/_tags/1"); n != 1 {
		t.Errorf("%d tag entries, want library/nginx/_tags/1 once", n)
	}
	if n := e.cacheEntries(rHub, "nginx/"); n != 0 {
		t.Errorf("%d entries below nginx/", n)
	}
	// A repository of another registry is not mapped: a one-component name stays as it is.
	if c := e.get(rep, rAnon, "app/manifests/1"); c.Status != 200 {
		t.Fatalf("%d %s", c.Status, c.Body)
	}
	if got := e.stub.Counts().ManifestGets("app", "1"); got != 1 {
		t.Errorf("app was requested %d times as app", got)
	}
	if got := e.stub.Counts().ManifestGets("library/app", "1"); got != 0 {
		t.Errorf("app was mapped below library/ for a registry that is not Docker Hub")
	}
}

// Mechanism of R-OCI-025, not a proof of its criteria: it checks a decision on both spellings, the quota answer or the mapping;
// the criteria are the single library/nginx tag item and the quota_header count of a mirror pass.
func TestAnUpstreamQuotaAnswerIsTheRegistrysRateLimitAnswer(t *testing.T) {
	e := newEnv(t, options{})
	rep := e.start(1)[0]
	if err := e.stub.SetMode(ocistub.Mode{PullQuota: &ocistub.PullQuota{Limit: 1}}); err != nil {
		t.Fatal(err)
	}
	if a := e.get(rep, rAnon, "acme/a/manifests/1.0"); a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	a := e.get(rep, rAnon, "acme/a/manifests/1.1")
	if a.Status != http.StatusServiceUnavailable || !strings.Contains(string(a.Body), "TOOMANYREQUESTS") || a.Header.Get("Retry-After") == "" {
		t.Fatalf("%d %v %s", a.Status, a.Header, a.Body)
	}
	// The first pull is served from the cache while the quota is used up.
	if b := e.get(rep, rAnon, "acme/a/manifests/1.0"); b.Status != 200 {
		t.Errorf("the stored tag: %d", b.Status)
	}
}

func TestR_REPO_024_TheUpstreamTestReadsABearerChallengeAsReachable(t *testing.T) {
	e := newEnv(t, options{})
	e.start(1)
	a := e.st.Do(e.st.Pick(), http.MethodPost, "/api/v1/projects/it/repositories/"+rAnon+"/upstream-test", e.admin, nil)
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	var r struct {
		Reachable     bool  `json:"reachable"`
		HTTPStatus    *int  `json:"httpStatus"`
		Authenticated *bool `json:"authenticated"`
	}
	if err := json.Unmarshal(a.Body, &r); err != nil {
		t.Fatal(err)
	}
	if !r.Reachable || r.HTTPStatus == nil || *r.HTTPStatus != 401 || r.Authenticated != nil {
		t.Errorf("result %s: a registry's 401 with a Bearer challenge is reachable and says nothing of the credential", a.Body)
	}
	if got := e.stub.Counts().OfKind(ocistub.KindBase, "GET"); got != 1 {
		t.Errorf("the probe asked the base resource %d times", got)
	}
}

// tagListGets counts the tags list requests of a repository that the registry answered (a challenge is not one).
func tagListGets(c ocistub.Counts, name string) int {
	n := 0
	for _, en := range c.Entries {
		if en.Kind == ocistub.KindTags && en.Name == name {
			n += en.Count - en.Statuses["401"]
		}
	}
	return n
}
