//go:build integration

package registry

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/registrytoken"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// R-OCI-001, part of its criteria: an image pushed under a repository's name is pulled back by tag and by digest, its blobs and tags
// are served, the unknown names answer 404 NAME_UNKNOWN, and raw access cannot write a tag. Not proven here, so the ID stays out of
// the name: the push of a real client (the blobs are seeded, the upload routes are another work package's), the pull through the
// virtual repository web/docker and the listing of the formats API.
func TestImagesArePushedAndPulledThroughV2(t *testing.T) {
	e := newEnv(t, nil)
	img := newImage("app")
	const name = "web/docker-local/team/app"
	if a := e.push(bearer(e.admin), name, "1.0", img); a.Code != http.StatusCreated {
		t.Fatalf("push: %d %s", a.Code, a.Body.String())
	}
	want := digestOf(img.manifest)

	byTag := e.send("default", req{method: "GET", path: "/v2/" + name + "/manifests/1.0", auth: bearer(e.admin), header: map[string]string{"Accept": manifestType}})
	if byTag.Code != http.StatusOK || byTag.Body.String() != string(img.manifest) || byTag.Header().Get("Docker-Content-Digest") != want {
		t.Fatalf("by tag: %d %v %s", byTag.Code, byTag.Header(), byTag.Body.String())
	}
	if byDigest := e.send("default", req{method: "GET", path: "/v2/" + name + "/manifests/" + want, auth: bearer(e.admin), header: map[string]string{"Accept": manifestType}}); byDigest.Code != http.StatusOK || byDigest.Body.String() != string(img.manifest) {
		t.Fatalf("by digest: %d %s", byDigest.Code, byDigest.Body.String())
	}
	if blob := e.send("default", req{method: "GET", path: "/v2/" + name + "/blobs/" + digestOf(img.layer), auth: bearer(e.admin)}); blob.Code != http.StatusOK || blob.Body.String() != string(img.layer) {
		t.Fatalf("blob: %d", blob.Code)
	}
	tags := e.send("default", req{method: "GET", path: "/v2/" + name + "/tags/list", auth: bearer(e.admin)})
	if tags.Code != http.StatusOK || !strings.Contains(tags.Body.String(), `"1.0"`) {
		t.Fatalf("tags: %d %s", tags.Code, tags.Body.String())
	}
	// A blob is held under the image name that uploaded it and under no other (R-OCI-010).
	if other := e.send("default", req{method: "GET", path: "/v2/web/docker-local/other/blobs/" + digestOf(img.layer), auth: bearer(e.admin)}); other.Code != http.StatusNotFound {
		t.Fatalf("the blob under another name: %d", other.Code)
	}

	for _, p := range []string{"/v2/web/nope/app/manifests/1", "/v2/nope/docker-local/app/manifests/1", "/v2/web/docker-local"} {
		if a := e.send("default", req{method: "GET", path: p, auth: bearer(e.admin)}); a.Code != http.StatusNotFound || a.code(t) != "NAME_UNKNOWN" {
			t.Errorf("%s: %d %s", p, a.Code, a.Body.String())
		}
	}

	// Manifests and tags are written through the protocol only: a raw write of a tag is refused and stores nothing.
	raw := e.send("default", req{method: "PUT", path: "/files/web/docker-local/team/app/_tags/2.0", auth: bearer(e.admin), body: img.manifest})
	if raw.Code != http.StatusConflict {
		t.Fatalf("raw tag write: %d %s", raw.Code, raw.Body.String())
	}
	if a := e.send("default", req{method: "GET", path: "/v2/" + name + "/manifests/2.0", auth: bearer(e.admin), header: map[string]string{"Accept": manifestType}}); a.Code != http.StatusNotFound {
		t.Fatalf("the refused raw write stored a tag: %d", a.Code)
	}
}

// R-OCI-003: the version check with the credential literally as `curl -u x:$TOKEN <base>/v2/` sends it.
func TestR_OCI_003_TheVersionCheckAcceptsBasicWithAnyUserName(t *testing.T) {
	e := newEnv(t, nil)
	a := e.send("default", req{method: "GET", path: "/v2/", auth: basic("x", e.admin)})
	if a.Code != http.StatusOK || a.Body.String() != "{}" || a.Header().Get(registryAPIVersionHeader) != "registry/2.0" {
		t.Fatalf("%d %v %q", a.Code, a.Header(), a.Body.String())
	}
	if a := e.send("default", req{method: "GET", path: "/v2/", auth: basic("x", "trv_pat_wrong")}); a.Code != http.StatusUnauthorized {
		t.Fatalf("a wrong secret: %d", a.Code)
	}
}

const registryAPIVersionHeader = "Docker-Distribution-API-Version"

// R-OCI-004: the login sequences of the two real clients, request by request as they send them.
func TestR_OCI_004_TheLoginSequencesOfDockerAndContainerd(t *testing.T) {
	e := newEnv(t, nil)
	const challenge = `Bearer realm="http://trove.test/v2/_token",service="trove.test"`
	tokenOf := func(a answer) string {
		t.Helper()
		var b struct {
			Token       string `json:"token"`
			AccessToken string `json:"access_token"`
			ExpiresIn   int    `json:"expires_in"`
			IssuedAt    string `json:"issued_at"`
		}
		if err := json.Unmarshal(a.Body.Bytes(), &b); err != nil || b.Token == "" || b.Token != b.AccessToken || b.ExpiresIn <= 0 || b.IssuedAt == "" {
			t.Fatalf("token answer %d %q: %v", a.Code, a.Body.String(), err)
		}
		return b.Token
	}
	t.Run("docker login: ping, token with Basic, ping with the Bearer", func(t *testing.T) {
		ping := e.send("default", req{method: "GET", path: "/v2/", header: map[string]string{"User-Agent": "docker/27.3.1 go/go1.22.7 os/linux arch/amd64"}})
		if ping.Code != http.StatusUnauthorized || ping.Header().Get("WWW-Authenticate") != challenge || ping.Header().Get(registryAPIVersionHeader) != "registry/2.0" {
			t.Fatalf("ping: %d %v", ping.Code, ping.Header())
		}
		// The query is the one Docker's token handler writes for `docker login`.
		tok := e.send("default", req{
			method: "GET",
			path:   "/v2/_token?account=rita&client_id=docker&offline_token=true&service=trove.test", auth: basic("rita", e.admin),
			header: map[string]string{"User-Agent": "docker/27.3.1 go/go1.22.7 os/linux arch/amd64"},
		})
		if tok.Code != http.StatusOK || tok.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("token: %d %v %s", tok.Code, tok.Header(), tok.Body.String())
		}
		if a := e.send("default", req{method: "GET", path: "/v2/", auth: bearer(tokenOf(tok))}); a.Code != http.StatusOK || a.Body.String() != "{}" {
			t.Fatalf("ping with the Bearer: %d %s", a.Code, a.Body.String())
		}
	})
	t.Run("docker pull: the token request names the scope the 401 named", func(t *testing.T) {
		// A registry token for no scope meets the manifest route: the challenge names what to ask for, and asking for it works.
		empty := tokenOf(e.send("default", req{method: "GET", path: "/v2/_token?service=trove.test", auth: basic("rita", e.admin)}))
		a := e.send("default", req{method: "GET", path: "/v2/web/docker-local/app/manifests/1", auth: bearer(empty)})
		scopeLine := `scope="repository:web/docker-local/app:pull"`
		if a.Code != http.StatusUnauthorized || !strings.Contains(a.Header().Get("WWW-Authenticate"), scopeLine) {
			t.Fatalf("%d %v", a.Code, a.Header())
		}
		withScope := tokenOf(e.send("default", req{
			method: "GET",
			path:   "/v2/_token?scope=" + url.QueryEscape("repository:web/docker-local/app:pull") + "&service=trove.test", auth: basic("rita", e.admin),
		}))
		if a := e.send("default", req{method: "GET", path: "/v2/web/docker-local/app/manifests/1", auth: bearer(withScope)}); a.Code != http.StatusNotFound || a.code(t) != "MANIFEST_UNKNOWN" {
			t.Fatalf("the granted pull reaches the manifest route: %d %s", a.Code, a.Body.String())
		}
	})
	t.Run("containerd: the POST form with the charset, the password grant", func(t *testing.T) {
		form := url.Values{
			"grant_type": {"password"}, "service": {"trove.test"}, "client_id": {"containerd-client"},
			"username": {"rita"}, "password": {e.admin}, "scope": {"repository:web/docker-local/app:pull,push"},
		}
		a := e.send("default", req{
			method: "POST", path: "/v2/_token", body: []byte(form.Encode()),
			header: map[string]string{"Content-Type": "application/x-www-form-urlencoded; charset=utf-8", "User-Agent": "containerd/v1.7.22"},
		})
		if a.Code != http.StatusOK {
			t.Fatalf("%d %s", a.Code, a.Body.String())
		}
		rgt := tokenOf(a)
		img := newImage("containerd")
		if a := e.push(bearer(rgt), "web/docker-local/app", "1", img); a.Code != http.StatusCreated {
			t.Fatalf("a push with the containerd token: %d %s", a.Code, a.Body.String())
		}
		// The grant is the scope that was asked for and no more.
		if a := e.send("default", req{method: "DELETE", path: "/v2/web/docker-local/app/manifests/" + digestOf([]byte("x")), auth: bearer(rgt)}); a.Code != http.StatusUnauthorized {
			t.Fatalf("a delete with a pull,push token: %d", a.Code)
		}
	})
	t.Run("a refused login is 401 in the Docker shape", func(t *testing.T) {
		a := e.send("default", req{method: "GET", path: "/v2/_token?account=rita&client_id=docker&offline_token=true&service=trove.test", auth: basic("rita", "trv_pat_nope")})
		if a.Code != http.StatusUnauthorized || a.code(t) != "UNAUTHORIZED" || a.Header().Get("WWW-Authenticate") == "" {
			t.Fatalf("%d %v %s", a.Code, a.Header(), a.Body.String())
		}
	})
}

// R-OCI-005: a grant is a ceiling and the decision point still decides every request: once the caller's deploy grant is
// removed, the next push is refused, with the token the caller already holds.
func TestR_OCI_005_RemovingTheDeployGrantStopsThePush(t *testing.T) {
	e := newEnv(t, nil)
	const name = "web/docker-local/pushed"
	img := newImage("pushed")
	if a := e.push(bearer(e.pusher), name, "1", img); a.Code != http.StatusCreated {
		t.Fatalf("the first push: %d %s", a.Code, a.Body.String())
	}
	rgt := e.mint("default", "", e.pusher, registrytoken.Grant{Entries: []registrytoken.Entry{{Repo: local, Name: "pushed", Actions: registrytoken.Pull | registrytoken.Push}}}, false)
	if a := e.push(bearer(rgt), name, "2", img); a.Code != http.StatusCreated {
		t.Fatalf("a push with the registry token: %d %s", a.Code, a.Body.String())
	}

	admin := authz.Subject{Kind: principal.KindUser, ID: e.rita.ID, Name: e.rita.Name}
	if _, err := e.rep.App.AuthzService().Delete(e.t.Context(), admin, "oci-push-deploy", errs.AnyRevision); err != nil {
		t.Fatal(err)
	}
	tag := 2
	testenv.WaitFor(t, "the push refused on this replica", 10*time.Second, func() bool {
		tag++
		return e.send("default", req{
			method: "PUT", path: "/v2/" + name + "/manifests/" + strconv.Itoa(tag), auth: bearer(rgt), body: img.manifest,
			header: map[string]string{"Content-Type": manifestType},
		}).Code == http.StatusForbidden
	})
	a := e.send("default", req{
		method: "PUT", path: "/v2/" + name + "/manifests/final", auth: bearer(rgt), body: img.manifest,
		header: map[string]string{"Content-Type": manifestType},
	})
	if a.Code != http.StatusForbidden || a.code(t) != "DENIED" {
		t.Fatalf("%d %s", a.Code, a.Body.String())
	}
	// The same caller reads on: only the removed action is gone.
	if a := e.send("default", req{method: "GET", path: "/v2/" + name + "/manifests/1", auth: bearer(e.pusher), header: map[string]string{"Accept": manifestType}}); a.Code != http.StatusOK {
		t.Fatalf("the read: %d %s", a.Code, a.Body.String())
	}
}

// R-OCI-006 / W3-5: the per-image decision on /v2/ is the handlers': a token whose scope grants read for the image names team/*
// only reads those, with the trove token presented directly.
func TestPathScopedGrantsAreDecidedPerImageOnV2(t *testing.T) {
	e := newEnv(t, nil)
	if a := e.push(bearer(e.admin), "web/docker-local/team/app", "1", newImage("a")); a.Code != http.StatusCreated {
		t.Fatalf("%d %s", a.Code, a.Body.String())
	}
	if a := e.push(bearer(e.admin), "web/docker-local/other/app", "1", newImage("b")); a.Code != http.StatusCreated {
		t.Fatalf("%d %s", a.Code, a.Body.String())
	}
	get := func(tok, name string) int {
		return e.send("default", req{method: "GET", path: "/v2/web/docker-local/" + name + "/manifests/1", auth: bearer(tok), header: map[string]string{"Accept": manifestType}}).Code
	}
	if c := get(e.scoped, "team/app"); c != http.StatusOK {
		t.Fatalf("an image the scope names: %d", c)
	}
	if c := get(e.scoped, "other/app"); c == http.StatusOK {
		t.Fatalf("an image the scope does not name was served")
	}
	// The same through a registry token the scoped caller obtained for both images: the token service grants only what the
	// decision point allows, and the handler decides again.
	rgt := e.mint("default", "", e.scoped, registrytoken.Grant{Entries: []registrytoken.Entry{
		{Repo: local, Name: "team/app", Actions: registrytoken.Pull}, {Repo: local, Name: "other/app", Actions: registrytoken.Pull},
	}}, false)
	if c := get(rgt, "team/app"); c != http.StatusOK {
		t.Fatalf("registry token, image in scope: %d", c)
	}
	if c := get(rgt, "other/app"); c == http.StatusOK {
		t.Fatalf("registry token, image out of scope was served")
	}
}

// R-OCI-017 through /v2/_catalog: the names the caller may read, from the data port's manifests, in byte order and in pages.
func TestR_OCI_017_TheCatalogListsWhatTheCallerMayRead(t *testing.T) {
	e := newEnv(t, nil)
	img := newImage("c")
	for _, n := range []string{"web/docker-local/alpine", "web/docker-local/team/app", "web/docker-local/team/lib", "web/docker-local/zed", "web/team-local/team/x", "web/team-local/y"} {
		if a := e.push(bearer(e.admin), n, "1", img); a.Code != http.StatusCreated {
			t.Fatalf("push %s: %d %s", n, a.Code, a.Body.String())
		}
	}
	list := func(auth, query string) (names []string, link string, code int) {
		a := e.send("default", req{method: "GET", path: "/v2/_catalog" + query, auth: auth})
		if a.Code == http.StatusOK {
			var body struct {
				Repositories []string `json:"repositories"`
			}
			if err := json.Unmarshal(a.Body.Bytes(), &body); err != nil || body.Repositories == nil {
				t.Fatalf("body %q: %v", a.Body.String(), err)
			}
			names = body.Repositories
		}
		return names, a.Header().Get("Link"), a.Code
	}
	all := []string{"web/docker-local/alpine", "web/docker-local/team/app", "web/docker-local/team/lib", "web/docker-local/zed", "web/team-local/team/x", "web/team-local/y"}

	if got, link, code := list(bearer(e.admin), ""); code != 200 || !slices.Equal(got, all) || link != "" {
		t.Fatalf("administrator: %d %v %q", code, got, link)
	}
	if got, _, _ := list(bearer(e.reader), ""); !slices.Equal(got, all[:4]) {
		t.Fatalf("a caller with read on web/docker-local only: %v", got)
	}
	if got, _, _ := list(bearer(e.scoped), ""); !slices.Equal(got, []string{"web/docker-local/team/app", "web/docker-local/team/lib"}) {
		t.Fatalf("a package-scoped token: %v", got)
	}
	if got, _, code := list(bearer(e.nobody), ""); code != 200 || len(got) != 0 {
		t.Fatalf("a caller who may read nothing: %d %v", code, got)
	}
	if _, _, code := list("", ""); code != http.StatusUnauthorized {
		t.Fatalf("no credential: %d", code)
	}
	// With anonymous access on and no anonymous grant on any repository, the anonymous caller gets an empty list.
	e.setAnonymous(true)
	if got, _, code := list("", ""); code != http.StatusOK || len(got) != 0 {
		t.Fatalf("anonymous without an anonymous grant: %d %v", code, got)
	}
	e.setAnonymous(false)
	// The scoped token with n=1 pages: the Link names the last name and following it ends without a Link.
	first, link, _ := list(bearer(e.scoped), "?n=1")
	if !slices.Equal(first, []string{"web/docker-local/team/app"}) || link != `</v2/_catalog?last=web/docker-local/team/app&n=1>; rel="next"` {
		t.Fatalf("first page %v %q", first, link)
	}
	if second, link, _ := list(bearer(e.scoped), "?n=1&last=web/docker-local/team/app"); !slices.Equal(second, []string{"web/docker-local/team/lib"}) || link != "" {
		t.Fatalf("second page %v %q", second, link)
	}
	var seen []string
	query := "?n=4"
	for i := 0; i < 5; i++ {
		got, link, _ := list(bearer(e.admin), query)
		seen = append(seen, got...)
		if link == "" {
			break
		}
		query = "?n=4&last=" + got[len(got)-1]
	}
	if !slices.Equal(seen, all) {
		t.Fatalf("pages %v", seen)
	}
	if _, _, code := list(bearer(e.admin), "?n=-1"); code != http.StatusBadRequest {
		t.Fatalf("n=-1: %d", code)
	}

	// A registry token needs registry:catalog:* and lists what its trove token may read.
	rgt := e.mint("default", "", e.reader, registrytoken.Grant{Catalog: true}, false)
	if got, _, code := list(bearer(rgt), ""); code != 200 || !slices.Equal(got, all[:4]) {
		t.Fatalf("registry token with the catalog grant: %d %v", code, got)
	}
	noCatalog := e.mint("default", "", e.reader, registrytoken.Grant{}, false)
	a := e.send("default", req{method: "GET", path: "/v2/_catalog", auth: bearer(noCatalog)})
	if a.Code != http.StatusUnauthorized || !strings.Contains(a.Header().Get("WWW-Authenticate"), `scope="registry:catalog:*"`) {
		t.Fatalf("registry token without it: %d %v", a.Code, a.Header())
	}

	// On a bound host the bound repository's names come without a prefix.
	bound := e.send("mirror", req{method: "GET", path: "/v2/_catalog", host: mirrorHost, auth: bearer(e.admin)})
	var body struct {
		Repositories []string `json:"repositories"`
	}
	if err := json.Unmarshal(bound.Body.Bytes(), &body); err != nil || !slices.Equal(body.Repositories, []string{"alpine", "team/app", "team/lib", "zed"}) {
		t.Fatalf("bound host: %d %s", bound.Code, bound.Body.String())
	}
}
