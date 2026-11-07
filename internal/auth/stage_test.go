package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/errs"
)

// fakeTokens is a token validator over a table of token values.
type fakeTokens struct {
	mu     sync.Mutex
	known  map[string]token.Info
	errFor map[string]error
	calls  atomic.Int64
	noted  []string
	// throttleAfter makes Check consult the throttle like the real validator.
}

// NoteUse records the uses the stage reports as successful authentications.
func (f *fakeTokens) NoteUse(id, addr string) {
	f.mu.Lock()
	f.noted = append(f.noted, id+"@"+addr)
	f.mu.Unlock()
}

func (f *fakeTokens) Validate(ctx context.Context, raw string) (token.Info, error) {
	return f.Check(ctx, raw, token.Request{})
}

func (f *fakeTokens) Check(ctx context.Context, raw string, req token.Request) (token.Info, error) {
	if _, err := token.Check(raw); err != nil {
		if req.Throttle != nil {
			req.Throttle.Count(ctx)
		}
		return token.Info{}, token.ErrMalformed
	}
	if req.Throttle != nil && req.Throttle.Exceeded(ctx) {
		return token.Info{}, &token.ThrottledError{RetryAfter: req.RetryAfter}
	}
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.errFor[raw]; err != nil {
		if req.Throttle != nil {
			req.Throttle.Count(ctx)
		}
		return token.Info{}, err
	}
	if info, ok := f.known[raw]; ok {
		return info, nil
	}
	if req.Throttle != nil {
		req.Throttle.Count(ctx)
	}
	return token.Info{}, token.ErrUnknown
}

type fakeStore struct {
	groups map[string][]principal.GroupRef
}

func (fakeStore) ByID(context.Context, string) (principal.Principal, error) {
	return principal.Principal{}, principal.ErrNotFound
}

func (fakeStore) ByName(context.Context, principal.Kind, string) (principal.Principal, error) {
	return principal.Principal{}, principal.ErrNotFound
}

func (s fakeStore) Groups(_ context.Context, id string) ([]principal.GroupRef, error) {
	return s.groups[id], nil
}

type fakeAnon struct{ on bool }

func (f fakeAnon) Effective(context.Context) (token.Effective, error) {
	return token.Effective{AnonymousAccess: f.on}, nil
}

type fakeSessions struct {
	info    session.Info
	err     error
	csrfErr error
}

func (f fakeSessions) Validate(context.Context, string) (session.Info, error) { return f.info, f.err }
func (f fakeSessions) CheckCSRF(*http.Request, session.Info) error            { return f.csrfErr }

func newToken(t *testing.T, typ token.Type) string {
	t.Helper()
	s, err := token.Generate(typ)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var alice = principal.Principal{ID: "0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a10", Kind: principal.KindUser, Name: "alice", Status: principal.StatusActive}

func defaultRules() EndpointRules {
	return EndpointRules{Name: "default", AcceptsSession: true, TokenTypes: []token.Type{token.TypePAT, token.TypeSAT, token.TypeCIT}, Anonymous: true}
}

func newStage(ft *fakeTokens, o Options) *Stage {
	o.Tokens = ft
	o.Uses = ft
	if o.Principals == nil {
		o.Principals = fakeStore{groups: map[string][]principal.GroupRef{alice.ID: {{ID: "g1", Name: "team-web"}}}}
	}
	if o.Settings == nil {
		o.Settings = fakeAnon{}
	}
	o.BasicEnabled = true
	return New(o)
}

func req(method, path string, hdr ...string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	return r
}

func wantErr(t *testing.T, err error, status int, code string) *errs.Error {
	t.Helper()
	e, ok := errs.As(err)
	if !ok {
		t.Fatalf("error = %v, want an *errs.Error %d %s", err, status, code)
	}
	if e.Status() != status || e.Code != code {
		t.Fatalf("error = %d %s, want %d %s", e.Status(), e.Code, status, code)
	}
	return e
}

func TestR_AUTH_001_BearerAndBasicYieldThePrincipal(t *testing.T) {
	pat := newToken(t, token.TypePAT)
	ft := &fakeTokens{known: map[string]token.Info{pat: {ID: "t1", Type: token.TypePAT, Scope: token.Scope{Inherit: true}, Owner: alice}}}
	s := newStage(ft, Options{})
	for name, h := range map[string]string{
		"R-AUTH-033 bearer": "Bearer " + pat,
		"R-AUTH-033 basic":  "Basic " + base64.StdEncoding.EncodeToString([]byte("whatever:"+pat)),
	} {
		t.Run(name, func(t *testing.T) {
			res, err := s.Authenticate(req("GET", "/api/v1/auth/whoami", "Authorization", h), defaultRules())
			if err != nil {
				t.Fatal(err)
			}
			if res.Anonymous || res.Principal.Name != "alice" || res.Credential.TokenID != "t1" || res.Credential.Scope == nil || !res.Credential.Scope.Inherit {
				t.Fatalf("result %+v", res)
			}
			if len(res.Groups) != 1 || res.Groups[0].Name != "team-web" {
				t.Fatalf("groups are resolved from the principal cache at request time: %+v", res.Groups)
			}
		})
	}
	t.Run("R-AUTH-001 the Basic username is recorded and otherwise ignored", func(t *testing.T) {
		h := "Basic " + base64.StdEncoding.EncodeToString([]byte("ci:"+pat))
		res, err := s.Authenticate(req("GET", "/files/x", "Authorization", h), defaultRules())
		if err != nil || res.Credential.BasicUsername != "ci" || res.Credential.Method != MethodBasic {
			t.Fatalf("%+v %v", res, err)
		}
	})
}

func TestR_AUTH_001_PrecedenceAndNoFallThrough(t *testing.T) {
	pat := newToken(t, token.TypePAT)
	expired := newToken(t, token.TypePAT)
	ft := &fakeTokens{
		known:  map[string]token.Info{pat: {ID: "t1", Type: token.TypePAT, Owner: alice}},
		errFor: map[string]error{expired: token.ErrExpired},
	}
	other := principal.Principal{ID: "bob-id", Kind: principal.KindUser, Name: "bob", Status: principal.StatusActive}
	s := newStage(ft, Options{Sessions: fakeSessions{info: session.Info{ID: "s1", Principal: other}}, Settings: fakeAnon{on: true}})
	cookie := &http.Cookie{Name: SessionCookie, Value: "sess"}

	t.Run("R-AUTH-001 the Authorization header wins over a session cookie of another user", func(t *testing.T) {
		r := req("GET", "/api/v1/auth/whoami", "Authorization", "Bearer "+pat)
		r.AddCookie(cookie)
		res, err := s.Authenticate(r, defaultRules())
		if err != nil || res.Principal.Name != "alice" || res.Credential.Method != MethodBearer {
			t.Fatalf("%+v %v", res, err)
		}
	})
	t.Run("R-AUTH-001 an expired token is 401 and never the anonymous response", func(t *testing.T) {
		_, err := s.Authenticate(req("GET", "/api/npm/npm-all/lodash", "Authorization", "Bearer "+expired), defaultRules())
		e := wantErr(t, err, 401, errs.CodeInvalidToken)
		if e.Challenge != `Basic realm="trove"` {
			t.Fatalf("package endpoints challenge with Basic: %q", e.Challenge)
		}
	})
	t.Run("R-AUTH-002 the management API challenge names invalid_token", func(t *testing.T) {
		_, err := s.Authenticate(req("GET", "/api/v1/tokens", "Authorization", "Bearer "+expired), defaultRules())
		e := wantErr(t, err, 401, errs.CodeInvalidToken)
		if e.Challenge != `Bearer realm="trove", error="invalid_token"` {
			t.Fatalf("challenge %q", e.Challenge)
		}
	})
	t.Run("R-AUTH-001 an identity-provider JWT as Bearer is refused without a lookup", func(t *testing.T) {
		before := ft.calls.Load()
		_, err := s.Authenticate(req("GET", "/api/v1/tokens", "Authorization", "Bearer eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ4In0.c2ln"), defaultRules())
		wantErr(t, err, 401, errs.CodeInvalidToken)
		if ft.calls.Load() != before {
			t.Fatal("a token-table query ran for a JWT")
		}
	})
	t.Run("R-AUTH-001 the break-glass password as Basic is refused without a lookup", func(t *testing.T) {
		before := ft.calls.Load()
		h := "Basic " + base64.StdEncoding.EncodeToString([]byte("breakglass:correct horse battery staple"))
		_, err := s.Authenticate(req("GET", "/api/v1/tokens", "Authorization", h), defaultRules())
		wantErr(t, err, 401, errs.CodeInvalidToken)
		if ft.calls.Load() != before {
			t.Fatal("a lookup ran for a password")
		}
	})
	t.Run("R-AUTH-001 an unknown scheme is a rejection, not anonymous", func(t *testing.T) {
		_, err := s.Authenticate(req("GET", "/api/npm/x/y", "Authorization", "Digest abc"), defaultRules())
		wantErr(t, err, 401, errs.CodeInvalidToken)
	})
	t.Run("R-AUTH-001 tokens in query parameters are not accepted", func(t *testing.T) {
		_, err := s.Authenticate(req("POST", "/api/v1/repositories?access_token="+pat), defaultRules())
		wantErr(t, err, 401, errs.CodeCredentialsRequired)
	})
}

func TestR_AUTH_001_SessionRules(t *testing.T) {
	ft := &fakeTokens{}
	sess := session.Info{ID: "s1", Principal: alice}
	cookie := &http.Cookie{Name: SessionCookie, Value: "sess"}
	do := func(sv session.Validator, method, path string, ep EndpointRules) (Result, error) {
		s := newStage(ft, Options{Sessions: sv})
		r := req(method, path)
		r.AddCookie(cookie)
		return s.Authenticate(r, ep)
	}
	t.Run("R-AUTH-001 accepted on /api/v1 for every method", func(t *testing.T) {
		res, err := do(fakeSessions{info: sess}, "POST", "/api/v1/tokens", defaultRules())
		if err != nil || res.Credential.Method != MethodSession || res.Credential.SessionID != "s1" || len(res.Groups) != 1 {
			t.Fatalf("%+v %v", res, err)
		}
	})
	t.Run("R-AUTH-001 accepted on /files for GET and PUT, ignored for POST", func(t *testing.T) {
		for _, m := range []string{"GET", "HEAD", "PUT", "DELETE"} {
			if res, err := do(fakeSessions{info: sess}, m, "/files/r/x", defaultRules()); err != nil || res.Credential.Method != MethodSession {
				t.Fatalf("%s: %+v %v", m, res, err)
			}
		}
		_, err := do(fakeSessions{info: sess}, "POST", "/files/r/x", defaultRules())
		wantErr(t, err, 401, errs.CodeCredentialsRequired)
	})
	t.Run("R-AUTH-001 on /api/<format> only GET and HEAD", func(t *testing.T) {
		if res, err := do(fakeSessions{info: sess}, "GET", "/api/npm/r/x", defaultRules()); err != nil || res.Credential.Method != MethodSession {
			t.Fatalf("%+v %v", res, err)
		}
		_, err := do(fakeSessions{info: sess}, "PUT", "/api/npm/r/x", defaultRules())
		wantErr(t, err, 401, errs.CodeCredentialsRequired)
	})
	t.Run("R-EP-010 ignored on an endpoint whose surface holds no ui", func(t *testing.T) {
		ep := defaultRules()
		ep.AcceptsSession = false
		_, err := do(fakeSessions{info: sess}, "GET", "/api/v1/tokens", ep)
		wantErr(t, err, 401, errs.CodeCredentialsRequired)
	})
	t.Run("R-AUTH-001 an ended session is 401 session_expired and never anonymous", func(t *testing.T) {
		_, err := do(fakeSessions{err: session.ErrInvalid}, "GET", "/api/v1/tokens", defaultRules())
		wantErr(t, err, 401, "session_expired")
	})
	t.Run("R-AUTH-001 Q-AUTH-177 the login options ignore the cookie, so an ended session leaves the sign-in screen reachable", func(t *testing.T) {
		res, err := do(fakeSessions{err: session.ErrInvalid}, "GET", "/api/v1/auth/login-options", defaultRules())
		if err != nil || !res.Anonymous {
			t.Fatalf("%+v %v", res, err)
		}
	})
	t.Run("R-AUTH-012 a failed CSRF check is 403", func(t *testing.T) {
		_, err := do(fakeSessions{info: sess, csrfErr: session.ErrCSRF}, "POST", "/api/v1/tokens", defaultRules())
		wantErr(t, err, 403, "csrf_failed")
	})
	t.Run("R-AUTH-001 a replica without sessions ignores the cookie", func(t *testing.T) {
		s := newStage(ft, Options{})
		r := req("GET", "/api/v1/tokens")
		r.AddCookie(cookie)
		_, err := s.Authenticate(r, defaultRules())
		wantErr(t, err, 401, errs.CodeCredentialsRequired)
	})
}

func TestR_AUTH_037_AnonymousAccess(t *testing.T) {
	ft := &fakeTokens{}
	on, off := newStage(ft, Options{Settings: fakeAnon{on: true}}), newStage(ft, Options{Settings: fakeAnon{}})
	t.Run("R-AUTH-037 off: a package request without credentials is 401 with the Basic challenge", func(t *testing.T) {
		_, err := off.Authenticate(req("GET", "/api/npm/npm-all/lodash"), defaultRules())
		e := wantErr(t, err, 401, errs.CodeCredentialsRequired)
		if e.Challenge != `Basic realm="trove"` {
			t.Fatalf("challenge %q", e.Challenge)
		}
	})
	t.Run("R-AUTH-002 a browser never gets the Basic challenge, a package client still does", func(t *testing.T) {
		for _, tc := range []struct {
			path string
			hdr  []string
			want string
		}{
			{"/files/generic-local/a.bin", nil, `Basic realm="trove"`},
			{"/api/npm/npm-all/lodash", nil, `Basic realm="trove"`},
			{"/files/generic-local/a.bin", []string{"Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document"}, `Bearer realm="trove"`},
			{"/api/npm/npm-all/lodash", []string{"Sec-Fetch-Mode", "cors"}, `Bearer realm="trove"`},
			{"/favicon.ico", []string{"Sec-Fetch-Dest", "image"}, `Bearer realm="trove"`},
			{"/favicon.ico", nil, `Bearer realm="trove"`},
			{"/robots.txt", nil, `Bearer realm="trove"`},
		} {
			_, err := off.Authenticate(req("GET", tc.path, tc.hdr...), defaultRules())
			e := wantErr(t, err, 401, errs.CodeCredentialsRequired)
			if e.Challenge != tc.want {
				t.Errorf("%s %v: challenge %q, want %q", tc.path, tc.hdr, e.Challenge, tc.want)
			}
		}
	})
	t.Run("R-AUTH-037 on: the request runs as the anonymous principal", func(t *testing.T) {
		res, err := on.Authenticate(req("GET", "/api/npm/npm-all/lodash"), defaultRules())
		if err != nil || !res.Anonymous || res.Principal.ID != principal.AnonymousID || res.Principal.Kind != principal.KindAnonymous {
			t.Fatalf("%+v %v", res, err)
		}
	})
	t.Run("R-AUTH-037 anonymous never passes an unsafe method on /api/v1", func(t *testing.T) {
		_, err := on.Authenticate(req("POST", "/api/v1/repositories"), defaultRules())
		wantErr(t, err, 401, errs.CodeCredentialsRequired)
	})
	t.Run("R-AUTH-037 the hard limit of /api/v1 holds for a path the router unescapes to /api/v1", func(t *testing.T) {
		// ServeMux routes on the unescaped segments, so /api/v%31/… reaches the management API: the limit reads the
		// path the router routes by, never the escaped one as received.
		mux := http.NewServeMux()
		for _, p := range []string{"/api/v1/", "/api/", "/"} {
			mux.Handle(p, http.NotFoundHandler())
		}
		if _, p := mux.Handler(req("POST", "/api/v%31/projects")); p != "/api/v1/" {
			t.Fatalf("the premise: ServeMux routes /api/v%%31/projects to %q", p)
		}
		for _, c := range []struct{ m, p string }{
			{"POST", "/api/v%31/projects"},
			{"DELETE", "/api/v%31/projects/it/repositories/npm-local"},
			{"PATCH", "/%61pi/v1/settings/auth"},
			{"PUT", "/api/%761/projects/it/repositories/npm-local"},
			{"POST", "/api/v%31"},
		} {
			_, err := on.Authenticate(req(c.m, c.p), defaultRules())
			e := wantErr(t, err, 401, errs.CodeCredentialsRequired)
			if e.Challenge != `Bearer realm="trove"` {
				t.Errorf("%s %s: challenge %q, the one of /api/v1", c.m, c.p, e.Challenge)
			}
		}
		// The same paths with a safe method stay anonymous, as /api/v1 does.
		if res, err := on.Authenticate(req("GET", "/api/v%31/projects"), defaultRules()); err != nil || !res.Anonymous {
			t.Fatalf("GET: %+v %v", res, err)
		}
	})
	t.Run("R-AUTH-037 an escape never opens a route: the always-open rules read the path as received", func(t *testing.T) {
		for _, c := range []struct{ m, p string }{
			{"POST", "/api/v1/tokens/revok%65"},
			{"GET", "/api/v1/auth/login-option%73"},
			{"POST", "/api/v1/auth%2Fdevice/authorize"},
			{"GET", "/u%69/"},
		} {
			_, err := off.Authenticate(req(c.m, c.p), defaultRules())
			wantErr(t, err, 401, errs.CodeCredentialsRequired)
		}
	})
	t.Run("R-AUTH-001 the session cookie's path rule reads the path the router routes by", func(t *testing.T) {
		live := newStage(ft, Options{Sessions: fakeSessions{info: session.Info{ID: "s1", Principal: alice}}, Settings: fakeAnon{on: true}})
		ep := defaultRules()
		ep.AcceptsSession = true
		// /api/np%6d/… is a package route to the router: the cookie is ignored for an unsafe method, as on /api/npm/….
		for _, p := range []string{"/api/np%6d/it/npm-local/pkg", "/api/npm/it/npm-local/pkg"} {
			res, err := live.Authenticate(req("PUT", p, "Cookie", SessionCookie+"=sess"), ep)
			if err != nil || res.Credential.Method == MethodSession {
				t.Errorf("PUT %s: %+v %v, want the cookie ignored", p, res, err)
			}
		}
		// /api/v%31/… is the management API: the cookie is the credential for every method.
		res, err := live.Authenticate(req("POST", "/api/v%31/projects", "Cookie", SessionCookie+"=sess"), ep)
		if err != nil || res.Credential.Method != MethodSession {
			t.Errorf("POST /api/v%%31/projects: %+v %v, want the session", res, err)
		}
	})
	t.Run("R-EP-010 an endpoint with auth.anonymous false makes no request anonymous", func(t *testing.T) {
		ep := defaultRules()
		ep.Anonymous = false
		_, err := on.Authenticate(req("GET", "/api/npm/npm-all/lodash"), ep)
		wantErr(t, err, 401, errs.CodeCredentialsRequired)
	})
	t.Run("R-AUTH-037 the always-open routes need no credential and no switch", func(t *testing.T) {
		for _, c := range []struct{ m, p string }{
			{"GET", "/api/v1/auth/login-options"},
			{"GET", "/auth/oidc/corp/start"},
			{"POST", "/auth/local/login"},
			{"POST", "/api/v1/tokens/revoke"},
			{"POST", "/api/v1/auth/device/authorize"},
			{"GET", "/-/v1/login"},
		} {
			res, err := off.Authenticate(req(c.m, c.p), defaultRules())
			if err != nil || !res.Anonymous {
				t.Errorf("%s %s: %+v %v", c.m, c.p, res, err)
			}
		}
	})
	t.Run("Q-ARCH-302 a route of a family the process does not serve is not always open: it is decided like an unknown path", func(t *testing.T) {
		packagesOnly := defaultRules()
		packagesOnly.PublicRoute = func(r *http.Request) bool { return r.URL.Path == "/api/v1/system/ping" }
		packagesOnly.Served = func(r *http.Request) bool {
			p := r.URL.Path
			return p == "/files/x" || p == "/does-not-exist" || strings.HasPrefix(p, "/api/npm/")
		}
		unknown := func(s *Stage) error {
			_, err := s.Authenticate(req("GET", "/does-not-exist"), packagesOnly)
			return err
		}
		for _, c := range []struct{ m, p string }{
			{"GET", "/"},
			{"GET", "/ui/"},
			{"GET", "/auth/oidc/corp/start"},
			{"POST", "/auth/local/login"},
			{"GET", "/api/v1/auth/login-options"},
			{"GET", "/api/v1/system/ping"},
			{"POST", "/api/v1/auth/oidc/token"},
			{"POST", "/api/v1/tokens/revoke"},
		} {
			_, got := off.Authenticate(req(c.m, c.p), packagesOnly)
			wantErr(t, got, 401, errs.CodeCredentialsRequired)
			if want := unknown(off); got.Error() != want.Error() {
				t.Errorf("%s %s: %v differs from the unknown path's %v", c.m, c.p, got, want)
			}
			// With the switch on, an unserved route is anonymous exactly as an unknown path of the same method is, never more
			// and never less: the hard limit of /api/v1 is a rule of the served API (Q-ARCH-306).
			res, err := on.Authenticate(req(c.m, c.p), packagesOnly)
			wantRes, wantE := on.Authenticate(req(c.m, "/does-not-exist"), packagesOnly)
			if (err == nil) != (wantE == nil) || res.Anonymous != wantRes.Anonymous {
				t.Errorf("%s %s with the switch on: %+v %v; the unknown path: %+v %v", c.m, c.p, res, err, wantRes, wantE)
			}
		}
		// A route it does serve stays open.
		if res, err := off.Authenticate(req("GET", "/api/npm/it/npm-all/-/v1/login"), packagesOnly); err != nil || !res.Anonymous {
			t.Errorf("the npm login start on packages: %+v %v", res, err)
		}
		// Without the rule, every family is served, as before.
		if res, err := off.Authenticate(req("GET", "/ui/"), defaultRules()); err != nil || !res.Anonymous {
			t.Errorf("served is nil: %+v %v", res, err)
		}
	})
	t.Run("R-AUTH-037 a public management operation is reachable with the switch off", func(t *testing.T) {
		ep := defaultRules()
		ep.PublicRoute = func(r *http.Request) bool { return r.URL.Path == "/api/v1/system/ping" }
		if res, err := off.Authenticate(req("GET", "/api/v1/system/ping"), ep); err != nil || !res.Anonymous {
			t.Fatalf("%+v %v", res, err)
		}
		_, err := off.Authenticate(req("GET", "/api/v1/system/replicas"), ep)
		wantErr(t, err, 401, errs.CodeCredentialsRequired)
	})
	t.Run("R-AUTH-001 exchange routes receive the ID token as Authorization without evaluation", func(t *testing.T) {
		for _, p := range []string{"/api/v1/auth/oidc/token", "/-/npm/v1/oidc/token/exchange/package/@acme%2Fui", "/api/npm/it/npm-all/-/npm/v1/oidc/token/exchange/package/@acme%2Fui"} {
			res, err := off.Authenticate(req("POST", p, "Authorization", "Bearer eyJhbGciOiJSUzI1NiJ9.e30.c2ln"), defaultRules())
			if err != nil || !res.Anonymous {
				t.Errorf("%s: %+v %v", p, res, err)
			}
		}
		_, err := off.Authenticate(req("GET", "/api/npm/npm-all/@acme%2Fui", "Authorization", "Bearer eyJhbGciOiJSUzI1NiJ9.e30.c2ln"), defaultRules())
		wantErr(t, err, 401, errs.CodeInvalidToken)
	})
}

// servedBy is the family rule of httpserver.Endpoint.ServesRequest for the default endpoint of a role: api serves ui, api
// and ci_exchange, packages serves packages and files, and a path of no family is served by both (the router decides it).
func servedBy(role string) func(*http.Request) bool {
	return func(r *http.Request) bool {
		p := r.URL.Path
		switch {
		case p == "/", p == "/ui" || strings.HasPrefix(p, "/ui/"), strings.HasPrefix(p, "/auth/"), strings.HasPrefix(p, "/api/v1/"):
			return role == "api"
		case strings.HasPrefix(p, "/files/"), strings.HasPrefix(p, "/api/"), strings.HasPrefix(p, "/v2/"), p == "/-/v1/login":
			return role == "packages"
		}
		return true
	}
}

// outcome is what a client sees of the stage's verdict.
type outcome struct {
	Anonymous                      bool
	Method                         Method
	Status                         int
	Code, Detail, Challenge, Other string
}

func outcomeOf(res Result, err error) outcome {
	if err == nil {
		return outcome{Anonymous: res.Anonymous, Method: res.Credential.Method}
	}
	e, ok := errs.As(err)
	if !ok {
		return outcome{Other: err.Error()}
	}
	return outcome{Status: e.Status(), Code: e.Code, Detail: e.Detail, Challenge: e.Challenge}
}

// TestQ_ARCH_306_EveryPathRuleOfTheStageAsksWhetherTheFamilyIsServed: on a role that does not serve a route's family, the
// stage's verdict on the request is the one on GET (or the same method of) /does-not-exist, whatever the request carries:
// no credential, a rejected token, a valid token, an ended or a live session cookie, from a package client or a browser,
// with the anonymous switch off and on (R-EP-005, Q-ARCH-302, Q-ARCH-306). The challenge (Basic on /files/ and the
// package protocols, error="invalid_token" on /api/v1), the cookie's path rule and the hard limit of /api/v1 are rules
// of the served family.
func TestQ_ARCH_306_EveryPathRuleOfTheStageAsksWhetherTheFamilyIsServed(t *testing.T) {
	pat := newToken(t, token.TypePAT)
	unknownPAT := newToken(t, token.TypePAT)
	ft := &fakeTokens{known: map[string]token.Info{pat: {ID: "t1", Type: token.TypePAT, Owner: alice, Scope: token.Scope{Inherit: true}}}}
	live := fakeSessions{info: session.Info{ID: "s1", Principal: alice}}
	ended := fakeSessions{err: session.ErrInvalid}
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:"+unknownPAT))
	credentials := map[string][]string{
		"no credential":    nil,
		"a valid token":    {"Authorization", "Bearer " + pat},
		"a rejected token": {"Authorization", "Bearer " + unknownPAT},
		"a rejected Basic": {"Authorization", basic},
		"a scheme of none": {"Authorization", "Negotiate abc"},
		"a session cookie": {"Cookie", SessionCookie + "=sess"},
		"a browser":        {"Sec-Fetch-Mode", "navigate", "Sec-Fetch-Dest", "document"},
	}
	unserved := map[string][]struct{ m, p string }{
		"api": {
			{"GET", "/files/generic-local/a.bin"},
			{"PUT", "/files/generic-local/a.bin"},
			{"DELETE", "/files/generic-local/a.bin"},
			{"GET", "/api/npm/npm-all/lodash"},
			{"HEAD", "/api/npm/npm-all/lodash"},
			{"GET", "/v2/"},
			{"GET", "/-/v1/login"},
		},
		"packages": {
			{"GET", "/"},
			{"GET", "/ui/"},
			{"GET", "/auth/oidc/corp/start"},
			{"GET", "/api/v1/system/ping"},
			{"GET", "/api/v1/repositories"},
			{"POST", "/api/v1/repositories"},
			{"PATCH", "/api/v1/settings/auth"},
			{"POST", "/api/v1/auth/oidc/token"},
			{"GET", "/api/v1/auth/login-options"},
		},
	}
	for role, probes := range unserved {
		for anon, settings := range map[string]fakeAnon{"off": {}, "on": {on: true}} {
			for name, sessions := range map[string]fakeSessions{"live": live, "ended": ended} {
				s := newStage(ft, Options{Sessions: sessions, Settings: settings})
				// The default endpoint of api accepts the session; the one of packages does not (R-EP-010), which the
				// cookie's answer there already shows.
				ep := defaultRules()
				ep.AcceptsSession = role == "api"
				ep.Served = servedBy(role)
				ep.PublicRoute = func(r *http.Request) bool { return r.URL.Path == "/api/v1/system/ping" }
				for cred, hdr := range credentials {
					for _, p := range probes {
						got := outcomeOf(s.Authenticate(req(p.m, p.p, hdr...), ep))
						want := outcomeOf(s.Authenticate(req(p.m, "/does-not-exist", hdr...), ep))
						if got != want {
							t.Errorf("%s, anonymous %s, %s session, %s: %s %s = %+v\nwant the unknown path's %+v", role, anon, name, cred, p.m, p.p, got, want)
						}
					}
				}
			}
		}
	}
	// The rules still hold where the family is served: the control that the comparison above compares something.
	s := newStage(ft, Options{Sessions: ended, Settings: fakeAnon{}})
	ep := defaultRules()
	ep.Served = servedBy("packages")
	if got := outcomeOf(s.Authenticate(req("GET", "/files/generic-local/a.bin"), ep)); got.Challenge != `Basic realm="trove"` {
		t.Errorf("/files/ on packages: %+v, want the Basic challenge", got)
	}
	r := req("GET", "/files/generic-local/a.bin")
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: "sess"})
	if got := outcomeOf(s.Authenticate(r, ep)); got.Code != "session_expired" {
		t.Errorf("an ended session on a served /files/: %+v, want session_expired", got)
	}
	ep.Served = servedBy("api")
	if got := outcomeOf(s.Authenticate(req("GET", "/api/v1/x", "Authorization", "Bearer "+unknownPAT), ep)); got.Challenge != `Bearer realm="trove", error="invalid_token"` {
		t.Errorf("a rejected token on a served /api/v1: %+v", got)
	}
}

func TestR_EP_010_EndpointRules(t *testing.T) {
	pat, sat := newToken(t, token.TypePAT), newToken(t, token.TypeSAT)
	ft := &fakeTokens{known: map[string]token.Info{
		pat: {ID: "t1", Type: token.TypePAT, Owner: alice, Scope: token.Scope{Inherit: true}},
		sat: {ID: "t2", Type: token.TypeSAT, Owner: alice, Scope: token.Scope{Inherit: true, Endpoints: []string{"customers"}}},
	}}
	s := newStage(ft, Options{})
	t.Run("R-EP-010 a type the endpoint does not accept is refused without a lookup", func(t *testing.T) {
		ep := defaultRules()
		ep.TokenTypes = []token.Type{token.TypeSAT}
		before := ft.calls.Load()
		_, err := s.Authenticate(req("GET", "/api/npm/x/y", "Authorization", "Bearer "+pat), ep)
		wantErr(t, err, 401, errs.CodeInvalidToken)
		if ft.calls.Load() != before {
			t.Fatal("lookup for a refused type")
		}
	})
	t.Run("R-EP-011 a token bound to other endpoints is rejected", func(t *testing.T) {
		_, err := s.Authenticate(req("GET", "/api/npm/x/y", "Authorization", "Bearer "+sat), defaultRules())
		wantErr(t, err, 401, errs.CodeInvalidToken)
		ep := defaultRules()
		ep.Name = "customers"
		if _, err := s.Authenticate(req("GET", "/api/npm/x/y", "Authorization", "Bearer "+sat), ep); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("R-EP-011 an unbound token on a bound-only endpoint is rejected", func(t *testing.T) {
		ep := defaultRules()
		ep.BoundTokensOnly = true
		_, err := s.Authenticate(req("GET", "/api/npm/x/y", "Authorization", "Bearer "+pat), ep)
		wantErr(t, err, 401, errs.CodeInvalidToken)
	})
}

// TestR_AUTH_031_OnlyASuccessfulAuthenticationIsAUse: a token that the stage refuses for
// its endpoint binding, or whose groups cannot be read, is not a use; a stolen bound
// token tried on the wrong endpoint must not move last_used_at or last_used_addr.
func TestR_AUTH_031_OnlyASuccessfulAuthenticationIsAUse(t *testing.T) {
	sat := newToken(t, token.TypeSAT)
	ft := &fakeTokens{known: map[string]token.Info{sat: {ID: "t1", Type: token.TypeSAT, Owner: alice, Scope: scope.Scope{Inherit: true, Endpoints: []string{"customers"}}}}}
	s := newStage(ft, Options{ClientAddr: clientAddr("203.0.113.9")})
	_, err := s.Authenticate(req("GET", "/api/npm/x/y", "Authorization", "Bearer "+sat), defaultRules())
	wantErr(t, err, 401, errs.CodeInvalidToken)
	if len(ft.noted) != 0 {
		t.Fatalf("a token refused for its binding was noted as used: %v", ft.noted)
	}
	ep := defaultRules()
	ep.Name = "customers"
	if _, err := s.Authenticate(req("GET", "/api/npm/x/y", "Authorization", "Bearer "+sat), ep); err != nil {
		t.Fatal(err)
	}
	if len(ft.noted) != 1 || ft.noted[0] != "t1@203.0.113.9" {
		t.Fatalf("a successful authentication is noted once with the client address: %v", ft.noted)
	}
}

func TestR_AUTH_033_BasicSwitch(t *testing.T) {
	pat := newToken(t, token.TypePAT)
	ft := &fakeTokens{known: map[string]token.Info{pat: {ID: "t1", Type: token.TypePAT, Owner: alice}}}
	s := New(Options{Tokens: ft, Principals: fakeStore{}, BasicEnabled: false})
	h := "Basic " + base64.StdEncoding.EncodeToString([]byte("any:"+pat))
	_, err := s.Authenticate(req("GET", "/files/x", "Authorization", h), defaultRules())
	wantErr(t, err, 401, errs.CodeInvalidToken)
	if _, err := s.Authenticate(req("GET", "/files/x", "Authorization", "Bearer "+pat), defaultRules()); err != nil {
		t.Fatalf("Bearer stays on: %v", err)
	}
}

func TestR_AUTH_002_FailureCodes(t *testing.T) {
	disabled, unavailable := newToken(t, token.TypePAT), newToken(t, token.TypePAT)
	ft := &fakeTokens{errFor: map[string]error{
		disabled:    token.ErrOwner,
		unavailable: errs.E(errs.Unavailable, errs.CodeDatabaseUnavailable, "down"),
	}}
	s := newStage(ft, Options{})
	_, err := s.Authenticate(req("GET", "/api/v1/tokens", "Authorization", "Bearer "+disabled), defaultRules())
	wantErr(t, err, 401, errs.CodePrincipalDisabled)
	_, err = s.Authenticate(req("GET", "/api/v1/tokens", "Authorization", "Bearer "+unavailable), defaultRules())
	wantErr(t, err, 503, errs.CodeDatabaseUnavailable)
	// A storage outage is 503 for an error the stage does not know as well.
	ft.errFor[unavailable] = errors.New("connection reset")
	_, err = s.Authenticate(req("GET", "/api/v1/tokens", "Authorization", "Bearer "+unavailable), defaultRules())
	wantErr(t, err, 503, errs.CodeDatabaseUnavailable)
}

func clientAddr(a string) func(*http.Request) netip.Addr {
	return func(*http.Request) netip.Addr { return netip.MustParseAddr(a) }
}

// TestR_AUTH_038_RandomWellFormedTokensAreValidatedAtMost300Times is the Basic
// and Bearer part of E1.8: 400 well-formed random tokens from one address, round
// robin over three replicas, are validated at most 300 times and the rest get
// 429; a token validated successfully from that address stays usable.
func TestR_AUTH_038_RandomWellFormedTokensAreValidatedAtMost300Times(t *testing.T) {
	good := newToken(t, token.TypePAT)
	run := func(t *testing.T, limiterFor func(replica int) FailureLimiter) {
		validations := &atomic.Int64{}
		var stages [3]*Stage
		tokens := make([]*fakeTokens, 3)
		for i := range stages {
			tokens[i] = &fakeTokens{known: map[string]token.Info{good: {ID: "t1", Type: token.TypePAT, Owner: alice}}}
			stages[i] = newStage(tokens[i], Options{Limiter: limiterFor(i), ClientAddr: clientAddr("203.0.113.9")})
		}
		var throttled int
		for n := 0; n < 400; n++ {
			s := stages[n%3]
			_, err := s.Authenticate(req("GET", "/api/npm/x/y", "Authorization", "Bearer "+newToken(t, token.TypePAT)), defaultRules())
			if e, ok := errs.As(err); ok && e.Status() == 429 {
				throttled++
				if e.RetryAfter <= 0 {
					t.Fatal("429 without Retry-After")
				}
			}
		}
		for _, ft := range tokens {
			validations.Add(ft.calls.Load())
		}
		if validations.Load() > 300 {
			t.Fatalf("%d validations, want at most 300", validations.Load())
		}
		if throttled < 100 {
			t.Fatalf("%d requests throttled, want at least 100", throttled)
		}
	}
	t.Run("R-AUTH-038 a cluster-wide counter shared by the replicas", func(t *testing.T) {
		shared := NewLocalLimiter(300, time.Minute, nil, nil)
		run(t, func(int) FailureLimiter { return shared })
	})
	t.Run("R-AUTH-038 Valkey stopped: per-replica shares of the configured value keep the bound", func(t *testing.T) {
		run(t, func(int) FailureLimiter { return NewLocalLimiter(300, time.Minute, func() int { return 3 }, nil) })
	})
	t.Run("R-AUTH-038 exempt addresses are never throttled", func(t *testing.T) {
		ft := &fakeTokens{}
		s := newStage(ft, Options{
			Limiter: NewLocalLimiter(1, time.Minute, nil, nil), ClientAddr: clientAddr("10.1.2.3"),
			ExemptCIDRs: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")},
		})
		for n := 0; n < 20; n++ {
			_, err := s.Authenticate(req("GET", "/api/npm/x/y", "Authorization", "Bearer "+newToken(t, token.TypePAT)), defaultRules())
			wantErr(t, err, 401, errs.CodeInvalidToken)
		}
		if ft.calls.Load() != 20 {
			t.Fatalf("validated %d of 20", ft.calls.Load())
		}
	})
}

func TestR_AUTH_038_LocalLimiterSlidingWindow(t *testing.T) {
	now := time.Unix(1_000_000, 0)
	l := NewLocalLimiter(10, time.Minute, nil, func() time.Time { return now })
	ctx := context.Background()
	for i := 0; i < 9; i++ {
		l.Count(ctx, "a")
	}
	if l.Exceeded(ctx, "a") {
		t.Fatal("9 of 10 failures leave room")
	}
	l.Count(ctx, "a")
	if !l.Exceeded(ctx, "a") {
		t.Fatal("the 10th failure uses up the burst")
	}
	l.Count(ctx, "a")
	if l.Exceeded(ctx, "b") {
		t.Fatal("buckets are independent")
	}
	now = now.Add(30 * time.Second)
	now = now.Add(31 * time.Second) // second window: previous counts 11 x (1 - 1/60)
	if !l.Exceeded(ctx, "a") {
		t.Fatal("the previous window still weighs")
	}
	now = now.Add(2 * time.Minute)
	if l.Exceeded(ctx, "a") {
		t.Fatal("a quiet period resets the bucket")
	}
	share := NewLocalLimiter(300, time.Minute, func() int { return 3 }, func() time.Time { return now })
	for i := 0; i < 99; i++ {
		share.Count(ctx, "x")
	}
	if share.Exceeded(ctx, "x") {
		t.Fatal("99 of a share of 100")
	}
	share.Count(ctx, "x")
	if !share.Exceeded(ctx, "x") {
		t.Fatal("share used up at 100")
	}
}
