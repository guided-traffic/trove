//go:build integration

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// The upstream-test operations (upstream_test.go) test the repository service
// with an upstream client of the test's own: its own catalog (a format with a
// probe path), an outbound factory trusting the TLS test upstream and a key ring
// of its own. The composition root builds none of that from the stack's
// configuration, so this suite serves the management API over those components
// directly, authenticating by header, like a component suite does. Everything the
// other suites of the package test goes through the harness.

// testAccess decides permissions from the principal the test authenticator
// built: the system capabilities of its token scope. An anonymous caller is 401.
type testAccess struct{}

func (testAccess) Require(ctx context.Context, permission string) error {
	p := httpserver.PrincipalFrom(ctx)
	if p.Anonymous {
		return errs.E(errs.Unauthenticated, errs.CodeUnauthenticated, "credentials required", errs.WithChallenge("Bearer"))
	}
	if p.Credential.Scope != nil {
		for _, c := range p.Credential.Scope.System {
			if c == permission {
				return nil
			}
		}
	}
	return errs.E(errs.Forbidden, errs.CodeForbidden, "missing "+permission)
}

// testAuth reads the caller from headers: X-Test-Principal is "<uuid>[:<token id>]",
// X-Test-Caps the comma-separated capabilities. No header is an anonymous caller.
func testAuth(r *http.Request) (httpserver.Principal, error) {
	id, _, _ := strings.Cut(r.Header.Get("X-Test-Principal"), ":")
	if id == "" {
		return httpserver.AnonymousPrincipal(), nil
	}
	token := id
	if _, t, ok := strings.Cut(r.Header.Get("X-Test-Principal"), ":"); ok {
		token = t
	}
	var caps []string
	if c := r.Header.Get("X-Test-Caps"); c != "" {
		caps = strings.Split(c, ",")
	}
	return httpserver.Principal{
		ID: id, Name: "user-" + id[:8], Kind: httpserver.KindUser,
		Credential: httpserver.Credential{Method: httpserver.MethodBearer, TokenID: token, Scope: &scope.Scope{Inherit: true, System: caps}},
	}, nil
}

type replicaLister struct{}

func (replicaLister) Replicas(context.Context) ([]gen.Replica, error) { return nil, nil }

type noFacts struct{}

func (noFacts) Facts(context.Context) (v1.Facts, error) { return v1.Facts{}, nil }

// serveComponent serves the management API over a database of the installation and
// the given repository services, behind the chain with the header authenticator,
// with every answer validated against the document, and returns the node of the
// test cluster that reaches it.
func (e *env) serveComponent(db *postgres.DB, repos v1.RepositoryServices) *node {
	t := e.t
	t.Helper()
	doc, err := v1.LoadDocument(v1.EmbeddedYAML())
	if err != nil {
		t.Fatal(err)
	}
	cfg := apiConfig()
	api := v1.New(v1.Options{
		Document: doc, Identity: v1.Identity{Replica: "component", Role: "web"}, Ready: func() bool { return true },
		Access: testAccess{}, Replicas: replicaLister{}, Facts: noFacts{},
		API: v1.Services{Config: cfg, Store: db}, Repositories: repos,
	})
	router := httpserver.NewClientRouter()
	api.Register(router)
	res, err := httpserver.NewResolver(nil, "http://"+testHost, nil)
	if err != nil {
		t.Fatal(err)
	}
	chain := httpserver.Chain{
		Router: router, Resolver: res, Authenticate: testAuth, Replica: "component", ReplicaHeader: httpserver.ReplicaAlways,
	}.Handler()
	srv := httptest.NewServer(e.stack.Validator().Handler(t, chain))
	t.Cleanup(srv.Close)
	return &node{base: srv.URL}
}

// testHost is the Host of the base URL the component server resolves.
const testHost = "trove.test"
