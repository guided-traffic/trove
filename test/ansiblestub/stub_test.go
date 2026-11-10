package ansiblestub

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/ansiblestub/ansiblegen"
)

func TestR_ANS_032_RootListsV3AndV1(t *testing.T) {
	f := newFixture(t, Config{})
	r := f.get(f.run.APIRoot())
	if r.status != 200 {
		t.Fatalf("status %d", r.status)
	}
	av := obj(r.json(t)["available_versions"])
	if av["v3"] != "v3/" || av["v1"] != "v1/" {
		t.Fatalf("available_versions %v", av)
	}
	if err := f.stub.SetMode(Mode{NoV1: true}); err != nil {
		t.Fatal(err)
	}
	av = obj(f.get(f.run.APIRoot()).json(t)["available_versions"])
	if _, ok := av["v1"]; ok || av["v3"] != "v3/" {
		t.Fatalf("no_v1: %v", av)
	}
	if r := f.get(f.api("v1/roles/")); r.status != 404 {
		t.Fatalf("v1 with no_v1: %d", r.status)
	}
}

func TestR_ANS_032_VersionListPagesNewestFirst(t *testing.T) {
	f := newFixture(t, Config{})
	base := f.api("v3/plugin/ansible/content/published/collections/index/many/versions/versions/")
	next := "?limit=100&offset=0"
	var pages, total int
	var first string
	for next != "" {
		r := f.get(base + next)
		if r.status != 200 {
			t.Fatalf("%s: status %d", next, r.status)
		}
		doc := r.json(t)
		if got := obj(doc["meta"])["count"]; got != float64(250) {
			t.Fatalf("meta.count %v", got)
		}
		data := list(doc["data"])
		if pages == 0 {
			first = obj(data[0])["version"].(string)
		}
		total += len(data)
		pages++
		n, _ := obj(doc["links"])["next"].(string)
		next = ""
		if n != "" {
			if !strings.HasPrefix(n, "/") {
				t.Fatalf("next link %q is not relative to the host", n)
			}
			next = n[strings.Index(n, "?"):]
		}
	}
	if pages != 3 || total != 250 {
		t.Fatalf("pages %d, versions %d", pages, total)
	}
	if first != "1.249.0" {
		t.Fatalf("newest first: got %s", first)
	}
	// limit and offset behave as in the public Galaxy: clamped to 1..100, an offset past the count is an empty page.
	r := f.get(base + "?limit=500&offset=0").json(t)
	if len(list(r["data"])) != 100 {
		t.Fatalf("limit=500 gave %d entries", len(list(r["data"])))
	}
	r = f.get(base + "?limit=10&offset=999").json(t)
	if len(list(r["data"])) != 0 || obj(r["links"])["next"] != nil {
		t.Fatalf("offset past the count: %v", r)
	}
	if got := f.counts().Query("GET", "/api/v3/plugin/ansible/content/published/collections/index/many/versions/versions/", "limit=100&offset=100"); got != 1 {
		t.Fatalf("the second page was requested %d times", got)
	}
}

func TestR_ANS_032_VersionsOrderBySemverAndHighestIsStable(t *testing.T) {
	f := newFixture(t, Config{})
	base := f.api("v3/plugin/ansible/content/published/collections/index/acme/tools/")
	var got []string
	for _, e := range list(f.get(base + "versions/").json(t)["data"]) {
		got = append(got, obj(e)["version"].(string))
	}
	if want := "2.1.0-rc.1 2.0.0 1.1.0 1.0.0"; strings.Join(got, " ") != want {
		t.Fatalf("order %v, want %s", got, want)
	}
	doc := f.get(base).json(t)
	if hv := obj(doc["highest_version"])["version"]; hv != "2.0.0" {
		t.Fatalf("highest_version %v: a pre-release is not the highest while a stable version exists", hv)
	}
	if doc["versions_url"] == "" || doc["deprecated"] != false {
		t.Fatalf("collection document %v", doc)
	}
}

func TestR_ANS_032_VersionDocumentAndArchiveAgree(t *testing.T) {
	f := newFixture(t, Config{})
	r := f.get(f.api("v3/plugin/ansible/content/published/collections/index/acme/tools/versions/2.0.0/"))
	if r.status != 200 {
		t.Fatalf("status %d", r.status)
	}
	doc := r.json(t)
	want := f.run.URL + pluginRoot + "artifacts/acme-tools-2.0.0.tar.gz"
	if doc["download_url"] != want {
		t.Fatalf("download_url %v, want %s", doc["download_url"], want)
	}
	art := obj(doc["artifact"])
	if doc["requires_ansible"] != ">=2.15.0" || obj(obj(doc["metadata"])["dependencies"])["acme.base"] != ">=1.0.0" {
		t.Fatalf("document %v", doc)
	}
	meta := obj(doc["metadata"])
	for _, k := range []string{"contents", "docs_blob", "manifest", "files"} {
		if _, ok := meta[k]; !ok {
			t.Errorf("metadata.%s is missing: a remote has nothing to drop", k)
		}
	}
	if list(doc["signatures"]) == nil || len(list(doc["signatures"])) != 0 {
		t.Errorf("signatures of an unsigned version: %v", doc["signatures"])
	}

	a := f.get(doc["download_url"].(string))
	if a.status != 200 {
		t.Fatalf("archive status %d", a.status)
	}
	sum, size := ansiblegen.Sum(a.body)
	if sum != art["sha256"] || float64(size) != art["size"] || art["filename"] != "acme-tools-2.0.0.tar.gz" {
		t.Fatalf("archive %s/%d, document %v", sum, size, art)
	}
	if got := f.get(doc["download_url"].(string), "Range", "bytes=0-9"); got.status != 206 || len(got.body) != 10 {
		t.Fatalf("range: status %d, %d bytes", got.status, len(got.body))
	}
}

func TestR_ANS_032_SignedVersionCarriesOpaqueSignatures(t *testing.T) {
	f := newFixture(t, Config{})
	doc := f.get(f.api("v3/plugin/ansible/content/published/collections/index/acme/signed/versions/1.0.0/")).json(t)
	sigs := list(doc["signatures"])
	if len(sigs) != 1 || !strings.Contains(obj(sigs[0])["signature"].(string), "BEGIN PGP SIGNATURE") {
		t.Fatalf("signatures %v", sigs)
	}
}

func TestR_ANS_032_IndexPagesAndNamesFillers(t *testing.T) {
	f := newFixture(t, Config{Fillers: 150})
	base := f.api("v3/plugin/ansible/content/published/collections/index/")
	doc := f.get(base).json(t)
	if obj(doc["meta"])["count"] != float64(155) || len(list(doc["data"])) != 100 {
		t.Fatalf("meta %v, %d entries", doc["meta"], len(list(doc["data"])))
	}
	first := obj(list(doc["data"])[0])
	if first["namespace"] != "acme" || first["name"] != "base" || obj(first["highest_version"])["version"] != "1.2.0" {
		t.Fatalf("first entry %v", first)
	}
	next := obj(doc["links"])["next"].(string)
	page2 := f.get(f.at(next)).json(t)
	if len(list(page2["data"])) != 55 || obj(page2["links"])["next"] != nil {
		t.Fatalf("second page: %d entries, links %v", len(list(page2["data"])), page2["links"])
	}
	// The short path serves the same document without the redirect, in the default mode.
	short := f.get(f.api("v3/collections/")).json(t)
	if obj(short["meta"])["count"] != float64(155) {
		t.Fatalf("short path: %v", short["meta"])
	}
}

func TestR_ANS_032_UnknownIsNotFoundInTheGalaxyShape(t *testing.T) {
	f := newFixture(t, Config{})
	for _, p := range []string{
		"v3/plugin/ansible/content/published/collections/index/no/such/",
		"v3/plugin/ansible/content/published/collections/index/acme/tools/versions/9.9.9/",
		"v3/plugin/ansible/content/published/collections/artifacts/acme-tools-9.9.9.tar.gz",
		"v3/nothing/",
	} {
		r := f.get(f.api(p))
		if r.status != 404 {
			t.Errorf("%s: %d", p, r.status)
			continue
		}
		e := obj(list(r.json(t)["errors"])[0])
		if e["status"] != "404" || e["code"] != "not_found" || e["title"] != "Not found." {
			t.Errorf("%s: error %v", p, e)
		}
	}
	if r := f.get(f.api("v1/roles/999/")); r.status != 404 || r.json(t)["detail"] != "Not found." {
		t.Errorf("unknown role: %d %s", r.status, r.body)
	}
}

func TestR_ANS_032_RedirectModeAnswersAsThePublicGalaxy(t *testing.T) {
	f := newFixture(t, Config{})
	if err := f.stub.SetMode(Mode{Redirect: true}); err != nil {
		t.Fatal(err)
	}
	r := f.get(f.api("v3/collections/acme/tools/versions/?limit=100&offset=0"))
	want := pluginRoot + "index/acme/tools/versions/?limit=100&offset=0"
	if r.status != 302 || r.header.Get("Location") != want {
		t.Fatalf("short path: %d %q, want 302 %q", r.status, r.header.Get("Location"), want)
	}
	if r := f.get(f.at(want)); r.status != 200 {
		t.Fatalf("long path: %d", r.status)
	}

	art := f.get(f.api("v3/plugin/ansible/content/published/collections/artifacts/acme-base-1.0.0.tar.gz"))
	loc := art.header.Get("Location")
	if art.status != 302 || !strings.HasPrefix(loc, "/api/pulp/content/published/acme-base-1.0.0.tar.gz?") || !strings.Contains(loc, "validate_token=") {
		t.Fatalf("artifact: %d %q", art.status, loc)
	}
	if got := f.get(f.at(loc)); got.status != 200 || !bytes.HasPrefix(got.body, []byte{0x1f, 0x8b}) {
		t.Fatalf("signed address: %d", got.status)
	}
	if got := f.get(f.api("pulp/content/published/acme-base-1.0.0.tar.gz")); got.status != 403 {
		t.Fatalf("signed address without its token: %d", got.status)
	}
}

func TestR_ANS_032_GzipOnlyWhenModeAndClientAgree(t *testing.T) {
	f := newFixture(t, Config{})
	plain := f.get(f.run.APIRoot(), "Accept-Encoding", "gzip")
	if plain.header.Get("Content-Encoding") != "" {
		t.Fatal("compressed without the mode")
	}
	if err := f.stub.SetMode(Mode{Gzip: true}); err != nil {
		t.Fatal(err)
	}
	if r := f.get(f.run.APIRoot()); r.header.Get("Content-Encoding") != "" {
		t.Fatal("compressed for a client that did not ask")
	}
	r := f.get(f.run.APIRoot(), "Accept-Encoding", "br, gzip;q=0.8")
	if r.header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("headers %v", r.header)
	}
	zr, err := gzip.NewReader(bytes.NewReader(r.body))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(zr)
	if !bytes.Equal(body, plain.body) {
		t.Fatalf("compressed body differs: %q", body)
	}
}

func TestR_ANS_032_DigestModes(t *testing.T) {
	f := newFixture(t, Config{})
	versionURL := f.api("v3/plugin/ansible/content/published/collections/index/acme/base/versions/1.0.0/")
	orig := obj(f.get(versionURL).json(t)["artifact"])
	origBytes := f.get(f.at(pluginRoot + "artifacts/acme-base-1.0.0.tar.gz")).body
	if sum, _ := ansiblegen.Sum(origBytes); sum != orig["sha256"] {
		t.Fatal("the healthy stub serves bytes that differ from the document")
	}

	// digest_changed: the upstream republished; document and bytes agree again, with another digest.
	if err := f.stub.SetMode(Mode{DigestChanged: []string{"acme.base"}}); err != nil {
		t.Fatal(err)
	}
	changed := obj(f.get(versionURL).json(t)["artifact"])
	changedBytes := f.get(f.at(pluginRoot + "artifacts/acme-base-1.0.0.tar.gz")).body
	if changed["sha256"] == orig["sha256"] {
		t.Fatal("the announced digest did not change")
	}
	if sum, _ := ansiblegen.Sum(changedBytes); sum != changed["sha256"] {
		t.Fatal("digest_changed: the document and the bytes disagree")
	}
	// A collection that is not listed keeps its digest.
	other := obj(f.get(f.api("v3/plugin/ansible/content/published/collections/index/other/stuff/versions/1.0.0/")).json(t)["artifact"])
	if info, _ := f.stub.Corpus(); other["sha256"] != corpusDigest(t, info, "other", "stuff", "1.0.0") {
		t.Fatal("an unlisted collection changed")
	}

	// wrong_digest: the document keeps the original digest, the bytes are another archive.
	if err := f.stub.SetMode(Mode{WrongDigest: []string{AllCollections}}); err != nil {
		t.Fatal(err)
	}
	wrong := obj(f.get(versionURL).json(t)["artifact"])
	wrongBytes := f.get(f.at(pluginRoot + "artifacts/acme-base-1.0.0.tar.gz")).body
	if wrong["sha256"] != orig["sha256"] {
		t.Fatal("wrong_digest changed the announced digest")
	}
	if sum, _ := ansiblegen.Sum(wrongBytes); sum == wrong["sha256"] {
		t.Fatal("wrong_digest served the announced bytes")
	}
	if err := f.stub.SetMode(Mode{DigestChanged: []string{"nodot"}}); err == nil {
		t.Fatal("a collection name without a dot was accepted")
	}
}

func corpusDigest(t *testing.T, info CorpusInfo, ns, name, version string) string {
	t.Helper()
	for _, c := range info.Collections {
		if c.Namespace == ns && c.Name == name {
			for _, v := range c.Versions {
				if v.Version == version {
					return v.SHA256
				}
			}
		}
	}
	t.Fatalf("%s.%s %s not in the corpus", ns, name, version)
	return ""
}

func TestR_ANS_032_RejectTokenAnswers401(t *testing.T) {
	f := newFixture(t, Config{})
	if err := f.stub.SetMode(Mode{RejectToken: true}); err != nil {
		t.Fatal(err)
	}
	r := f.get(f.run.APIRoot(), "Authorization", "Token secret")
	if r.status != 401 || obj(list(r.json(t)["errors"])[0])["code"] != "not_authenticated" {
		t.Fatalf("%d %s", r.status, r.body)
	}
	if err := f.stub.SetMode(Mode{}); err != nil {
		t.Fatal(err)
	}
	if r := f.get(f.run.APIRoot(), "Authorization", "Token secret"); r.status != 200 {
		t.Fatalf("after clearing: %d", r.status)
	}
}

func TestR_ANS_032_CountsRecordPathHostAuthAndStatus(t *testing.T) {
	f := newFixture(t, Config{})
	f.get(f.run.APIRoot())
	f.get(f.run.APIRoot(), "Authorization", "Token abc")
	f.get(f.run.APIRoot(), "Authorization", "Token abc")
	f.get(f.api("v3/plugin/ansible/content/published/collections/index/no/such/"))
	f.control("GET", "counts", "") // not counted

	c := f.counts()
	if c.Total() != 4 || c.Requests("GET", "/api/") != 3 || c.OfKind(ClassRoot, "GET") != 3 || c.OfKind(ClassCollection, "") != 1 {
		t.Fatalf("counts %+v", c)
	}
	if c.WithAuth(GalaxyLoopbackHost, "Token") != 2 || c.WithAuth(GalaxyLoopbackHost, "") != 2 || c.OnHost(GalaxyLoopbackHost) != 4 {
		t.Fatalf("auth or host: %+v", c)
	}
	var notFound int
	for _, e := range c.Entries {
		notFound += e.status(404)
	}
	if notFound != 1 {
		t.Fatalf("404 answers: %d", notFound)
	}
	if c.Delivered(ClassRoot) != 3 {
		t.Fatalf("delivered %d", c.Delivered(ClassRoot))
	}
	f.stub.Reset()
	if f.counts().Total() != 0 {
		t.Fatal("Reset left counts")
	}
}

func TestR_ANS_032_ConditionalRequestsAreCountedApart(t *testing.T) {
	f := newFixture(t, Config{})
	f.get(f.run.APIRoot())
	f.get(f.run.APIRoot(), "If-None-Match", `"x"`)
	if c := f.counts(); c.Conditional(ClassRoot) != 1 || c.Requests("GET", "/api/") != 2 {
		t.Fatalf("%+v", c)
	}
}

func TestR_ANS_032_AddAndDropVersions(t *testing.T) {
	f := newFixture(t, Config{})
	versions := func() []string {
		var out []string
		for _, e := range list(f.get(f.api("v3/plugin/ansible/content/published/collections/index/acme/base/versions/")).json(t)["data"]) {
			out = append(out, obj(e)["version"].(string))
		}
		return out
	}
	if err := f.stub.DropVersion("acme.base", "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(versions(), " "); got != "1.2.0" {
		t.Fatalf("after drop: %s", got)
	}
	if r := f.get(f.at(pluginRoot + "index/acme/base/versions/1.0.0/")); r.status != 404 {
		t.Fatalf("version document after drop: %d", r.status)
	}
	if r := f.get(f.at(pluginRoot + "artifacts/acme-base-1.0.0.tar.gz")); r.status != 404 {
		t.Fatalf("archive after drop: %d", r.status)
	}
	if err := f.stub.AddVersion("acme.base", "1.3.0"); err != nil {
		t.Fatal(err)
	}
	if err := f.stub.AddVersion("acme.base", "1.3.0"); err == nil {
		t.Fatal("a version was added twice")
	}
	if err := f.stub.AddVersion("acme.base", "not-a-version"); err == nil {
		t.Fatal("an invalid version was added")
	}
	if got := strings.Join(versions(), " "); got != "1.3.0 1.2.0" {
		t.Fatalf("after add: %s", got)
	}
	if r := f.get(f.at(pluginRoot + "artifacts/acme-base-1.3.0.tar.gz")); r.status != 200 {
		t.Fatalf("archive of the added version: %d", r.status)
	}
	if err := f.stub.AddVersion("fresh.thing", "0.1.0"); err != nil {
		t.Fatal(err)
	}
	if r := f.get(f.at(pluginRoot + "index/fresh/thing/")); r.status != 200 {
		t.Fatalf("new collection: %d", r.status)
	}
	if err := f.stub.DropVersion("fresh.thing", "9.9.9"); err == nil {
		t.Fatal("dropped a version that does not exist")
	}
	f.stub.ResetAll()
	if got := strings.Join(versions(), " "); got != "1.2.0 1.0.0" {
		t.Fatalf("after ResetAll: %s", got)
	}
}

func TestR_ANS_032_RoleLookupAndVersionPaging(t *testing.T) {
	f := newFixture(t, Config{})
	r := f.get(f.api("v1/roles/?owner__username=geerlingguy&name=pip")).json(t)
	res := list(r["results"])
	if r["count"] != float64(1) || len(res) != 1 {
		t.Fatalf("lookup %v", r)
	}
	role := obj(res[0])
	if role["id"] != float64(16) || role["github_user"] != "geerlingguy" || role["github_repo"] != "ansible-role-pip" {
		t.Fatalf("role %v", role)
	}
	if none := f.get(f.api("v1/roles/?owner__username=geerlingguy&name=nope")).json(t); none["count"] != float64(0) {
		t.Fatalf("no match: %v", none)
	}

	// 60 tags are two pages of page_size=50, joined by next_link relative to the host.
	p1 := f.get(f.api("v1/roles/18/versions/?page_size=50")).json(t)
	if len(list(p1["results"])) != 50 || p1["count"] != float64(60) {
		t.Fatalf("first page: %v", p1["count"])
	}
	next, _ := p1["next_link"].(string)
	if !strings.HasPrefix(next, "/api/v1/roles/18/versions/?") {
		t.Fatalf("next_link %q", next)
	}
	p2 := f.get(f.at(next)).json(t)
	if len(list(p2["results"])) != 10 || p2["next_link"] != nil {
		t.Fatalf("second page: %d, next %v", len(list(p2["results"])), p2["next_link"])
	}
}

func TestR_ANS_032_RoleArchivesFollowTheGitHubRedirect(t *testing.T) {
	f := newFixture(t, Config{})
	vs := list(f.get(f.api("v1/roles/16/versions/?page_size=50")).json(t)["results"])
	var dl string
	for _, v := range vs {
		if obj(v)["name"] == "2.0.0" {
			dl = obj(v)["download_url"].(string)
		}
	}
	if want := f.run.GitHubURL + "/geerlingguy/ansible-role-pip/archive/2.0.0.tar.gz"; dl != want {
		t.Fatalf("download_url %q, want %q", dl, want)
	}
	gh := f.get(dl)
	if gh.status != 302 || gh.header.Get("Location") != f.run.CodeloadURL+"/geerlingguy/ansible-role-pip/tar.gz/2.0.0" {
		t.Fatalf("github: %d %q", gh.status, gh.header.Get("Location"))
	}
	cl := f.get(gh.header.Get("Location"))
	if cl.status != 200 {
		t.Fatalf("codeload: %d", cl.status)
	}
	zr, err := gzip.NewReader(bytes.NewReader(cl.body))
	if err != nil {
		t.Fatal(err)
	}
	h, err := tar.NewReader(zr).Next()
	if err != nil || h.Name != "ansible-role-pip-2.0.0/" {
		t.Fatalf("first member %v, %v", h, err)
	}
	if r := f.get(f.run.GitHubURL + "/geerlingguy/ansible-role-pip/archive/9.9.9.tar.gz"); r.status != 404 {
		t.Fatalf("unknown tag: %d", r.status)
	}
	c := f.counts()
	if c.OnHost(DefaultGitHubHost) != 2 || c.OnHost(DefaultCodeloadHost) != 1 || c.OfKind(ClassRoleArchive, "GET") != 3 {
		t.Fatalf("counts %+v", c)
	}
	if f.get(f.api("pulp/x")).status != 404 {
		t.Fatal("unexpected route")
	}
}

func TestR_ANS_032_RoleVersionOnTheUnlistedHost(t *testing.T) {
	f := newFixture(t, Config{})
	vs := list(f.get(f.api("v1/roles/17/versions/?page_size=50")).json(t)["results"])
	hosts := map[string]string{}
	for _, v := range vs {
		hosts[obj(v)["name"].(string)] = obj(v)["download_url"].(string)
	}
	if !strings.HasPrefix(hosts["0.9.0"], f.run.UnlistedURL+"/") || !strings.HasPrefix(hosts["1.0.0"], f.run.GitHubURL+"/") {
		t.Fatalf("download_urls %v", hosts)
	}
	if r := f.get(hosts["0.9.0"]); r.status != 200 || !bytes.HasPrefix(r.body, []byte{0x1f, 0x8b}) {
		t.Fatalf("unlisted host: %d", r.status)
	}
	info, err := f.stub.Corpus()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range info.Roles {
		if r.ID == 17 && (len(r.UnlistedVersions) != 1 || r.UnlistedVersions[0] != "0.9.0") {
			t.Fatalf("corpus: %+v", r)
		}
	}
}

func TestR_ANS_032_RealmIssuesAndExpiresAccessTokens(t *testing.T) {
	var now atomic.Int64
	now.Store(time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC).Unix())
	f := newFixture(t, Config{Now: func() time.Time { return time.Unix(now.Load(), 0) }})
	if err := f.stub.SetRealm(Realm{Require: true, RefreshTokens: []string{"offline-1"}, ClientID: "cloud-services", AccessTTLSeconds: 60}); err != nil {
		t.Fatal(err)
	}
	exchange := func(client, refresh string) resp {
		body := "grant_type=refresh_token&client_id=" + client + "&refresh_token=" + refresh
		return f.do("POST", f.run.RealmURL+DefaultRealmPath, strings.NewReader(body), "Content-Type", "application/x-www-form-urlencoded")
	}

	if r := f.get(f.run.APIRoot()); r.status != 401 {
		t.Fatalf("no token: %d", r.status)
	}
	if r := f.get(f.run.APIRoot(), "Authorization", "Bearer made-up"); r.status != 401 {
		t.Fatalf("made-up token: %d", r.status)
	}
	bad := exchange("cloud-services", "wrong")
	if bad.status != 400 || bad.json(t)["error"] != "invalid_grant" {
		t.Fatalf("wrong refresh token: %d %s", bad.status, bad.body)
	}
	if r := exchange("other-client", "offline-1"); r.status != 400 || r.json(t)["error"] != "invalid_client" {
		t.Fatalf("wrong client: %d %s", r.status, r.body)
	}
	ok := exchange("cloud-services", "offline-1")
	if ok.status != 200 {
		t.Fatalf("exchange: %d %s", ok.status, ok.body)
	}
	doc := ok.json(t)
	token := doc["access_token"].(string)
	if doc["expires_in"] != float64(60) || doc["token_type"] != "Bearer" {
		t.Fatalf("token document %v", doc)
	}
	if r := f.get(f.run.APIRoot(), "Authorization", "Bearer "+token); r.status != 200 {
		t.Fatalf("valid token: %d", r.status)
	}

	// A lifetime that passed, and a revoked token, are refused; the exchange issues a different token.
	now.Add(61)
	if r := f.get(f.run.APIRoot(), "Authorization", "Bearer "+token); r.status != 401 {
		t.Fatalf("expired token: %d", r.status)
	}
	second := exchange("cloud-services", "offline-1").json(t)["access_token"].(string)
	if second == token {
		t.Fatal("an exchange reissued the same token")
	}
	if r := f.get(f.run.APIRoot(), "Authorization", "Bearer "+second); r.status != 200 {
		t.Fatalf("second token: %d", r.status)
	}
	if n := f.stub.ExpireAccessTokens(); n != 1 {
		t.Fatalf("revoked %d tokens", n)
	}
	if r := f.get(f.run.APIRoot(), "Authorization", "Bearer "+second); r.status != 401 {
		t.Fatalf("revoked token: %d", r.status)
	}

	st := f.stub.RealmState()
	if st.Exchanges != 2 || st.Failed != 2 || st.Accepted != 2 || st.Rejected != 4 || len(st.Issued) != 2 {
		t.Fatalf("state %+v", st)
	}
	if len(st.ClientIDs) != 2 || st.ClientIDs[0] != "cloud-services" {
		t.Fatalf("client ids %v", st.ClientIDs)
	}
	if want := []string{"made-up", token, second}; strings.Join(st.RejectedTokens, ",") != strings.Join(want, ",") {
		t.Fatalf("rejected tokens %v, want %v", st.RejectedTokens, want)
	}
	// The realm host counts as the realm class, and no Galaxy request goes to it.
	if c := f.counts(); c.OfKind(ClassRealm, "POST") != 4 || c.OnHost(DefaultRealmHost) != 4 {
		t.Fatalf("counts %+v", c)
	}
	if r := f.do("GET", f.run.RealmURL+DefaultRealmPath, nil); r.status != 405 {
		t.Fatalf("GET on the token endpoint: %d", r.status)
	}
	if r := f.get(f.run.RealmURL + "/elsewhere"); r.status != 404 {
		t.Fatalf("other path on the realm host: %d", r.status)
	}
}

func TestR_ANS_032_RealmDoesNotGateWhenOff(t *testing.T) {
	f := newFixture(t, Config{})
	if err := f.stub.SetRealm(Realm{RefreshTokens: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	if r := f.get(f.run.APIRoot()); r.status != 200 {
		t.Fatalf("%d", r.status)
	}
	if err := f.stub.SetRealm(Realm{AccessTTLSeconds: -1}); err == nil {
		t.Fatal("a negative lifetime was accepted")
	}
}

func TestR_ANS_032_ThrottleAnswersLikeARateLimitedUpstream(t *testing.T) {
	f := newFixture(t, Config{})
	if err := f.stub.SetThrottle(Throttle{Mode: ThrottleTooManyRequests, Class: ClassIndex, RetryAfterSeconds: 7, Nth: 2}); err != nil {
		t.Fatal(err)
	}
	idx := f.api("v3/plugin/ansible/content/published/collections/index/")
	if r := f.get(idx); r.status != 200 {
		t.Fatalf("first: %d", r.status)
	}
	r := f.get(idx)
	if r.status != 429 || r.header.Get("Retry-After") != "7" {
		t.Fatalf("second: %d %q", r.status, r.header.Get("Retry-After"))
	}
	if r := f.get(idx); r.status != 200 {
		t.Fatalf("third: %d", r.status)
	}
	// Requests of another class are neither counted nor throttled.
	if r := f.get(f.run.APIRoot()); r.status != 200 {
		t.Fatalf("root: %d", r.status)
	}
	if st := f.stub.ThrottleState(); st.Seen != 3 || st.Throttled != 1 {
		t.Fatalf("state %+v", st)
	}
	if err := f.stub.SetThrottle(Throttle{Mode: "418"}); err == nil {
		t.Fatal("an unknown mode was accepted")
	}
}

func TestR_ANS_032_DownAndFailStatusModes(t *testing.T) {
	f := newFixture(t, Config{})
	if err := f.stub.SetMode(Mode{FailStatus: map[string]int{ClassArtifact: 502}}); err != nil {
		t.Fatal(err)
	}
	if r := f.get(f.api("v3/plugin/ansible/content/published/collections/artifacts/acme-base-1.0.0.tar.gz")); r.status != 502 {
		t.Fatalf("artifact: %d", r.status)
	}
	if r := f.get(f.run.APIRoot()); r.status != 200 {
		t.Fatalf("root: %d", r.status)
	}
	if err := f.stub.SetMode(Mode{Down: DownUnavailable}); err != nil {
		t.Fatal(err)
	}
	if r := f.get(f.run.APIRoot()); r.status != 503 {
		t.Fatalf("down: %d", r.status)
	}
	// The realm is not part of the upstream that is down.
	if r := f.do("POST", f.run.RealmURL+DefaultRealmPath, strings.NewReader("grant_type=refresh_token"), "Content-Type", "application/x-www-form-urlencoded"); r.status != 400 {
		t.Fatalf("realm while down: %d", r.status)
	}
	if err := f.stub.SetMode(Mode{Down: DownReset}); err != nil {
		t.Fatal(err)
	}
	if res, err := f.client.Get(f.run.APIRoot()); err == nil {
		_ = res.Body.Close()
		t.Fatal("a reset connection answered")
	}
	// Counted, as a cut answer.
	waitFor(t, func() bool {
		for _, e := range f.counts().Entries {
			if e.Statuses["0"] > 0 {
				return true
			}
		}
		return false
	})
	if err := f.stub.SetMode(Mode{DelayMS: map[string]int{ClassRoot: 5}}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	f.get(f.run.APIRoot())
	if time.Since(start) < 5*time.Millisecond {
		t.Fatal("the delay did not apply")
	}
	for _, bad := range []Mode{{Down: "x"}, {DelayMS: map[string]int{"nope": 1}}, {FailStatus: map[string]int{ClassRoot: 404}}} {
		if bad.Validate() == nil {
			t.Errorf("mode %+v was accepted", bad)
		}
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestR_ANS_032_ControlAPI(t *testing.T) {
	f := newFixture(t, Config{ControlToken: testToken})
	if r := f.do("GET", f.run.URL+"/-/stub/counts", nil); r.status != 401 {
		t.Fatalf("without a token: %d", r.status)
	}
	f.get(f.run.APIRoot())
	var c Counts
	mustJSON(t, f.control("GET", "counts", "").body, &c)
	if c.Requests("GET", "/api/") != 1 || c.Total() != 1 {
		t.Fatalf("counts %+v", c)
	}
	if r := f.control("POST", "mode", `{"gzip":true,"digest_changed":["acme.tools"]}`); r.status != 200 {
		t.Fatalf("mode: %d %s", r.status, r.body)
	}
	if m := f.stub.Mode(); !m.Gzip || len(m.DigestChanged) != 1 {
		t.Fatalf("mode %+v", m)
	}
	if r := f.control("POST", "mode", `{"unknown":1}`); r.status != 400 {
		t.Fatalf("unknown field: %d", r.status)
	}
	if r := f.control("POST", "mode", `{"down":"x"}`); r.status != 400 {
		t.Fatalf("invalid mode: %d", r.status)
	}
	if r := f.control("POST", "throttle", `{"mode":"503","retry_after_seconds":1,"count":-1}`); r.status != 200 {
		t.Fatalf("throttle: %d %s", r.status, r.body)
	}
	if r := f.control("POST", "throttle", `{}`); r.status != 200 {
		t.Fatalf("clear throttle: %d", r.status)
	}
	if r := f.control("POST", "realm", `{"require":true,"refresh_tokens":["a"]}`); r.status != 200 {
		t.Fatalf("realm: %d %s", r.status, r.body)
	}
	if r := f.control("POST", "realm/expire", ""); r.status != 200 || r.json(t)["revoked"] != float64(0) {
		t.Fatalf("expire: %d %s", r.status, r.body)
	}
	if r := f.control("POST", "realm", `{}`); r.status != 200 {
		t.Fatalf("clear realm: %d", r.status)
	}
	if r := f.control("POST", "versions/drop", `{"collection":"acme.base","version":"1.0.0"}`); r.status != 204 {
		t.Fatalf("drop: %d %s", r.status, r.body)
	}
	if r := f.control("POST", "versions/drop", `{"collection":"acme.base","version":"1.0.0"}`); r.status != 404 {
		t.Fatalf("drop twice: %d", r.status)
	}
	if r := f.control("POST", "versions/add", `{"collection":"acme.base","version":"x"}`); r.status != 400 {
		t.Fatalf("add invalid: %d", r.status)
	}
	if r := f.control("POST", "versions/add", `{"collection":"acme.base","version":"1.0.0"}`); r.status != 204 {
		t.Fatalf("add: %d", r.status)
	}
	var corpus CorpusInfo
	mustJSON(t, f.control("GET", "corpus", "").body, &corpus)
	if len(corpus.Collections) != 5 || len(corpus.Roles) != 3 || corpusDigest(t, corpus, "acme", "base", "1.2.0") == "" {
		t.Fatalf("corpus %+v", corpus)
	}
	for _, c := range corpus.Collections {
		if c.Name == "versions" && len(c.Versions) != 250 {
			t.Fatalf("many.versions has %d versions", len(c.Versions))
		}
	}
	if r := f.control("POST", "reset?all=true", ""); r.status != 204 {
		t.Fatalf("reset: %d", r.status)
	}
	if m := f.stub.Mode(); m.Gzip || f.counts().Total() != 0 {
		t.Fatalf("after reset all: %+v", m)
	}
	var in Inflight
	mustJSON(t, f.control("GET", "inflight", "").body, &in)
	if in.Open != 0 {
		t.Fatalf("inflight %+v", in)
	}
	if r := f.control("GET", "nothing", ""); r.status != 404 {
		t.Fatalf("unknown control path: %d", r.status)
	}

	off := newFixture(t, Config{})
	if r := off.control("GET", "counts", ""); r.status != 404 {
		t.Fatalf("control API without a token configured: %d", r.status)
	}
}

func TestR_ANS_032_InflightCountsConcurrentRequests(t *testing.T) {
	f := newFixture(t, Config{})
	// A hung request stays open until its client leaves, so the count is reached by an event and not by a delay.
	if err := f.stub.SetMode(Mode{Down: DownHang}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for range 3 {
		go func() {
			req, _ := http.NewRequestWithContext(ctx, http.MethodGet, f.run.APIRoot(), nil)
			if res, err := f.client.Do(req); err == nil {
				_ = res.Body.Close()
			}
		}()
	}
	waitFor(t, func() bool { return f.stub.Inflight().Open == 3 })
	cancel()
	waitFor(t, func() bool { return f.stub.Inflight().Open == 0 })
	if in := f.stub.Inflight(); in.Requests != 3 || in.Max != 3 {
		t.Fatalf("inflight %+v", in)
	}
}

func TestR_ANS_032_OnlyReadsAreServed(t *testing.T) {
	f := newFixture(t, Config{})
	r := f.do("POST", f.api("v3/artifacts/collections/"), strings.NewReader("x"))
	if r.status != http.StatusMethodNotAllowed || r.header.Get("Allow") != "GET, HEAD" {
		t.Fatalf("%d %v", r.status, r.header)
	}
	if h := f.do("HEAD", f.run.APIRoot(), nil); h.status != 200 || len(h.body) != 0 {
		t.Fatalf("HEAD: %d, %d bytes", h.status, len(h.body))
	}
}

func TestR_ANS_032_ConfigIsValidated(t *testing.T) {
	if _, err := New(Config{GitHubHost: "same.localhost", CodeloadHost: "same.localhost"}); err == nil {
		t.Error("two hosts with one name were accepted")
	}
	if _, err := New(Config{RealmPath: "token"}); err == nil {
		t.Error("a realm path without a slash was accepted")
	}
	if _, err := New(Config{Fillers: -1}); err == nil {
		t.Error("negative fillers were accepted")
	}
	if _, err := New(Config{}); err != nil {
		t.Error(err)
	}
}

func TestR_ANS_032_StartNeedsATLSCertificate(t *testing.T) {
	s, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Start("127.0.0.1:0", nil); err == nil {
		t.Fatal("the stub started without a certificate")
	}
}

func TestSemverPrecedence(t *testing.T) {
	ordered := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0", "10.0.0"}
	for i := range ordered {
		for j := range ordered {
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := compareVersions(ordered[i], ordered[j]); got != want {
				t.Errorf("compare(%s, %s) = %d, want %d", ordered[i], ordered[j], got, want)
			}
		}
	}
	if compareVersions("1.0.0+a", "1.0.0+b") != 0 {
		t.Error("build metadata changed the precedence")
	}
	for _, bad := range []string{"1.0", "01.0.0", "1.0.0-", "a.b.c", ""} {
		if _, err := parseSemver(bad); err == nil {
			t.Errorf("%q was parsed", bad)
		}
	}
}

func mustJSON(t *testing.T, b []byte, into any) {
	t.Helper()
	if err := json.Unmarshal(b, into); err != nil {
		t.Fatalf("%s: %v", b, err)
	}
}
