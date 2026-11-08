//go:build integration

package registry

import (
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/api/registry"
	"github.com/guided-traffic/trove/internal/api/registry/registrytest"
	"github.com/guided-traffic/trove/internal/auth/registrytoken"
	"github.com/guided-traffic/trove/internal/formats"
	_ "github.com/guided-traffic/trove/internal/formats/all" // registers the OCI format
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

func ociFormat(t *testing.T) formats.Format {
	t.Helper()
	for _, f := range formats.List() {
		if f.Descriptor().RegistryRoot {
			return f
		}
	}
	t.Fatal("no registry-root format is registered")
	return nil
}

// The end-to-end proof of Q-OCI-56: the walk runs through the real handler of the endpoint - the chain with the host stage, the
// authentication stage, the router, the enforcement point and the dispatcher's handler - with registry tokens from the real token
// service, and every route of the OCI format and the three core routes pass it.
func TestR_OCI_005_TheWalkThroughTheRealHandler(t *testing.T) {
	e := newEnv(t, nil)
	f := ociFormat(t)
	t.Run("the default endpoint", func(t *testing.T) {
		registrytest.Walk{
			Enforcer: e.rep.App.RegistryEnforcer("default"), Handler: e.handler("default"), Patterns: e.rep.App.EndpointRouter("default").Patterns(),
			Format: f, Repo: registry.ExampleRepo, Served: e.served,
			Mint:    func(g registrytoken.Grant) string { return e.mint("default", "", e.admin, g, false) },
			Prepare: func(r *http.Request) { r.Host, r.RemoteAddr = testenv.DefaultHost, "203.0.113.7:4711" },
		}.Run(t)
	})
	// R-OCI-029: the bound host on a test endpoint; the Docker Engine mirror pull through a remote is another work package's.
	t.Run("the mirror endpoint, on the host bound to web/docker-local", func(t *testing.T) {
		registrytest.Walk{
			Enforcer: e.rep.App.RegistryEnforcer("mirror"), Handler: e.handler("mirror"), Patterns: e.rep.App.EndpointRouter("mirror").Patterns(),
			Format: f, Repo: registry.ExampleRepo, Served: e.served,
			Mint:    func(g registrytoken.Grant) string { return e.mint("mirror", mirrorHost, e.admin, g, true) },
			Prepare: func(r *http.Request) { r.RemoteAddr = "203.0.113.7:4711" },
			Bound:   &registrytest.BoundHost{Host: mirrorHost, Repo: local},
		}.Run(t)
	})
	t.Run("R-ARCH-005 the route table of each endpoint holds the registry root and nothing else below /v2/", func(t *testing.T) {
		for _, name := range []string{"default", "mirror"} {
			var below []string
			for _, p := range e.rep.App.EndpointRouter(name).Patterns() {
				if path := httpserver.PatternPath(p); path == "/v2" || strings.HasPrefix(path, "/v2/") {
					below = append(below, p)
				}
			}
			if !slices.Equal(below, []string{registry.BarePattern, registry.Pattern}) {
				t.Errorf("endpoint %s has the patterns %v below /v2/, want exactly %s and %s", name, below, registry.BarePattern, registry.Pattern)
			}
			if uncleaned := e.rep.App.RegistryEnforcer(name).UncleanedPrefixes(); !slices.Equal(uncleaned, []string{"/v2/"}) {
				t.Errorf("endpoint %s: uncleaned prefixes %v", name, uncleaned)
			}
		}
	})
}

// An endpoint whose surface leaves out packages has no registry root: /v2/ is an unknown path there (R-EP-005, R-ARCH-005).
// R-OCI-003 (an endpoint without the packages family has no registry root; the ID stays out of the name).
func TestAnEndpointWithoutPackagesHasNoRegistryRoot(t *testing.T) {
	e := newEnv(t, nil)
	if e.rep.App.RegistryEnforcer("filesonly") != nil {
		t.Fatal("an endpoint without packages has an enforcement point")
	}
	for _, p := range e.rep.App.EndpointRouter("filesonly").Patterns() {
		if path := httpserver.PatternPath(p); path == "/v2" || strings.HasPrefix(path, "/v2/") {
			t.Errorf("pattern %q below /v2/ on an endpoint without packages", p)
		}
	}
	unknown := e.do("filesonly", filesHost, "GET", "/does-not-exist", e.admin)
	if unknown.Code != http.StatusNotFound {
		t.Fatalf("%d", unknown.Code)
	}
	for _, p := range []string{"/v2/", "/v2/_token", "/v2/web/docker-local/app/manifests/1"} {
		a := e.do("filesonly", filesHost, "GET", p, e.admin)
		if a.Code != http.StatusNotFound || a.Header().Get(registry.APIVersionHeader) != "" ||
			!strings.Contains(a.Body.String(), `"code":"not_found"`) {
			t.Errorf("%s: %d %v %s", p, a.Code, a.Header(), a.Body.String())
		}
	}
}

func TestR_OCI_003_TheVersionCheck(t *testing.T) {
	e := newEnv(t, nil)
	challenge := `Bearer realm="http://trove.test/v2/_token",service="trove.test"`
	t.Run("without a credential and without anonymous access: 401 with the challenge, in the OCI shape", func(t *testing.T) {
		a := e.do("default", "", "GET", "/v2/", "")
		if a.Code != http.StatusUnauthorized || a.Header().Get("WWW-Authenticate") != challenge || a.Header().Get(registry.APIVersionHeader) != registry.APIVersion {
			t.Fatalf("%d %v %s", a.Code, a.Header(), a.Body.String())
		}
		if a.code(t) != "UNAUTHORIZED" {
			t.Fatalf("code %s", a.code(t))
		}
	})
	t.Run("with any valid credential: 200 {} with the version header; HEAD without the body", func(t *testing.T) {
		for _, tok := range []string{e.admin, e.nobody} {
			a := e.do("default", "", "GET", "/v2/", tok)
			if a.Code != http.StatusOK || a.Body.String() != "{}" || a.Header().Get(registry.APIVersionHeader) != "registry/2.0" {
				t.Fatalf("%d %v %q", a.Code, a.Header(), a.Body.String())
			}
		}
		if h := e.do("default", "", "HEAD", "/v2/", e.admin); h.Code != http.StatusOK || h.Body.Len() != 0 || h.Header().Get("Content-Length") != "2" {
			t.Fatalf("HEAD: %d %v %q", h.Code, h.Header(), h.Body.String())
		}
		if a := e.do("default", "", "POST", "/v2/", e.admin); a.Code != http.StatusMethodNotAllowed || a.Header().Get("Allow") != "GET, HEAD" || a.code(t) != "UNSUPPORTED" {
			t.Fatalf("POST: %d %v %s", a.Code, a.Header(), a.Body.String())
		}
	})
	t.Run("a registry token is a credential of the version check, even with an empty grant", func(t *testing.T) {
		rgt := e.mint("default", "", e.nobody, registrytoken.Grant{}, false)
		if a := e.do("default", "", "GET", "/v2/", rgt); a.Code != http.StatusOK {
			t.Fatalf("%d %s", a.Code, a.Body.String())
		}
	})
	t.Run("an invalid credential is 401 UNAUTHORIZED", func(t *testing.T) {
		a := e.do("default", "", "GET", "/v2/", "trv_pat_notatoken")
		if a.Code != http.StatusUnauthorized || a.code(t) != "UNAUTHORIZED" || a.Header().Get(registry.APIVersionHeader) != registry.APIVersion {
			t.Fatalf("%d %v %s", a.Code, a.Header(), a.Body.String())
		}
	})
	t.Run("Q-OCI-20 with anonymous access on, the version check itself answers the challenge to a caller without a credential", func(t *testing.T) {
		const body = `{"anonymousAccess":true}`
		if a := e.st.Do(e.rep, "PATCH", "/api/v1/settings/auth", e.admin, body, "Content-Type", "application/merge-patch+json", "If-Match", "*"); a.Status != http.StatusOK {
			t.Fatalf("switch: %d %s", a.Status, a.Body)
		}
		testenv.WaitFor(t, "anonymous access on", 10*time.Second, func() bool {
			a := e.do("default", "", "GET", "/v2/_token", "")
			return a.Code == http.StatusOK
		})
		var a answer
		label := e.served(func() { a = e.do("default", "", "GET", "/v2/", "") })
		if a.Code != http.StatusUnauthorized || a.Header().Get("WWW-Authenticate") != challenge || label != "registry.version" {
			t.Fatalf("%d %v served by %q", a.Code, a.Header(), label)
		}
		anon := e.mint("default", "", "", registrytoken.Grant{}, false)
		if a := e.do("default", "", "GET", "/v2/", anon); a.Code != http.StatusOK {
			t.Fatalf("the anonymous registry token: %d %s", a.Code, a.Body.String())
		}
	})
}

// R-OCI-001 (the 404 NAME_UNKNOWN criterion; the ID stays out of the name because the criteria of the requirement are not all proven here).
func TestNameUnknownIsTheSameForWhatDoesNotExistAndWhatTheCallerCannotSee(t *testing.T) {
	e := newEnv(t, nil)
	paths := []string{
		"/v2/web", "/v2/web/docker-local", "/v2/web/docker-local/", "/v2/web/nope/app/manifests/1", "/v2/nope/docker-local/app/manifests/1",
		"/v2/web/files/app/manifests/1", // a repository of another format
	}
	for _, p := range paths {
		a := e.do("default", "", "GET", p, e.admin)
		if a.Code != http.StatusNotFound || a.code(t) != "NAME_UNKNOWN" || a.Header().Get(registry.APIVersionHeader) != registry.APIVersion {
			t.Errorf("%s: %d %v %s", p, a.Code, a.Header(), a.Body.String())
		}
	}
	// A repository that exists, which the caller cannot read, answers byte for byte as one that does not exist.
	hidden := e.do("default", "", "GET", "/v2/web/team-local/app/manifests/1", e.reader)
	missing := e.do("default", "", "GET", "/v2/web/ghost-local/app/manifests/1", e.reader)
	if hidden.Code != http.StatusNotFound || hidden.Code != missing.Code || hidden.Body.String() != missing.Body.String() {
		t.Fatalf("hidden: %d %s\nmissing: %d %s", hidden.Code, hidden.Body.String(), missing.Code, missing.Body.String())
	}
	// A readable repository reaches the route: the route answers its own 404 MANIFEST_UNKNOWN for a manifest nothing pushed.
	if a := e.do("default", "", "GET", "/v2/web/docker-local/library/app/manifests/1", e.reader); a.Code != http.StatusNotFound || a.code(t) != "MANIFEST_UNKNOWN" {
		t.Fatalf("a readable repository: %d %s", a.Code, a.Body.String())
	}
	for _, p := range []string{"/v2/web/docker-local/a//b/manifests/1", "/v2/web/docker-local/a/../b/manifests/1"} {
		if a := e.do("default", "", "GET", p, e.admin); a.Code != http.StatusBadRequest || a.code(t) != "NAME_INVALID" {
			t.Errorf("%s: %d %s", p, a.Code, a.Body.String())
		}
	}
}

// R-OCI-029 (the bound host on a test endpoint; the Docker Engine mirror via the stub is the remote class's work, so the ID stays in this comment).
func TestABoundHostIsTheDistributionAPIOfItsRepository(t *testing.T) {
	e := newEnv(t, nil)
	t.Run("the image path is read in the bound repository and decided on it", func(t *testing.T) {
		var a answer
		label := e.served(func() { a = e.do("mirror", mirrorHost, "GET", "/v2/library/alpine/manifests/3", e.reader) })
		if a.Code != http.StatusNotFound || label != "oci.manifest.get.tag" {
			t.Fatalf("%d served by %q: %s", a.Code, label, a.Body.String())
		}
		if a := e.do("mirror", mirrorHost, "GET", "/v2/library/alpine/manifests/3", e.nobody); a.Code != http.StatusNotFound || a.code(t) != "NAME_UNKNOWN" {
			t.Fatalf("a caller who cannot read the bound repository: %d %s", a.Code, a.Body.String())
		}
	})
	t.Run("the token service and the challenge name the image without a prefix", func(t *testing.T) {
		pull := registrytoken.Grant{Entries: []registrytoken.Entry{{Repo: local, Name: "library/alpine", Actions: registrytoken.Pull}}}
		rgt := e.mint("mirror", mirrorHost, e.admin, pull, true)
		if a := e.do("mirror", mirrorHost, "GET", "/v2/library/alpine/manifests/3", rgt); a.Code != http.StatusNotFound {
			t.Fatalf("pull with the token: %d %s", a.Code, a.Body.String())
		}
		a := e.do("mirror", mirrorHost, "PUT", "/v2/library/alpine/manifests/3", rgt)
		want := `Bearer realm="http://mirror.trove.test/v2/_token",service="mirror.trove.test",scope="repository:library/alpine:push",error="insufficient_scope"`
		if a.Code != http.StatusUnauthorized || a.Header().Get("WWW-Authenticate") != want {
			t.Fatalf("push with a pull token: %d %v", a.Code, a.Header())
		}
		// A scope written with the repository prefix names another image of the bound repository.
		prefixed := e.mint("mirror", mirrorHost, e.admin, registrytoken.Grant{Entries: []registrytoken.Entry{{Repo: local, Name: "web/docker-local/library/alpine", Actions: registrytoken.Pull}}}, true)
		if a := e.do("mirror", mirrorHost, "GET", "/v2/library/alpine/manifests/3", prefixed); a.Code != http.StatusUnauthorized {
			t.Fatalf("the prefixed scope pulled the unprefixed image: %d", a.Code)
		}
	})
	t.Run("a binding to a repository of another format behaves as no binding", func(t *testing.T) {
		if a := e.do("mirror", plainHost, "GET", "/v2/library/alpine/manifests/3", e.admin); a.Code != http.StatusNotFound || a.code(t) != "NAME_UNKNOWN" {
			t.Fatalf("%d %s", a.Code, a.Body.String())
		}
		// The unbound meaning of the path stays.
		if a := e.do("mirror", plainHost, "GET", "/v2/web/docker-local/app/manifests/1", e.admin); a.Code != http.StatusNotFound {
			t.Fatalf("%d %s", a.Code, a.Body.String())
		}
		// The token service reads the scope as the match does: as a full name.
		tok := e.mint("mirror", plainHost, e.admin, registrytoken.Grant{Entries: []registrytoken.Entry{{Repo: local, Name: "app", Actions: registrytoken.Pull}}}, false)
		if a := e.do("mirror", plainHost, "GET", "/v2/web/docker-local/app/manifests/1", tok); a.Code != http.StatusNotFound {
			t.Fatalf("a token for the full name on the unbound host: %d %s", a.Code, a.Body.String())
		}
	})
	t.Run("the core routes of the host are its own", func(t *testing.T) {
		a := e.do("mirror", mirrorHost, "GET", "/v2/", e.admin)
		if a.Code != http.StatusOK {
			t.Fatalf("%d", a.Code)
		}
		u := e.do("mirror", mirrorHost, "GET", "/v2/", "")
		if u.Header().Get("WWW-Authenticate") != `Bearer realm="http://mirror.trove.test/v2/_token",service="mirror.trove.test"` {
			t.Fatalf("challenge %q", u.Header().Get("WWW-Authenticate"))
		}
	})
}
