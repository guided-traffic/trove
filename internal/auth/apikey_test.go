package auth

import (
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/errs"
)

// The NuGet API key (R-AUTH-001 item 1, R-AUTH-033, R-NUGET-012). The stage reads
// X-NuGet-ApiKey only where EndpointRules.APIKeyRoute says the request reaches a route
// that declares it; the tests stand the dispatcher's answer in with a path list.

// apiKeyPaths are the routes of the tests that declare the API key: push, delete and
// relist of a NuGet feed, and a GET route that declares it only to show that the cookie
// rule follows the declaration and not the method (Q-AUTH-187).
var apiKeyPaths = map[string]bool{
	"PUT /api/nuget/nuget-local/package":                       true,
	"DELETE /api/nuget/nuget-local/package/acme.widgets/1.2.3": true,
	"POST /api/nuget/nuget-local/package/acme.widgets/1.2.3":   true,
	"PUT /api/nuget/nuget-local/symbolpackage":                 true,
	"GET /api/keyed/r/declared-get":                            true,
}

// keyRules are the default endpoint's rules with the API-key routes above, counting
// how often the stage asked.
func keyRules(asked *atomic.Int64) EndpointRules {
	ep := defaultRules()
	ep.APIKeyRoute = func(r *http.Request) bool {
		if asked != nil {
			asked.Add(1)
		}
		return apiKeyPaths[r.Method+" "+r.URL.Path]
	}
	return ep
}

func basic(user, tok string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+tok))
}

// apiKeyCast is the cast of the API-key tests: alice's PAT and SAT, bob's PAT, and
// tokens that are revoked, expired, unknown or of a disabled owner.
type apiKeyCast struct {
	ft                                     *fakeTokens
	pat, sat, bobPAT                       string
	revoked, expired, unknown, ownerFrozen string
}

var bob = principal.Principal{ID: "0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a11", Kind: principal.KindUser, Name: "bob", Status: principal.StatusActive}

func newAPIKeyCast(t *testing.T) apiKeyCast {
	t.Helper()
	c := apiKeyCast{
		pat: newToken(t, token.TypePAT), sat: newToken(t, token.TypeSAT), bobPAT: newToken(t, token.TypePAT),
		revoked: newToken(t, token.TypePAT), expired: newToken(t, token.TypePAT), unknown: newToken(t, token.TypePAT),
		ownerFrozen: newToken(t, token.TypePAT),
	}
	c.ft = &fakeTokens{
		known: map[string]token.Info{
			c.pat:    {ID: "t-pat", Type: token.TypePAT, Owner: alice, Scope: token.Scope{Inherit: true}},
			c.sat:    {ID: "t-sat", Type: token.TypeSAT, Owner: alice, Scope: token.Scope{Inherit: true}},
			c.bobPAT: {ID: "t-bob", Type: token.TypePAT, Owner: bob, Scope: token.Scope{Inherit: true}},
		},
		errFor: map[string]error{c.revoked: token.ErrRevoked, c.expired: token.ErrExpired, c.ownerFrozen: token.ErrOwner},
	}
	return c
}

// TestR_AUTH_033_TheAPIKeyRule is the API-key rule of R-AUTH-033 as a table: which
// credential decides, for every form of the header value, with and without
// Authorization, on API-key routes and elsewhere.
func TestR_AUTH_033_TheAPIKeyRule(t *testing.T) {
	c := newAPIKeyCast(t)
	const push, relist, del, symbols = "/api/nuget/nuget-local/package", "/api/nuget/nuget-local/package/acme.widgets/1.2.3",
		"/api/nuget/nuget-local/package/acme.widgets/1.2.3", "/api/nuget/nuget-local/symbolpackage"
	type want struct {
		status int    // 0: authenticated
		code   string // the error code of a rejection
		name   string // the principal of an authentication; "" with status 0: anonymous
		method Method
	}
	for _, tc := range []struct {
		name         string
		method, path string
		hdr          []string
		anonymous    bool
		want         want
	}{
		{
			"R-AUTH-033 a bare token is the credential on a push", "PUT", push,
			[]string{HeaderAPIKey, c.pat},
			false,
			want{name: "alice", method: MethodAPIKey},
		},
		{
			"R-AUTH-033 a bare token is the credential on a delete", "DELETE", del,
			[]string{HeaderAPIKey, c.sat},
			false,
			want{name: "alice", method: MethodAPIKey},
		},
		{
			"R-AUTH-033 a bare token is the credential on a relist", "POST", relist,
			[]string{HeaderAPIKey, c.pat},
			false,
			want{name: "alice", method: MethodAPIKey},
		},
		{
			"R-AUTH-033 a bare token is the credential on a symbol push", "PUT", symbols,
			[]string{HeaderAPIKey, c.pat},
			false,
			want{name: "alice", method: MethodAPIKey},
		},
		{
			"R-AUTH-033 <user>:<token>, the setapikey convention, takes the part after the colon", "PUT", push,
			[]string{HeaderAPIKey, "ci:" + c.pat},
			false,
			want{name: "alice", method: MethodAPIKey},
		},
		{
			"R-AUTH-033 the part after the last colon", "PUT", push,
			[]string{HeaderAPIKey, "domain:ci:" + c.pat},
			false,
			want{name: "alice", method: MethodAPIKey},
		},
		{
			"R-AUTH-033 surrounding whitespace is not part of the value", "PUT", push,
			[]string{HeaderAPIKey, " " + c.pat + " "},
			false,
			want{name: "alice", method: MethodAPIKey},
		},
		{
			"R-AUTH-033 the API key decides over Basic credentials of another principal", "PUT", push,
			[]string{HeaderAPIKey, c.pat, "Authorization", basic("bob", c.bobPAT)},
			false,
			want{name: "alice", method: MethodAPIKey},
		},
		{
			"R-AUTH-033 the API key decides over a Bearer token of another principal", "POST", relist,
			[]string{HeaderAPIKey, c.pat, "Authorization", "Bearer " + c.bobPAT},
			false,
			want{name: "alice", method: MethodAPIKey},
		},
		{
			"R-AUTH-033 the API key decides over an Authorization header the stage would reject", "PUT", push,
			[]string{HeaderAPIKey, c.pat, "Authorization", "Digest abc"},
			false,
			want{name: "alice", method: MethodAPIKey},
		},
		{
			"R-AUTH-033 the placeholder az is ignored and Basic applies", "PUT", push,
			[]string{HeaderAPIKey, "az", "Authorization", basic("ci", c.bobPAT)},
			false,
			want{name: "bob", method: MethodBasic},
		},
		{
			"R-AUTH-033 the placeholder az alone is no credential", "PUT", push,
			[]string{HeaderAPIKey, "az"},
			false,
			want{status: 401, code: errs.CodeCredentialsRequired},
		},
		{
			"R-AUTH-033 an empty value is no credential", "PUT", push,
			[]string{HeaderAPIKey, ""},
			false,
			want{status: 401, code: errs.CodeCredentialsRequired},
		},
		{
			"R-AUTH-033 a token with a broken checksum is not well-formed: ignored, Bearer applies", "PUT", push,
			[]string{HeaderAPIKey, c.pat[:len(c.pat)-1] + flip(c.pat[len(c.pat)-1]), "Authorization", "Bearer " + c.bobPAT},
			false,
			want{name: "bob", method: MethodBearer},
		},
		{
			"R-AUTH-033 a value whose part after the colon is no token is ignored", "PUT", push,
			[]string{HeaderAPIKey, c.pat + ":az", "Authorization", "Bearer " + c.bobPAT},
			false,
			want{name: "bob", method: MethodBearer},
		},
		{
			"R-AUTH-033 a JWT in the header is ignored", "PUT", push,
			[]string{HeaderAPIKey, "eyJhbGciOiJSUzI1NiJ9.e30.c2ln", "Authorization", basic("ci", c.bobPAT)},
			false,
			want{name: "bob", method: MethodBasic},
		},
		{
			"R-AUTH-033 a revoked token as API key is 401 and never falls back to valid Basic credentials", "PUT", push,
			[]string{HeaderAPIKey, c.revoked, "Authorization", basic("ci", c.bobPAT)},
			false,
			want{status: 401, code: errs.CodeInvalidToken},
		},
		{
			"R-AUTH-033 a revoked token in the user:token form is 401 as well", "DELETE", del,
			[]string{HeaderAPIKey, "ci:" + c.revoked, "Authorization", "Bearer " + c.bobPAT},
			false,
			want{status: 401, code: errs.CodeInvalidToken},
		},
		{
			"R-AUTH-001 an expired API key is 401, not the anonymous response", "PUT", push,
			[]string{HeaderAPIKey, c.expired},
			true,
			want{status: 401, code: errs.CodeInvalidToken},
		},
		{
			"R-AUTH-001 an unknown API key is 401 invalid_token", "POST", relist,
			[]string{HeaderAPIKey, c.unknown},
			true,
			want{status: 401, code: errs.CodeInvalidToken},
		},
		{
			"R-AUTH-002 an API key of a disabled owner is 401 principal_disabled", "PUT", push,
			[]string{HeaderAPIKey, c.ownerFrozen},
			false,
			want{status: 401, code: errs.CodePrincipalDisabled},
		},
		{
			"R-NUGET-012 the header is not examined on a route that does not declare it", "GET", "/api/nuget/nuget-local/v3/index.json",
			[]string{HeaderAPIKey, c.pat},
			false,
			want{status: 401, code: errs.CodeCredentialsRequired},
		},
		{
			"R-NUGET-012 not examined on a route without the declaration, Authorization applies", "GET", "/api/nuget/nuget-local/v3/index.json",
			[]string{HeaderAPIKey, c.pat, "Authorization", "Bearer " + c.bobPAT},
			false,
			want{name: "bob", method: MethodBearer},
		},
		{
			"R-NUGET-012 a revoked API key elsewhere is not a rejection: the header is not read", "GET", "/api/nuget/nuget-local/v3/index.json",
			[]string{HeaderAPIKey, c.revoked},
			true,
			want{},
		},
		{
			"R-NUGET-012 not examined on the management API", "GET", "/api/v1/auth/whoami",
			[]string{HeaderAPIKey, c.pat},
			false,
			want{status: 401, code: errs.CodeCredentialsRequired},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStage(c.ft, Options{APIKeyHeader: true})
			ep := keyRules(nil)
			ep.Anonymous = tc.anonymous
			res, err := s.Authenticate(req(tc.method, tc.path, tc.hdr...), ep)
			if tc.want.status != 0 {
				e := wantErr(t, err, tc.want.status, tc.want.code)
				if strings.HasPrefix(tc.path, "/api/nuget/") && e.Challenge != `Basic realm="trove"` {
					t.Fatalf("R-NUGET-012 the challenge of a NuGet endpoint is Basic: %q", e.Challenge)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.want.name == "" {
				if !res.Anonymous {
					t.Fatalf("want the anonymous principal, got %+v", res)
				}
				return
			}
			if res.Anonymous || res.Principal.Name != tc.want.name || res.Credential.Method != tc.want.method {
				t.Fatalf("got %s by %q, want %s by %q", res.Principal.Name, res.Credential.Method, tc.want.name, tc.want.method)
			}
			if tc.want.method == MethodAPIKey && (res.Credential.TokenID == "" || res.Credential.Scope == nil || res.Credential.BasicUsername != "") {
				t.Fatalf("an API key yields the token's ID and scope and no username: %+v", res.Credential)
			}
		})
	}
}

// flip changes a base62 digit into another one.
func flip(b byte) string {
	if b == 'A' {
		return "B"
	}
	return "A"
}

// TestR_AUTH_033_TheAPIKeySwitch: with auth.clients.nuget_api_key_header off the header
// is never examined, a revoked token in it changes nothing, and Authorization applies.
func TestR_AUTH_033_TheAPIKeySwitch(t *testing.T) {
	c := newAPIKeyCast(t)
	s := newStage(c.ft, Options{APIKeyHeader: false})
	var asked atomic.Int64
	const push = "/api/nuget/nuget-local/package"
	closed := keyRules(&asked)
	closed.Anonymous = false
	_, err := s.Authenticate(req("PUT", push, HeaderAPIKey, c.pat), closed)
	wantErr(t, err, 401, errs.CodeCredentialsRequired)
	res, err := s.Authenticate(req("PUT", push, HeaderAPIKey, c.revoked, "Authorization", basic("ci", c.bobPAT)), keyRules(&asked))
	if err != nil || res.Principal.Name != "bob" || res.Credential.Method != MethodBasic {
		t.Fatalf("%+v %v", res, err)
	}
	if asked.Load() != 0 {
		t.Fatalf("the route was asked %d times for a header the stage does not read", asked.Load())
	}
}

// TestR_NUGET_012_NoSessionCookieOnAnAPIKeyRoute: a page in a signed-in user's browser
// can send a cross-site POST, which is the relist method; the cookie it carries is no
// credential on an API-key route, so the request is unauthenticated (R-NUGET-012
// security note). The rule follows the route's declaration, not only its method
// (Q-AUTH-187): even a GET that declared the key would not accept the cookie, while the
// cookie stays a credential for GET and HEAD on every other package route.
func TestR_NUGET_012_NoSessionCookieOnAnAPIKeyRoute(t *testing.T) {
	c := newAPIKeyCast(t)
	var validated atomic.Int64
	sv := countingSessions{fakeSessions: fakeSessions{info: session.Info{ID: "s1", Principal: alice}}, n: &validated}
	cookie := &http.Cookie{Name: SessionCookie, Value: "sess"}
	for _, tc := range []struct {
		name         string
		method, path string
		anonymous    bool
		wantSession  bool
		wantCode     string // the rejection, "" for a result
	}{
		{"R-NUGET-012 a POST relist with only a cookie answers 401", "POST", "/api/nuget/nuget-local/package/acme.widgets/1.2.3", false, false, errs.CodeCredentialsRequired},
		{"R-NUGET-012 with anonymous access on it runs as anonymous, never as the cookie's user", "POST", "/api/nuget/nuget-local/package/acme.widgets/1.2.3", true, false, ""},
		{"R-NUGET-012 a push with only a cookie answers 401", "PUT", "/api/nuget/nuget-local/package", false, false, errs.CodeCredentialsRequired},
		{"R-NUGET-012 a delete with only a cookie answers 401", "DELETE", "/api/nuget/nuget-local/package/acme.widgets/1.2.3", false, false, errs.CodeCredentialsRequired},
		{"Q-AUTH-187 a GET that declares the key does not take the cookie either", "GET", "/api/keyed/r/declared-get", false, false, errs.CodeCredentialsRequired},
		{"R-AUTH-001 a download route keeps the cookie for GET", "GET", "/api/nuget/nuget-local/v3-flatcontainer/acme.widgets/1.2.3/acme.widgets.1.2.3.nupkg", false, true, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			validated.Store(0)
			s := newStage(c.ft, Options{APIKeyHeader: true, Sessions: sv})
			r := req(tc.method, tc.path)
			r.AddCookie(cookie)
			ep := keyRules(nil)
			ep.Anonymous = tc.anonymous
			res, err := s.Authenticate(r, ep)
			if tc.wantCode != "" {
				wantErr(t, err, 401, tc.wantCode)
			} else if err != nil {
				t.Fatal(err)
			}
			if got := validated.Load() > 0; got != tc.wantSession {
				t.Fatalf("the session was validated: %v, want %v", got, tc.wantSession)
			}
			if tc.wantSession && res.Credential.Method != MethodSession {
				t.Fatalf("%+v", res)
			}
			if tc.wantCode == "" && !tc.wantSession && !res.Anonymous {
				t.Fatalf("want anonymous, got %+v", res)
			}
		})
	}
	t.Run("R-NUGET-012 an API key with a cookie of another user: the key decides", func(t *testing.T) {
		s := newStage(c.ft, Options{APIKeyHeader: true, Sessions: sv})
		r := req("POST", "/api/nuget/nuget-local/package/acme.widgets/1.2.3", HeaderAPIKey, c.bobPAT)
		r.AddCookie(cookie)
		res, err := s.Authenticate(r, keyRules(nil))
		if err != nil || res.Principal.Name != "bob" || res.Credential.Method != MethodAPIKey {
			t.Fatalf("%+v %v", res, err)
		}
	})
}

type countingSessions struct {
	fakeSessions
	n *atomic.Int64
}

func (c countingSessions) Validate(ctx context.Context, v string) (session.Info, error) {
	c.n.Add(1)
	return c.fakeSessions.Validate(ctx, v)
}

// TestR_AUTH_033_AnAPIKeyFollowsTheRulesOfABearerToken: the endpoint's token rules, the
// failed-credential throttle, the last use and the metrics treat an API key like a
// Bearer token (R-EP-010, R-AUTH-038, R-AUTH-031, R-OPS-015).
func TestR_AUTH_033_AnAPIKeyFollowsTheRulesOfABearerToken(t *testing.T) {
	const push = "/api/nuget/nuget-local/package"
	t.Run("R-EP-010 a token type the endpoint does not accept is refused without a lookup", func(t *testing.T) {
		c := newAPIKeyCast(t)
		s := newStage(c.ft, Options{APIKeyHeader: true})
		ep := keyRules(nil)
		ep.TokenTypes = []token.Type{token.TypeSAT}
		var refused []Refusal
		ep.Refused = func(_ *http.Request, f Refusal) { refused = append(refused, f) }
		_, err := s.Authenticate(req("PUT", push, HeaderAPIKey, c.pat, "Authorization", basic("ci", c.sat)), ep)
		wantErr(t, err, 401, errs.CodeInvalidToken)
		if c.ft.calls.Load() != 0 || len(refused) != 1 || refused[0].Reason != RefusedTokenType {
			t.Fatalf("lookups %d, refusals %+v", c.ft.calls.Load(), refused)
		}
	})
	t.Run("R-EP-011 a token bound to another endpoint is refused", func(t *testing.T) {
		c := newAPIKeyCast(t)
		c.ft.known[c.sat] = token.Info{ID: "t-sat", Type: token.TypeSAT, Owner: alice, Scope: token.Scope{Inherit: true, Endpoints: []string{"customers"}}}
		s := newStage(c.ft, Options{APIKeyHeader: true})
		_, err := s.Authenticate(req("PUT", push, HeaderAPIKey, c.sat), keyRules(nil))
		wantErr(t, err, 401, errs.CodeInvalidToken)
	})
	t.Run("R-AUTH-038 a rejected API key counts toward the address's budget, a placeholder does not", func(t *testing.T) {
		c := newAPIKeyCast(t)
		lim := NewLocalLimiter(2, time.Minute, nil, nil)
		s := newStage(c.ft, Options{APIKeyHeader: true, Limiter: lim, ClientAddr: clientAddr("203.0.113.9")})
		for range 5 {
			ep := keyRules(nil)
			ep.Anonymous = false
			_, err := s.Authenticate(req("PUT", push, HeaderAPIKey, "az"), ep)
			wantErr(t, err, 401, errs.CodeCredentialsRequired)
		}
		if lim.Exceeded(context.Background(), "203.0.113.9") {
			t.Fatal("a placeholder, which is no credential, was counted as a rejected one")
		}
		for range 2 {
			_, err := s.Authenticate(req("PUT", push, HeaderAPIKey, newToken(t, token.TypePAT)), keyRules(nil))
			wantErr(t, err, 401, errs.CodeInvalidToken)
		}
		_, err := s.Authenticate(req("PUT", push, HeaderAPIKey, newToken(t, token.TypePAT)), keyRules(nil))
		e := wantErr(t, err, 429, errs.CodeRateLimited)
		if e.RetryAfter <= 0 {
			t.Fatal("429 without Retry-After")
		}
	})
	t.Run("R-AUTH-031 a successful API key is a use of the token, with the client address", func(t *testing.T) {
		c := newAPIKeyCast(t)
		s := newStage(c.ft, Options{APIKeyHeader: true, ClientAddr: clientAddr("203.0.113.9")})
		if _, err := s.Authenticate(req("PUT", push, HeaderAPIKey, c.pat), keyRules(nil)); err != nil {
			t.Fatal(err)
		}
		if len(c.ft.noted) != 1 || c.ft.noted[0] != "t-pat@203.0.113.9" {
			t.Fatalf("noted %v", c.ft.noted)
		}
	})
	t.Run("R-OPS-015 the attempts are counted under the method nuget_apikey", func(t *testing.T) {
		c := newAPIKeyCast(t)
		reg := prometheus.NewRegistry()
		m, err := principal.NewMetrics(reg)
		if err != nil {
			t.Fatal(err)
		}
		s := newStage(c.ft, Options{APIKeyHeader: true, Metrics: m})
		_, _ = s.Authenticate(req("PUT", push, HeaderAPIKey, c.pat), keyRules(nil))
		_, _ = s.Authenticate(req("PUT", push, HeaderAPIKey, c.revoked), keyRules(nil))
		got := map[string]float64{}
		mfs, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, mf := range mfs {
			if mf.GetName() != "trove_auth_attempts_total" {
				continue
			}
			for _, x := range mf.GetMetric() {
				labels := map[string]string{}
				for _, l := range x.GetLabel() {
					labels[l.GetName()] = l.GetValue()
				}
				got[labels["method"]+"/"+labels["outcome"]+"/"+labels["reason"]] += x.GetCounter().GetValue()
			}
		}
		if got["nuget_apikey/success/"] != 1 || got["nuget_apikey/failure/revoked"] != 1 || len(got) != 2 {
			t.Fatalf("trove_auth_attempts_total: %v", got)
		}
	})
}

// TestR_NUGET_012_TheRouteIsAskedOnlyWhenACredentialDependsOnIt: the stage asks the
// dispatcher's match at most once, and not at all for a request with neither a
// token-shaped API key nor a cookie the path rule accepts: nearly every request pays
// nothing for the API key (Q-AUTH-186).
func TestR_NUGET_012_TheRouteIsAskedOnlyWhenACredentialDependsOnIt(t *testing.T) {
	c := newAPIKeyCast(t)
	s := newStage(c.ft, Options{APIKeyHeader: true, Sessions: fakeSessions{info: session.Info{ID: "s1", Principal: alice}}})
	cookie := &http.Cookie{Name: SessionCookie, Value: "sess"}
	for _, tc := range []struct {
		name         string
		method, path string
		hdr          []string
		cookie       bool
		want         int64
	}{
		{"no header, no cookie", "GET", "/api/nuget/nuget-local/v3/index.json", nil, false, 0},
		{"Authorization only", "PUT", "/api/nuget/nuget-local/package", []string{"Authorization", "Bearer " + c.pat}, false, 0},
		{"a placeholder", "PUT", "/api/nuget/nuget-local/package", []string{HeaderAPIKey, "az", "Authorization", "Bearer " + c.pat}, false, 0},
		{"a cookie the path rule ignores (POST)", "POST", "/api/nuget/nuget-local/package/a/1.0.0", nil, true, 0},
		{"a token-shaped key", "PUT", "/api/nuget/nuget-local/package", []string{HeaderAPIKey, c.pat}, false, 1},
		{"a token-shaped key off the API-key routes and a cookie: asked once", "GET", "/api/nuget/nuget-local/v3/index.json", []string{HeaderAPIKey, c.pat}, true, 1},
		{"a cookie on a GET of a package route", "GET", "/api/nuget/nuget-local/v3/index.json", nil, true, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var asked atomic.Int64
			r := req(tc.method, tc.path, tc.hdr...)
			if tc.cookie {
				r.AddCookie(cookie)
			}
			_, _ = s.Authenticate(r, keyRules(&asked))
			if asked.Load() != tc.want {
				t.Fatalf("asked %d times, want %d", asked.Load(), tc.want)
			}
		})
	}
}
