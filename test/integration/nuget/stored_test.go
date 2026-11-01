//go:build integration

package nuget

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

const guard = 30 * time.Second

func decode(t testing.TB, a testenv.Answer) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(a.Body, &m); err != nil {
		t.Fatalf("%d %q is no JSON object: %v", a.Status, a.Body, err)
	}
	return m
}

func gunzip(t testing.TB, b []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func strs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

// leaves returns the catalog entries of the registration index of a small package, in order.
func leaves(t testing.TB, idx map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, p := range idx["items"].([]any) {
		for _, l := range p.(map[string]any)["items"].([]any) {
			out = append(out, l.(map[string]any)["catalogEntry"].(map[string]any))
		}
	}
	return out
}

// TestR_NUGET_008_TheDocumentsOfPushedVersions pushes versions through the whole stack and reads
// every document and file of them back: the version list in version order, the package byte for
// byte, the generated .nuspec, the registration index, a page and a leaf in the hives with the
// metadata the push extracted, the weak ETag and its 304, gzip, two replicas, and what an
// unlist and a hard delete do to them (R-NUGET-008, R-NUGET-009, R-NUGET-030).
func TestR_NUGET_008_TheDocumentsOfPushedVersions(t *testing.T) {
	e := newWriteEnv(t)
	a, b := e.replica("web-a"), e.replica("web-b")
	const feed = "/api/nuget/nuget-local/v3/"
	pkgs := map[string][]byte{}
	for _, v := range []string{"1.2.3", "1.0.0", "1.1.0-ci.5", "1.3.0-beta.1+sha.abc"} {
		s := pkgSpec{id: "Acme.Widgets", version: v, title: "Widget Toolkit", tags: "ui toolkit"}
		if v == "1.0.0" {
			s.deps = true
		}
		pkgs[v] = nupkg(t, s)
		e.mustPush(a, "nuget-local", e.tokDev, pkgs[v])
	}
	get := func(r *testenv.Replica, path string, hdr ...string) testenv.Answer {
		return e.st.Do(r, "GET", feed+path, e.tokReader, nil, hdr...)
	}

	t.Run("the flat container lists the versions in version order, lower-case and without metadata", func(t *testing.T) {
		ans := get(a, "flatcontainer/acme.widgets/index.json")
		if ans.Status != 200 {
			t.Fatalf("%d %s", ans.Status, ans.Body)
		}
		got := strs(decode(t, ans)["versions"])
		if want := []string{"1.0.0", "1.1.0-ci.5", "1.2.3", "1.3.0-beta.1"}; strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("versions = %v, want %v", got, want)
		}
		if upper := get(a, "flatcontainer/Acme.Widgets/index.json"); upper.Status != 200 || !bytes.Equal(upper.Body, ans.Body) {
			t.Errorf("the mixed-case ID answers %d, a different body", upper.Status)
		}
	})

	t.Run("the package is served byte for byte and the .nuspec is the generated one", func(t *testing.T) {
		ans := get(a, "flatcontainer/acme.widgets/1.0.0/acme.widgets.1.0.0.nupkg")
		if ans.Status != 200 || !bytes.Equal(ans.Body, pkgs["1.0.0"]) {
			t.Fatalf("%d, %d bytes, want the %d pushed", ans.Status, len(ans.Body), len(pkgs["1.0.0"]))
		}
		if ct := ans.Header.Get("Content-Type"); ct != "application/octet-stream" {
			t.Errorf("package content type %q", ct)
		}
		ns := get(a, "flatcontainer/acme.widgets/1.0.0/acme.widgets.nuspec")
		if ns.Status != 200 || !strings.Contains(string(ns.Body), "<id>Acme.Widgets</id>") || !strings.HasPrefix(ns.Header.Get("Content-Type"), "application/xml") {
			t.Fatalf(".nuspec %d %q %q", ns.Status, ns.Header.Get("Content-Type"), ns.Body)
		}
	})

	t.Run("the registration index carries what the push extracted, per hive", func(t *testing.T) {
		semver2 := decode(t, get(a, "registration-semver2/acme.widgets/index.json"))
		ls := leaves(t, semver2)
		if len(ls) != 4 {
			t.Fatalf("semver2 hive lists %d versions, want 4", len(ls))
		}
		first := ls[0]
		if first["version"] != "1.0.0" || first["id"] != "Acme.Widgets" || first["listed"] != true || first["title"] != "Widget Toolkit" {
			t.Errorf("catalog entry = %v", first)
		}
		if got := strings.Join(strs(first["tags"]), " "); got != "ui toolkit" {
			t.Errorf("tags = %q", got)
		}
		groups := first["dependencyGroups"].([]any)
		dep := groups[0].(map[string]any)["dependencies"].([]any)[0].(map[string]any)
		if groups[0].(map[string]any)["targetFramework"] != "net8.0" || dep["id"] != "Contoso.Util" || dep["range"] != "[1.0.0, 2.0.0)" {
			t.Errorf("dependency groups = %v", groups)
		}
		if plain := leaves(t, decode(t, get(a, "registration/acme.widgets/index.json"))); len(plain) != 2 {
			t.Errorf("the SemVer 1 hive lists %d versions, want 2 (1.1.0-ci.5 has a dotted label and 1.3.0-beta.1+sha.abc has build metadata: both are SemVer 2)", len(plain))
		}
		leaf := decode(t, get(a, "registration-semver2/acme.widgets/1.2.3.json"))
		if leaf["packageContent"] != "https://"+testenv.DefaultHost+strings.TrimSuffix(feed, "/v3/")+"/v3/flatcontainer/acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg" &&
			!strings.HasSuffix(leaf["packageContent"].(string), "/v3/flatcontainer/acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg") {
			t.Errorf("leaf packageContent = %v", leaf["packageContent"])
		}
		page := get(a, "registration-semver2/acme.widgets/page/1.0.0/1.3.0-beta.1.json")
		if page.Status != 200 || decode(t, page)["count"] != float64(4) {
			t.Errorf("page %d %s", page.Status, page.Body)
		}
	})

	t.Run("a weak ETag answers 304, and gzip is sent on consent only", func(t *testing.T) {
		ans := get(a, "registration-gz/acme.widgets/index.json", "Accept-Encoding", "gzip")
		etag := ans.Header.Get("ETag")
		if ans.Status != 200 || !strings.HasPrefix(etag, `W/"`) || ans.Header.Get("Content-Encoding") != "gzip" {
			t.Fatalf("%d etag %q encoding %q", ans.Status, etag, ans.Header.Get("Content-Encoding"))
		}
		plain := get(a, "registration-gz/acme.widgets/index.json")
		if plain.Header.Get("Content-Encoding") != "" || plain.Header.Get("ETag") != etag || !bytes.Equal(plain.Body, gunzip(t, ans.Body)) {
			t.Errorf("the identity coding differs: encoding %q etag %q", plain.Header.Get("Content-Encoding"), plain.Header.Get("ETag"))
		}
		if nm := get(a, "registration-gz/acme.widgets/index.json", "If-None-Match", etag); nm.Status != http.StatusNotModified || len(nm.Body) != 0 {
			t.Errorf("conditional GET: %d with %d bytes", nm.Status, len(nm.Body))
		}
	})

	t.Run("two replicas render the same bytes and the same ETag", func(t *testing.T) {
		for _, p := range []string{"flatcontainer/acme.widgets/index.json", "registration-semver2/acme.widgets/index.json", "registration/acme.widgets/1.2.3.json"} {
			x, y := get(a, p), get(b, p)
			if x.Status != 200 || sha256.Sum256(x.Body) != sha256.Sum256(y.Body) || x.Header.Get("ETag") != y.Header.Get("ETag") {
				t.Errorf("%s differs between the replicas (%d, %d)", p, x.Status, y.Status)
			}
		}
	})

	t.Run("an unlisted version stays in the documents, marked unlisted, and the ETag moves", func(t *testing.T) {
		before := get(a, "registration-semver2/acme.widgets/index.json").Header.Get("ETag")
		if ans := e.send(a, "DELETE", "nuget-local", "acme.widgets/1.2.3", e.tokDev); ans.Status != 204 {
			t.Fatalf("unlist: %d %s", ans.Status, ans.Body)
		}
		after := get(b, "registration-semver2/acme.widgets/index.json")
		if after.Header.Get("ETag") == before {
			t.Error("the ETag did not change with the unlist")
		}
		var seen bool
		for _, l := range leaves(t, decode(t, after)) {
			if l["version"] == "1.2.3" {
				seen = true
				if l["listed"] != false {
					t.Errorf("1.2.3 listed = %v, want false", l["listed"])
				}
			}
		}
		if !seen {
			t.Error("the unlisted version left the registration")
		}
		if got := strs(decode(t, get(b, "flatcontainer/acme.widgets/index.json"))["versions"]); len(got) != 4 {
			t.Errorf("flat container lists %v, want all four", got)
		}
		if pk := get(b, "flatcontainer/acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg"); pk.Status != 200 {
			t.Errorf("an unlisted package is served, got %d", pk.Status)
		}
		if ans := e.send(a, "POST", "nuget-local", "acme.widgets/1.2.3", e.tokDev); ans.Status != 200 {
			t.Fatalf("relist: %d", ans.Status)
		}
	})

	t.Run("a hard delete takes the version out of every document and leaves a tombstone", func(t *testing.T) {
		const hard = "/api/nuget/nuget-hard/v3/"
		only := nupkg(t, pkgSpec{id: "Acme.Gadgets", version: "1.0.0"})
		e.mustPush(a, "nuget-hard", e.tokDev, only)
		if ans := e.st.Do(a, "GET", hard+"flatcontainer/acme.gadgets/index.json", e.tokReader, nil); ans.Status != 200 {
			t.Fatalf("before: %d %s", ans.Status, ans.Body)
		}
		if ans := e.send(a, "DELETE", "nuget-hard", "acme.gadgets/1.0.0", e.tokDev); ans.Status != 204 {
			t.Fatalf("delete: %d %s", ans.Status, ans.Body)
		}
		for _, p := range []string{"flatcontainer/acme.gadgets/index.json", "flatcontainer/acme.gadgets/1.0.0/acme.gadgets.1.0.0.nupkg", "registration-semver2/acme.gadgets/index.json", "registration/acme.gadgets/1.0.0.json"} {
			if ans := e.st.Do(b, "GET", hard+p, e.tokReader, nil); ans.Status != 404 {
				t.Errorf("%s after the delete: %d", p, ans.Status)
			}
		}
		if kind, _ := e.tombstone("nuget-hard", "acme.gadgets", "1.0.0"); kind != "removed" {
			t.Errorf("tombstone kind %q", kind)
		}
	})
}

type searchResult struct {
	TotalHits int `json:"totalHits"`
	Data      []struct {
		ID      string `json:"id"`
		Version string `json:"version"`
	} `json:"data"`
}

// TestR_NUGET_010_SearchFindsWhatWasPushed pushes packages and searches them: free text over the
// title and the tags, the id field, the pre-release switch, paging with an exact total, and
// the disappearance of an unlisted version (R-NUGET-010).
func TestR_NUGET_010_SearchFindsWhatWasPushed(t *testing.T) {
	e := newWriteEnv(t)
	a := e.replica("web-a")
	for _, s := range []pkgSpec{
		{id: "Acme.Widgets", version: "1.0.0", title: "Widget Toolkit", tags: "ui toolkit"},
		{id: "Acme.Widgets", version: "1.1.0", title: "Widget Toolkit", tags: "ui toolkit"},
		{id: "Acme.Gadgets", version: "2.0.0-beta.1", description: "Gadget primitives"},
		{id: "Other.Thing", version: "3.0.0", description: "Something else"},
	} {
		e.mustPush(a, "nuget-local", e.tokDev, nupkg(t, s))
	}
	search := func(r *testenv.Replica, token, query string) searchResult {
		t.Helper()
		ans := e.st.Do(r, "GET", "/api/nuget/nuget-local/v3/search?"+query, token, nil)
		if ans.Status != 200 {
			t.Fatalf("search %s: %d %s", query, ans.Status, ans.Body)
		}
		var out searchResult
		if err := json.Unmarshal(ans.Body, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	if r := search(a, e.tokReader, "q=widget"); r.TotalHits != 1 || r.Data[0].ID != "Acme.Widgets" || r.Data[0].Version != "1.1.0" {
		t.Errorf("q=widget: %+v", r)
	}
	if r := search(a, e.tokReader, "q=tags:toolkit"); r.TotalHits != 1 {
		t.Errorf("q=tags:toolkit: %+v", r)
	}
	if r := search(a, e.tokReader, "q=id:acme"); r.TotalHits != 1 {
		t.Errorf("a package with pre-release versions only is hidden by default: %+v", r)
	}
	if r := search(a, e.tokReader, "q=id:acme&prerelease=true"); r.TotalHits != 1 {
		t.Errorf("a dotted pre-release label is SemVer 2 and needs semVerLevel=2.0.0: %+v", r)
	}
	if r := search(a, e.tokReader, "q=id:acme&prerelease=true&semVerLevel=2.0.0"); r.TotalHits != 2 {
		t.Errorf("prerelease=true: %+v", r)
	}
	if r := search(a, e.tokReader, "q=id:acme&prerelease=true&semVerLevel=2.0.0&take=1&skip=1"); r.TotalHits != 2 || len(r.Data) != 1 {
		t.Errorf("paging keeps the total exact: %+v", r)
	}
	if r := search(a, e.tokReader, "q=nothing-matches-this"); r.TotalHits != 0 || len(r.Data) != 0 {
		t.Errorf("no match: %+v", r)
	}
	if ans := e.send(a, "DELETE", "nuget-local", "acme.widgets/1.1.0", e.tokDev); ans.Status != 204 {
		t.Fatalf("unlist: %d", ans.Status)
	}
	if r := search(a, e.tokReader, "q=widget"); r.TotalHits != 1 || r.Data[0].Version != "1.0.0" {
		t.Errorf("an unlisted version leaves the result, the older listed one is the latest: %+v", r)
	}
	if ans := e.st.Do(a, "GET", "/api/nuget/nuget-local/v3/search?q=widget", e.tokNobody, nil); ans.Status == 200 && func() bool {
		var r searchResult
		_ = json.Unmarshal(ans.Body, &r)
		return r.TotalHits != 0
	}() {
		t.Errorf("a caller without a grant searched: %s", ans.Body)
	}
}

func basicAuth(user, tok string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+tok))
}

// TestR_NUGET_012_PushWithTheAPIKey pushes with the header the NuGet clients send: the token as
// the key, as user:token and with the placeholder az beside Basic credentials, and E3.5: a
// revoked key gets 401 although valid Basic credentials come with it (R-NUGET-012, R-AUTH-033).
func TestR_NUGET_012_PushWithTheAPIKey(t *testing.T) {
	e := newWriteEnv(t)
	a := e.replica("web-a")
	push := func(pkg []byte, hdr ...string) testenv.Answer {
		body, ct := form(t, pkg)
		return e.st.Do(a, "PUT", "/api/nuget/nuget-local/v3/package", "", body, append([]string{"Content-Type", ct}, hdr...)...)
	}
	if ans := push(nupkg(t, pkgSpec{id: "Key.One", version: "1.0.0"}), "X-NuGet-ApiKey", e.tokDev); ans.Status != 201 {
		t.Fatalf("the token as key: %d %s", ans.Status, ans.Body)
	}
	if ans := push(nupkg(t, pkgSpec{id: "Key.Two", version: "1.0.0"}), "X-NuGet-ApiKey", "ci:"+e.tokDev); ans.Status != 201 {
		t.Fatalf("user:token as key: %d %s", ans.Status, ans.Body)
	}
	if ans := push(nupkg(t, pkgSpec{id: "Key.Three", version: "1.0.0"}), "X-NuGet-ApiKey", "az", "Authorization", basicAuth("dev", e.tokDev)); ans.Status != 201 {
		t.Fatalf("the placeholder az beside Basic credentials: %d %s", ans.Status, ans.Body)
	}
	if got := e.st.Text(`SELECT actor_id::text FROM audit_event WHERE action = 'package.published' AND path LIKE 'key.one/%' LIMIT 1`); got != e.devID {
		t.Errorf("package.published actor = %q, want the owner of the key %q", got, e.devID)
	}
	if ans := push(nupkg(t, pkgSpec{id: "Key.Four", version: "1.0.0"}), "X-NuGet-ApiKey", e.tokReader); ans.Status != http.StatusForbidden {
		t.Errorf("a key that may only read: %d %s", ans.Status, ans.Body)
	}

	// E3.5: the key is revoked; the Basic credentials beside it are valid and change nothing.
	if ans := e.st.Do(a, "POST", "/api/v1/tokens/revoke", "", map[string]string{"token": e.tokDev}); ans.Status != 202 {
		t.Fatalf("revoke by value: %d %s", ans.Status, ans.Body)
	}
	var last testenv.Answer
	testenv.WaitFor(t, "the revoked key refused", guard, func() bool {
		last = push(nupkg(t, pkgSpec{id: "Key.Five", version: "1.0.0"}), "X-NuGet-ApiKey", e.tokDev, "Authorization", basicAuth("rita", e.tokAdmin))
		return last.Status == http.StatusUnauthorized
	})
	if last.Header.Get("WWW-Authenticate") != `Basic realm="trove"` || code(t, last) != errs.CodeInvalidToken {
		t.Errorf("%d %q %v", last.Status, last.Body, last.Header)
	}
	if e.st.Number(`SELECT count(*) FROM nuget_version WHERE lower_id = 'key.five'`) != 0 {
		t.Error("the refused push stored a version")
	}
	// Without the key the same Basic credentials push.
	if ans := push(nupkg(t, pkgSpec{id: "Key.Five", version: "1.0.0"}), "Authorization", basicAuth("rita", e.tokAdmin)); ans.Status != 201 {
		t.Errorf("Basic alone: %d %s", ans.Status, ans.Body)
	}
}
