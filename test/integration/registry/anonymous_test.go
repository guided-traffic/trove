//go:build integration

package registry

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// R-OCI-004: a repository whose artifacts are public is pulled without a login, through the token flow of Docker (ping, anonymous
// token, pull), and a push without a login is refused as unauthorized. The default endpoint admits requests without a credential
// (R-EP-010); the level of the repository is what they read (R-AUTHZ-033).
func TestR_OCI_004_AnonymousPullThroughV2AndPushWithoutLoginFails(t *testing.T) {
	e := newEnv(t, func(e *env, _ *testenv.ReplicaOptions) {
		e.st.RepositoryIn("web", "docker-public", "local", "oci", "{}")
		e.st.Exec("UPDATE project SET visibility = 'public' WHERE key = 'web'")
		e.st.Exec("UPDATE repositories SET visibility_artifacts = 'public', visibility_wiki = 'public' WHERE key = 'docker-public'")
	})
	img := newImage("public")
	const name = "web/docker-public/app"
	if a := e.push(bearer(e.admin), name, "1", img); a.Code != http.StatusCreated {
		t.Fatalf("push: %d %s", a.Code, a.Body.String())
	}

	ping := e.send("default", req{method: "GET", path: "/v2/"})
	if ping.Code != http.StatusUnauthorized || !strings.HasPrefix(ping.Header().Get("WWW-Authenticate"), `Bearer realm="http://trove.test/v2/_token"`) {
		t.Fatalf("ping: %d %v", ping.Code, ping.Header())
	}
	tokenFor := func(scope string) string {
		t.Helper()
		a := e.send("default", req{method: "GET", path: "/v2/_token?service=trove.test&scope=" + url.QueryEscape(scope)})
		if a.Code != http.StatusOK {
			t.Fatalf("anonymous token for %q: %d %s", scope, a.Code, a.Body.String())
		}
		return jsonField(t, a, "token")
	}
	pull := tokenFor("repository:" + name + ":pull")
	got := e.send("default", req{method: "GET", path: "/v2/" + name + "/manifests/1", auth: bearer(pull), header: map[string]string{"Accept": manifestType}})
	if got.Code != http.StatusOK || got.Body.String() != string(img.manifest) {
		t.Fatalf("anonymous pull: %d %s", got.Code, got.Body.String())
	}
	// A repository without an anonymous grant is the same as one that does not exist, with the anonymous token and with no credential:
	// status, challenge and body are identical, the name the client sent aside.
	same := func(what string, auth string) {
		t.Helper()
		answer := func(repo string) (int, string, string) {
			a := e.send("default", req{method: "GET", path: "/v2/web/" + repo + "/app/manifests/1", auth: auth, header: map[string]string{"Accept": manifestType}})
			norm := func(x string) string { return strings.ReplaceAll(x, repo, "REPO") }
			return a.Code, norm(a.Header().Get("WWW-Authenticate")), norm(a.Body.String())
		}
		hc, hch, hb := answer("docker-local")
		mc, mch, mb := answer("ghost-local")
		if hc != http.StatusUnauthorized || hc != mc || hch != mch || hb != mb {
			t.Errorf("%s: hidden %d %q %s\nmissing %d %q %s", what, hc, hch, hb, mc, mch, mb)
		}
		if !strings.HasPrefix(hch, `Bearer realm="http://trove.test/v2/_token",service="trove.test"`) {
			t.Errorf("%s: the 401 carries no registry challenge: %q", what, hch)
		}
	}
	same("anonymous token", bearer(tokenFor("repository:web/docker-local/app:pull")))
	same("no credential", "")
	if a := e.send("default", req{method: "GET", path: "/v2/web/docker-local/app/manifests/1", header: map[string]string{"Accept": manifestType}}); a.Code != http.StatusUnauthorized ||
		!strings.Contains(a.Header().Get("WWW-Authenticate"), `scope="repository:web/docker-local/app:pull"`) {
		t.Errorf("no credential, private repository: %d, challenge %q; want 401 naming the scope", a.Code, a.Header().Get("WWW-Authenticate"))
	}

	// A push without a login: no token at all, and the anonymous token asked for push, which grants nothing.
	push := req{method: "PUT", path: "/v2/" + name + "/manifests/2", body: img.manifest, header: map[string]string{"Content-Type": manifestType}}
	if a := e.send("default", push); a.Code != http.StatusUnauthorized || a.code(t) != "UNAUTHORIZED" {
		t.Fatalf("push without a credential: %d %s", a.Code, a.Body.String())
	}
	push.auth = bearer(tokenFor("repository:" + name + ":push"))
	a := e.send("default", push)
	if a.Code != http.StatusUnauthorized || a.code(t) != "UNAUTHORIZED" || !strings.Contains(a.Header().Get("WWW-Authenticate"), `error="insufficient_scope"`) {
		t.Fatalf("push with an anonymous token: %d %v %s", a.Code, a.Header(), a.Body.String())
	}
	if m := e.send("default", req{method: "GET", path: "/v2/" + name + "/manifests/2", auth: bearer(pull), header: map[string]string{"Accept": manifestType}}); m.Code != http.StatusNotFound {
		t.Fatalf("the refused push stored a tag: %d", m.Code)
	}
}
