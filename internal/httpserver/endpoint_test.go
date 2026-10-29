package httpserver

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/tlsfiles/tlstest"
)

// endpointFixture resolves an endpoint from a server section, like the loader does.
func endpointFixture(t *testing.T, s config.Server, name string) *Endpoint {
	t.Helper()
	for _, r := range s.ResolveEndpoints() {
		if r.Name == name {
			e, err := NewEndpoint(r, s.AdditionalBaseURLs)
			if err != nil {
				t.Fatal(err)
			}
			return e
		}
	}
	t.Fatalf("no endpoint %s", name)
	return nil
}

func twoEndpoints() config.Server {
	return config.Server{
		Listen: ":8080", OpsListen: ":8081", BaseURL: "https://trove.example.com", MaxInFlight: 4096,
		ReplicaHeader: "authenticated", TrustedProxies: []string{"10.0.0.0/8"},
		Endpoints: map[string]config.Endpoint{
			"customers": {
				Listen: ":8443", BaseURL: "https://packages.example.com", Hosts: []string{"pkgs.example.org"},
				WildcardHost: "*.registry.example.com",
				HostRoots:    map[string]string{"pkgs.example.org": "docker-hub"},
			},
		},
	}
}

type countingEvents struct {
	mu                sync.Mutex
	misdirected, fwdd map[string]int
}

func (c *countingEvents) Misdirected(e string) { c.mu.Lock(); c.misdirected[e]++; c.mu.Unlock() }
func (c *countingEvents) ForwardedHostIgnored(e string) {
	c.mu.Lock()
	c.fwdd[e]++
	c.mu.Unlock()
}

// endpointChain builds the chain of an endpoint around a router with the generic
// route families; the handler echoes the endpoint of the context and the info.
func endpointChain(t *testing.T, s config.Server, name string, ev EndpointEvents) http.Handler {
	t.Helper()
	e := endpointFixture(t, s, name)
	rt := NewEndpointRouter(e)
	echo := func(w http.ResponseWriter, r *http.Request) {
		info := InfoFrom(r.Context())
		_ = json.NewEncoder(w).Encode(map[string]string{
			"endpoint": EndpointFrom(r.Context()).Name(), "base": info.BaseURL, "host": info.Host,
			"label": info.HostLabel, "root": RootRepository(r.Context()),
		})
	}
	for _, p := range []string{
		"GET /api/v1/system/ping", "GET /api/npm/{repoKey}/{rest...}", "GET /files/{repoKey}/{path...}",
		"/ui/", "POST /api/v1/auth/oidc/token", "GET /test-fixture/x",
	} {
		rt.HandleFunc(p, echo)
	}
	res, err := NewResolver(e.TrustedProxies(), e.BaseURL(), nil, ForEndpoint(e))
	if err != nil {
		t.Fatal(err)
	}
	return Chain{Router: rt, Endpoint: e, Events: ev, Resolver: res, MaxInFlight: e.MaxInFlight(), ReplicaHeader: e.ReplicaHeader(), Replica: "pod-1"}.Handler()
}

func call(h http.Handler, method, path, host, peer string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.Host, r.RemoteAddr = host, peer
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestEndpointIdentity(t *testing.T) {
	s := twoEndpoints()
	ev := &countingEvents{misdirected: map[string]int{}, fwdd: map[string]int{}}
	listeners := map[string]http.Handler{
		"default":   endpointChain(t, s, "default", ev),
		"customers": endpointChain(t, s, "customers", ev),
	}

	t.Run("R-EP-002 the endpoint in the context is the listener's whatever Host and forwarded host claim", func(t *testing.T) {
		hosts := []string{"trove.example.com", "packages.example.com", "pkgs.example.org", "docker-all.registry.example.com", "evil.example", "", "10.0.0.5"}
		forwarded := []string{"", "trove.example.com", "packages.example.com", "evil.example"}
		peers := []string{"192.0.2.9:4000", "10.1.2.3:4000"}
		for name, h := range listeners {
			for _, host := range hosts {
				for _, fwd := range forwarded {
					for _, peer := range peers {
						var hdr []string
						if fwd != "" {
							hdr = []string{"X-Forwarded-Host", fwd, "X-Forwarded-Proto", "https"}
						}
						w := call(h, "GET", "/api/npm/npm-all/x", host, peer, hdr...)
						if w.Code != 200 {
							if w.Code != 421 {
								t.Fatalf("%s host=%q fwd=%q peer=%s: %d", name, host, fwd, peer, w.Code)
							}
							continue
						}
						var got map[string]string
						if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
							t.Fatal(err)
						}
						if got["endpoint"] != name {
							t.Fatalf("listener %s, host=%q fwd=%q: endpoint %q", name, host, fwd, got["endpoint"])
						}
					}
				}
			}
		}
	})

	t.Run("R-EP-003 a host outside the set answers 421 with the problem code, before routing", func(t *testing.T) {
		h := listeners["customers"]
		for _, host := range []string{"trove.example.com", "10.0.0.5", "", "registry.example.com", "a.b.registry.example.com", "evil.example"} {
			w := call(h, "GET", "/api/npm/npm-all/", host, "192.0.2.9:1")
			if w.Code != 421 {
				t.Errorf("Host %q: %d", host, w.Code)
				continue
			}
			if p := problemOf(t, w); p.Code != "misdirected_request" || p.Status != 421 {
				t.Errorf("Host %q: %+v", host, p)
			}
		}
		// The answer does not depend on the path: even an unknown path gives 421, not 404.
		if w := call(h, "GET", "/does-not-exist", "trove.example.com", "192.0.2.9:1"); w.Code != 421 {
			t.Errorf("unknown path on a foreign host: %d", w.Code)
		}
		if ev.misdirected["customers"] == 0 {
			t.Error("misdirected not counted")
		}
	})

	t.Run("R-EP-003 own hosts, the wildcard and the response base URL", func(t *testing.T) {
		h := listeners["customers"]
		for _, tc := range []struct{ host, base, label string }{
			{"packages.example.com:8443", "https://packages.example.com", ""},
			{"PACKAGES.example.com", "https://packages.example.com", ""},
			{"pkgs.example.org", "https://pkgs.example.org", ""},
			{"docker-all.registry.example.com", "https://docker-all.registry.example.com", "docker-all"},
		} {
			w := call(h, "GET", "/api/npm/npm-all/", tc.host, "192.0.2.9:1")
			if w.Code != 200 {
				t.Errorf("%s: %d", tc.host, w.Code)
				continue
			}
			var got map[string]string
			_ = json.Unmarshal(w.Body.Bytes(), &got)
			if got["base"] != tc.base || got["label"] != tc.label {
				t.Errorf("%s: %v", tc.host, got)
			}
		}
	})

	t.Run("R-EP-003 a base URL port is kept for hosts of the endpoint", func(t *testing.T) {
		s := twoEndpoints()
		c := s.Endpoints["customers"]
		c.BaseURL = "https://packages.example.com:8443"
		s.Endpoints["customers"] = c
		h := endpointChain(t, s, "customers", nil)
		w := call(h, "GET", "/api/npm/npm-all/", "pkgs.example.org", "192.0.2.9:1")
		var got map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		if got["base"] != "https://pkgs.example.org:8443" {
			t.Errorf("base = %q", got["base"])
		}
		w = call(h, "GET", "/api/npm/npm-all/", "packages.example.com", "192.0.2.9:1")
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		if got["base"] != "https://packages.example.com:8443" {
			t.Errorf("own base = %q", got["base"])
		}
	})

	t.Run("R-EP-003 forwarded hosts count only from a trusted proxy and only within the set", func(t *testing.T) {
		h := listeners["customers"]
		before := ev.fwdd["customers"]
		// untrusted peer: the forwarded host is not read, Host is foreign
		if w := call(h, "GET", "/api/npm/npm-all/", "evil.example", "192.0.2.9:1", "X-Forwarded-Host", "packages.example.com"); w.Code != 421 {
			t.Errorf("untrusted peer: %d", w.Code)
		}
		// trusted peer: forwarded host within the set wins
		w := call(h, "GET", "/api/npm/npm-all/", "evil.example", "10.1.2.3:1", "X-Forwarded-Host", "packages.example.com")
		var got map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		if w.Code != 200 || got["base"] != "https://packages.example.com" {
			t.Errorf("trusted peer: %d %v", w.Code, got)
		}
		// trusted peer, forwarded host outside the set: ignored and counted, Host decides
		w = call(h, "GET", "/api/npm/npm-all/", "packages.example.com", "10.1.2.3:1", "X-Forwarded-Host", "evil.example")
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		if w.Code != 200 || got["base"] != "https://packages.example.com" {
			t.Errorf("ignored forwarded host: %d %v", w.Code, got)
		}
		if ev.fwdd["customers"] != before+1 {
			t.Errorf("ignored counter = %d, want %d", ev.fwdd["customers"], before+1)
		}
	})

	t.Run("R-EP-003 the default endpoint without hosts keeps R-OPS-005 and checks once hosts are set", func(t *testing.T) {
		h := listeners["default"]
		_ = h
		s := config.Server{Listen: ":8080", OpsListen: ":8081", BaseURL: "https://trove.example.com", MaxInFlight: 10, ReplicaHeader: "authenticated"}
		plain := endpointChain(t, s, "default", nil)
		if w := call(plain, "GET", "/api/v1/system/ping", "anything.example", "192.0.2.9:1"); w.Code != 200 {
			t.Fatalf("no check: %d", w.Code)
		}
		s.Endpoints = map[string]config.Endpoint{"default": {Hosts: []string{"trove.example.com"}}}
		checked := endpointChain(t, s, "default", nil)
		if w := call(checked, "GET", "/api/v1/system/ping", "packages.example.com", "192.0.2.9:1"); w.Code != 421 {
			t.Errorf("foreign host on the checking default endpoint: %d", w.Code)
		}
		if w := call(checked, "GET", "/api/v1/system/ping", "trove.example.com", "192.0.2.9:1"); w.Code != 200 {
			t.Errorf("own host: %d", w.Code)
		}
	})

	t.Run("R-EP-003 the default endpoint host set holds the additional base URLs", func(t *testing.T) {
		s := config.Server{
			Listen: ":8080", OpsListen: ":8081", BaseURL: "https://trove.example.com", MaxInFlight: 10, ReplicaHeader: "authenticated",
			AdditionalBaseURLs: []string{"https://trove.internal:8443"},
			Endpoints:          map[string]config.Endpoint{"default": {Hosts: []string{"alias.example.com"}}},
		}
		h := endpointChain(t, s, "default", nil)
		for host, want := range map[string]string{
			"trove.example.com": "https://trove.example.com", "trove.internal": "https://trove.internal:8443", "alias.example.com": "https://alias.example.com",
		} {
			w := call(h, "GET", "/api/v1/system/ping", host, "192.0.2.9:1")
			var got map[string]string
			_ = json.Unmarshal(w.Body.Bytes(), &got)
			if w.Code != 200 || got["base"] != want {
				t.Errorf("%s: %d %v, want %s", host, w.Code, got, want)
			}
		}
	})

	t.Run("R-EP-002 the in-flight limit counts per endpoint", func(t *testing.T) {
		s := twoEndpoints()
		c := s.Endpoints["customers"]
		c.MaxInFlight = 1
		s.Endpoints["customers"] = c
		s.MaxInFlight = 10
		hold, release := make(chan struct{}), make(chan struct{})
		build := func(name string) http.Handler {
			e := endpointFixture(t, s, name)
			rt := NewEndpointRouter(e)
			rt.HandleFunc("GET /api/npm/{repoKey}/{rest...}", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("slow") != "" {
					hold <- struct{}{}
					<-release
				}
			})
			res, _ := NewResolver(nil, e.BaseURL(), nil, ForEndpoint(e))
			return Chain{Router: rt, Endpoint: e, Resolver: res, MaxInFlight: e.MaxInFlight()}.Handler()
		}
		cust, def := build("customers"), build("default")
		done := make(chan struct{})
		go func() {
			call(cust, "GET", "/api/npm/x/y?slow=1", "packages.example.com", "192.0.2.9:1")
			close(done)
		}()
		<-hold
		w := call(cust, "GET", "/api/npm/x/y", "packages.example.com", "192.0.2.9:1")
		if w.Code != 503 || problemOf(t, w).Code != "server_busy" {
			t.Errorf("second request on customers: %d", w.Code)
		}
		if w := call(def, "GET", "/api/npm/x/y", "trove.example.com", "192.0.2.9:1"); w.Code != 200 {
			t.Errorf("request on default: %d", w.Code)
		}
		close(release)
		<-done
	})

	t.Run("R-EP-009 the replica header follows the endpoint's setting", func(t *testing.T) {
		s := twoEndpoints()
		c := s.Endpoints["customers"]
		c.ReplicaHeader = "always"
		s.Endpoints["customers"] = c
		s.ReplicaHeader = "never"
		if got := call(endpointChain(t, s, "customers", nil), "GET", "/api/npm/a/b", "packages.example.com", "192.0.2.9:1").Header().Get(HeaderReplica); got != "pod-1" {
			t.Errorf("customers: %q", got)
		}
		if got := call(endpointChain(t, s, "default", nil), "GET", "/api/v1/system/ping", "trove.example.com", "192.0.2.9:1").Header().Get(HeaderReplica); got != "" {
			t.Errorf("default: %q", got)
		}
		// a further endpoint sends none by default
		if got := call(endpointChain(t, twoEndpoints(), "customers", nil), "GET", "/api/npm/a/b", "packages.example.com", "192.0.2.9:1").Header().Get(HeaderReplica); got != "" {
			t.Errorf("customers default: %q", got)
		}
	})
}

func TestEndpointSurface(t *testing.T) {
	s := twoEndpoints()
	noop := func(http.ResponseWriter, *http.Request) {}
	register := func(r *Router) {
		for _, p := range []string{
			"GET /api/v1/system/ping", "GET /api/v1/repositories", "GET /api/npm/{repoKey}/{rest...}", "/ui/", "/auth/",
			"POST /api/v1/auth/oidc/token", "GET /files/{repoKey}/{path...}", "POST /-/npm/v1/oidc/token/exchange/package/{name...}",
			"GET /test-fixture/x",
		} {
			r.HandleFunc(p, noop)
		}
	}

	t.Run("R-EP-005 a further endpoint registers only the routes of its surface", func(t *testing.T) {
		e := endpointFixture(t, s, "customers")
		r := NewEndpointRouter(e)
		register(r)
		got := r.Patterns()
		want := []string{"/", "GET /api/npm/{repoKey}/{rest...}"}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("patterns = %v, want %v", got, want)
		}
		if len(r.Dropped()) != 8 {
			t.Errorf("dropped = %v", r.Dropped())
		}
		if v := CheckSurface(got, e); len(v) != 0 {
			t.Errorf("violations: %v", v)
		}
	})

	t.Run("R-EP-005 the default endpoint registers every family and the fixed root row", func(t *testing.T) {
		e := endpointFixture(t, s, "default")
		r := NewEndpointRouter(e)
		register(r)
		if len(r.Dropped()) != 0 || len(r.Patterns()) != 11 {
			t.Errorf("patterns = %v dropped = %v", r.Patterns(), r.Dropped())
		}
		if v := CheckSurface(r.Patterns(), e); len(v) != 0 {
			t.Errorf("violations: %v", v)
		}
	})

	t.Run("R-EP-005 the surface check fails on a files pattern registered on an endpoint without files", func(t *testing.T) {
		e := endpointFixture(t, s, "customers")
		v := CheckSurface([]string{"/", "GET /api/npm/{repoKey}/{rest...}", "GET /files/{repoKey}/{path...}"}, e)
		if len(v) != 1 || !strings.Contains(v[0], "files") || !strings.Contains(v[0], "customers") {
			t.Fatalf("violations: %v", v)
		}
		if v := CheckSurface([]string{"/", "GET /{$}"}, e); len(v) != 1 {
			t.Errorf("root row without ui: %v", v)
		}
	})

	t.Run("R-EP-005 paths of families the endpoint does not serve answer the byte-identical 404 of an unknown path", func(t *testing.T) {
		e := endpointFixture(t, s, "customers")
		r := NewEndpointRouter(e)
		register(r)
		res, _ := NewResolver(nil, e.BaseURL(), nil, ForEndpoint(e))
		h := Chain{Router: r, Endpoint: e, Resolver: res}.Handler()
		body := func(method, path string) (int, string) {
			w := call(h, method, path, "packages.example.com", "192.0.2.9:1")
			var p map[string]any
			_ = json.Unmarshal(w.Body.Bytes(), &p)
			delete(p, "instance")
			delete(p, "request_id")
			delete(p, "requestId")
			b, _ := json.Marshal(p)
			return w.Code, string(b)
		}
		wantCode, wantBody := body("GET", "/does-not-exist")
		if wantCode != 404 {
			t.Fatalf("unknown path: %d", wantCode)
		}
		for _, tc := range [][2]string{
			{"GET", "/ui/"},
			{"GET", "/"},
			{"GET", "/auth/oidc/corp/start"},
			{"GET", "/api/v1/repositories"},
			{"GET", "/api/v1/system/ping"},
			{"POST", "/api/v1/auth/oidc/token"},
			{"GET", "/files/generic-local/x"},
			{"PUT", "/api/v1/repositories"},
			{"GET", "/v2/"},
			{"GET", "/v2/docker-all/manifests/latest"},
			{"GET", "/.well-known/terraform.json"},
		} {
			if code, b := body(tc[0], tc[1]); code != 404 || b != wantBody {
				t.Errorf("%s %s: %d %s (want 404 %s)", tc[0], tc[1], code, b, wantBody)
			}
		}
		if code, _ := body("GET", "/api/npm/npm-customers/"); code != 200 {
			t.Errorf("package route: %d", code)
		}
	})

	t.Run("R-EP-005 a surface can add api, ci_exchange and files", func(t *testing.T) {
		s := twoEndpoints()
		c := s.Endpoints["customers"]
		c.Surface = []string{"packages", "api", "ci_exchange", "files"}
		s.Endpoints["customers"] = c
		e := endpointFixture(t, s, "customers")
		r := NewEndpointRouter(e)
		register(r)
		if len(r.Dropped()) != 3 { // /ui/, /auth/, the test fixture
			t.Errorf("dropped = %v", r.Dropped())
		}
	})

	t.Run("R-EP-005 the family of a pattern", func(t *testing.T) {
		for p, want := range map[string]string{
			"GET /{$}": "ui", "/ui/": "ui", "/auth/": "ui", "GET /auth/oidc/{name}/start": "ui",
			"/api/v1/": "api", "GET /api/v1/system/ping": "api", "POST /api/v1/auth/oidc/token": "ci_exchange",
			"POST /-/npm/v1/oidc/token/exchange/package/{name...}": "ci_exchange",
			"/api/npm/{repoKey}/{rest...}":                         "packages", "/v2/": "packages", "/.well-known/terraform.json": "packages",
			"/files/{repoKey}/{path...}": "files", "GET /test-fixture/hold": "other",
		} {
			if got := FamilyOf(p); got != want {
				t.Errorf("FamilyOf(%q) = %q, want %q", p, got, want)
			}
		}
	})

	t.Run("R-EP-002 R-HA-041 the route table rules hold for the router of every endpoint", func(t *testing.T) {
		for _, name := range []string{"default", "customers"} {
			e := endpointFixture(t, s, name)
			r := NewEndpointRouter(e)
			register(r)
			if v := CheckRouteTable(r.Patterns(), nil); len(v) != 0 {
				t.Errorf("%s: %v", name, v)
			}
			bad := NewEndpointRouter(e)
			bad.HandleFunc("GET /v2/x", noop)
			bad.HandleFunc("GET /api/npm/{repoKey}/x", noop)
			if v := CheckRouteTable(bad.Patterns(), nil); len(v) != 1 {
				t.Errorf("%s: a /v2/ fixture gives %v", name, v)
			}
		}
	})

	t.Run("R-EP-006 the /.well-known/ prefix is reserved on every endpoint", func(t *testing.T) {
		for _, name := range []string{"default", "customers"} {
			e := endpointFixture(t, s, name)
			r := NewEndpointRouter(e)
			r.HandleFunc("GET /.well-known/x", noop)
			v := CheckRouteTable(r.Patterns(), nil)
			if len(v) != 1 || !strings.Contains(v[0], "/.well-known/x") {
				t.Errorf("%s: violations %v", name, v)
			}
			// removing the fixture passes again
			if v := CheckRouteTable(NewEndpointRouter(e).Patterns(), nil); len(v) != 0 {
				t.Errorf("%s after removing: %v", name, v)
			}
		}
		if v := CheckRouteTable([]string{"/.well-known/terraform.json"}, []string{"/.well-known/terraform.json"}); len(v) != 0 {
			t.Errorf("a format's own pattern is exempt: %v", v)
		}
		for _, p := range []string{"/.well-known", "/.well-known/", "GET example.com/.well-known/x"} {
			if v := CheckRouteTable([]string{p}, nil); len(v) != 1 {
				t.Errorf("%q: %v", p, v)
			}
		}
	})
}

func TestHostRootBinding(t *testing.T) {
	s := twoEndpoints()
	c := s.Endpoints["customers"]
	c.WildcardRoot = "label"
	s.Endpoints["customers"] = c
	e := endpointFixture(t, s, "customers")

	t.Run("R-EP-006 step 5 finds the binding of the effective host", func(t *testing.T) {
		for host, want := range map[string]string{
			"pkgs.example.org":                "docker-hub",
			"PKGS.example.org":                "docker-hub",
			"packages.example.com":            "",
			"docker-all.registry.example.com": "docker-all",
			"registry.example.com":            "",
			"unknown.example":                 "",
		} {
			if got := e.HostRoot(host); got != want {
				t.Errorf("HostRoot(%q) = %q, want %q", host, got, want)
			}
		}
	})

	t.Run("R-EP-006 the binding reaches the request context and routes like an unbound host", func(t *testing.T) {
		h := endpointChain(t, s, "customers", nil)
		var got map[string]string
		w := call(h, "GET", "/api/npm/npm-all/x", "pkgs.example.org", "192.0.2.9:1")
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		if w.Code != 200 || got["root"] != "docker-hub" {
			t.Errorf("bound host: %d %v", w.Code, got)
		}
		// /v2/ and /.well-known/ on a bound host answer the 404 problem: no format uses them yet.
		for _, p := range []string{"/v2/", "/.well-known/terraform.json"} {
			if w := call(h, "GET", p, "pkgs.example.org", "192.0.2.9:1"); w.Code != 404 {
				t.Errorf("%s on a bound host: %d", p, w.Code)
			}
		}
		w = call(h, "GET", "/api/npm/npm-all/x", "packages.example.com", "192.0.2.9:1")
		_ = json.Unmarshal(w.Body.Bytes(), &got)
		if got["root"] != "" {
			t.Errorf("unbound host: %v", got)
		}
	})

	t.Run("R-EP-006 a request outside a chain has no endpoint and no binding", func(t *testing.T) {
		if EndpointFrom(context.Background()) != nil || RootRepository(context.Background()) != "" {
			t.Error("context without a chain has an endpoint")
		}
		if EndpointFrom(context.Background()).Name() != "" {
			t.Error("nil endpoint name")
		}
	})
}

func TestEndpointTLS(t *testing.T) {
	ca := tlstest.NewCA(t, "endpoint ca")
	dir := t.TempDir()
	exact, exactKey := ca.Issue(t, tlstest.Options{DNSNames: []string{"packages.example.com"}, Serial: big.NewInt(100)}).Files(t, dir, "exact")
	wild, wildKey := ca.Issue(t, tlstest.Options{DNSNames: []string{"*.packages.example.com"}, Serial: big.NewInt(200)}).Files(t, dir, "wild")
	def, defKey := ca.Issue(t, tlstest.Options{DNSNames: []string{"trove.example.com"}, Serial: big.NewInt(300)}).Files(t, dir, "default")

	s := config.Server{
		Listen: ":8080", OpsListen: ":8081", BaseURL: "https://trove.example.com", MaxInFlight: 10, ReplicaHeader: "authenticated",
		TLS: config.ServerTLS{Certificates: pairOf(def, defKey), MinVersion: "1.2", ReloadInterval: 50 * time.Millisecond, ClientAuth: "none"},
		Endpoints: map[string]config.Endpoint{"customers": {
			Listen: ":8443", BaseURL: "https://packages.example.com",
			TLS: config.EndpointTLS{Certificates: []config.TLSCertificate{{CertFile: exact, KeyFile: exactKey}, {CertFile: wild, KeyFile: wildKey}}},
		}},
	}
	resolved := map[string]config.ResolvedEndpoint{}
	for _, r := range s.ResolveEndpoints() {
		resolved[r.Name] = r
	}
	serve := func(t *testing.T, name string) string {
		t.Helper()
		l, err := NewListenerTLS("client:"+name, resolved[name].TLS)
		if err != nil {
			t.Fatal(err)
		}
		_, addr := start(t, ServerConfig{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), TLS: l})
		return addr
	}
	serial := func(addr, sni string) int64 {
		t.Helper()
		c := &tls.Config{RootCAs: ca.Pool(), InsecureSkipVerify: true, ServerName: sni, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}} //nolint:gosec // the test reads which certificate arrives
		n, err := dial(addr, c)
		if err != nil {
			t.Fatalf("%q: %v", sni, err)
		}
		return n
	}

	t.Run("R-EP-004 SNI picks the endpoint's pair, an exact name before a wildcard, the first pair as fallback", func(t *testing.T) {
		addr := serve(t, "customers")
		for sni, want := range map[string]int64{
			"packages.example.com": 100, "a.packages.example.com": 200, "other.example": 100, "": 100,
		} {
			if got := serial(addr, sni); got != want {
				t.Errorf("SNI %q: serial %d, want %d", sni, got, want)
			}
		}
	})

	t.Run("R-EP-004 the default endpoint serves the pair of server.tls and never an endpoint's pair", func(t *testing.T) {
		addr := serve(t, "default")
		for _, sni := range []string{"packages.example.com", "a.packages.example.com", "trove.example.com", ""} {
			if got := serial(addr, sni); got != 300 {
				t.Errorf("SNI %q on default: serial %d", sni, got)
			}
		}
	})

	t.Run("R-EP-004 a further endpoint without pairs inherits the pair of server.tls", func(t *testing.T) {
		s2 := s
		s2.Endpoints = map[string]config.Endpoint{"customers": {Listen: ":8443", BaseURL: "https://packages.example.com"}}
		var tlsBlock config.ServerTLS
		for _, r := range s2.ResolveEndpoints() {
			if r.Name == "customers" {
				tlsBlock = r.TLS
			}
		}
		l, err := NewListenerTLS("client:customers", tlsBlock)
		if err != nil {
			t.Fatal(err)
		}
		_, addr := start(t, ServerConfig{Handler: http.NotFoundHandler(), TLS: l})
		if got := serial(addr, "packages.example.com"); got != 300 {
			t.Errorf("inherited pair: serial %d", got)
		}
	})

	t.Run("R-EP-004 rotating a pair of the second listener serves it within the reload interval, and a broken rotation keeps the old pair", func(t *testing.T) {
		addr := serve(t, "customers")
		if got := serial(addr, "a.packages.example.com"); got != 200 {
			t.Fatalf("before: %d", got)
		}
		// a pair whose key does not match: the previous pair stays in service
		other := ca.Issue(t, tlstest.Options{DNSNames: []string{"*.packages.example.com"}, Serial: big.NewInt(250)})
		tlstest.WriteFile(t, wild, other.CertPEM)
		time.Sleep(300 * time.Millisecond)
		if got := serial(addr, "a.packages.example.com"); got != 200 {
			t.Errorf("mismatched rotation changed the served pair: %d", got)
		}
		ca.Issue(t, tlstest.Options{DNSNames: []string{"*.packages.example.com"}, Serial: big.NewInt(201)}).Write(t, wild, wildKey)
		deadline := time.Now().Add(3 * time.Second)
		for serial(addr, "a.packages.example.com") != 201 {
			if time.Now().After(deadline) {
				t.Fatal("rotated pair not served")
			}
			time.Sleep(25 * time.Millisecond)
		}
		if got := serial(addr, "packages.example.com"); got != 100 {
			t.Errorf("the other pair changed: %d", got)
		}
	})

	t.Run("R-EP-004 a client CA on a further endpoint requires a client certificate there only", func(t *testing.T) {
		clientCA := tlstest.NewCA(t, "client ca")
		caFile := clientCA.WriteCA(t, t.TempDir(), "ca.pem")
		s2 := s
		c := s.Endpoints["customers"]
		c.TLS.ClientCAFile = caFile
		s2.Endpoints = map[string]config.Endpoint{"customers": c}
		var blk config.ServerTLS
		for _, r := range s2.ResolveEndpoints() {
			if r.Name == "customers" {
				blk = r.TLS
			}
		}
		if blk.ClientAuth != config.ClientAuthRequire {
			t.Fatalf("client auth = %q", blk.ClientAuth)
		}
		l, err := NewListenerTLS("client:customers", blk)
		if err != nil {
			t.Fatal(err)
		}
		_, addr := start(t, ServerConfig{Handler: http.NotFoundHandler(), TLS: l})
		conf := &tls.Config{InsecureSkipVerify: true, ServerName: "packages.example.com", MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}} //nolint:gosec // client side of a refusal test
		if _, err := dial(addr, conf); err == nil {
			t.Error("a handshake without a client certificate succeeded")
		}
		// the default endpoint is unaffected
		if got := serial(serve(t, "default"), "trove.example.com"); got != 300 {
			t.Errorf("default endpoint: %d", got)
		}
	})
}
