package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
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
	// throttleAfter makes Check consult the throttle like the real validator.
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
		for _, p := range []string{"/api/v1/auth/oidc/token", "/-/npm/v1/oidc/token/exchange/package/@acme%2Fui", "/api/npm/npm-all/-/npm/v1/oidc/token/exchange/package/@acme%2Fui"} {
			res, err := off.Authenticate(req("POST", p, "Authorization", "Bearer eyJhbGciOiJSUzI1NiJ9.e30.c2ln"), defaultRules())
			if err != nil || !res.Anonymous {
				t.Errorf("%s: %+v %v", p, res, err)
			}
		}
		_, err := off.Authenticate(req("GET", "/api/npm/npm-all/@acme%2Fui", "Authorization", "Bearer eyJhbGciOiJSUzI1NiJ9.e30.c2ln"), defaultRules())
		wantErr(t, err, 401, errs.CodeInvalidToken)
	})
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
