package auth

import (
	"net/http"
	"sync"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/session"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/errs"
)

// refusals collects what the endpoint rules refused.
type refusals struct {
	mu  sync.Mutex
	got []Refusal
}

func (r *refusals) hook(_ *http.Request, f Refusal) {
	r.mu.Lock()
	r.got = append(r.got, f)
	r.mu.Unlock()
}

func (r *refusals) last() Refusal {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.got) == 0 {
		return Refusal{}
	}
	return r.got[len(r.got)-1]
}

// customersRules are the defaults of a further endpoint (R-EP-010, Q-EP-10).
func customersRules(seen *refusals) EndpointRules {
	return EndpointRules{Name: "customers", TokenTypes: []token.Type{token.TypeSAT}, BoundTokensOnly: true, Refused: seen.hook}
}

func TestR_EP_010_EndpointCredentialRules(t *testing.T) {
	pat, unbound, bound, elsewhere := newToken(t, token.TypePAT), newToken(t, token.TypeSAT), newToken(t, token.TypeSAT), newToken(t, token.TypeSAT)
	sa := principal.Principal{ID: "0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a11", Kind: principal.KindServiceAccount, Name: "customer-acme", Status: principal.StatusActive}
	ft := &fakeTokens{known: map[string]token.Info{
		pat:       {ID: "t-pat", Type: token.TypePAT, Scope: token.DefaultScope(), Owner: alice},
		unbound:   {ID: "t-unbound", Type: token.TypeSAT, Scope: token.DefaultScope(), Owner: sa},
		bound:     {ID: "t-bound", Type: token.TypeSAT, Scope: token.Scope{Inherit: true, Endpoints: []string{"customers"}}, Owner: sa},
		elsewhere: {ID: "t-else", Type: token.TypeSAT, Scope: token.Scope{Inherit: true, Endpoints: []string{"partners"}}, Owner: sa},
	}}
	s := newStage(ft, Options{Sessions: fakeSessions{info: session.Info{ID: "s1", Principal: alice}}})
	seen := &refusals{}
	cust := customersRules(seen)

	t.Run("R-EP-010 a PAT on customers is 401 invalid_token without a lookup", func(t *testing.T) {
		before := ft.calls.Load()
		_, err := s.Authenticate(req("GET", "/api/npm/npm-customers/x", "Authorization", "Bearer "+pat), cust)
		wantErr(t, err, 401, errs.CodeInvalidToken)
		if ft.calls.Load() != before {
			t.Fatal("the PAT was looked up")
		}
		if f := seen.last(); f.Reason != RefusedTokenType || f.TokenType != token.TypePAT || f.TokenID != "" {
			t.Fatalf("%+v", f)
		}
	})
	t.Run("R-EP-010 R-EP-011 an unbound SAT is 401 after one lookup; a SAT bound elsewhere the same", func(t *testing.T) {
		for _, raw := range []string{unbound, elsewhere} {
			before := ft.calls.Load()
			_, err := s.Authenticate(req("GET", "/files/generic-local/x", "Authorization", "Bearer "+raw), cust)
			wantErr(t, err, 401, errs.CodeInvalidToken)
			if ft.calls.Load() != before+1 {
				t.Fatalf("%d lookups", ft.calls.Load()-before)
			}
			if f := seen.last(); f.Reason != RefusedTokenBinding || f.TokenID == "" {
				t.Fatalf("%+v", f)
			}
		}
	})
	t.Run("R-EP-011 a SAT bound to customers reaches authorization there and is 401 on default", func(t *testing.T) {
		res, err := s.Authenticate(req("GET", "/files/generic-local/x", "Authorization", "Bearer "+bound), cust)
		if err != nil || res.Principal.ID != sa.ID || res.Credential.TokenID != "t-bound" {
			t.Fatalf("%+v %v", res, err)
		}
		n := len(seen.got)
		def := defaultRules()
		def.Refused = seen.hook
		_, err = s.Authenticate(req("GET", "/files/generic-local/x", "Authorization", "Bearer "+bound), def)
		wantErr(t, err, 401, errs.CodeInvalidToken)
		if len(seen.got) != n+1 || seen.last().Reason != RefusedTokenBinding {
			t.Fatalf("%+v", seen.got)
		}
	})
	t.Run("R-EP-010 a binding refusal answers the bytes of an unknown token", func(t *testing.T) {
		_, refused := s.Authenticate(req("GET", "/api/v1/system/ping", "Authorization", "Bearer "+elsewhere), cust)
		_, unknown := s.Authenticate(req("GET", "/api/v1/system/ping", "Authorization", "Bearer "+newToken(t, token.TypeSAT)), cust)
		a, b := wantErr(t, refused, 401, errs.CodeInvalidToken), wantErr(t, unknown, 401, errs.CodeInvalidToken)
		if a.Detail != b.Detail || a.Challenge != b.Challenge {
			t.Fatalf("%+v vs %+v", a, b)
		}
	})
	t.Run("R-EP-010 the session cookie is ignored without ui and the request is refused as anonymous", func(t *testing.T) {
		r := req("GET", "/api/npm/npm-customers/x")
		r.AddCookie(&http.Cookie{Name: SessionCookie, Value: "valid"})
		_, err := s.Authenticate(r, cust)
		wantErr(t, err, 401, errs.CodeCredentialsRequired)
		if f := seen.last(); f.Reason != RefusedAnonymous {
			t.Fatalf("%+v", f)
		}
	})
	t.Run("R-EP-010 no refusal is reported on the default endpoint's own rules", func(t *testing.T) {
		n := len(seen.got)
		def := defaultRules()
		def.Refused = seen.hook
		if _, err := s.Authenticate(req("GET", "/x", "Authorization", "Bearer "+pat), def); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Authenticate(req("GET", "/ui/"), def); err != nil {
			t.Fatal(err)
		}
		if len(seen.got) != n {
			t.Fatalf("%+v", seen.got[n:])
		}
	})
}
