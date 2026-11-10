package ansible

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/formats"
	"github.com/guided-traffic/trove/internal/formats/ansible/galaxydoc"
)

func TestR_ANS_016_TheApiRootIsParsedIntoRelativePrefixes(t *testing.T) {
	d, err := parseDiscovery([]byte(`{"available_versions":{"v3":"v3/","v1":"v1"}}`))
	if err != nil || d.v3 != "v3/" || d.v1 != "v1/" {
		t.Fatalf("%+v, %v", d, err)
	}
	d, err = parseDiscovery([]byte(`{"available_versions":{"v3":"api/v3/"}}`))
	if err != nil || d.v3 != "api/v3/" || d.v1 != "" {
		t.Fatalf("a hub without v1: %+v, %v", d, err)
	}
	for _, bad := range []string{
		`{"available_versions":{"v3":"https://evil.example/v3/"}}`, `{"available_versions":{"v3":"/v3/"}}`, `{"available_versions":{"v3":"../v3/"}}`,
		`{"available_versions":{"v3":"a/./b/"}}`, `{"available_versions":{"v3":"v3/?x"}}`, `{"available_versions":{"v1":"v1/"}}`,
		`{"available_versions":{"v3":"v3/","v1":"//evil/"}}`, `{}`, `[]`, ``,
	} {
		if _, err := parseDiscovery([]byte(bad)); err == nil {
			t.Errorf("%s was accepted", bad)
		} else if e, ok := errs.As(err); !ok || e.Kind != errs.UpstreamFailed {
			t.Errorf("%s: error %v is no upstream failure", bad, err)
		}
	}
	// The hint of the public Galaxy's API root.
	_, err = parseDiscovery([]byte(`{"x":1}`))
	if err == nil || !strings.Contains(err.Error(), "/api/") {
		t.Errorf("the refusal of a document that is no API root names no hint: %v", err)
	}
}

func upstreamRoot(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestR_ANS_016_UpstreamRequestsAreBuiltFromTheApiRootAndThePrefixes(t *testing.T) {
	a := remoteAdapter{f: New()}
	rc := formats.RemoteContext{Upstream: upstreamRoot(t, "https://user:pw@galaxy.example/api"), Discovery: &discovery{v3: "v3/", v1: "v1/"}}
	for _, tc := range []struct {
		path, variant, want string
	}{
		{"collections/acme/tools/collection.json", "doc", "https://galaxy.example/api/v3/collections/acme/tools/"},
		{"collections/acme/tools/versions.json", "offset=200", "https://galaxy.example/api/v3/collections/acme/tools/versions/?limit=100&offset=200"},
		{"collections/acme/tools/1.0.0-rc.1+b.2.json", "doc", "https://galaxy.example/api/v3/collections/acme/tools/versions/1.0.0-rc.1+b.2/"},
		{"collections/acme/tools/acme-tools-1.0.0.tar.gz", "", "https://galaxy.example/api/v3/artifacts/collections/acme-tools-1.0.0.tar.gz"},
		{"roles/lookup.json", "owner=geerlingguy&name=pip", "https://galaxy.example/api/v1/roles/?owner__username=geerlingguy&name=pip"},
		{"roles/16/versions.json", "page=2", "https://galaxy.example/api/v1/roles/16/versions/?page_size=50&page=2"},
	} {
		req, err := a.UpstreamRequest(context.Background(), rc, formats.Path{Path: tc.path}, tc.variant)
		if err != nil {
			t.Errorf("%s: %v", tc.path, err)
			continue
		}
		if req.URL.String() != tc.want || req.Method != http.MethodGet || req.URL.User != nil {
			t.Errorf("%s: %s %s, want %s", tc.path, req.Method, req.URL, tc.want)
		}
	}
	// The API root itself is the repository's URL, without its credentials.
	req, err := a.UpstreamRequest(context.Background(), formats.RemoteContext{Upstream: rc.Upstream}, formats.Path{Path: RootDocument}, documentVariant)
	if err != nil || req.URL.String() != "https://galaxy.example/api" {
		t.Errorf("root: %v %v", req, err)
	}
	// Without a v1 API the role paths are a missing resource; a page variant the format did not store is refused.
	rc.Discovery = &discovery{v3: "v3/"}
	if _, err := a.UpstreamRequest(context.Background(), rc, formats.Path{Path: "roles/16/versions.json"}, "page=1"); !isNotFound(err) {
		t.Errorf("a role of an upstream without v1: %v", err)
	}
	for _, variant := range []string{"", "offset=-1", "offset=51200", "offset=01", "page=1", "offset=1;x"} {
		if _, err := a.UpstreamRequest(context.Background(), rc, formats.Path{Path: "collections/acme/tools/versions.json"}, variant); err == nil {
			t.Errorf("the variant %q of a version list was accepted", variant)
		}
	}
	if _, err := a.UpstreamRequest(context.Background(), formats.RemoteContext{Upstream: rc.Upstream}, formats.Path{Path: "collections/acme/tools/collection.json"}, "doc"); err == nil {
		t.Error("an upstream request was built before the API root was resolved")
	}
	for _, bad := range []string{"collections/acme/tools/other.json", "collections/Acme/tools/collection.json", "collections/acme/tools/acme-other-1.0.0.tar.gz", "roles/x/versions.json", "elsewhere"} {
		if _, err := a.UpstreamRequest(context.Background(), rc, formats.Path{Path: bad}, "doc"); !isNotFound(err) {
			t.Errorf("%s: %v", bad, err)
		}
	}
}

func isNotFound(err error) bool {
	e, ok := errs.As(err)
	return ok && e.Kind == errs.NotFound
}

const goodSum = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func versionDoc(ns, name, ver, download string) []byte {
	return []byte(`{"version":"` + ver + `","href":"/x/","created_at":"2026-10-01T10:00:00Z","updated_at":"2026-10-01T10:00:00Z","requires_ansible":">=2.14.0",` +
		`"artifact":{"filename":"` + ns + `-` + name + `-` + ver + `.tar.gz","sha256":"` + goodSum + `","size":99},"collection":{"id":"i","name":"` + name + `","href":"/c/"},` +
		`"download_url":"` + download + `","name":"` + name + `","namespace":{"name":"` + ns + `"},"signatures":[],"metadata":{"dependencies":{}}}`)
}

func TestR_ANS_017_AVersionDocumentIsCheckedBeforeItIsStored(t *testing.T) {
	a := remoteAdapter{f: New()}
	rc := formats.RemoteContext{Upstream: upstreamRoot(t, "https://galaxy.example/api/")}
	hosts := []string{"cdn.example"}
	tgt := target{kind: kindVersion, ns: "acme", name: "tools", version: "1.0.0"}

	maps, ann, err := a.checkVersion(rc, hosts, versionDoc("acme", "tools", "1.0.0", "https://galaxy.example/api/v3/artifacts/acme-tools-1.0.0.tar.gz"), tgt)
	if err != nil || len(maps) != 1 || maps[0].Path.Path != "collections/acme/tools/acme-tools-1.0.0.tar.gz" ||
		maps[0].Upstream != "https://galaxy.example/api/v3/artifacts/acme-tools-1.0.0.tar.gz" || ann.Name != "acme.tools" || ann.Version != "1.0.0" || ann.Size != 99 {
		t.Fatalf("%+v %+v %v", maps, ann, err)
	}
	// A path from the root is resolved against the upstream; an extra host is allowed.
	if maps, _, err = a.checkVersion(rc, hosts, versionDoc("acme", "tools", "1.0.0", "/files/acme-tools-1.0.0.tar.gz"), tgt); err != nil || maps[0].Upstream != "https://galaxy.example/files/acme-tools-1.0.0.tar.gz" {
		t.Errorf("relative download_url: %+v %v", maps, err)
	}
	if _, _, err = a.checkVersion(rc, hosts, versionDoc("acme", "tools", "1.0.0", "https://cdn.example/x.tar.gz"), tgt); err != nil {
		t.Errorf("an extra host: %v", err)
	}
	for name, doc := range map[string][]byte{
		"another collection": versionDoc("acme", "other", "1.0.0", "https://galaxy.example/x"),
		"another namespace":  versionDoc("evil", "tools", "1.0.0", "https://galaxy.example/x"),
		"another version":    versionDoc("acme", "tools", "1.0.1", "https://galaxy.example/x"),
		"a foreign host":     versionDoc("acme", "tools", "1.0.0", "https://evil.example/x"),
		"a scheme":           versionDoc("acme", "tools", "1.0.0", "ftp://galaxy.example/x"),
		"user information":   versionDoc("acme", "tools", "1.0.0", "https://u:p@evil.example/x"),
		"a relative path":    versionDoc("acme", "tools", "1.0.0", "x/y"),
		"no object":          []byte(`[]`),
		"a short digest":     []byte(strings.Replace(string(versionDoc("acme", "tools", "1.0.0", "https://galaxy.example/x")), goodSum, "abc", 1)),
	} {
		if _, _, err := a.checkVersion(rc, hosts, doc, tgt); err == nil {
			t.Errorf("%s was accepted", name)
		} else if e, ok := errs.As(err); !ok || e.Kind != errs.UpstreamFailed {
			t.Errorf("%s: %v is no upstream failure", name, err)
		}
	}
}

func TestR_ANS_017_ACollectionDocumentMustBeThePathsCollection(t *testing.T) {
	a := remoteAdapter{f: New()}
	tgt := target{kind: kindCollection, ns: "acme", name: "tools"}
	if err := a.checkCollection([]byte(`{"namespace":"Acme","name":"Tools","highest_version":null}`), tgt); err != nil {
		t.Errorf("a name in another case: %v", err)
	}
	if err := a.checkCollection([]byte(`{"namespace":"acme","name":"base"}`), tgt); err == nil || !strings.Contains(err.Error(), "identity_mismatch") {
		t.Errorf("another collection: %v", err)
	}
	if err := a.checkCollection([]byte(`{}`), tgt); err == nil {
		t.Error("a document without a name")
	}
}

func TestR_ANS_017_AStoredVersionListPageIsShownAtTheOffsetItsLinksName(t *testing.T) {
	const entry = `{"version":"1.0.0","href":"/h/","created_at":"2026-10-01T10:00:00Z","updated_at":"2026-10-01T10:00:00Z","requires_ansible":null}`
	doc := func(prev, next string) []byte {
		return []byte(`{"meta":{"count":9},"links":{"previous":` + prev + `,"next":` + next + `},"data":[` + entry + `,` + entry + `,` + entry + `]}`)
	}
	for _, tc := range []struct {
		name       string
		prev, next string
		want       int
	}{
		{"first page", `null`, `"/x/?limit=3&offset=3"`, 0},
		{"middle page", `"/x/?limit=3&offset=0"`, `"/x/?limit=3&offset=6"`, 3},
		{"last page", `"/x/?limit=3&offset=3"`, `null`, 6},
	} {
		if got := pageOffsetOf(doc(tc.prev, tc.next), 3, 9); got != tc.want {
			t.Errorf("%s: offset %d, want %d", tc.name, got, tc.want)
		}
	}
	if _, err := parsePage([]byte(`{"meta":{"count":60000},"data":[]}`)); err == nil {
		t.Error("a list longer than the bound was accepted")
	}
}

func rolesPage(entries ...string) []byte {
	return []byte(`{"count":9,"next":"https://galaxy.example/api/v1/roles/16/versions/?page=3&page_size=50","previous":null,"next_link":"/api/v1/roles/16/versions/?page=3&page_size=50",` +
		`"previous_link":"/api/v1/roles/16/versions/?page=1&page_size=50","cur_page":2,"results":[` + strings.Join(entries, ",") + `]}`)
}

func roleEntryJSON(name, download string) string {
	return `{"id":1,"name":"` + name + `","download_url":"` + download + `","commit_sha":"abc"}`
}

func TestR_ANS_020_AVersionOnAnUnlistedHostIsDroppedAndTheRestPointsToTheRepository(t *testing.T) {
	hosts := []string{"github.com", "*.github.com"}
	allowed := func(u string) bool { return roleHostAllowed(hosts, u) }
	doc := rolesPage(
		roleEntryJSON("1.0.0", "https://github.com/acme/r/archive/1.0.0.tar.gz"),
		roleEntryJSON("1.1.0", "https://codeload.github.com/acme/r/tar.gz/1.1.0"),
		roleEntryJSON("0.9.0", "https://evil.example/acme/r/archive/0.9.0.tar.gz"),
		roleEntryJSON("0.8.0", "http://github.com/acme/r/archive/0.8.0.tar.gz"),
		roleEntryJSON("bad ref", "https://github.com/acme/r/archive/x.tar.gz"),
		roleEntryJSON("0.7.0", ""),
		roleEntryJSON("0.6.0", "https://u:p@github.com/x.tar.gz"),
	)
	out, err := renderRoleVersions(doc, "16", "https://trove.example/api/ansible/p/r/", allowed, "")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Results []struct {
			Name        string `json:"name"`
			DownloadURL string `json:"download_url"`
			CommitSHA   string `json:"commit_sha"`
		} `json:"results"`
		NextLink     string `json:"next_link"`
		PreviousLink string `json:"previous_link"`
		Next         string `json:"next"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Results) != 2 || got.Results[0].Name != "1.0.0" || got.Results[1].Name != "1.1.0" {
		t.Fatalf("results %+v", got.Results)
	}
	for _, r := range got.Results {
		if r.DownloadURL != "https://trove.example/api/ansible/p/r/v1/roles/archives/16/"+r.Name+".tar.gz" || r.CommitSHA != "abc" {
			t.Errorf("entry %+v", r)
		}
	}
	const repo = "https://trove.example/api/ansible/p/r/v1/roles/16/versions/?"
	if got.NextLink != repo+"page=3&page_size=50" || got.Next != got.NextLink || got.PreviousLink != repo+"page=1&page_size=50" {
		t.Errorf("links %q %q %q", got.NextLink, got.Next, got.PreviousLink)
	}
	if strings.Contains(string(out), "github.com") || strings.Contains(string(out), "galaxy.example") {
		t.Errorf("an upstream address is left: %s", out)
	}

	// PrepareFill records an archive per kept version, built from the document's own role id, and counts the dropped ones.
	maps, dropped, err := remoteAdapter{}.roleMappings(hosts, doc, target{kind: kindRoleVersions, roleID: "16"})
	if err != nil || dropped != 5 || len(maps) != 2 || maps[0].Path.Path != "roles/16/1.0.0.tar.gz" || maps[1].Upstream != "https://codeload.github.com/acme/r/tar.gz/1.1.0" {
		t.Errorf("%+v dropped %d %v", maps, dropped, err)
	}
	// Rewriting its own output keeps the same entries: Rewrite is idempotent.
	again, err := renderRoleVersions(out, "16", "https://trove.example/api/ansible/p/r/", allowed, "https://trove.example/api/ansible/p/r/")
	if err != nil || string(again) != string(out) {
		t.Errorf("a rewritten document changed: %v\n%s\n%s", err, out, again)
	}
}

func TestR_ANS_020_ALookupKeepsTheRolesFieldsAndNamesTheRepositoryAsTheirAddress(t *testing.T) {
	doc := []byte(`{"count":1,"next":null,"previous":null,"results":[{"id":16,"url":"/api/v1/roles/16/","related":{"versions":"/api/v1/roles/16/versions/"},` +
		`"github_user":"geerlingguy","summary_fields":{"tags":["x"]}},{"id":"bad","url":"/api/v1/roles/x/"}]}`)
	out, err := renderRoleLookup(doc, "https://trove.example/api/ansible/p/r/", "geerlingguy", "pip")
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Results []map[string]json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(out, &got); err != nil || len(got.Results) != 1 {
		t.Fatalf("%v %s", err, out)
	}
	r := got.Results[0]
	if string(r["url"]) != `"https://trove.example/api/ansible/p/r/v1/roles/16/"` ||
		string(r["related"]) != `{"versions":"https://trove.example/api/ansible/p/r/v1/roles/16/versions/"}` ||
		string(r["github_user"]) != `"geerlingguy"` || !strings.Contains(string(r["summary_fields"]), "tags") {
		t.Errorf("role %v", r)
	}
	if _, err := renderRoleLookup([]byte(`[1]`), "https://t/", "a", "b"); err == nil {
		t.Error("a lookup that is no object was accepted")
	}
}

func TestR_ANS_020_ARoleArchiveBeyondTheLimitIsRefusedBeforeAndWhileItIsRead(t *testing.T) {
	a := remoteAdapter{}
	old := roleMax.Load()
	roleMax.Store(10)
	defer roleMax.Store(old)
	archive := formats.Path{Path: "roles/16/1.0.0.tar.gz"}

	resp := &http.Response{ContentLength: 11, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 11)))}
	if err := a.AcceptResponse(archive, resp); err == nil || errs.KindOf(err) != errs.UpstreamFailed {
		t.Errorf("a declared length beyond the limit: %v", err)
	}
	resp = &http.Response{ContentLength: -1, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 11)))}
	if err := a.AcceptResponse(archive, resp); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(resp.Body); err == nil || errs.KindOf(err) != errs.UpstreamFailed {
		t.Errorf("a body of unknown length beyond the limit: %v", err)
	}
	resp = &http.Response{ContentLength: -1, Body: io.NopCloser(strings.NewReader(strings.Repeat("x", 10)))}
	if err := a.AcceptResponse(archive, resp); err != nil {
		t.Fatal(err)
	}
	if b, err := io.ReadAll(resp.Body); err != nil || len(b) != 10 {
		t.Errorf("a body of exactly the limit: %d bytes, %v", len(b), err)
	}
	// Collection archives and documents are not bounded here.
	resp = &http.Response{ContentLength: 1 << 40, Body: io.NopCloser(strings.NewReader(""))}
	if err := a.AcceptResponse(formats.Path{Path: "collections/acme/tools/acme-tools-1.0.0.tar.gz"}, resp); err != nil {
		t.Errorf("a collection archive: %v", err)
	}
}

func TestR_ANS_020_TheNameOfARoleIsAPackageOfTheCoresNameMachinery(t *testing.T) {
	d := New().Descriptor()
	for in, want := range map[string]string{"role/16": "role/16", "Role/16": "role/16", "role/123456789012": "role/123456789012"} {
		if got, ok := d.Names.Normalize(in); !ok || got != want {
			t.Errorf("Normalize(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"role/", "role/x", "role/1234567890123", "role/1/2", "roles/16", "role/16/"} {
		if _, ok := d.Names.Normalize(bad); ok {
			t.Errorf("Normalize(%q) accepted", bad)
		}
	}
	f := New()
	p, ok := f.PackageDocumentPath("role/16")
	if !ok || p.Path != "roles/16/versions.json" {
		t.Errorf("PackageDocumentPath = %q %v", p.Path, ok)
	}
	if info := f.Classify(p); info.Package != "role/16" || info.Kind != formats.PathMetadata {
		t.Errorf("Classify(%q) = %+v", p.Path, info)
	}
	// A role package is not a collection: it has no "<ns>.<name>" form.
	if _, ok := f.PackageDocumentPath("role/x"); ok {
		t.Error("a package document for role/x")
	}
}

func TestR_ANS_018_TheArchiveOfAnotherCollectionIsRefusedAtTheFill(t *testing.T) {
	// The reader and the identity check are proven over HTTP in test/integration/ansibleremote; here the path grammar of the hook.
	a := remoteAdapter{f: New()}
	for p, want := range map[string]bool{
		"collections/acme/tools/acme-tools-1.0.0.tar.gz": true, "roles/16/1.0.0.tar.gz": true,
		"collections/acme/tools/1.0.0.json": false, "roles/16/versions.json": false, "api-root.json": false, "roles/lookup.json": false,
	} {
		if got := a.InspectsFill(formats.Path{Path: p}); got != want {
			t.Errorf("InspectsFill(%q) = %v, want %v", p, got, want)
		}
	}
}

func TestR_ANS_017_TheRenderingOfAStoredDocumentIsDeterministic(t *testing.T) {
	rec := httptest.NewRecorder()
	rq := &formats.Request{Request: httptest.NewRequest(http.MethodGet, "/", nil)}
	rq.Header.Set("If-None-Match", `W/"a", W/"b"`)
	called := false
	if err := remoteReply(rq, rec, `W/"b"`, "", func() ([]byte, error) { called = true; return nil, nil }); err != nil || rec.Code != http.StatusNotModified || called {
		t.Errorf("a matching validator: %d, rendered %v, %v", rec.Code, called, err)
	}
	rec = httptest.NewRecorder()
	rq.Header.Set("If-None-Match", "*")
	if err := remoteReply(rq, rec, `W/"b"`, "stored", func() ([]byte, error) { return []byte(`{}`), nil }); err != nil || rec.Code != http.StatusOK ||
		rec.Header().Get("Cache-Status") != "stored" || rec.Header().Get("Content-Type") != "application/json" {
		t.Errorf("the wildcard never matches: %d %v", rec.Code, err)
	}
	rec = httptest.NewRecorder()
	boom := errors.New("boom")
	if err := remoteReply(rq, rec, `W/"c"`, "", func() ([]byte, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Errorf("an error of the rendering is returned: %v", err)
	}
	// The renderer of a version leaves out the plugin listing and names the repository.
	v, _, err := galaxydoc.ParseUpstreamVersion(versionDoc("acme", "tools", "1.0.0", "https://galaxy.example/x"))
	if err != nil {
		t.Fatal(err)
	}
	if out := galaxydoc.RenderVersion(v, galaxydoc.Addr{Repo: "https://t/api/ansible/p/r/"}); strings.Contains(string(out), "galaxy.example") {
		t.Errorf("an upstream address is left: %s", out)
	}
}
