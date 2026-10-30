package edge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/httpserver"
)

// serve runs a request through the chain of the named endpoint, which stores the
// endpoint and the caller in the context as production does, and returns what the
// handler made of the request.
func serve(t *testing.T, endpoint string, p httpserver.Principal, handler func(*http.Request)) {
	t.Helper()
	s := config.Server{
		Listen: ":8080", OpsListen: ":8081", BaseURL: "https://trove.example.com", MaxInFlight: 4096,
		Endpoints: map[string]config.Endpoint{
			"customers": {Listen: ":8443", BaseURL: "https://packages.example.com"},
		},
	}
	var e *httpserver.Endpoint
	for _, r := range s.ResolveEndpoints() {
		if r.Name == endpoint {
			var err error
			if e, err = httpserver.NewEndpoint(r, s.AdditionalBaseURLs); err != nil {
				t.Fatal(err)
			}
		}
	}
	if e == nil {
		t.Fatalf("no endpoint %s", endpoint)
	}
	rt := httpserver.NewEndpointRouter(e)
	rt.HandleFunc("GET /api/npm/{repoKey}/{rest...}", func(_ http.ResponseWriter, r *http.Request) { handler(r) })
	res, err := httpserver.NewResolver(e.TrustedProxies(), e.BaseURL(), nil, httpserver.ForEndpoint(e))
	if err != nil {
		t.Fatal(err)
	}
	h := httpserver.Chain{
		Router: rt, Endpoint: e, Resolver: res, MaxInFlight: e.MaxInFlight(),
		Authenticate: func(*http.Request) (httpserver.Principal, error) { return p, nil },
	}.Handler()
	req := httptest.NewRequest("GET", "/api/npm/npm-all/x", nil)
	req.Host, req.RemoteAddr = "packages.example.com", "192.0.2.9:4000"
	if endpoint == "default" {
		req.Host = "trove.example.com"
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("the chain answered the request itself: %d %s", w.Code, w.Body.String())
	}
}

func TestR_AUTHZ_002_SubjectFrom(t *testing.T) {
	t.Run("R-AUTHZ-002 anonymous", func(t *testing.T) {
		s := SubjectOf(context.Background())
		if !s.Anonymous || s.Scope != nil || s.ID != "" || s.Endpoint != "" {
			t.Fatalf("subject = %+v", s)
		}
	})
	t.Run("R-AUTHZ-013 the scope is the credential's, a ceiling that carries the endpoint binding", func(t *testing.T) {
		sc := &scope.Scope{
			Repositories: []scope.Grant{{Repositories: []string{"generic-local"}, Actions: []string{"read"}, Paths: scope.Patterns{Include: []string{"releases/**"}}}},
			Endpoints:    []string{"customers"},
		}
		p := httpserver.Principal{
			Kind: httpserver.KindServiceAccount, ID: "id1", Name: "sa1",
			Groups:   []httpserver.GroupRef{{ID: "g1", Name: "devs"}},
			MinEpoch: 7,
			Credential: httpserver.Credential{
				Method: httpserver.MethodBearer, TokenID: "t1", TokenType: httpserver.TokenSAT, Scope: sc,
			},
		}
		var s authz.Subject
		serve(t, "customers", p, func(r *http.Request) { s = SubjectFrom(r) })
		if s.Scope != sc || s.Scope.Inherit || s.Scope.Repositories[0].Paths.Include[0] != "releases/**" || s.Scope.Endpoints[0] != "customers" {
			t.Fatalf("scope = %+v", s.Scope)
		}
		if s.TokenID != "t1" || s.AuthMethod != "bearer" || s.MinEpoch != 7 || len(s.Groups) != 1 || s.Groups[0].Name != "devs" || string(s.Kind) != "service_account" {
			t.Fatalf("subject = %+v", s)
		}
	})
}

// TestR_EP_013_TheSubjectCarriesTheListenersEndpoint: the one conversion takes the
// endpoint from the listener's chain, for the anonymous caller as for a
// credential, and a context outside a chain yields none (Q-EP-20 (b)).
func TestR_EP_013_TheSubjectCarriesTheListenersEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name     string
		endpoint string
		p        httpserver.Principal
	}{
		{"a token on customers", "customers", httpserver.Principal{Kind: httpserver.KindUser, ID: "u1", Name: "alice"}},
		{"an anonymous caller on customers", "customers", httpserver.AnonymousPrincipal()},
		{"a token on default", "default", httpserver.Principal{Kind: httpserver.KindUser, ID: "u1", Name: "alice"}},
	} {
		t.Run("R-EP-013 R-EP-002 "+tc.name, func(t *testing.T) {
			var viaRequest, viaContext authz.Subject
			serve(t, tc.endpoint, tc.p, func(r *http.Request) { viaRequest, viaContext = SubjectFrom(r), SubjectOf(r.Context()) })
			if viaRequest.Endpoint != tc.endpoint || viaContext.Endpoint != tc.endpoint {
				t.Fatalf("the subject names endpoint %q / %q, the listener is %q", viaRequest.Endpoint, viaContext.Endpoint, tc.endpoint)
			}
			if viaRequest.Anonymous != tc.p.Anonymous {
				t.Fatalf("subject = %+v", viaRequest)
			}
		})
	}
	t.Run("R-EP-013 a context outside a request carries no endpoint, so a job's subject has no ceiling", func(t *testing.T) {
		if ep := SubjectOf(httpserver.WithPrincipal(context.Background(), httpserver.Principal{Kind: httpserver.KindUser, ID: "u1"})).Endpoint; ep != "" {
			t.Fatalf("endpoint %q", ep)
		}
	})
}

func TestQ_OPS_49_ActorFrom(t *testing.T) {
	a := ActorFrom(httpserver.Principal{
		Kind: httpserver.KindServiceAccount, ID: "id1", Name: "sa1",
		Credential: httpserver.Credential{Method: httpserver.MethodBearer, TokenID: "t1"},
	})
	if a.Kind != "service_account" || a.ID != "id1" || a.Name != "sa1" || a.TokenID != "t1" || a.AuthMethod != "bearer" {
		t.Fatalf("actor = %+v", a)
	}
	if got := ActorFrom(httpserver.AnonymousPrincipal()); got.Kind != "anonymous" || got.ID != "" {
		t.Fatalf("anonymous actor = %+v", got)
	}
}
