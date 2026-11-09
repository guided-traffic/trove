package oci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/mirror/discover"
)

// fakeUpstream is the discover.Upstream of a plan test: documents and artifacts by canonical path, and a handler for Get.
type fakeUpstream struct {
	base      *url.URL
	settings  json.RawMessage
	docs      map[string][]byte
	artifacts map[string][]byte
	get       func(u *url.URL) *discover.Response
	getErr    error // answered by Get instead of a response
	gets      []string
	asked     []string
}

func newFake(t *testing.T, base, settings string) *fakeUpstream {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeUpstream{base: u, settings: json.RawMessage(settings), docs: map[string][]byte{}, artifacts: map[string][]byte{}}
}

func (f *fakeUpstream) Base() *url.URL            { b := *f.base; return &b }
func (f *fakeUpstream) Settings() json.RawMessage { return f.settings }

func (f *fakeUpstream) Get(_ context.Context, u *url.URL, _ string, _ int64) (*discover.Response, error) {
	f.gets = append(f.gets, u.RequestURI())
	if f.getErr != nil {
		return nil, f.getErr
	}
	if f.get == nil {
		return &discover.Response{Status: http.StatusNotFound}, nil
	}
	return f.get(u), nil
}

func (f *fakeUpstream) Document(_ context.Context, path, _ string) ([]byte, bool, error) {
	f.asked = append(f.asked, "doc "+path)
	d, ok := f.docs[path]
	return d, ok, nil
}

func (f *fakeUpstream) Artifact(_ context.Context, path string, max int64) ([]byte, bool, error) {
	f.asked = append(f.asked, "artifact "+path)
	a, ok := f.artifacts[path]
	if ok && int64(len(a)) > max {
		return nil, true, nil
	}
	return a, ok, nil
}

func digestOfString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// imageManifest is an OCI image manifest with a config and the layers named by their seed.
func imageManifest(seed string, layers ...string) (body []byte, d string) {
	type desc struct {
		MediaType string `json:"mediaType"`
		Digest    string `json:"digest"`
		Size      int64  `json:"size"`
	}
	m := struct {
		SchemaVersion int    `json:"schemaVersion"`
		MediaType     string `json:"mediaType"`
		Config        desc   `json:"config"`
		Layers        []desc `json:"layers"`
	}{
		SchemaVersion: 2, MediaType: "application/vnd.oci.image.manifest.v1+json",
		Config: desc{"application/vnd.oci.image.config.v1+json", digestOfString("config-" + seed), 100},
	}
	for _, l := range layers {
		m.Layers = append(m.Layers, desc{"application/vnd.oci.image.layer.v1.tar+gzip", digestOfString("layer-" + l), 1000})
	}
	body, _ = json.Marshal(m)
	return body, digestOfString(string(body))
}

type indexEntry struct {
	digest, platform string
	annotations      map[string]string
}

// imageIndex is an OCI image index; platform "" is unknown/unknown.
func imageIndex(entries ...indexEntry) []byte {
	var list []map[string]any
	for _, e := range entries {
		m := map[string]any{"mediaType": "application/vnd.oci.image.manifest.v1+json", "digest": e.digest, "size": 400}
		os, arch, variant := "unknown", "unknown", ""
		if e.platform != "" {
			parts := strings.Split(e.platform, "/")
			os, arch = parts[0], parts[1]
			if len(parts) > 2 {
				variant = parts[2]
			}
		}
		p := map[string]string{"os": os, "architecture": arch}
		if variant != "" {
			p["variant"] = variant
		}
		m["platform"] = p
		if e.annotations != nil {
			m["annotations"] = e.annotations
		}
		list = append(list, m)
	}
	b, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": "application/vnd.oci.image.index.v1+json", "manifests": list})
	return b
}

func tagsAnswer(tags ...string) *discover.Response {
	b, _ := json.Marshal(map[string]any{"name": "x", "tags": tags})
	return &discover.Response{Status: http.StatusOK, Header: http.Header{}, Body: b}
}

func versionsOf(items []discover.Item) []string {
	var out []string
	for _, it := range items {
		if len(out) == 0 || out[len(out)-1] != it.Version {
			out = append(out, it.Version)
		}
	}
	return out
}

func pathsOf(items []discover.Item) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.Path)
	}
	return out
}

// R-OCI-026 (Mirror discovery): the tags a mirror keeps are the ones oci.mirror.tags selects minus oci.mirror.excludeTags; the wildcard
// matches come newest first by the tag order, and the tags an exact pattern names follow, pinned, so that maxVersionsPerPackage counts
// the wildcard matches only.
//
// Mechanism of R-OCI-026, not a proof of its criteria: it checks the mirror plan against fixtures; the criteria are the defaults,
// the 422 for a platform and the restriction, and the pass itself is proven under R-REPO-051 in test/integration/mirror.
func TestTheTagsAMirrorKeepsAreSelectedAndOrdered(t *testing.T) {
	all := []string{"1.0", "1.1", "1.2", "2.0", "latest", "1.3-rc1", "edge"}
	for _, tc := range []struct {
		name string
		ms   MirrorSettings
		want []chosenTag
	}{
		{
			"wildcard matches newest first, an exact tag pinned and last",
			MirrorSettings{Tags: []string{"1.*", "latest"}},
			[]chosenTag{{"1.3-rc1", false}, {"1.2", false}, {"1.1", false}, {"1.0", false}, {"latest", true}},
		},
		{
			"an exclude pattern removes a match",
			MirrorSettings{Tags: []string{"1.*", "latest"}, ExcludeTags: []string{"*-rc*"}},
			[]chosenTag{{"1.2", false}, {"1.1", false}, {"1.0", false}, {"latest", true}},
		},
		{
			"an exact tag that a wildcard also matches is pinned",
			MirrorSettings{Tags: []string{"1.*", "1.0"}},
			[]chosenTag{{"1.3-rc1", false}, {"1.2", false}, {"1.1", false}, {"1.0", true}},
		},
		{
			"an exact tag the upstream does not have is not planned",
			MirrorSettings{Tags: []string{"latest", "9.9"}},
			[]chosenTag{{"latest", true}},
		},
		{
			"an exact tag is pinned even when excluded patterns do not match it",
			MirrorSettings{Tags: []string{"edge"}, ExcludeTags: []string{"latest"}},
			[]chosenTag{{"edge", true}},
		},
		{
			"an exact tag that is excluded is dropped",
			MirrorSettings{Tags: []string{"edge"}, ExcludeTags: []string{"edge"}},
			nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := selectTags(all, tc.ms)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("selectTags = %v, want %v", got, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		pattern, tag string
		want         bool
	}{
		{"1.*", "1.2", true},
		{"1.*", "2.0", false},
		{"*", "anything", true},
		{"?.0", "1.0", true},
		{"?.0", "10.0", false},
		{"v*-alpine", "v3-alpine", true},
		{"v*-alpine", "v3-alpine3", false},
		{"a*b*c", "aXbYbZc", true},
		{"a*b*c", "aXbYbZ", false},
		{"latest", "latest", true},
		{"latest", "latestx", false},
	} {
		if got := matchTag(tc.pattern, tc.tag); got != tc.want {
			t.Errorf("matchTag(%q, %q) = %v, want %v", tc.pattern, tc.tag, got, tc.want)
		}
	}
}

// R-OCI-026 (Mirror discovery): the plan of an image lists the tags newest first, every item of a tag carries the tag as its version,
// and a layer two tags share is listed once per tag.
//
// Mechanism of R-OCI-026, not a proof of its criteria: it checks the mirror plan against fixtures; the criteria are the defaults,
// the 422 for a platform and the restriction, and the pass itself is proven under R-REPO-051 in test/integration/mirror.
func TestThePlanOfAnImageNamesItsTagsItsConfigAndItsLayers(t *testing.T) {
	f := newFake(t, "https://registry.example.com/", `{"mirror":{"tags":["1.*","latest"]}}`)
	f.get = func(u *url.URL) *discover.Response { return tagsAnswer("1.0", "1.1", "1.2", "2.0", "latest") }
	digests := map[string]string{}
	for _, tag := range []string{"1.0", "1.1", "1.2", "2.0", "latest"} {
		body, d := imageManifest(tag, "base", tag)
		f.docs[TagPath("acme/a", tag)] = body
		digests[tag] = d
	}
	plan, err := adapter().MirrorPlan(context.Background(), f, "acme/a")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"1.2", "1.1", "1.0", "latest"}; !reflect.DeepEqual(versionsOf(plan.Items), want) {
		t.Errorf("versions %v, want %v", versionsOf(plan.Items), want)
	}
	for _, it := range plan.Items {
		if it.Pinned != (it.Version == "latest") {
			t.Errorf("%s of %s: Pinned = %v", it.Path, it.Version, it.Pinned)
		}
		if it.Size != 100 && it.Size != 1000 {
			t.Errorf("%s: Size %d is not the announced one", it.Path, it.Size)
		}
		if !strings.HasPrefix(it.Path, "acme/a/_blobs/sha256:") {
			t.Errorf("an image manifest has blobs only, got %s", it.Path)
		}
	}
	// config, base layer and the tag's own layer of each of four tags: three items per tag, the base layer once per tag
	if len(plan.Items) != 12 {
		t.Errorf("%d items, want 12: %v", len(plan.Items), pathsOf(plan.Items))
	}
	if len(f.gets) != 1 || !strings.HasPrefix(f.gets[0], "/v2/acme/a/tags/list?") || !strings.Contains(f.gets[0], "n=1000") {
		t.Errorf("tags list requests %v", f.gets)
	}
}

// R-OCI-026 (Mirror discovery), R-OCI-025: on Docker Hub a one-component name is asked for below library/, and the cache paths carry
// it; any other upstream keeps the name.
//
// Mechanism of R-OCI-025, not a proof of its criteria: it checks a decision on both spellings, the quota answer or the mapping;
// the criteria are the single library/nginx tag item and the quota_header count of a mirror pass.
func TestTheMirrorMapsAOneComponentNameOnDockerHubOnly(t *testing.T) {
	body, _ := imageManifest("n", "l")
	for _, tc := range []struct {
		base, requested, wantName string
	}{
		{"https://registry-1.docker.io/", "/v2/library/nginx/tags/list?", "library/nginx"},
		{"https://registry.example.com/", "/v2/nginx/tags/list?", "nginx"},
		{"https://registry-1.docker.io/bitnami", "/v2/bitnami/nginx/tags/list?", "nginx"},
	} {
		f := newFake(t, tc.base, ``)
		f.get = func(*url.URL) *discover.Response { return tagsAnswer("latest") }
		wantName := tc.wantName
		f.docs[TagPath(wantName, "latest")] = body
		plan, err := adapter().MirrorPlan(context.Background(), f, "nginx")
		if err != nil {
			t.Fatal(err)
		}
		if len(f.gets) != 1 || !strings.HasPrefix(f.gets[0], tc.requested) {
			t.Errorf("%s: tags list request %v, want %s", tc.base, f.gets, tc.requested)
		}
		for _, it := range plan.Items {
			if !strings.HasPrefix(it.Path, wantName+"/_blobs/") {
				t.Errorf("%s: item %s is not below %s", tc.base, it.Path, wantName)
			}
		}
		if len(plan.Items) != 2 {
			t.Errorf("%s: %d items, want the config and one layer", tc.base, len(plan.Items))
		}
	}
}

// R-OCI-026 (Mirror discovery): oci.mirror.platforms selects the manifests of an index by platform; an attestation entry follows the
// manifest it names; a platform without a variant matches every variant; a single-platform image is kept whatever the platforms say.
//
// Mechanism of R-OCI-026, not a proof of its criteria: it checks the mirror plan against fixtures; the criteria are the defaults,
// the 422 for a platform and the restriction, and the pass itself is proven under R-REPO-051 in test/integration/mirror.
func TestAPlatformFilterKeepsTheSelectedManifestsAndTheirAttestations(t *testing.T) {
	amd, amdD := imageManifest("amd", "base", "amd")
	arm, armD := imageManifest("arm", "base", "arm")
	attAmd, attAmdD := imageManifest("att-amd", "sbom-amd")
	attArm, attArmD := imageManifest("att-arm", "sbom-arm")
	index := imageIndex(
		indexEntry{digest: amdD, platform: "linux/amd64"},
		indexEntry{digest: armD, platform: "linux/arm64/v8"},
		indexEntry{digest: attAmdD, annotations: map[string]string{"vnd.docker.reference.type": "attestation-manifest", "vnd.docker.reference.digest": amdD}},
		indexEntry{digest: attArmD, annotations: map[string]string{"vnd.docker.reference.type": "attestation-manifest", "vnd.docker.reference.digest": armD}},
	)
	single, _ := imageManifest("single", "only")
	setup := func(platforms string) *fakeUpstream {
		f := newFake(t, "https://registry.example.com/", `{"mirror":{"tags":["1.0","single"],"platforms":`+platforms+`}}`)
		f.get = func(*url.URL) *discover.Response { return tagsAnswer("1.0", "single") }
		f.docs[TagPath("acme/b", "1.0")] = index
		f.docs[TagPath("acme/b", "single")] = single
		for d, b := range map[string][]byte{amdD: amd, armD: arm, attAmdD: attAmd, attArmD: attArm} {
			f.artifacts[ManifestPath("acme/b", d)] = b
		}
		return f
	}
	manifestsOf := func(items []discover.Item, version string) (out []string) {
		for _, it := range items {
			if it.Version == version && strings.Contains(it.Path, "/_manifests/") {
				out = append(out, strings.TrimPrefix(it.Path, "acme/b/_manifests/"))
			}
		}
		sort.Strings(out)
		return out
	}
	want := func(ds ...string) []string { sort.Strings(ds); return ds }

	for _, tc := range []struct {
		name, platforms string
		manifests       []string
		layers          int
	}{
		{"linux/arm64 matches the variant v8 and takes its attestation", `["linux/arm64"]`, want(armD, attArmD), 5},
		{"linux/arm64/v8 matches the same", `["linux/arm64/v8"]`, want(armD, attArmD), 5},
		{"another variant matches nothing", `["linux/arm64/v7"]`, nil, 0},
		{"two platforms take both with their attestations", `["linux/amd64","linux/arm64"]`, want(amdD, armD, attAmdD, attArmD), 9},
		{"no platforms is every manifest", `[]`, want(amdD, armD, attAmdD, attArmD), 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(tc.platforms)
			plan, err := adapter().MirrorPlan(context.Background(), f, "acme/b")
			if err != nil {
				t.Fatal(err)
			}
			if got := manifestsOf(plan.Items, "1.0"); !reflect.DeepEqual(got, tc.manifests) {
				t.Errorf("manifests %v, want %v", got, tc.manifests)
			}
			blobs := 0
			for _, it := range plan.Items {
				if it.Version == "1.0" && strings.Contains(it.Path, "/_blobs/") {
					blobs++
				}
			}
			// a config and the layers per image: amd and arm three each with the base layer once, an attestation two
			if blobs != tc.layers {
				t.Errorf("%d blobs of the index, want %d: %v", blobs, tc.layers, pathsOf(plan.Items))
			}
			if got := manifestsOf(plan.Items, "single"); len(got) != 0 {
				t.Errorf("a single-platform image has no child manifests, got %v", got)
			}
			single := 0
			for _, it := range plan.Items {
				if it.Version == "single" {
					single++
				}
			}
			if single != 2 {
				t.Errorf("a single-platform image is kept whatever platforms say: %d items, want 2", single)
			}
		})
	}
}

// R-OCI-026: a layer with urls is a foreign layer the registry is not asked to hold; a tag whose manifest the registry does not accept
// has no items and does not fail the name.
//
// Mechanism of R-OCI-026, not a proof of its criteria: it checks the mirror plan against fixtures; the criteria are the defaults,
// the 422 for a platform and the restriction, and the pass itself is proven under R-REPO-051 in test/integration/mirror.
func TestForeignLayersAndInvalidManifestsAreSkipped(t *testing.T) {
	f := newFake(t, "https://registry.example.com/", `{"mirror":{"tags":["*"]}}`)
	f.get = func(*url.URL) *discover.Response { return tagsAnswer("good", "old") }
	foreign := fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":10},`+
		`"layers":[{"mediaType":"application/vnd.oci.image.layer.nondistributable.v1.tar+gzip","digest":%q,"size":20,"urls":["https://elsewhere.example/blob"]},`+
		`{"mediaType":"application/vnd.oci.image.layer.v1.tar+gzip","digest":%q,"size":30}]}`,
		digestOfString("c"), digestOfString("f"), digestOfString("l"))
	f.docs[TagPath("acme/c", "good")] = []byte(foreign)
	f.docs[TagPath("acme/c", "old")] = []byte(`{"schemaVersion":1,"name":"acme/c"}`)
	plan, err := adapter().MirrorPlan(context.Background(), f, "acme/c")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"acme/c/_blobs/" + digestOfString("c"), "acme/c/_blobs/" + digestOfString("l")}
	if got := pathsOf(plan.Items); !reflect.DeepEqual(got, want) {
		t.Errorf("items %v, want %v", got, want)
	}
}

// R-OCI-026: a name the upstream does not have is an empty plan, and a name with too many tags fails.
//
// Mechanism of R-OCI-026, not a proof of its criteria: it checks the mirror plan against fixtures; the criteria are the defaults,
// the 422 for a platform and the restriction, and the pass itself is proven under R-REPO-051 in test/integration/mirror.
func TestAnUnknownNameIsAnEmptyPlan(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer *discover.Response
	}{
		{"404", &discover.Response{Status: 404}},
		{"the 401 of an image that does not exist", &discover.Response{Status: 401, Header: http.Header{"Www-Authenticate": {`Bearer realm="https://auth.example/token",error="insufficient_scope"`}}}},
	} {
		f := newFake(t, "https://registry.example.com/", ``)
		f.get = func(*url.URL) *discover.Response { return tc.answer }
		plan, err := adapter().MirrorPlan(context.Background(), f, "acme/gone")
		if err != nil || len(plan.Items) != 0 {
			t.Errorf("%s: plan %v, error %v", tc.name, plan, err)
		}
	}
	f := newFake(t, "https://registry.example.com/", ``)
	f.get = func(*url.URL) *discover.Response { return &discover.Response{Status: 500} }
	if _, err := adapter().MirrorPlan(context.Background(), f, "acme/broken"); err == nil {
		t.Error("a 500 for the tags list is no empty plan")
	}
}

// R-OCI-026: the tags list is read in pages, following the Link, and a list of more than 100,000 tags fails its plan.
//
// Mechanism of R-OCI-026, not a proof of its criteria: it checks the mirror plan against fixtures; the criteria are the defaults,
// the 422 for a platform and the restriction, and the pass itself is proven under R-REPO-051 in test/integration/mirror.
func TestTheTagsListIsReadInPagesAndBounded(t *testing.T) {
	f := newFake(t, "https://registry.example.com/", ``)
	pages := map[string]*discover.Response{
		"":   {Status: 200, Header: http.Header{"Link": {`</v2/acme/a/tags/list?last=b&n=1000>; rel="next"`}}, Body: []byte(`{"name":"acme/a","tags":["a","b"]}`)},
		"b":  {Status: 200, Header: http.Header{}, Body: []byte(`{"name":"acme/a","tags":["c","d"]}`)},
		"zz": nil,
	}
	f.get = func(u *url.URL) *discover.Response { return pages[u.Query().Get("last")] }
	tags, found, err := adapter().readTags(context.Background(), f, "acme/a")
	if err != nil || !found || !reflect.DeepEqual(tags, []string{"a", "b", "c", "d"}) {
		t.Fatalf("tags %v found %v error %v", tags, found, err)
	}

	endless := newFake(t, "https://registry.example.com/", ``)
	n := 0
	endless.get = func(u *url.URL) *discover.Response {
		var tags []string
		for range 1000 {
			n++
			tags = append(tags, fmt.Sprintf("t%06d", n))
		}
		b, _ := json.Marshal(map[string]any{"name": "x", "tags": tags})
		return &discover.Response{Status: 200, Header: http.Header{"Link": {`<>; rel="next"`}}, Body: b}
	}
	_, _, err = adapter().readTags(context.Background(), endless, "acme/a")
	if ee, ok := errs.As(err); !ok || !strings.Contains(ee.Detail, "too_many_tags") {
		t.Errorf("error %v, want too_many_tags", err)
	}
}

func catalogAnswer(link string, names ...string) *discover.Response {
	b, _ := json.Marshal(map[string]any{"repositories": names})
	h := http.Header{}
	if link != "" {
		h.Set("Link", link)
	}
	return &discover.Response{Status: 200, Header: h, Body: b}
}

// R-OCI-026 (Mirror discovery): the listing method reads the catalog, drops the namespace prefix, checks every name against the scope
// and ends with a page shorter than n and no Link.
//
// Mechanism of R-OCI-026, not a proof of its criteria: it checks the mirror plan against fixtures; the criteria are the defaults,
// the 422 for a platform and the restriction, and the pass itself is proven under R-REPO-051 in test/integration/mirror.
func TestTheCatalogIsTheListingMethod(t *testing.T) {
	scope := discover.Scope{Admits: func(n string) bool { return strings.HasPrefix(n, "acme/") }}
	f := newFake(t, "https://registry.example.com/", ``)
	f.get = func(u *url.URL) *discover.Response {
		switch u.Query().Get("last") {
		case "":
			if u.Query().Get("n") == "1" {
				return catalogAnswer("", "acme/a")
			}
			return catalogAnswer(`</v2/_catalog?last=other%2Fc&n=3>; rel="next"`, "acme/a", "acme/b", "other/c")
		default:
			return catalogAnswer("", "zeta/z")
		}
	}
	methods, err := adapter().MirrorMethods(context.Background(), f)
	if err != nil || !reflect.DeepEqual(methods, []discover.Method{discover.MethodListing, discover.MethodNames}) {
		t.Fatalf("methods %v, error %v", methods, err)
	}
	page, err := adapter().MirrorNames(context.Background(), f, discover.PageRequest{Method: discover.MethodListing, Scope: scope, Limit: 3})
	if err != nil || page.Done || !reflect.DeepEqual(page.Names, []string{"acme/a", "acme/b"}) || string(page.Cursor) != `{"last":"other/c"}` {
		t.Fatalf("first page %+v, error %v", page, err)
	}
	page, err = adapter().MirrorNames(context.Background(), f, discover.PageRequest{Method: discover.MethodListing, Scope: scope, Limit: 3, Cursor: page.Cursor})
	if err != nil || !page.Done || len(page.Names) != 0 {
		t.Fatalf("last page %+v, error %v", page, err)
	}

	// A registry without the catalog offers names only, and the method listing is unavailable.
	none := newFake(t, "https://registry.example.com/", ``)
	methods, err = adapter().MirrorMethods(context.Background(), none)
	if err != nil || !reflect.DeepEqual(methods, []discover.Method{discover.MethodNames}) {
		t.Errorf("methods %v, error %v", methods, err)
	}
	if _, err := adapter().MirrorNames(context.Background(), none, discover.PageRequest{Method: discover.MethodListing, Scope: scope}); !errors.Is(err, discover.ErrMethodUnavailable) {
		t.Errorf("listing without a catalog: %v", err)
	}
	if _, err := adapter().MirrorNames(context.Background(), none, discover.PageRequest{Method: discover.MethodFeed, Scope: scope}); !errors.Is(err, discover.ErrMethodUnavailable) {
		t.Errorf("feed: %v", err)
	}

	// A namespace prefix: only the names below it, without it.
	pre := newFake(t, "https://registry.example.com/acme", ``)
	pre.get = func(*url.URL) *discover.Response {
		return catalogAnswer("", "acme/a", "acme/b/c", "other/c", "acmex/d")
	}
	page, err = adapter().MirrorNames(context.Background(), pre, discover.PageRequest{Method: discover.MethodListing, Scope: discover.Scope{Admits: func(string) bool { return true }}})
	if err != nil || !page.Done || !reflect.DeepEqual(page.Names, []string{"a", "b/c"}) {
		t.Errorf("prefixed page %+v, error %v", page, err)
	}
	if got := pre.gets[len(pre.gets)-1]; !strings.HasPrefix(got, "/v2/_catalog?") {
		t.Errorf("the catalog is the registry's, not below the prefix: %s", got)
	}
}

// R-OCI-026 (Mirror discovery): the names method takes the include patterns without a wildcard, as they are written, sorted, admitted
// ones only.
//
// Mechanism of R-OCI-026, not a proof of its criteria: it checks the mirror plan against fixtures; the criteria are the defaults,
// the 422 for a platform and the restriction, and the pass itself is proven under R-REPO-051 in test/integration/mirror.
func TestTheNamesMethodTakesTheExactIncludePatterns(t *testing.T) {
	scope := discover.Scope{
		Include: []string{"nginx", "acme/*", "bitnami/redis", "Bad_Name", "acme/b", "nginx"},
		Admits:  func(n string) bool { return n != "acme/b" },
	}
	page, err := adapter().MirrorNames(context.Background(), nil, discover.PageRequest{Method: discover.MethodNames, Scope: scope})
	if err != nil || !page.Done || !reflect.DeepEqual(page.Names, []string{"bitnami/redis", "nginx"}) {
		t.Errorf("names %+v, error %v", page, err)
	}
	page, err = adapter().MirrorNames(context.Background(), nil, discover.PageRequest{Method: discover.MethodNames, Scope: scope, Limit: 1})
	if err != nil || page.Done || !reflect.DeepEqual(page.Names, []string{"bitnami/redis"}) {
		t.Fatalf("first page %+v, error %v", page, err)
	}
	page, err = adapter().MirrorNames(context.Background(), nil, discover.PageRequest{Method: discover.MethodNames, Scope: scope, Limit: 1, Cursor: page.Cursor})
	if err != nil || !page.Done || !reflect.DeepEqual(page.Names, []string{"nginx"}) {
		t.Errorf("second page %+v, error %v", page, err)
	}
}

// boundedFake is a fakeUpstream that tells the plan the bounds of the engine.
type boundedFake struct {
	*fakeUpstream
	max  int
	pre  bool
	read *[]string
}

func (b boundedFake) VersionBounds() (int, bool) { return b.max, b.pre }

func (b boundedFake) Document(ctx context.Context, path, variant string) ([]byte, bool, error) {
	*b.read = append(*b.read, path)
	return b.fakeUpstream.Document(ctx, path, variant)
}

// R-REPO-051, R-OCI-026: a plan reads the manifests of the tags the engine keeps and no others: the newest maxVersionsPerPackage of the
// wildcard matches, without the prereleases when they are off, and the exact tags besides.
func TestR_REPO_051_ThePlanOfAnImageReadsOnlyTheTagsTheEngineKeeps(t *testing.T) {
	tags := []string{"1.0", "1.1", "1.2", "1.3-rc1", "2.0", "latest"}
	for _, tc := range []struct {
		name string
		max  int
		pre  bool
		want []string
	}{
		{"two newest without prereleases", 2, false, []string{"1.2", "1.1", "latest"}},
		{"two newest with prereleases", 2, true, []string{"1.3-rc1", "1.2", "latest"}},
		{"no bound", 0, true, []string{"1.3-rc1", "1.2", "1.1", "1.0", "latest"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake(t, "https://registry.example.com/", `{"mirror":{"tags":["1.*","latest"]}}`)
			f.get = func(*url.URL) *discover.Response { return tagsAnswer(tags...) }
			for _, tag := range tags {
				body, _ := imageManifest(tag, tag)
				f.docs[TagPath("acme/a", tag)] = body
			}
			var read []string
			if _, err := adapter().MirrorPlan(context.Background(), boundedFake{f, tc.max, tc.pre, &read}, "acme/a"); err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, p := range read {
				got = append(got, strings.TrimPrefix(p, "acme/a/_tags/"))
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("tags read %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMirrorSettingsMember(t *testing.T) {
	got := mirrorMemberOf(json.RawMessage(`{"mirror":{"tags":["1.*"],"platforms":["linux/amd64"]}}`))
	want := MirrorSettings{Tags: []string{"1.*"}, ExcludeTags: []string{}, Platforms: []string{"linux/amd64"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%+v, want %+v", got, want)
	}
	if got := mirrorMemberOf(nil); !reflect.DeepEqual(got, defaultMirrorSettings()) {
		t.Errorf("no member: %+v", got)
	}
}

// A registry whose token service refuses the catalog scope (Docker Hub answers 400) offers no catalog; the mirror goes on with the exact names.
func TestARefusedCatalogTokenMeansNoCatalog(t *testing.T) {
	f := newFake(t, "https://registry.example.com/", ``)
	f.getErr = errs.E(errs.UpstreamFailed, errs.CodeUpstreamUnexpectedStatus, "The upstream answered 400, which trove does not act on.")
	methods, err := adapter().MirrorMethods(context.Background(), f)
	if err != nil || !reflect.DeepEqual(methods, []discover.Method{discover.MethodNames}) {
		t.Fatalf("methods %v, error %v", methods, err)
	}
	f.getErr = errs.E(errs.UpstreamFailed, errs.CodeUpstreamFailed, "The upstream cannot be reached.")
	if _, err := adapter().MirrorMethods(context.Background(), f); err == nil {
		t.Error("an unreachable upstream is no answer about the catalog")
	}
}
