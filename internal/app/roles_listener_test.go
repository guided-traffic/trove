package app

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// answer is what a client sees of one request, without the fields that name the request or the replica: the header
// without X-Request-Id and Date, and without Content-Length, which follows the problem's instance member (the request
// path) that the body is compared without.
type answer struct {
	code        int
	contentType string
	header      string
	body        string
}

func headerOf(h http.Header) string {
	h = h.Clone()
	for _, k := range []string{"X-Request-Id", "Date", "Content-Length"} {
		h.Del(k)
	}
	keys := slices.Sorted(maps.Keys(h))
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %q\n", k, h[k])
	}
	return b.String()
}

func ask(t *testing.T, addr, method, path string, hdr ...string) answer {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+addr+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	a := answer{code: resp.StatusCode, contentType: resp.Header.Get("Content-Type"), header: headerOf(resp.Header), body: string(b)}
	var pr map[string]any
	if json.Unmarshal(b, &pr) == nil {
		delete(pr, "instance")
		delete(pr, "requestId")
		out, _ := json.Marshal(pr)
		a.body = string(out)
	}
	return a
}

// TestR_ARCH_001_RouteFamiliesPerRole is the in-process check of E6a.2: a replica of each role answers a route of the other
// role's family with the 404 problem of an unknown path, byte for byte apart from the request ID (R-ARCH-005, R-EP-005,
// Q-ARCH-298), and still serves its own. The shell profile has the routes of every family and no infrastructure, so the
// difference between the roles is the one the role makes.
func TestR_ARCH_001_RouteFamiliesPerRole(t *testing.T) {
	type probe struct{ method, path string }
	cases := map[string]struct {
		other []probe // routes of the other role's families: the 404 of an unknown path
		own   []probe // routes of the role's own families: anything but that 404
	}{
		"api": {
			other: []probe{
				{"GET", "/files/generic-local/x"},
				{"PUT", "/files/generic-local/x"},
				{"GET", "/api/npm/npm-local/@acme%2fsdk"},
				{"GET", "/api/nuget/nuget-local/v3/index.json"},
				{"GET", "/v2/"},
			},
			own: []probe{{"GET", "/api/v1/system/ping"}, {"GET", "/ui/"}, {"GET", "/"}},
		},
		"packages": {
			other: []probe{
				{"GET", "/ui/"},
				{"GET", "/"},
				{"GET", "/auth/oidc/corp/start"},
				{"POST", "/auth/local/login"},
				{"GET", "/api/v1/system/ping"},
				{"GET", "/api/v1/repositories"},
				{"POST", "/api/v1/repositories"},
				{"POST", "/api/v1/auth/oidc/token"},
				{"DELETE", "/api/v1/repositories/generic-local/content/x"},
			},
			own: []probe{{"GET", "/files/generic-local/x"}},
		},
	}
	for role, tc := range cases {
		t.Run("R-ARCH-001 R-EP-005 a "+role+" replica answers the other role's routes like an unknown path and serves its own", func(t *testing.T) {
			h := start(t, role, nil)
			addr := h.app.EndpointAddr("default")
			unknown := ask(t, addr, "GET", "/does-not-exist")
			if unknown.code != http.StatusNotFound || !strings.Contains(unknown.contentType, "problem+json") {
				t.Fatalf("an unknown path: %+v", unknown)
			}
			for _, p := range tc.other {
				// The reference is the unknown path of the same method, header included: the hard limit of R-AUTH-037 on an
				// unsafe anonymous request to /api/v1 is a rule of the served API, and so is every other path rule of the
				// chain (Q-ARCH-306).
				want := ask(t, addr, p.method, "/does-not-exist")
				if got := ask(t, addr, p.method, p.path); got != want {
					t.Errorf("%s %s on %s:\n got %+v\nwant %+v", p.method, p.path, role, got, want)
				}
			}
			for _, p := range tc.own {
				if got := ask(t, addr, p.method, p.path); got == unknown {
					t.Errorf("%s %s on %s answers like an unknown path", p.method, p.path, role)
				}
			}
		})
	}

	t.Run("R-ARCH-001 web and all serve every family: no route of either role answers like an unknown path", func(t *testing.T) {
		for _, role := range []string{"web", "all"} {
			h := start(t, role, nil)
			addr := h.app.EndpointAddr("default")
			unknown := ask(t, addr, "GET", "/does-not-exist")
			for _, p := range []string{"/api/v1/system/ping", "/ui/", "/files/generic-local/x"} {
				if got := ask(t, addr, "GET", p); got == unknown {
					t.Errorf("%s on %s answers like an unknown path", p, role)
				}
			}
		}
	})

	t.Run("R-EP-010 R-ARCH-001 the default endpoint of packages accepts no session cookie, the one of api, web and all does", func(t *testing.T) {
		for role, want := range map[string]bool{"api": true, "packages": false, "web": true, "all": true} {
			h := start(t, role, nil)
			var rules bool
			for _, rt := range h.app.eps {
				if rt.cfg.Default {
					rules = endpointRules(rt).AcceptsSession
				}
			}
			if rules != want {
				t.Errorf("%s: AcceptsSession = %v, want %v", role, rules, want)
			}
		}
	})
}

// TestR_ARCH_001_ListenerRule covers the rule of R-EP-002 and Q-ARCH-298: a process opens an endpoint's listener when its
// pool and the intersection of the endpoint's surface with the role's families allow it, with those families only.
func TestR_ARCH_001_ListenerRule(t *testing.T) {
	srv := config.Server{
		BaseURL: "https://trove.example.com",
		Endpoints: map[string]config.Endpoint{
			"customers": {Listen: ":8443", BaseURL: "https://packages.example.com", Surface: []string{"packages", "files", "api"}},
			"mirror":    {Listen: ":8444", BaseURL: "https://mirror.example.com", Surface: []string{"packages"}},
			"tools":     {Listen: ":8445", BaseURL: "https://tools.example.com", Surface: []string{"api", "ci_exchange"}},
			"pooled":    {Listen: ":8446", BaseURL: "https://pooled.example.com", Surface: []string{"packages", "files"}, Pool: "ep-pooled"},
		},
	}
	all := srv.ResolveEndpoints()
	opens := func(role, pool string) map[string][]string {
		out := map[string][]string{}
		for _, e := range all {
			if !servesEndpoint(role, pool, e, all) {
				continue
			}
			ep, err := httpserver.NewEndpoint(e, nil, httpserver.ForRoleFamilies(config.RoleFamilies(role)))
			if err != nil {
				t.Fatal(err)
			}
			out[e.Name] = ep.Surface()
		}
		return out
	}
	eq := func(t *testing.T, got, want map[string][]string) {
		t.Helper()
		if len(got) != len(want) {
			t.Errorf("opens %v, want %v", got, want)
			return
		}
		for name, fams := range want {
			if !slices.Equal(got[name], fams) {
				t.Errorf("endpoint %s: families %v, want %v (all: %v)", name, got[name], fams, got)
			}
		}
	}

	t.Run("R-EP-002 both main Deployments open an endpoint of [packages, files, api] with their own families only", func(t *testing.T) {
		eq(t, opens("api", ""), map[string][]string{
			"default": {"ui", "api", "ci_exchange"}, "customers": {"api"}, "tools": {"api", "ci_exchange"},
		})
		eq(t, opens("packages", ""), map[string][]string{
			"default": {"packages", "files"}, "customers": {"packages", "files"}, "mirror": {"packages"},
		})
	})
	t.Run("R-EP-002 an endpoint none of whose families the role serves is not opened", func(t *testing.T) {
		if _, ok := opens("api", "")["mirror"]; ok {
			t.Error("api opens the packages-only endpoint mirror")
		}
		if _, ok := opens("packages", "")["tools"]; ok {
			t.Error("packages opens the api-only endpoint tools")
		}
	})
	t.Run("R-EP-002 the pool still decides which endpoints a process opens", func(t *testing.T) {
		eq(t, opens("packages", "ep-pooled"), map[string][]string{"pooled": {"packages", "files"}})
		if got := opens("api", "ep-pooled"); len(got) != 0 {
			t.Errorf("an api process of the pool that only serves packages and files opens %v", got)
		}
		// A packages pool that no endpoint names serves the endpoints without a pool, like the main Deployment.
		eq(t, opens("packages", "path-pool"), opens("packages", ""))
	})
	t.Run("R-EP-002 the web bridge and the development role open every endpoint with every family of its surface", func(t *testing.T) {
		// web is a main Deployment (its pool is the main one); all opens every endpoint of every pool.
		for role, n := range map[string]int{"web": 4, "all": 5} {
			got := opens(role, "")
			if len(got) != n {
				t.Errorf("%s opens %v, want %d endpoints", role, got, n)
			}
			if !slices.Equal(got["customers"], []string{"packages", "files", "api"}) {
				t.Errorf("%s: customers families %v", role, got["customers"])
			}
		}
		if got := opens("worker", ""); len(got) != 0 {
			t.Errorf("a worker opens %v", got)
		}
	})
}

// TestR_EP_005_RoleNarrowedEndpoint covers the effective surface of an endpoint: the router registers and the check accepts
// only the families of the intersection, and the endpoint keeps its configured surface for the configuration view.
func TestR_EP_005_RoleNarrowedEndpoint(t *testing.T) {
	r := config.ResolvedEndpoint{Name: "default", Default: true, Surface: config.Surfaces()}
	for role, want := range map[string][]string{
		"api":      {"ui", "api", "ci_exchange"},
		"packages": {"packages", "files"},
		"web":      config.Surfaces(),
	} {
		ep, err := httpserver.NewEndpoint(r, nil, httpserver.ForRoleFamilies(config.RoleFamilies(role)))
		if err != nil {
			t.Fatal(err)
		}
		if got := ep.Surface(); !slices.Equal(got, want) {
			t.Errorf("%s: surface %v, want %v", role, got, want)
		}
		if got := ep.ConfiguredSurface(); !slices.Equal(got, config.Surfaces()) {
			t.Errorf("%s: configured surface %v", role, got)
		}
		for _, f := range config.Surfaces() {
			if ep.Serves(f) != slices.Contains(want, f) {
				t.Errorf("%s: Serves(%s) = %v", role, f, ep.Serves(f))
			}
		}
		router := httpserver.NewEndpointRouter(ep)
		for _, pat := range []string{"GET /ui/", "GET /api/v1/system/ping", "POST /api/v1/auth/oidc/token", "GET /files/{key}/{path...}", "GET /api/npm/{key}/{rest...}"} {
			router.Handle(pat, http.NotFoundHandler())
		}
		if v := httpserver.CheckSurface(router.Patterns(), ep); len(v) != 0 {
			t.Errorf("%s: %v", role, v)
		}
		registered := fmt.Sprint(router.Patterns())
		for pat, fam := range map[string]string{
			"GET /ui/": "ui", "GET /api/v1/system/ping": "api", "POST /api/v1/auth/oidc/token": "ci_exchange",
			"GET /files/{key}/{path...}": "files", "GET /api/npm/{key}/{rest...}": "packages",
		} {
			if has := strings.Contains(registered, pat); has != slices.Contains(want, fam) {
				t.Errorf("%s: %q registered = %v", role, pat, has)
			}
		}
	}
}
