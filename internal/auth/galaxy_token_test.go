package auth

import (
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/errs"
)

// The scheme "Token" of the Ansible Galaxy clients (R-ANS-006, Q-ANS-4, ADR-191). The stage reads "Authorization: Token <token>" only
// where EndpointRules.TokenSchemeRoute says the request reaches a route that declares it; the tests stand the dispatcher's answer in
// with a path list.

const (
	galaxyRead    = "/api/ansible/web/ansible-local/v3/collections/acme/tools/"
	galaxyPublish = "/api/ansible/web/ansible-local/v3/artifacts/collections/"
	otherFormat   = "/api/npm/npm-local/acme"
)

var tokenSchemePaths = map[string]bool{"GET " + galaxyRead: true, "POST " + galaxyPublish: true}

// galaxyRules are the default endpoint's rules with the Token-scheme routes above, counting how often the stage asked.
func galaxyRules(asked *atomic.Int64) EndpointRules {
	ep := defaultRules()
	ep.TokenSchemeRoute = func(r *http.Request) bool {
		if asked != nil {
			asked.Add(1)
		}
		return tokenSchemePaths[r.Method+" "+r.URL.Path]
	}
	return ep
}

func TestR_ANS_006_TheTokenSchemeIsATroveTokenOnlyOnARouteThatDeclaresIt(t *testing.T) {
	c := newAPIKeyCast(t)
	for _, tc := range []struct {
		name         string
		method, path string
		header       string
		rules        EndpointRules
		wantCode     string // "" authenticated as alice
		wantMethod   Method
	}{
		{"R-ANS-006 a token on a declared GET route", "GET", galaxyRead, "Token " + c.pat, galaxyRules(nil), "", MethodGalaxyToken},
		{"R-ANS-006 a token on the declared publish route", "POST", galaxyPublish, "Token " + c.pat, galaxyRules(nil), "", MethodGalaxyToken},
		{"R-ANS-006 the scheme name is case-insensitive", "GET", galaxyRead, "token " + c.sat, galaxyRules(nil), "", MethodGalaxyToken},
		{"R-ANS-006 a token on a route of another format is not a trove token", "GET", otherFormat, "Token " + c.pat, galaxyRules(nil), errs.CodeInvalidToken, ""},
		{"R-ANS-006 a token on the management API is not a trove token", "GET", "/api/v1/users/me", "Token " + c.pat, galaxyRules(nil), errs.CodeInvalidToken, ""},
		{"R-ANS-006 without the declaration anywhere the header is not a trove token", "GET", galaxyRead, "Token " + c.pat, defaultRules(), errs.CodeInvalidToken, ""},
		{"R-ANS-006 a token that is revoked is rejected", "GET", galaxyRead, "Token " + c.revoked, galaxyRules(nil), errs.CodeInvalidToken, ""},
		{"R-ANS-006 a token that is unknown is rejected", "GET", galaxyRead, "Token " + c.unknown, galaxyRules(nil), errs.CodeInvalidToken, ""},
		{"R-ANS-006 a value that is no token is rejected", "GET", galaxyRead, "Token not-a-trove-token", galaxyRules(nil), errs.CodeInvalidToken, ""},
		{"R-ANS-006 a scheme without a value is rejected", "GET", galaxyRead, "Token", galaxyRules(nil), errs.CodeInvalidToken, ""},
		{"R-ANS-006 Bearer keeps working on a declared route", "GET", galaxyRead, "Bearer " + c.pat, galaxyRules(nil), "", MethodBearer},
		{"R-ANS-006 a Basic user name is ignored on a declared route", "GET", galaxyRead, basic("anything", c.pat), galaxyRules(nil), "", MethodBasic},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStage(c.ft, Options{})
			res, err := s.Authenticate(req(tc.method, tc.path, "Authorization", tc.header), tc.rules)
			if tc.wantCode != "" {
				wantErr(t, err, 401, tc.wantCode)
				return
			}
			if err != nil || res.Principal.Name != "alice" || res.Credential.Method != tc.wantMethod {
				t.Fatalf("%+v %v", res, err)
			}
		})
	}
}

// TestR_ANS_006_ATokenOfAnotherTypeThanTheEndpointAcceptsIsRefused: the checks of a Bearer token apply to the scheme (R-EP-010).
func TestR_ANS_006_ATokenOfAnotherTypeThanTheEndpointAcceptsIsRefused(t *testing.T) {
	c := newAPIKeyCast(t)
	ep := galaxyRules(nil)
	ep.TokenTypes = []token.Type{token.TypePAT}
	var refused []Refusal
	ep.Refused = func(_ *http.Request, f Refusal) { refused = append(refused, f) }
	s := newStage(c.ft, Options{})
	_, err := s.Authenticate(req("GET", galaxyRead, "Authorization", "Token "+c.sat), ep)
	wantErr(t, err, 401, errs.CodeInvalidToken)
	if len(refused) != 1 || refused[0].Reason != RefusedTokenType || refused[0].TokenType != token.TypeSAT {
		t.Errorf("refused = %+v", refused)
	}
	res, err := s.Authenticate(req("GET", galaxyRead, "Authorization", "Token "+c.pat), ep)
	if err != nil || res.Credential.Method != MethodGalaxyToken {
		t.Fatalf("%+v %v", res, err)
	}
}

// TestR_ANS_006_TheSessionCookieIsNoCredentialNextToATokenHeader: a request that carries the header is decided by the header, with the
// cookie of a signed-in browser never validated.
func TestR_ANS_006_TheSessionCookieIsNoCredentialNextToATokenHeader(t *testing.T) {
	c := newAPIKeyCast(t)
	var validated atomic.Int64
	sv := countingSessions{fakeSessions: fakeSessions{info: session.Info{ID: "s1", Principal: alice}}, n: &validated}
	cookie := &http.Cookie{Name: SessionCookie, Value: "sess"}
	s := newStage(c.ft, Options{Sessions: sv})
	r := req("GET", galaxyRead, "Authorization", "Token "+c.bobPAT)
	r.AddCookie(cookie)
	res, err := s.Authenticate(r, galaxyRules(nil))
	if err != nil || res.Principal.Name != "bob" || res.Credential.Method != MethodGalaxyToken || validated.Load() != 0 {
		t.Fatalf("%+v %v, session validated %d times", res, err, validated.Load())
	}
	// A rejected header is a rejection and never a fall-through to the cookie.
	r = req("GET", galaxyRead, "Authorization", "Token "+c.revoked)
	r.AddCookie(cookie)
	_, err = s.Authenticate(r, galaxyRules(nil))
	wantErr(t, err, 401, errs.CodeInvalidToken)
	if validated.Load() != 0 {
		t.Fatalf("the session was validated %d times next to a rejected token", validated.Load())
	}
}

// TestR_ANS_006_TheRouteIsAskedOnlyForATokenHeader: the stage asks the dispatcher's match at most once and only for a request whose
// Authorization header has the scheme Token; every other request pays nothing for the scheme.
func TestR_ANS_006_TheRouteIsAskedOnlyForATokenHeader(t *testing.T) {
	c := newAPIKeyCast(t)
	s := newStage(c.ft, Options{})
	for _, tc := range []struct {
		name   string
		header []string
		want   int64
	}{
		{"no header", nil, 0},
		{"a Bearer token", []string{"Authorization", "Bearer " + c.pat}, 0},
		{"a Basic credential", []string{"Authorization", basic("u", c.pat)}, 0},
		{"a Token header", []string{"Authorization", "Token " + c.pat}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var asked atomic.Int64
			_, _ = s.Authenticate(req("GET", galaxyRead, tc.header...), galaxyRules(&asked))
			if asked.Load() != tc.want {
				t.Fatalf("the route was asked %d times, want %d", asked.Load(), tc.want)
			}
		})
	}
}

// TestR_ANS_006_AttemptsAreCountedUnderTheMethodGalaxyToken: the label of trove_auth_attempts_total is galaxy_token (R-OPS-015).
func TestR_ANS_006_AttemptsAreCountedUnderTheMethodGalaxyToken(t *testing.T) {
	c := newAPIKeyCast(t)
	reg := prometheus.NewRegistry()
	m, err := principal.NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	s := newStage(c.ft, Options{Metrics: m})
	_, _ = s.Authenticate(req("GET", galaxyRead, "Authorization", "Token "+c.pat), galaxyRules(nil))
	_, _ = s.Authenticate(req("GET", galaxyRead, "Authorization", "Token "+c.revoked), galaxyRules(nil))
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
	if got["galaxy_token/success/"] != 1 || got["galaxy_token/failure/revoked"] != 1 || len(got) != 2 {
		t.Fatalf("trove_auth_attempts_total: %v", got)
	}
}
