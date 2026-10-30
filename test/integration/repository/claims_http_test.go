//go:build integration

package repository

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	v1 "github.com/guided-traffic/trove/internal/api/v1"
	"github.com/guided-traffic/trove/internal/api/v1/apitest"
	"github.com/guided-traffic/trove/internal/authz"
	"github.com/guided-traffic/trove/internal/config"
	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/httpserver"
	"github.com/guided-traffic/trove/internal/repository"
)

// claimsAccess is the decision point of this test, small enough to read: the
// shared step of the API asks it for read and manage on a repository, the claims
// handler asks it whether the caller holds repositories:write-config.
//
//	admin  manage on every repository and repositories:write-config
//	lead   manage, no write-config
//	reader read only
//	other  nothing: the repository does not exist for it
type claimsAccess struct{}

func nameOf(ctx context.Context) string { return httpserver.PrincipalFrom(ctx).Name }

func (claimsAccess) Require(ctx context.Context, permission string) error {
	if permission == string(authz.RepositoriesWrite) && nameOf(ctx) == "admin" {
		return nil
	}
	return errs.E(errs.Forbidden, errs.CodeForbidden, "The operation is not permitted.")
}

func (a claimsAccess) Holds(ctx context.Context, permission string) (bool, error) {
	return a.Require(ctx, permission) == nil, nil
}

func (claimsAccess) RequireAction(ctx context.Context, c authz.Coordinate) error {
	name := nameOf(ctx)
	switch {
	case name == "":
		return errs.E(errs.Unauthenticated, errs.CodeUnauthenticated, "credentials required", errs.WithChallenge("Bearer"))
	case name == "other":
		return errs.E(errs.NotFound, errs.CodeNotFound, "not found")
	case c.Actions[0] == authz.Manage && name == "reader":
		return errs.E(errs.Forbidden, errs.CodeForbidden, "The operation is not permitted.")
	}
	return nil
}

type claimsServer struct {
	t   *testing.T
	url string
	rep *replica
}

func (e *env) serveClaims(rep *replica) *claimsServer {
	t := e.t
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + ln.Addr().String()
	_ = ln.Close()
	doc, err := v1.LoadDocument(v1.EmbeddedYAML())
	if err != nil {
		t.Fatal(err)
	}
	router := httpserver.NewClientRouter()
	v1.New(v1.Options{
		Document: doc, Identity: v1.Identity{Replica: "claims", Role: "web"}, Ready: func() bool { return true },
		Access: claimsAccess{},
		API: v1.Services{Config: config.API{
			PageSizeDefault: 50, PageSizeMax: 500, IdempotencyRetention: time.Hour,
			MaxRequestBodyBytes: config.MiB, MaxConfigurationBodyBytes: 4 * config.MiB,
		}},
		Repositories: v1.RepositoryServices{Service: rep.svc},
	}).Register(router)
	resolver, err := httpserver.NewResolver(nil, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	authenticate := func(r *http.Request) (httpserver.Principal, error) {
		h := r.Header.Get("Authorization")
		if h == "" {
			return httpserver.AnonymousPrincipal(), nil
		}
		name := strings.TrimPrefix(h, "Bearer ")
		return httpserver.Principal{
			Kind: httpserver.KindUser, ID: "0198a000-0000-7000-8000-00000000000" + string(name[0]), Name: name,
			Credential: httpserver.Credential{Method: httpserver.MethodBearer, TokenID: "test-" + name},
		}, nil
	}
	chain := httpserver.Chain{Router: router, Resolver: resolver, Authenticate: authenticate, Replica: "claims", ReplicaHeader: httpserver.ReplicaAlways}
	val := apitest.New(t, v1.EmbeddedYAML())
	srv, err := httpserver.NewServer(httpserver.ServerConfig{
		Name: "client", Addr: strings.TrimPrefix(url, "http://"), Handler: val.Handler(t, chain.Handler()), Log: quiet,
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 1 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Bind(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve() }()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})
	return &claimsServer{t: t, url: url, rep: rep}
}

type answer struct {
	status int
	header http.Header
	body   []byte
}

func (a answer) json(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(a.body, &m); err != nil {
		t.Fatalf("answer %d is not a JSON object: %v\n%s", a.status, err, a.body)
	}
	return m
}

func (s *claimsServer) do(user, method, path, body string, hdr ...string) answer {
	s.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, s.url+path, rd)
	if err != nil {
		s.t.Fatal(err)
	}
	if user != "" {
		req.Header.Set("Authorization", "Bearer "+user)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := (&http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return answer{status: res.StatusCode, header: res.Header, body: b}
}

func TestClaimsAPI(t *testing.T) {
	e := newEnv(t)
	rep := e.newReplica("claims-http", replicaOpts{poll: 300 * time.Millisecond, catalog: pkgCatalog()})
	rep.mustCreate("npm-local", "local", "pkgfmt", "")
	srv := e.serveClaims(rep)
	const path = "/api/v1/repositories/npm-local/claims"

	t.Run("R-REPO-023 GET answers the claims with ETag claims_version", func(t *testing.T) {
		a := srv.do("reader", "GET", path, "")
		if a.status != 200 || a.header.Get("ETag") != `"1"` {
			t.Fatalf("%d %q %s", a.status, a.header.Get("ETag"), a.body)
		}
		if claims, _ := a.json(t)["claims"].([]any); len(claims) != 0 {
			t.Fatalf("claims = %v", claims)
		}
		if a := srv.do("reader", "GET", path, "", "If-None-Match", `"1"`); a.status != 304 {
			t.Fatalf("If-None-Match: %d", a.status)
		}
	})
	t.Run("R-REPO-023 a caller who cannot read the repository gets 404, an anonymous one 401", func(t *testing.T) {
		if a := srv.do("other", "GET", path, ""); a.status != 404 {
			t.Fatalf("%d %s", a.status, a.body)
		}
		if a := srv.do("", "GET", path, ""); a.status != 401 {
			t.Fatalf("%d", a.status)
		}
	})
	t.Run("R-REPO-023 R-API-012 PUT without If-Match answers 428 and changes no claim", func(t *testing.T) {
		a := srv.do("lead", "PUT", path, `{"claims":[{"pattern":"@acme/*"}]}`)
		if a.status != 428 || a.json(t)["code"] != "precondition_required" {
			t.Fatalf("%d %s", a.status, a.body)
		}
		if n := e.scalar(`SELECT count(*) FROM namespace_claims`); n != 0 {
			t.Fatalf("%d claims", n)
		}
	})
	t.Run("R-REPO-023 manage is enough for a members claim: 200 with the new tag", func(t *testing.T) {
		a := srv.do("lead", "PUT", path, `{"claims":[{"pattern":"@acme/*"}]}`, "If-Match", `"1"`)
		if a.status != 200 || a.header.Get("ETag") != `"2"` {
			t.Fatalf("%d %q %s", a.status, a.header.Get("ETag"), a.body)
		}
		m := a.json(t)
		claims := m["claims"].([]any)
		first := claims[0].(map[string]any)
		if len(claims) != 1 || first["pattern"] != "@acme/*" || first["scope"] != "members" || first["source"] != "manual" {
			t.Fatalf("claims = %v", claims)
		}
		if w, ok := m["warnings"].([]any); !ok || len(w) != 0 {
			t.Fatalf("warnings = %v", m["warnings"])
		}
	})
	t.Run("R-REPO-023 a stale If-Match answers 412 with currentEtag", func(t *testing.T) {
		a := srv.do("lead", "PUT", path, `{"claims":[{"pattern":"@acme/*"}]}`, "If-Match", `"1"`)
		m := a.json(t)
		if a.status != 412 || m["currentEtag"] != `"2"` {
			t.Fatalf("%d %s", a.status, a.body)
		}
	})
	t.Run("R-REPO-023 a global claim without repositories:write-config is 403, with it 200; manage alone keeps the same pattern as members", func(t *testing.T) {
		body := `{"claims":[{"pattern":"@acme/*","scope":"global"}]}`
		if a := srv.do("lead", "PUT", path, body, "If-Match", `"2"`); a.status != 403 {
			t.Fatalf("lead: %d %s", a.status, a.body)
		}
		if a := srv.do("reader", "PUT", path, body, "If-Match", `"2"`); a.status != 403 {
			t.Fatalf("reader: %d %s", a.status, a.body)
		}
		a := srv.do("admin", "PUT", path, body, "If-Match", `"2"`)
		if a.status != 200 || a.header.Get("ETag") != `"3"` {
			t.Fatalf("admin: %d %q %s", a.status, a.header.Get("ETag"), a.body)
		}
		// The same pattern as a members claim, written by the manager with the current tag:
		// narrowing a global claim is a global change and needs the permission as well.
		if a := srv.do("lead", "PUT", path, `{"claims":[{"pattern":"@acme/*"}]}`, "If-Match", `"3"`); a.status != 403 {
			t.Fatalf("lead narrowing: %d %s", a.status, a.body)
		}
		if a := srv.do("admin", "PUT", path, `{"claims":[{"pattern":"@acme/*"}]}`, "If-Match", `"3"`); a.status != 200 {
			t.Fatalf("admin narrowing: %d %s", a.status, a.body)
		}
		if a := srv.do("lead", "PUT", path, `{"claims":[{"pattern":"@acme/*"},{"pattern":"left-*"}]}`, "If-Match", `"4"`); a.status != 200 {
			t.Fatalf("lead members claims: %d %s", a.status, a.body)
		}
	})
	t.Run("R-REPO-023 an automatic claim moves the tag: the PUT with the tag read before answers 412 with the new one", func(t *testing.T) {
		before := srv.do("lead", "GET", path, "").header.Get("ETag")
		if _, err := rep.autoClaim("npm-local", "fresh-name"); err != nil {
			t.Fatal(err)
		}
		a := srv.do("lead", "PUT", path, `{"claims":[]}`, "If-Match", before)
		m := a.json(t)
		now := srv.do("lead", "GET", path, "").header.Get("ETag")
		if a.status != 412 || m["currentEtag"] != now || now == before {
			t.Fatalf("%d %s (tag before %s, now %s)", a.status, a.body, before, now)
		}
	})
	t.Run("R-REPO-023 R-API-009 an invalid pattern is 422 invalid_pattern, an unknown member 400", func(t *testing.T) {
		tag := srv.do("lead", "GET", path, "").header.Get("ETag")
		a := srv.do("lead", "PUT", path, `{"claims":[{"pattern":"a*b"}]}`, "If-Match", tag)
		m := a.json(t)
		if a.status != 422 || m["code"] != "validation_failed" || !strings.Contains(string(a.body), `"invalid_pattern"`) {
			t.Fatalf("%d %s", a.status, a.body)
		}
		if a := srv.do("lead", "PUT", path, `{"claim":[]}`, "If-Match", tag); a.status != 400 {
			t.Fatalf("%d %s", a.status, a.body)
		}
		if a := srv.do("lead", "PUT", path, `{"claims":[{"pattern":"x","scope":"galactic"}]}`, "If-Match", tag); a.status != 422 {
			t.Fatalf("%d %s", a.status, a.body)
		}
	})
	t.Run("R-REPO-023 removeAuto drops an automatic claim and keeps the manual set", func(t *testing.T) {
		tag := srv.do("lead", "GET", path, "").header.Get("ETag")
		a := srv.do("lead", "PUT", path, `{"claims":[{"pattern":"@acme/*"}],"removeAuto":["fresh-name"]}`, "If-Match", tag)
		if a.status != 200 {
			t.Fatalf("%d %s", a.status, a.body)
		}
		if n := e.scalar(`SELECT count(*) FROM namespace_claims WHERE pattern = 'fresh-name'`); n != 0 {
			t.Fatal("the automatic claim remains")
		}
	})
	t.Run("R-REPO-023 the audit trail records the claim changes", func(t *testing.T) {
		n := 0
		for _, ty := range rep.rec.types() {
			if ty == repository.EventClaimsChanged {
				n++
			}
		}
		if n < 5 {
			t.Fatalf("%d claims_changed events: %v", n, rep.rec.types())
		}
	})
}
