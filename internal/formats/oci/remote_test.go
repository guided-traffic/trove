package oci

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/formattest"
)

func rctx(t *testing.T, upstream string) formats.RemoteContext {
	t.Helper()
	u, err := url.Parse(upstream)
	if err != nil {
		t.Fatal(err)
	}
	return formats.RemoteContext{Upstream: u}
}

func adapter() remoteAdapter { return New().Remote().(remoteAdapter) }

func TestR_OCI_021_TheUpstreamRequestCarriesTheNamespacePrefixOfTheURL(t *testing.T) {
	d := sha([]byte("x"))
	cases := []struct {
		upstream, path, want string
	}{
		{"https://registry-1.docker.io/", "library/nginx/_tags/1.27", "https://registry-1.docker.io/v2/library/nginx/manifests/1.27"},
		{"https://ghcr.io/acme", "app/_tags/1", "https://ghcr.io/v2/acme/app/manifests/1"},
		{"https://other.example.com/web/docker/", "team/app/_manifests/" + d, "https://other.example.com/v2/web/docker/team/app/manifests/" + d},
		{"https://ghcr.io/acme", "app/_blobs/" + d, "https://ghcr.io/v2/acme/app/blobs/" + d},
		{"https://ghcr.io/acme", "app/_taglist", "https://ghcr.io/v2/acme/app/tags/list?n=10000"},
	}
	for _, c := range cases {
		req, err := adapter().UpstreamRequest(context.Background(), rctx(t, c.upstream), formats.Path{Path: c.path}, "")
		if err != nil {
			t.Fatalf("%s: %v", c.path, err)
		}
		if req.URL.String() != c.want || req.Method != http.MethodGet {
			t.Errorf("%s %s: %s %s, want %s", c.upstream, c.path, req.Method, req.URL, c.want)
		}
	}
	// A path outside the layout, or with a tag or a digest outside the grammars, names no upstream resource.
	for _, p := range []string{"app", "app/_tags/", "app/_tags/a:b", "app/_blobs/sha256:xyz", "Upper/_tags/1", "app/_uploads/1"} {
		if _, err := adapter().UpstreamRequest(context.Background(), rctx(t, "https://r.example.com/"), formats.Path{Path: p}, ""); err == nil {
			t.Errorf("%q gave an upstream request", p)
		}
	}
}

func TestR_OCI_022_AManifestIsAskedForWithTheFourManifestTypes(t *testing.T) {
	d := sha([]byte("x"))
	for _, p := range []string{"app/_tags/1", "app/_manifests/" + d} {
		req, err := adapter().UpstreamRequest(context.Background(), rctx(t, "https://r.example.com/"), formats.Path{Path: p}, "")
		if err != nil {
			t.Fatal(err)
		}
		got := req.Header.Get("Accept")
		for _, mt := range []string{
			"application/vnd.oci.image.index.v1+json", "application/vnd.docker.distribution.manifest.list.v2+json",
			"application/vnd.oci.image.manifest.v1+json", "application/vnd.docker.distribution.manifest.v2+json",
		} {
			if !strings.Contains(got, mt) {
				t.Errorf("%s: Accept %q lacks %s", p, got, mt)
			}
		}
	}
	req, _ := adapter().UpstreamRequest(context.Background(), rctx(t, "https://r.example.com/"), formats.Path{Path: "app/_blobs/" + d}, "")
	if req.Header.Get("Accept") != "" {
		t.Errorf("a blob is asked for with Accept %q", req.Header.Get("Accept"))
	}
}

func TestR_OCI_022_ATagIsAskedForByDigestWithTheSameRequestTheRevalidationDerives(t *testing.T) {
	rc := rctx(t, "https://r.example.com/")
	get, err := adapter().UpstreamRequest(context.Background(), rc, formats.Path{Path: "app/_tags/1"}, "")
	if err != nil {
		t.Fatal(err)
	}
	stored := formats.RemoteDocument{Digest: sha256.Sum256([]byte("m")), MediaType: ctImage}
	head, ok, err := adapter().RevalidationRequest(context.Background(), rc, formats.Path{Path: "app/_tags/1"}, "", stored, get)
	if err != nil || !ok {
		t.Fatalf("ok %v err %v", ok, err)
	}
	if head.Method != http.MethodHead || head.URL.String() != get.URL.String() || head.Header.Get("Accept") != get.Header.Get("Accept") {
		t.Errorf("revalidation = %s %s Accept %q; the GET is %s Accept %q", head.Method, head.URL, head.Header.Get("Accept"), get.URL, get.Header.Get("Accept"))
	}
	if get.Method != http.MethodGet {
		t.Errorf("the engine's GET was changed: %s", get.Method)
	}
	// The tags list and every artifact are revalidated by the engine's conditional GET.
	for _, p := range []string{"app/_taglist", "app/_manifests/" + sha([]byte("m"))} {
		if _, ok, _ := adapter().RevalidationRequest(context.Background(), rc, formats.Path{Path: p}, "", stored, get); ok {
			t.Errorf("%s: the format revalidates it by HEAD", p)
		}
	}
	same := "sha256:" + hex.EncodeToString(stored.Digest[:])
	for name, c := range map[string]struct {
		digest, ct string
		want       bool
	}{
		"the stored digest":             {same, ctImage, true},
		"the stored digest, no type":    {same, "", true},
		"the stored digest, a type":     {same, ctImage + "; charset=utf-8", true},
		"another digest":                {sha([]byte("other")), ctImage, false},
		"no digest header":              {"", ctImage, false},
		"a digest of another family":    {"sha512:" + strings.Repeat("a", 128), ctImage, false},
		"the stored digest, other type": {same, ctIndex, false},
	} {
		h := http.Header{}
		if c.digest != "" {
			h.Set("Docker-Content-Digest", c.digest)
		}
		if c.ct != "" {
			h.Set("Content-Type", c.ct)
		}
		resp := &http.Response{StatusCode: 200, Header: h, Body: http.NoBody}
		defer resp.Body.Close()
		if got := adapter().Unchanged(formats.Path{Path: "app/_tags/1"}, stored, resp); got != c.want {
			t.Errorf("%s: Unchanged = %v, want %v", name, got, c.want)
		}
	}
}

func TestR_OCI_022_ASchemaOneManifestIsNeverStored(t *testing.T) {
	// By type, before a byte is stored.
	for _, ct := range []string{"application/vnd.docker.distribution.manifest.v1+json", "application/vnd.docker.distribution.manifest.v1+prettyjws"} {
		resp := &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {ct}}, Body: io.NopCloser(strings.NewReader("{}"))}
		err := adapter().AcceptResponse(formats.Path{Path: "app/_tags/1"}, resp)
		var e *errs.Error
		if !errors.As(err, &e) || e.Kind != errs.UpstreamFailed || !strings.Contains(e.Detail, "MANIFEST_INVALID") {
			t.Errorf("%s: err = %v", ct, err)
		}
	}
	// By content, when the type says nothing.
	v1 := []byte(`{"schemaVersion":1,"name":"app","tag":"1","architecture":"amd64","fsLayers":[],"history":[]}`)
	_, err := adapter().PrepareFill(context.Background(), rctx(t, "https://r.example.com/"), formats.Path{Path: "app/_tags/1"}, "", strings.NewReader(string(v1)), int64(len(v1)))
	var e *errs.Error
	if !errors.As(err, &e) || e.Kind != errs.UpstreamFailed || !strings.Contains(e.Detail, "MANIFEST_INVALID") {
		t.Errorf("PrepareFill: err = %v", err)
	}
}

func TestR_OCI_020_AManifestWhoseDigestIsNotTheOneTheUpstreamAnnouncedIsRefused(t *testing.T) {
	body := imageManifestBytes(sha([]byte("cfg")), sha([]byte("layer")))
	read := func(announced string) error {
		h := http.Header{"Content-Type": {ctImage}}
		if announced != "" {
			h.Set("Docker-Content-Digest", announced)
		}
		resp := &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(string(body)))}
		if err := adapter().AcceptResponse(formats.Path{Path: "app/_tags/1"}, resp); err != nil {
			t.Fatal(err)
		}
		_, err := io.ReadAll(resp.Body)
		return err
	}
	if err := read(sha(body)); err != nil {
		t.Errorf("the announced digest: %v", err)
	}
	if err := read(""); err != nil {
		t.Errorf("no announced digest: %v", err)
	}
	err := read(sha([]byte("other")))
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != errs.CodeUpstreamIntegrityMismatch {
		t.Errorf("a wrong digest: err = %v", err)
	}
}

func TestR_OCI_022_AFetchedTagIsEnteredUnderItsDigest(t *testing.T) {
	cfg, layer := sha([]byte("cfg")), sha([]byte("layer"))
	body := imageManifestBytes(cfg, layer)
	p, err := adapter().PrepareFill(context.Background(), rctx(t, "https://r.example.com/"), formats.Path{Path: "team/app/_tags/1"}, "", strings.NewReader(string(body)), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Aliases) != 1 || p.Aliases[0].Path != "team/app/_manifests/"+sha(body) {
		t.Errorf("aliases = %v, want the manifest of its digest", p.Aliases)
	}
	if fm, ok := p.Data.(*fillManifest); !ok || fm.name != "team/app" || len(fm.m.References()) != 2 {
		t.Errorf("data = %#v", p.Data)
	}
	// An index without a mediaType member is read as an index, an image manifest without one as an image manifest (the member is optional).
	idx := []byte(`{"schemaVersion":2,"manifests":[{"mediaType":"` + ctImage + `","digest":"` + sha(body) + `","size":3}]}`)
	if _, err := adapter().PrepareFill(context.Background(), rctx(t, "https://r.example.com/"), formats.Path{Path: "app/_tags/1"}, "", strings.NewReader(string(idx)), int64(len(idx))); err != nil {
		t.Errorf("an index without a mediaType: %v", err)
	}
	noType := []byte(`{"schemaVersion":2,"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":"` + cfg + `","size":2},"layers":[]}`)
	if _, err := adapter().PrepareFill(context.Background(), rctx(t, "https://r.example.com/"), formats.Path{Path: "app/_tags/1"}, "", strings.NewReader(string(noType)), int64(len(noType))); err != nil {
		t.Errorf("a manifest without a mediaType: %v", err)
	}
	// A document the parser will not read is a 502 with MANIFEST_INVALID in the detail, and nothing is stored.
	bad := []byte(`{"schemaVersion":2,"mediaType":"` + ctImage + `","layers":[{"digest":"sha256:../../x"}]}`)
	_, err = adapter().PrepareFill(context.Background(), rctx(t, "https://r.example.com/"), formats.Path{Path: "app/_tags/1"}, "", strings.NewReader(string(bad)), int64(len(bad)))
	var e *errs.Error
	if !errors.As(err, &e) || e.Kind != errs.UpstreamFailed || !strings.Contains(e.Detail, "MANIFEST_INVALID") {
		t.Errorf("a crafted manifest: err = %v", err)
	}
	// Above the bound the document is not read.
	_, err = adapter().PrepareFill(context.Background(), rctx(t, "https://r.example.com/"), formats.Path{Path: "app/_tags/1"}, "", strings.NewReader(""), MaxDocumentBytes+1)
	if !errors.As(err, &e) || e.Code != errs.CodeUpstreamItemTooLarge {
		t.Errorf("an oversized manifest: err = %v", err)
	}
}

func TestR_OCI_024_APassThroughAnswerIsAcceptedAndAPathOutsideTheLayoutIsNot(t *testing.T) {
	accept := func(p string) error {
		resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("{}"))}
		defer resp.Body.Close()
		return adapter().AcceptResponse(formats.Path{Path: p}, resp)
	}
	for _, p := range []string{"app/referrers/" + sha([]byte("x")), "team/app/tags/list"} {
		if err := accept(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	for _, p := range []string{"app", "app/referrers/xyz", "Upper/tags/list", "app/uploads/1"} {
		if err := accept(p); err == nil {
			t.Errorf("%s was accepted", p)
		}
	}
}

func TestR_OCI_020_AManifestFetchedByDigestIsReadBeforeItIsStored(t *testing.T) {
	body := imageManifestBytes(sha([]byte("cfg")), sha([]byte("layer")))
	a := adapter()
	if !a.InspectsFill(formats.Path{Path: "app/_manifests/" + sha(body)}) || a.InspectsFill(formats.Path{Path: "app/_blobs/" + sha(body)}) || a.InspectsFill(formats.Path{Path: "app/_tags/1"}) {
		t.Fatal("only a manifest by digest is inspected")
	}
	ins, err := a.InspectFill(context.Background(), rctx(t, "https://r.example.com/"), formats.Path{Path: "app/_manifests/" + sha(body)}, spooled(body))
	if err != nil || ins.Package != "app" {
		t.Fatalf("%v %v", ins, err)
	}
	if fm, ok := ins.Payload.(*fillManifest); !ok || fm.m.Digest != sha(body) {
		t.Errorf("payload = %#v", ins.Payload)
	}
	_, err = a.InspectFill(context.Background(), rctx(t, "https://r.example.com/"), formats.Path{Path: "app/_manifests/" + sha([]byte("{}"))}, spooled([]byte(`{"schemaVersion":2,"layers":"x"}`)))
	var e *errs.Error
	if !errors.As(err, &e) || e.Kind != errs.UpstreamFailed {
		t.Errorf("a crafted manifest: err = %v", err)
	}
}

// spooled is a spooled artifact of fixed bytes.
type spooled []byte

func (s spooled) Digests() formats.Digests {
	sum := sha256.Sum256(s)
	return formats.Digests{SHA256: sum[:]}
}
func (s spooled) Size() int64 { return int64(len(s)) }
func (s spooled) Open() (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(string(s))), nil
}

func (s spooled) ReaderAt() (io.ReaderAt, error) {
	return strings.NewReader(string(s)), nil
}

func TestR_OCI_022_AManifestAndABlobAreVerifiedAgainstTheDigestOfTheirPath(t *testing.T) {
	d := sha([]byte("x"))
	for _, p := range []string{"app/_manifests/" + d, "app/_blobs/" + d} {
		got, err := adapter().BeforeArtifact(context.Background(), nil, nil, formats.Path{Path: p})
		if err != nil || hex.EncodeToString(got.SHA256) != strings.TrimPrefix(d, "sha256:") {
			t.Errorf("%s: %x %v", p, got.SHA256, err)
		}
	}
	got, err := adapter().BeforeArtifact(context.Background(), nil, nil, formats.Path{Path: "app/_taglist"})
	if err != nil || got.SHA256 != nil {
		t.Errorf("the tags list announces a digest: %x %v", got.SHA256, err)
	}
}

func TestR_OCI_021_TheTokenRequestTakesTheScopeFromTheRequestAndOnlyTheRealmAndTheServiceFromTheChallenge(t *testing.T) {
	rc := rctx(t, "https://ghcr.io/acme")
	challenge := http.Header{"Www-Authenticate": {`Bearer realm="https://auth.example.com/token?x=1",service="registry.example.com",scope="repository:somebody/else:pull,push"`}}
	// A fetch of a document: the scope names the prefixed name of the path, whatever scope the (remembered) challenge carries.
	orig, _ := http.NewRequest(http.MethodGet, "https://ghcr.io/v2/acme/app/manifests/1", nil)
	req, key, ok, err := adapter().TokenRequest(context.Background(), rc, orig, formats.Path{Path: "app/_tags/1"}, http.StatusUnauthorized, challenge)
	if err != nil || !ok {
		t.Fatalf("ok %v err %v", ok, err)
	}
	q := req.URL.Query()
	if req.Method != http.MethodGet || req.URL.Host != "auth.example.com" || req.URL.Path != "/token" || q.Get("x") != "1" ||
		q.Get("service") != "registry.example.com" || q.Get("scope") != "repository:acme/app:pull" {
		t.Errorf("request = %s", req.URL)
	}
	if key != "scope=repository:acme/app:pull" {
		t.Errorf("key = %q", key)
	}
	// Another path under the same challenge: another scope and so another key.
	_, key2, _, _ := adapter().TokenRequest(context.Background(), rc, orig, formats.Path{Path: "other/_blobs/" + sha([]byte("b"))}, http.StatusUnauthorized, challenge)
	if key2 == key || key2 != "scope=repository:acme/other:pull" {
		t.Errorf("key of another path = %q", key2)
	}
	// A request that has no repository path: the scope is read from the request, not from the challenge.
	for orig, want := range map[string]string{
		"https://ghcr.io/v2/acme/app/tags/list?n=100&last=a": "repository:acme/app:pull",
		"https://ghcr.io/v2/acme/app/referrers/" + sha(nil):  "repository:acme/app:pull",
		"https://ghcr.io/v2/_catalog":                        "registry:catalog:*",
		"https://ghcr.io/v2/acme/team/app/manifests/latest":  "repository:acme/team/app:pull",
		"https://ghcr.io/v2/":                                "",
	} {
		o, _ := http.NewRequest(http.MethodGet, orig, nil)
		r, key, ok, err := adapter().TokenRequest(context.Background(), rc, o, formats.Path{}, http.StatusUnauthorized, challenge)
		if err != nil || !ok {
			t.Fatalf("%s: ok %v err %v", orig, ok, err)
		}
		if got := r.URL.Query().Get("scope"); got != want || key != "scope="+want {
			t.Errorf("%s: scope %q key %q, want %q", orig, got, key, want)
		}
	}
}

func TestR_OCI_021_OnlyABearerChallengeIsAHandshake(t *testing.T) {
	rc := rctx(t, "https://r.example.com/")
	orig, _ := http.NewRequest(http.MethodGet, "https://r.example.com/v2/app/manifests/1", nil)
	p := formats.Path{Path: "app/_tags/1"}
	for name, h := range map[string]http.Header{
		"basic":     {"Www-Authenticate": {`Basic realm="registry"`}},
		"none":      {},
		"no realm":  {"Www-Authenticate": {`Bearer service="x"`}},
		"too large": {"Www-Authenticate": {`Bearer realm="https://a.example.com/` + strings.Repeat("a", 9000) + `"`}},
	} {
		if _, _, ok, err := adapter().TokenRequest(context.Background(), rc, orig, p, 401, h); ok || err != nil {
			t.Errorf("%s: ok %v err %v", name, ok, err)
		}
	}
	// Basic first, Bearer second, in one field or in two.
	for name, h := range map[string]http.Header{
		"one field":  {"Www-Authenticate": {`Basic realm="r", Bearer realm="https://a.example.com/t",service="s"`}},
		"two fields": {"Www-Authenticate": {`Basic realm="r"`, `Bearer realm="https://a.example.com/t",service="s"`}},
		"mixed case": {"Www-Authenticate": {`BEARER Realm="https://a.example.com/t", SERVICE=s`}},
	} {
		req, _, ok, err := adapter().TokenRequest(context.Background(), rc, orig, p, 401, h)
		if err != nil || !ok || req.URL.Host != "a.example.com" || req.URL.Query().Get("service") != "s" {
			t.Errorf("%s: ok %v err %v %v", name, ok, err, req)
		}
	}
	// A realm that is no address is the fetch's error, not a request.
	for _, realm := range []string{"/token", "ftp://a.example.com/t", "://x"} {
		_, _, ok, err := adapter().TokenRequest(context.Background(), rc, orig, p, 401, http.Header{"Www-Authenticate": {`Bearer realm="` + realm + `"`}})
		if ok || err == nil {
			t.Errorf("realm %q: ok %v err %v", realm, ok, err)
		}
	}
}

func TestR_OCI_021_ATokenAnswerIsReadWithinItsBounds(t *testing.T) {
	a := adapter()
	rc := formats.RemoteContext{}
	cases := []struct {
		body string
		auth string
		ttl  time.Duration
		bad  bool
	}{
		{`{"token":"abc.def","expires_in":300}`, "Bearer abc.def", 300 * time.Second, false},
		{`{"access_token":"xyz"}`, "Bearer xyz", 0, false},
		{`{"token":"a","access_token":"b","expires_in":60}`, "Bearer a", 60 * time.Second, false},
		{`{"token":"a","expires_in":-1}`, "Bearer a", 0, false},
		{`{"expires_in":300}`, "", 0, true},
		{`{"token":""}`, "", 0, true},
		{`{"token":"a b"}`, "", 0, true},
		{"{\"token\":\"a\\nb\"}", "", 0, true},
		{`{"token":"` + strings.Repeat("a", MaxTokenBytes+1) + `"}`, "", 0, true},
		{`not json`, "", 0, true},
	}
	for _, c := range cases {
		auth, ttl, err := a.ReadToken(context.Background(), rc, 200, []byte(c.body))
		if (err != nil) != c.bad || auth != c.auth || ttl != c.ttl {
			t.Errorf("%.40s: %q %v %v", c.body, auth, ttl, err)
		}
	}
}

func TestR_OCI_024_ATagsListTheUpstreamCutIsMarked(t *testing.T) {
	read := func(link, body string) string {
		h := http.Header{}
		if link != "" {
			h.Set("Link", link)
		}
		resp := &http.Response{StatusCode: 200, Header: h, Body: io.NopCloser(strings.NewReader(body))}
		defer resp.Body.Close()
		if err := adapter().AcceptResponse(formats.Path{Path: "app/_taglist"}, resp); err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	cut := read(`</v2/app/tags/list?last=b&n=2>; rel="next"`, "{\"name\":\"app\",\"tags\":[\"a\",\"b\"]}\n")
	st, err := parseTaglist([]byte(cut))
	if err != nil || !st.Truncated || len(st.Tags) != 2 {
		t.Errorf("cut list = %s: %+v %v", cut, st, err)
	}
	whole := read("", `{"name":"app","tags":["a","b"]}`)
	if st, err := parseTaglist([]byte(whole)); err != nil || st.Truncated {
		t.Errorf("whole list = %s: %+v %v", whole, st, err)
	}
	if got := read(`<x>; rel="next"`, `{}`); got != `{"x-trove-truncated":true}` {
		t.Errorf("empty object = %s", got)
	}
	if got := read(`<x>; rel="next"`, `[1]`); got != `[1]` {
		t.Errorf("not an object = %s", got)
	}
}

func TestR_OCI_020_ATagsListIsReadWithinItsBounds(t *testing.T) {
	p := func(doc string) error {
		_, err := adapter().PrepareFill(context.Background(), rctx(t, "https://r.example.com/"), formats.Path{Path: "app/_taglist"}, "", strings.NewReader(doc), int64(len(doc)))
		return err
	}
	if err := p(`{"name":"app","tags":null}`); err != nil {
		t.Errorf("no tags: %v", err)
	}
	if err := p(`{"name":"app","tags":["a","b"]}`); err != nil {
		t.Errorf("tags: %v", err)
	}
	if err := p(`not json`); err == nil {
		t.Error("not json")
	}
	big := `{"name":"app","tags":[` + strings.Repeat(`"t",`, MaxTaglistEntries) + `"t"]}`
	if err := p(big); err == nil {
		t.Error("more than 100,000 entries")
	}
	st, _ := parseTaglist([]byte(`{"name":"a","tags":["b","a","../x","a","B"]}`))
	if strings.Join(st.Tags, ",") != "B,a,b" {
		t.Errorf("tags = %v: invalid tags are dropped, the rest is in byte order without repeats", st.Tags)
	}
}

func TestR_OCI_025_TheMappingReadsTheRepositoryOfTheRequest(t *testing.T) {
	// The mapping reads the upstream of the repository; a repository without remote settings maps nothing.
	rq := &formats.Request{Repo: newFixture(t, formattest.HostOptions{Class: formats.Remote}).h.Repo()}
	if got := remoteName(rq, "nginx"); got != "nginx" {
		t.Errorf("remoteName without settings = %q", got)
	}
	if got := remoteName(rq, "library/nginx"); got != "library/nginx" {
		t.Errorf("remoteName of a path = %q", got)
	}
}

func TestR_OCI_023_TheRegistryResponseTypesAreExpected(t *testing.T) {
	a := adapter()
	d := sha([]byte("x"))
	for _, c := range []struct {
		path, mt string
		want     bool
	}{
		{"app/_tags/1", ctImage, true},
		{"app/_tags/1", ctIndex, true},
		{"app/_manifests/" + d, "application/vnd.docker.distribution.manifest.v2+json", true},
		{"app/_manifests/" + d, "text/html", false},
		{"app/_blobs/" + d, "application/octet-stream", true},
		{"app/_blobs/" + d, "application/vnd.oci.image.layer.v1.tar+gzip", true},
		{"app/_blobs/" + d, "text/html", false},
		{"app/_taglist", "application/json", true},
		{"app/_taglist", "text/html", false},
	} {
		if got := a.ExpectsType(formats.Path{Path: c.path}, c.mt); got != c.want {
			t.Errorf("%s %s: %v", c.path, c.mt, got)
		}
	}
}

// remoteFixture is a remote repository over the in-memory engine of the kit: the stored documents and artifacts by canonical path.
func remoteFixture(t *testing.T, docs, artifacts map[string][]byte, queries func(q formats.Query) (*formats.QueryResult, error)) (*fixture, *formattest.MemRemote) {
	t.Helper()
	m := &formattest.MemRemote{Docs: docs, Artifacts: artifacts}
	if queries != nil {
		m.Queries = func(_ context.Context, _ *formats.Request, q formats.Query) (*formats.QueryResult, error) {
			return queries(q)
		}
	}
	return newFixture(t, formattest.HostOptions{Class: formats.Remote, Remote: m}), m
}

func TestR_OCI_022_ATagIsServedAsStoredWithTheDigestTheClientVerifies(t *testing.T) {
	body := imageManifestBytes(sha([]byte("cfg")), sha([]byte("layer")))
	fx, _ := remoteFixture(t, map[string][]byte{TagPath("app", "1"): body}, nil, nil)
	resp := fx.get("app/manifests/1")
	fx.wantStatus(resp, http.StatusOK, "")
	if resp.Body.String() != string(body) || resp.Header().Get("Docker-Content-Digest") != sha(body) || resp.Header().Get("ETag") != `"`+sha(body)+`"` ||
		resp.Header().Get("Content-Type") != ctImage {
		t.Errorf("headers %v body %q", resp.Header(), resp.Body)
	}
	// A client that sends back the entity tag it was given is answered 304.
	fx.wantStatus(fx.get("app/manifests/1", func(r *http.Request) { r.Header.Set("If-None-Match", `"`+sha(body)+`"`) }), http.StatusNotModified, "")
	// HEAD answers the headers.
	head := fx.request(http.MethodHead, "app/manifests/1", nil, "")
	if head.Code != http.StatusOK || head.Header().Get("Docker-Content-Digest") != sha(body) || head.Body.Len() != 0 {
		t.Errorf("HEAD %d %v %q", head.Code, head.Header(), head.Body)
	}
	// A tag the upstream does not have is MANIFEST_UNKNOWN.
	fx.wantStatus(fx.get("app/manifests/2"), http.StatusNotFound, "MANIFEST_UNKNOWN")
	fx.wantStatus(fx.get("app/manifests/bad:tag"), http.StatusBadRequest, "DIGEST_INVALID")
}

func TestR_OCI_022_AManifestByDigestAndABlobAreArtifactsOfTheName(t *testing.T) {
	cfg, layer := []byte("cfg"), []byte("layer")
	body := imageManifestBytes(sha(cfg), sha(layer))
	fx, _ := remoteFixture(t, nil, map[string][]byte{
		ManifestPath("app", sha(body)): body, BlobPath("app", sha(layer)): layer,
	}, nil)
	resp := fx.get("app/manifests/" + sha(body))
	fx.wantStatus(resp, http.StatusOK, "")
	if resp.Body.String() != string(body) || resp.Header().Get("Docker-Content-Digest") != sha(body) {
		t.Errorf("manifest: headers %v", resp.Header())
	}
	resp = fx.get("app/blobs/" + sha(layer))
	fx.wantStatus(resp, http.StatusOK, "")
	if resp.Body.String() != "layer" || resp.Header().Get("Docker-Content-Digest") != sha(layer) || resp.Header().Get("Content-Type") != blobContentType {
		t.Errorf("blob: headers %v", resp.Header())
	}
	fx.wantStatus(fx.get("app/manifests/"+sha([]byte("none"))), http.StatusNotFound, "MANIFEST_UNKNOWN")
	fx.wantStatus(fx.get("app/blobs/"+sha([]byte("none"))), http.StatusNotFound, "BLOB_UNKNOWN")
	fx.wantStatus(fx.get("app/blobs/sha256:short"), http.StatusBadRequest, "DIGEST_INVALID")
}

func TestR_OCI_024_ATagsListIsCutFromTheStoredDocument(t *testing.T) {
	list := func(name string, tags ...string) []byte {
		return []byte(`{"name":"` + name + `","tags":["` + strings.Join(tags, `","`) + `"]}`)
	}
	fx, _ := remoteFixture(t, map[string][]byte{
		TaglistPath("whole"): list("whole", "d", "b", "a", "c"),
	}, nil, nil)
	type page struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	read := func(path string) (page, *formattest.Response) {
		resp := fx.getq(path)
		fx.wantStatus(resp, http.StatusOK, "")
		var p page
		if err := jsonDecode(resp.Body.Bytes(), &p); err != nil {
			t.Fatalf("%s: %v %s", path, err, resp.Body)
		}
		return p, resp
	}
	// A whole document answers every window, in byte order, with a Link while more follow.
	p, resp := read("whole/tags/list?n=2")
	if strings.Join(p.Tags, ",") != "a,b" || !strings.Contains(resp.Header().Get("Link"), "last=b") || !strings.Contains(resp.Header().Get("Link"), "n=2") {
		t.Errorf("first page = %v link %q", p.Tags, resp.Header().Get("Link"))
	}
	p, resp = read("whole/tags/list?n=2&last=b")
	if strings.Join(p.Tags, ",") != "c,d" || resp.Header().Get("Link") != "" {
		t.Errorf("last page = %v link %q", p.Tags, resp.Header().Get("Link"))
	}
	if p, _ = read("whole/tags/list?n=0"); len(p.Tags) != 0 {
		t.Errorf("n=0 = %v", p.Tags)
	}
	// A name the upstream does not have is NAME_UNKNOWN.
	fx.wantStatus(fx.get("none/tags/list"), http.StatusNotFound, "NAME_UNKNOWN")
}

func TestR_OCI_024_ReferrersArePassedThroughAndFiltered(t *testing.T) {
	subject := sha([]byte("subject"))
	sig, sbom := sha([]byte("sig")), sha([]byte("sbom"))
	upstream := `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[` +
		`{"mediaType":"` + ctImage + `","digest":"` + sig + `","size":9,"artifactType":"application/vnd.example.signature"},` +
		`{"mediaType":"` + ctImage + `","digest":"` + sbom + `","size":9,"artifactType":"application/vnd.example.sbom"},` +
		`{"mediaType":"` + ctImage + `","digest":"sha256:../x","size":9}]}`
	var got []formats.Query
	fx, _ := remoteFixture(t, nil, nil, func(q formats.Query) (*formats.QueryResult, error) {
		got = append(got, q)
		if strings.HasPrefix(q.Path.Path, "gone/") {
			return nil, errs.E(errs.NotFound, errs.CodeNotFound, "The upstream has no referrers API.")
		}
		return &formats.QueryResult{ContentType: ctIndex, Body: []byte(upstream)}, nil
	})
	resp := fx.get("app/referrers/" + subject)
	fx.wantStatus(resp, http.StatusOK, "")
	var idx struct {
		SchemaVersion int `json:"schemaVersion"`
		Manifests     []struct {
			Digest string `json:"digest"`
		} `json:"manifests"`
	}
	if err := jsonDecode(resp.Body.Bytes(), &idx); err != nil || idx.SchemaVersion != 2 || len(idx.Manifests) != 2 || idx.Manifests[0].Digest != sig {
		t.Fatalf("%v %s", err, resp.Body)
	}
	if resp.Header().Get("Content-Type") != ctIndex || resp.Header().Get("OCI-Filters-Applied") != "" {
		t.Errorf("headers %v", resp.Header())
	}
	if q := got[0]; q.Path.Path != "app/referrers/"+subject || q.Params.Get("artifactType") != "" || q.TTL <= 0 {
		t.Errorf("query = %+v", q)
	}
	// The filter is passed on, and applied here when the upstream ignores it.
	resp = fx.getq("app/referrers/" + subject + "?artifactType=application/vnd.example.sbom")
	fx.wantStatus(resp, http.StatusOK, "")
	if err := jsonDecode(resp.Body.Bytes(), &idx); err != nil || len(idx.Manifests) != 1 || idx.Manifests[0].Digest != sbom ||
		resp.Header().Get("OCI-Filters-Applied") != "artifactType" || got[1].Params.Get("artifactType") != "application/vnd.example.sbom" {
		t.Errorf("filtered: %v %s %v", err, resp.Body, resp.Header())
	}
	// An upstream without the API answers 404, so the client falls back to the tag schema through the same remote.
	fx.wantStatus(fx.get("gone/referrers/"+subject), http.StatusNotFound, "")
	fx.wantStatus(fx.get("app/referrers/sha256:xyz"), http.StatusBadRequest, "DIGEST_INVALID")
}

// getq is a GET whose path may carry a query.
func (fx *fixture) getq(path string) *formattest.Response {
	fx.t.Helper()
	path, query, _ := strings.Cut(path, "?")
	return fx.get(path, func(r *http.Request) { r.URL.RawQuery = query })
}

func jsonDecode(b []byte, v any) error { return json.Unmarshal(b, v) }
