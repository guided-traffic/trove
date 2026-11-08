// Self-tests of the counting stub of R-OCI-035; they check the test tool, not trove, and carry no requirement ID (Q-ROAD-5).

package ocistub

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"testing"
)

func TestManifestByTagAndDigest(t *testing.T) {
	f := newFixture(t, Config{})
	f.open()
	want, _ := f.stub.Digest("library/alpine", "3")
	r := f.manifestOf("library/alpine", "3")
	if r.status != 200 || r.header.Get("Content-Type") != MediaOCIIndex || r.header.Get("Docker-Content-Digest") != want || sha(r.body) != want {
		t.Fatalf("by tag: %d %s digest %s, body digest %s, want %s", r.status, r.header.Get("Content-Type"), r.header.Get("Docker-Content-Digest"), sha(r.body), want)
	}
	if etag := r.header.Get("Etag"); etag != `"`+want+`"` {
		t.Errorf("etag = %q", etag)
	}
	byDigest := f.manifestOf("library/alpine", want)
	if byDigest.status != 200 || !bytes.Equal(byDigest.body, r.body) {
		t.Errorf("by digest: %d, same bytes = %v", byDigest.status, bytes.Equal(byDigest.body, r.body))
	}

	head := f.do(http.MethodHead, f.reg+"/v2/library/alpine/manifests/3", map[string]string{"Accept": acceptAll})
	if head.status != 200 || len(head.body) != 0 || head.header.Get("Docker-Content-Digest") != want || head.header.Get("Content-Length") != itoa(len(r.body)) {
		t.Errorf("HEAD: %d, body %d bytes, digest %q, length %q", head.status, len(head.body), head.header.Get("Docker-Content-Digest"), head.header.Get("Content-Length"))
	}
	if cond := f.get(f.reg+"/v2/library/alpine/manifests/3", map[string]string{"Accept": acceptAll, "If-None-Match": `"` + want + `"`}); cond.status != 304 {
		t.Errorf("conditional GET = %d", cond.status)
	}

	// The Docker flavor keeps its media types.
	if d := f.manifestOf("library/busybox", "latest"); d.header.Get("Content-Type") != MediaDockerList {
		t.Errorf("busybox:latest content type = %q", d.header.Get("Content-Type"))
	}
	if d := f.manifestOf("library/busybox", "1.35"); d.header.Get("Content-Type") != MediaDockerManifest {
		t.Errorf("busybox:1.35 content type = %q", d.header.Get("Content-Type"))
	}
}

func TestManifestErrors(t *testing.T) {
	f := newFixture(t, Config{})
	f.open()
	for _, tc := range []struct {
		path   string
		status int
		code   string
	}{
		{"/v2/library/alpine/manifests/nope", 404, "MANIFEST_UNKNOWN"},
		{"/v2/library/alpine/manifests/sha256:" + strings.Repeat("a", 64), 404, "MANIFEST_UNKNOWN"},
		{"/v2/library/nothing/manifests/latest", 404, "NAME_UNKNOWN"},
		{"/v2/Bad_Name/manifests/latest", 400, "NAME_INVALID"},
		{"/v2/library/alpine/blobs/sha256:" + strings.Repeat("a", 64), 404, "BLOB_UNKNOWN"},
		{"/v2/library/alpine/blobs/not-a-digest", 404, "BLOB_UNKNOWN"},
	} {
		if r := f.get(f.reg+tc.path, nil); r.status != tc.status || r.errorCode(t) != tc.code {
			t.Errorf("%s = %d, want %d %s", tc.path, r.status, tc.status, tc.code)
		}
	}
	// A manifest of one repository is not served under another repository's name.
	d, _ := f.stub.Digest("acme/a", "1.0")
	if r := f.manifestOf("other/c", d); r.status != 404 {
		t.Errorf("a manifest of acme/a under other/c = %d", r.status)
	}
}

// The rules of distribution v3.1.1 (registry/handlers/manifests.go): an OCI manifest or index is served only to a client that lists its type,
// a Docker manifest list requested by tag and not accepted is replaced by the linux/amd64 manifest (which needs the schema 2 type),
// nothing is rewritten by digest, and there is no wildcard.
func TestAcceptRulesOfDistribution(t *testing.T) {
	f := newFixture(t, Config{})
	f.open()
	getWith := func(path string, accept ...string) reply {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, f.reg+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range accept {
			req.Header.Add("Accept", a)
		}
		resp, err := f.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return reply{status: resp.StatusCode, header: resp.Header, body: body}
	}
	alpine := "/v2/library/alpine/manifests/3"
	// OCI index: listed (also on a later Accept line, with parameters), else 404; no header and */* list nothing.
	if r := getWith(alpine, MediaDockerManifest, MediaOCIIndex+";q=0.9"); r.status != 200 || r.header.Get("Content-Type") != MediaOCIIndex {
		t.Errorf("OCI index listed on the second Accept line = %d %s", r.status, r.header.Get("Content-Type"))
	}
	if r := getWith(alpine, MediaDockerList+", "+MediaOCIIndex); r.status != 200 {
		t.Errorf("OCI index listed after a comma = %d", r.status)
	}
	for name, accept := range map[string][]string{"no header": nil, "wildcard": {"*/*"}, "docker only": {MediaDockerManifest, MediaDockerList}, "garbage": {";;;"}} {
		if r := getWith(alpine, accept...); r.status != 404 || r.errorCode(t) != "MANIFEST_UNKNOWN" {
			t.Errorf("OCI index with %s = %d %s", name, r.status, r.body)
		}
	}
	// OCI manifest, by tag and by digest.
	d, _ := f.stub.Digest("library/alpine", "3.19")
	for _, ref := range []string{"3.19", d} {
		if r := getWith("/v2/library/alpine/manifests/"+ref, MediaDockerManifest); r.status != 404 {
			t.Errorf("OCI manifest %s without its type = %d", ref, r.status)
		}
		if r := getWith("/v2/library/alpine/manifests/"+ref, MediaOCIManifest); r.status != 200 || r.header.Get("Content-Type") != MediaOCIManifest {
			t.Errorf("OCI manifest %s with its type = %d", ref, r.status)
		}
	}
	// Docker manifest list by tag: listed, served; not listed, the amd64 manifest; neither type, MANIFEST_INVALID.
	busybox := "/v2/library/busybox/manifests/latest"
	info, _ := f.stub.Image("library/busybox", "latest")
	amd64, _ := info.ForPlatform("linux/amd64")
	if r := getWith(busybox, MediaDockerList); r.status != 200 || r.header.Get("Content-Type") != MediaDockerList {
		t.Errorf("Docker list with its type = %d %s", r.status, r.header.Get("Content-Type"))
	}
	if r := getWith(busybox, MediaDockerManifest); r.status != 200 || r.header.Get("Content-Type") != MediaDockerManifest || r.header.Get("Docker-Content-Digest") != amd64.Digest || sha(r.body) != amd64.Digest {
		t.Errorf("Docker list rewritten = %d %s %s, want the amd64 manifest %s", r.status, r.header.Get("Content-Type"), r.header.Get("Docker-Content-Digest"), amd64.Digest)
	}
	if r := getWith(busybox, MediaOCIManifest); r.status != 400 || r.errorCode(t) != "MANIFEST_INVALID" {
		t.Errorf("Docker list with no Docker type = %d %s", r.status, r.body)
	}
	if r := getWith(busybox); r.status != 400 || r.errorCode(t) != "MANIFEST_INVALID" {
		t.Errorf("Docker list with no Accept = %d", r.status)
	}
	// By digest nothing is rewritten: the list is served whatever the client accepts.
	if r := getWith("/v2/library/busybox/manifests/"+info.Digest, MediaOCIManifest); r.status != 200 || r.header.Get("Content-Type") != MediaDockerList {
		t.Errorf("Docker list by digest = %d %s", r.status, r.header.Get("Content-Type"))
	}
	// A Docker manifest is served to everyone.
	if r := getWith("/v2/library/busybox/manifests/1.35"); r.status != 200 || r.header.Get("Content-Type") != MediaDockerManifest {
		t.Errorf("Docker manifest with no Accept = %d", r.status)
	}
}

// The counters record the media types of the Accept lines a request carried, sorted, so that a test can show what a client asked for
// (R-OCI-022 asks for the four manifest types).
func TestCountsRecordTheAcceptTypes(t *testing.T) {
	f := newFixture(t, Config{})
	f.open()
	f.get(f.reg+"/v2/library/alpine/manifests/3", map[string]string{"Accept": MediaOCIIndex + ";q=0.5, " + MediaDockerManifest})
	f.manifestOf("library/alpine", "3")
	entries := f.stub.Counts().ManifestEntries("library/alpine", "3", "GET")
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	want := map[string]bool{
		MediaDockerManifest + "," + MediaOCIIndex:                                                          true,
		strings.Join([]string{MediaDockerList, MediaDockerManifest, MediaOCIIndex, MediaOCIManifest}, ","): true,
	}
	for _, e := range entries {
		if !want[strings.Join(e.Accept, ",")] {
			t.Errorf("accept = %v", e.Accept)
		}
	}
}

func TestBlobRedirectsToTheCDNHost(t *testing.T) {
	f := newFixture(t, Config{})
	f.open()
	layer := firstLayer(t, f.stub, "library/alpine", "3.19")
	path := "/v2/library/alpine/blobs/" + layer

	r := f.get(f.reg+path, nil)
	loc, err := url.Parse(r.header.Get("Location"))
	if err != nil || r.status != http.StatusTemporaryRedirect {
		t.Fatalf("blob GET = %d, Location %q", r.status, r.header.Get("Location"))
	}
	if loc.Host != hostPort(t, f.cdn) || loc.Path != "/cdn/"+layer || loc.Query().Get("repo") != "library/alpine" || loc.Query().Get("sig") == "" {
		t.Fatalf("Location = %s, want the CDN host %s with the repository and a signature", loc, hostPort(t, f.cdn))
	}
	blob := f.get(loc.String(), nil)
	if blob.status != 200 || sha(blob.body) != layer || blob.header.Get("Docker-Content-Digest") != layer {
		t.Fatalf("CDN fetch = %d, body digest %s", blob.status, sha(blob.body))
	}

	// HEAD is redirected too, as distribution does with a storage driver that redirects; the CDN answers it with the length.
	head := f.do(http.MethodHead, f.reg+path, nil)
	if head.status != http.StatusTemporaryRedirect || head.header.Get("Location") != r.header.Get("Location") {
		t.Fatalf("HEAD blob = %d, Location %q", head.status, head.header.Get("Location"))
	}
	if h := f.do(http.MethodHead, head.header.Get("Location"), nil); h.status != 200 || h.header.Get("Content-Length") != itoa(len(blob.body)) {
		t.Errorf("HEAD on the CDN = %d, length %q", h.status, h.header.Get("Content-Length"))
	}
	// With HeadDirect the registry answers the HEAD itself while a GET still redirects.
	if err := f.stub.SetMode(Mode{Auth: AuthNone, HeadDirect: true}); err != nil {
		t.Fatal(err)
	}
	if h := f.do(http.MethodHead, f.reg+path, nil); h.status != 200 || h.header.Get("Content-Length") != itoa(len(blob.body)) || h.header.Get("Docker-Content-Digest") != layer {
		t.Errorf("HEAD with HeadDirect = %d, length %q", h.status, h.header.Get("Content-Length"))
	}
	if g := f.get(f.reg+path, nil); g.status != http.StatusTemporaryRedirect {
		t.Errorf("GET with HeadDirect = %d", g.status)
	}
	if err := f.stub.SetMode(Mode{Auth: AuthNone}); err != nil {
		t.Fatal(err)
	}

	c := f.stub.Counts()
	if n := c.BlobFetches(layer); n != 1 {
		t.Errorf("blob fetches = %d, want 1 (the redirect is no fetch)", n)
	}
	if c.BlobRequests("library/alpine", layer, "GET") != 2 || c.BlobRequests("library/alpine", layer, "HEAD") != 2 || c.CDNRequests(layer) != 2 {
		t.Errorf("counts: registry GET %d, HEAD %d, CDN %d", c.BlobRequests("library/alpine", layer, "GET"), c.BlobRequests("library/alpine", layer, "HEAD"), c.CDNRequests(layer))
	}
	if c.WithStatus(307) != 3 {
		t.Errorf("307 answers = %d", c.WithStatus(307))
	}
}

func TestBlobsDirectAndRange(t *testing.T) {
	f := newFixture(t, Config{})
	if err := f.stub.SetMode(Mode{Auth: AuthNone, BlobsDirect: true}); err != nil {
		t.Fatal(err)
	}
	layer := firstLayer(t, f.stub, "library/alpine", "3.19")
	r := f.get(f.reg+"/v2/library/alpine/blobs/"+layer, nil)
	if r.status != 200 || sha(r.body) != layer {
		t.Fatalf("direct blob = %d", r.status)
	}
	part := f.get(f.reg+"/v2/library/alpine/blobs/"+layer, map[string]string{"Range": "bytes=10-19"})
	if part.status != 206 || !bytes.Equal(part.body, r.body[10:20]) {
		t.Errorf("range = %d, %d bytes", part.status, len(part.body))
	}
	if n := f.stub.Counts().BlobFetches(layer); n != 2 {
		t.Errorf("fetches = %d, want 2 (a 200 and a 206)", n)
	}
	// A blob of another repository is not served.
	other := appLayer(t, f.stub, "acme/a", "1.0")
	if r := f.get(f.reg+"/v2/other/c/blobs/"+other, nil); r.status != 404 {
		t.Errorf("a blob of acme/a under other/c = %d", r.status)
	}
	// A layer all images of a platform share is served by every repository that holds it.
	shared := firstLayer(t, f.stub, "acme/a", "1.0")
	if r := f.get(f.reg+"/v2/library/alpine/blobs/"+shared, nil); r.status != 200 {
		t.Errorf("the shared base layer under library/alpine = %d", r.status)
	}
}

func TestCDNChecksSignatureAndCredentials(t *testing.T) {
	f := newFixture(t, Config{})
	f.open()
	layer := firstLayer(t, f.stub, "library/alpine", "3.19")
	redirect := f.get(f.reg+"/v2/library/alpine/blobs/"+layer, nil).header.Get("Location")

	if r := f.get(f.cdn+"/cdn/"+layer+"?repo=library/alpine&sig=bad", nil); r.status != 403 {
		t.Errorf("bad signature = %d", r.status)
	}
	if r := f.get(f.cdn+"/cdn/"+layer, nil); r.status != 403 {
		t.Errorf("no signature = %d", r.status)
	}
	// By default the CDN refuses a request that carries credentials, as an object store does for a signed URL; the counters show
	// the scheme that reached it.
	if r := f.get(redirect, map[string]string{"Authorization": "Bearer leaked"}); r.status != 400 {
		t.Errorf("CDN with credentials = %d, want 400", r.status)
	}
	if got := f.stub.Counts().AuthSchemes(hostPort(t, f.cdn)); !slices.Contains(got, "bearer") {
		t.Errorf("CDN host saw %v, want bearer among them", got)
	}
	if r := f.get(redirect, nil); r.status != 200 {
		t.Errorf("CDN without credentials = %d", r.status)
	}
	// With the switch it serves a request that has credentials.
	if err := f.stub.SetMode(Mode{Auth: AuthNone, CDNAcceptAuth: true}); err != nil {
		t.Fatal(err)
	}
	if r := f.get(redirect, map[string]string{"Authorization": "Bearer leaked"}); r.status != 200 {
		t.Errorf("CDN with credentials and the switch = %d", r.status)
	}
}

func TestTagsPagination(t *testing.T) {
	f := newFixture(t, Config{})
	f.open()
	var all []string
	var sizes []int
	next := "/v2/" + BulkRepository + "/tags/list"
	for next != "" {
		r := f.get(f.reg+next, nil)
		var doc struct {
			Name string   `json:"name"`
			Tags []string `json:"tags"`
		}
		r.json(t, &doc)
		if r.status != 200 || doc.Name != BulkRepository {
			t.Fatalf("page: %d %s", r.status, r.body)
		}
		all = append(all, doc.Tags...)
		sizes = append(sizes, len(doc.Tags))
		next = ""
		if link := r.header.Get("Link"); link != "" {
			rest, ok := strings.CutPrefix(link, "<")
			target, rel, _ := strings.Cut(rest, ">; ")
			if !ok || rel != `rel="next"` {
				t.Fatalf("Link = %q", link)
			}
			next = target
		}
		if len(sizes) > 10 {
			t.Fatal("more than ten pages")
		}
	}
	if !slices.Equal(sizes, []int{700, 700, 700, 400}) {
		t.Errorf("page sizes = %v, want four pages of 700, 700, 700 and 400", sizes)
	}
	if len(all) != BulkTags || !sort.StringsAreSorted(all) || len(slices.Compact(slices.Clone(all))) != BulkTags {
		t.Errorf("%d tags, sorted %v: want %d distinct tags in order", len(all), sort.StringsAreSorted(all), BulkTags)
	}

	// n is honoured below the cap and capped above it; last continues after a tag.
	var doc struct{ Tags []string }
	r := f.get(f.reg+"/v2/"+BulkRepository+"/tags/list?n=5", nil)
	r.json(t, &doc)
	if len(doc.Tags) != 5 || r.header.Get("Link") != "</v2/"+BulkRepository+"/tags/list?last="+doc.Tags[4]+"&n=5>; rel=\"next\"" {
		t.Errorf("n=5: %v, Link %q", doc.Tags, r.header.Get("Link"))
	}
	r = f.get(f.reg+"/v2/"+BulkRepository+"/tags/list?n=10000", nil)
	doc.Tags = nil
	r.json(t, &doc)
	if len(doc.Tags) != DefaultPageSize || r.header.Get("Link") == "" {
		t.Errorf("n=10000 returned %d tags, want the cap %d with a Link", len(doc.Tags), DefaultPageSize)
	}
	r = f.get(f.reg+"/v2/"+BulkRepository+"/tags/list?n=3&last=t0010", nil)
	doc.Tags = nil
	r.json(t, &doc)
	if !slices.Equal(doc.Tags, []string{"t0011", "t0012", "t0013"}) {
		t.Errorf("last=t0010: %v", doc.Tags)
	}
	r = f.get(f.reg+"/v2/"+BulkRepository+"/tags/list?n=0", nil)
	doc.Tags = nil
	r.json(t, &doc)
	if r.status != 200 || len(doc.Tags) != 0 || r.header.Get("Link") != "" || !strings.Contains(string(r.body), `"tags":[]`) {
		t.Errorf("n=0: %d %s", r.status, r.body)
	}
	for _, bad := range []string{"n=abc", "n=-1"} {
		if r := f.get(f.reg+"/v2/"+BulkRepository+"/tags/list?"+bad, nil); r.status != 400 || r.errorCode(t) != "PAGINATION_NUMBER_INVALID" {
			t.Errorf("%s = %d", bad, r.status)
		}
	}
	// A short list is one page without a Link; the tags are in byte order.
	small := f.get(f.reg+"/v2/library/alpine/tags/list", nil)
	doc.Tags = nil
	small.json(t, &doc)
	if !slices.Equal(doc.Tags, []string{"3", "3.19", "3.20", "latest"}) || small.header.Get("Link") != "" {
		t.Errorf("alpine tags = %v, Link %q", doc.Tags, small.header.Get("Link"))
	}
}

func TestPageSizeIsConfigurable(t *testing.T) {
	f := newFixture(t, Config{PageSize: 1000})
	f.open()
	r := f.get(f.reg+"/v2/"+BulkRepository+"/tags/list", nil)
	var doc struct{ Tags []string }
	r.json(t, &doc)
	if len(doc.Tags) != 1000 || r.header.Get("Link") == "" {
		t.Errorf("page of %d tags, Link %q", len(doc.Tags), r.header.Get("Link"))
	}
}

func TestCatalogBehindASwitch(t *testing.T) {
	f := newFixture(t, Config{})
	f.open()
	if r := f.get(f.reg+"/v2/_catalog?n=1", nil); r.status != 404 {
		t.Fatalf("catalog switched off = %d", r.status)
	}
	if err := f.stub.SetMode(Mode{Auth: AuthNone, Catalog: true}); err != nil {
		t.Fatal(err)
	}
	var names []string
	next := "/v2/_catalog?n=3"
	pages := 0
	for next != "" {
		r := f.get(f.reg+next, nil)
		var doc struct{ Repositories []string }
		r.json(t, &doc)
		if r.status != 200 {
			t.Fatalf("catalog page = %d", r.status)
		}
		names = append(names, doc.Repositories...)
		pages++
		next = ""
		if link := r.header.Get("Link"); link != "" {
			next = strings.TrimSuffix(strings.TrimPrefix(link, "<"), ">; rel=\"next\"")
		}
	}
	if !slices.Equal(names, f.stub.Repositories()) || pages != 4 {
		t.Errorf("catalog in %d pages = %v, want all of %v in 4", pages, names, f.stub.Repositories())
	}
	var doc struct{ Repositories []string }
	f.get(f.reg+"/v2/_catalog?last=acme/signed&n=2", nil).json(t, &doc)
	if !slices.Equal(doc.Repositories, []string{"acme/tool", "app"}) {
		t.Errorf("last=acme/signed: %v", doc.Repositories)
	}
	// The catalog needs its own scope in the handshake.
	if err := f.stub.SetMode(Mode{Catalog: true}); err != nil {
		t.Fatal(err)
	}
	r := f.get(f.reg+"/v2/_catalog", nil)
	if ch := parseChallenge(t, r.header.Get("WWW-Authenticate")); ch["scope"] != "registry:catalog:*" {
		t.Errorf("catalog challenge scope = %q", ch["scope"])
	}
	if r := f.get(f.reg+"/v2/_catalog", f.bearer("registry:catalog:*")); r.status != 200 {
		t.Errorf("catalog with its scope = %d", r.status)
	}
}

func TestReferrersBehindASwitch(t *testing.T) {
	f := newFixture(t, Config{})
	f.open()
	subject, _ := f.stub.Digest("acme/signed", "1.0")
	path := "/v2/acme/signed/referrers/" + subject
	if r := f.get(f.reg+path, nil); r.status != 404 {
		t.Fatalf("referrers switched off = %d", r.status)
	}
	// Off, the tag schema still answers: the client's fallback.
	fb := f.manifestOf("acme/signed", FallbackTag(subject))
	if fb.status != 200 || fb.header.Get("Content-Type") != MediaOCIIndex {
		t.Fatalf("fallback tag = %d %s", fb.status, fb.header.Get("Content-Type"))
	}

	if err := f.stub.SetMode(Mode{Auth: AuthNone, Referrers: true}); err != nil {
		t.Fatal(err)
	}
	r := f.get(f.reg+path, nil)
	var idx indexManifest
	r.json(t, &idx)
	if r.status != 200 || r.header.Get("Content-Type") != MediaOCIIndex || len(idx.Manifests) != 2 || idx.Manifests[0].ArtifactType != ArtifactSignature {
		t.Fatalf("referrers = %d %s", r.status, r.body)
	}
	if !bytes.Equal(r.body, fb.body) {
		t.Error("the referrers API and the tag schema fallback answer different bytes")
	}
	filtered := f.get(f.reg+path+"?artifactType="+url.QueryEscape(ArtifactSBOM), nil)
	var fidx indexManifest
	filtered.json(t, &fidx)
	if filtered.header.Get("OCI-Filters-Applied") != "artifactType" || len(fidx.Manifests) != 1 || fidx.Manifests[0].ArtifactType != ArtifactSBOM {
		t.Errorf("filtered referrers = %s, header %q", filtered.body, filtered.header.Get("OCI-Filters-Applied"))
	}
	// A digest with no referrers is an empty index, not an error.
	empty := f.get(f.reg+"/v2/acme/a/referrers/"+subject, nil)
	var eidx indexManifest
	empty.json(t, &eidx)
	if empty.status != 200 || eidx.Manifests == nil || len(eidx.Manifests) != 0 {
		t.Errorf("empty referrers = %d %s", empty.status, empty.body)
	}
	if r := f.get(f.reg+"/v2/acme/signed/referrers/nope", nil); r.status != 400 {
		t.Errorf("bad digest = %d", r.status)
	}
	// The manifests the index names are served by digest.
	if m := f.manifestOf("acme/signed", idx.Manifests[0].Digest); m.status != 200 {
		t.Errorf("referrer manifest = %d", m.status)
	}
}

func TestWritesAreRefused(t *testing.T) {
	f := newFixture(t, Config{})
	f.open()
	for _, method := range []string{http.MethodPut, http.MethodPost, http.MethodDelete, http.MethodPatch} {
		r := f.do(method, f.reg+"/v2/acme/a/manifests/9.9", nil)
		if r.status != 405 || r.errorCode(t) != "UNSUPPORTED" || r.header.Get("Allow") != "GET, HEAD" {
			t.Errorf("%s = %d %q", method, r.status, r.header.Get("Allow"))
		}
	}
}

func TestUnknownPathAnswers404(t *testing.T) {
	f := newFixture(t, Config{})
	f.open()
	for _, p := range []string{"/", "/robots.txt", "/v2/library/alpine/unknown/x"} {
		if r := f.get(f.reg+p, nil); r.status != 404 {
			t.Errorf("%s = %d", p, r.status)
		}
	}
}

func TestImageInfo(t *testing.T) {
	s, _ := New(Config{})
	info, err := s.Image("acme/signed", "1.0")
	if err != nil {
		t.Fatal(err)
	}
	blobs := info.Blobs()
	if len(blobs) != 3 || blobs[0] != info.Config.Digest {
		t.Errorf("blobs of a single image = %v", blobs)
	}
	idx, _ := s.Image("library/alpine", "3")
	// Two platforms with a shared base layer each, two attestations: the lists have no repeats.
	seen := map[string]bool{}
	for _, d := range idx.Blobs() {
		if seen[d] {
			t.Errorf("blob %s listed twice", d)
		}
		seen[d] = true
	}
	if _, err := s.Image("acme/signed", "nope"); err == nil {
		t.Error("an unknown tag is an error")
	}
	if _, err := s.Image("nope/x", "1"); err == nil {
		t.Error("an unknown repository is an error")
	}
	var round ImageInfo
	b, _ := json.Marshal(idx)
	if err := json.Unmarshal(b, &round); err != nil || round.Digest != idx.Digest || len(round.Manifests) != 4 {
		t.Errorf("ImageInfo does not round-trip through JSON: %v", err)
	}
}
