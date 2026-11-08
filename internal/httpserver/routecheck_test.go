package httpserver

import (
	"net/http"
	"strings"
	"testing"
)

// ServeMux unescapes the literal segments of a pattern and a host-only pattern serves every path of its host, so the text of a
// pattern is not the whole rule: the check puts each pattern into a scratch mux and probes the paths of the registry root.
func TestRouteTableProbesWhatThePatternServes(t *testing.T) {
	for _, p := range []string{"/v%32/{rest...}", "GET /v%32/_token", "/v%32/", "example.com/", "POST registry.example.com/"} {
		if v := CheckRouteTable([]string{p}, nil, nil); len(v) != 1 {
			t.Errorf("R-HA-041 %q: violations %v", p, v)
		}
	}
	t.Run("R-HA-041 the probe names the path the pattern serves", func(t *testing.T) {
		v := CheckRouteTable([]string{"/v%32/{rest...}"}, nil, nil)
		if len(v) != 1 || !strings.Contains(v[0], "/v2") {
			t.Fatalf("violations %v", v)
		}
	})
	t.Run("R-HA-041 patterns that serve no path of the registry root pass", func(t *testing.T) {
		for _, p := range []string{"/v20/y", "/ui/", "GET /api/v1/system/ping", "example.com/ui/", "POST /files/{projectKey}/{repoKey}/{path...}"} {
			if v := CheckRouteTable([]string{p}, nil, nil); len(v) != 0 {
				t.Errorf("%q: %v", p, v)
			}
		}
	})
	t.Run("R-HA-041 an escaped literal segment is judged as what it serves", func(t *testing.T) {
		for _, p := range []string{"GET /v%32/web/docker-local/{name}/manifests/{ref}", "/v%32/web/docker-local/{name}/manifests/{ref}", "/%76%32/x"} {
			if v := CheckRouteTable([]string{p}, nil, nil); len(v) != 1 {
				t.Errorf("%q: %v", p, v)
			}
			// ... and an exemption by the text of the pattern lists the same spelling only.
			if v := CheckRouteTable([]string{p}, []string{p}, nil); len(v) != 0 {
				t.Errorf("%q listed by the enforcement point: %v", p, v)
			}
		}
		if got := LiteralPatternPath("GET example.com/v%32/{a}/%5F"); got != "/v2/{a}/_" {
			t.Errorf("LiteralPatternPath = %q", got)
		}
	})
	t.Run("R-HA-041 the patterns of the enforcement point stay exempt", func(t *testing.T) {
		if v := CheckRouteTable([]string{"/v2/{rest...}"}, []string{"/v2/{rest...}"}, nil); len(v) != 0 {
			t.Fatalf("%v", v)
		}
	})
}

// A router serves paths that no pattern names: the uncleaned prefixes and the fallback (R-HA-041, Q-OCI-101).
func TestRouteTableChecksTheWaysAroundThePatterns(t *testing.T) {
	h := http.NotFoundHandler()
	mustPanic := func(name string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s: no panic", name)
			}
		}()
		f()
	}
	t.Run("R-HA-041 HandleUncleaned refuses a prefix at or below /v2", func(t *testing.T) {
		for _, p := range []string{"/v2", "/v2/", "/v2/x/", "/v2/web/docker-local/"} {
			mustPanic(p, func() { NewClientRouter().HandleUncleaned(p, h) })
		}
		for _, p := range []string{"/files/", "/api/oci/", "/v2x/", "/api/v2/"} {
			NewClientRouter().HandleUncleaned(p, h)
		}
	})
	t.Run("R-HA-041 HandleRegistryUncleaned takes only a prefix at or below /v2", func(t *testing.T) {
		NewClientRouter().HandleRegistryUncleaned("/v2/", h)
		for _, p := range []string{"/files/", "/", "/v2x/"} {
			mustPanic(p, func() { NewClientRouter().HandleRegistryUncleaned(p, h) })
		}
	})
	t.Run("R-HA-041 an uncleaned prefix below /v2 is admitted only when the enforcement point registered it", func(t *testing.T) {
		r := NewClientRouter()
		r.HandleRegistryUncleaned("/v2/", h)
		if v := CheckRouter(r, nil, nil, nil); len(v) != 1 || !strings.Contains(v[0], `"/v2/"`) {
			t.Fatalf("an unlisted prefix: %v", v)
		}
		if v := CheckRouter(r, nil, []string{"/v2/"}, nil); len(v) != 0 {
			t.Fatalf("the listed prefix: %v", v)
		}
		if v := CheckRouter(r, nil, []string{"/v2/other/"}, nil); len(v) != 1 {
			t.Fatalf("another prefix listed: %v", v)
		}
	})
	t.Run("R-HA-041 an escaped uncleaned prefix is judged as what it serves", func(t *testing.T) {
		mustPanic("escaped", func() { NewClientRouter().HandleUncleaned("/v%32/", h) })
		mustPanic("escaped letters", func() { NewClientRouter().HandleUncleaned("/%76%32/x/", h) })
		r := NewClientRouter()
		r.HandleUncleaned("/%76/", h) // "/v/" does not enclose /v2/
		if v := CheckRouter(r, nil, nil, nil); len(v) != 0 {
			t.Fatalf("%v", v)
		}
	})
	t.Run("R-HA-041 a prefix is registered once: a second registration would shadow the first", func(t *testing.T) {
		r := NewClientRouter()
		r.HandleRegistryUncleaned("/v2/", h)
		mustPanic("duplicate", func() { r.HandleRegistryUncleaned("/v2/", h) })
		r.HandleUncleaned("/files/", h)
		mustPanic("duplicate", func() { r.HandleUncleaned("/files/", h) })
	})
	t.Run("R-HA-041 a prefix that encloses /v2/ serves it too", func(t *testing.T) {
		r := NewClientRouter()
		r.HandleUncleaned("/", h) // what the Bastion's router registers; its router is not checked
		if v := CheckRouter(r, nil, []string{"/v2/"}, nil); len(v) != 1 {
			t.Fatalf("the root prefix: %v", v)
		}
	})
	t.Run("R-HA-041 a fallback handler answers /v2/ and is refused", func(t *testing.T) {
		r := NewClientRouter()
		if v := CheckRouter(r, nil, nil, nil); len(v) != 0 {
			t.Fatalf("a router without a fallback: %v", v)
		}
		r.SetFallback(h)
		if v := CheckRouter(r, nil, nil, nil); len(v) != 1 || !strings.Contains(v[0], "fallback") {
			t.Fatalf("a fallback: %v", v)
		}
	})
	t.Run("R-HA-041 the patterns are checked as before", func(t *testing.T) {
		r := NewClientRouter()
		r.HandleFunc("GET /v2/x", func(http.ResponseWriter, *http.Request) {})
		if v := CheckRouter(r, nil, nil, nil); len(v) != 1 {
			t.Fatalf("%v", v)
		}
	})
}
