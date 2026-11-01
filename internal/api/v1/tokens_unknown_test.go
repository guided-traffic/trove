package v1

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/api/v1/gen"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/errs"
)

func TestR_EP_011_TokenListingShowsUnknownEndpoints(t *testing.T) {
	rec := func(endpoints ...string) token.Record {
		return token.Record{
			ID: "0192f0c4-7a7e-7b51-9b1e-2f0d6c9e1a10", Type: token.TypeSAT, CreatedAt: time.Unix(0, 0),
			Scope: token.Scope{Inherit: true, Endpoints: endpoints},
		}
	}
	configured := func(n string) bool { return n == "customers" || n == "default" }
	s := New(Options{Auth: AuthServices{EndpointConfigured: configured}})

	t.Run("R-EP-011 a bound name that no endpoint carries is listed", func(t *testing.T) {
		got := s.toToken(rec("customers", "gone", "old")).UnknownEndpoints
		if strings.Join(got, ",") != "gone,old" {
			t.Fatalf("unknownEndpoints = %v", got)
		}
	})
	t.Run("R-EP-011 a token bound to configured endpoints, and an unbound token, list none (an array, not null)", func(t *testing.T) {
		for _, r := range []token.Record{rec("customers"), rec()} {
			b, err := json.Marshal(s.toToken(r))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(b), `"unknownEndpoints":[]`) {
				t.Errorf("%s", b)
			}
		}
	})
	t.Run("R-EP-011 without a configuration to ask, nothing is unknown", func(t *testing.T) {
		bare := New(Options{})
		if got := bare.toToken(rec("anything")).UnknownEndpoints; len(got) != 0 {
			t.Fatalf("unknownEndpoints = %v", got)
		}
	})
}

func TestR_EP_011_CreateRefusesABindingTheTypeCannotUse(t *testing.T) {
	accepts := map[string][]string{"default": {"pat", "sat"}, "customers": {"sat"}}
	s := New(Options{Auth: AuthServices{ScopeOptions: scope.Options{
		Endpoint:        func(n string) bool { _, ok := accepts[n]; return ok },
		EndpointAccepts: func(n, tt string) bool { return slices.Contains(accepts[n], tt) },
	}}})
	bound := func(names ...string) *gen.TokenScope { return &gen.TokenScope{Endpoints: &names} }

	if err := s.requireEndpointTypes(bound("customers"), token.TypeSAT); err != nil {
		t.Fatalf("a service-account token on customers: %v", err)
	}
	if err := s.requireEndpointTypes(nil, token.TypePAT); err != nil {
		t.Fatalf("an unbound token: %v", err)
	}
	err := s.requireEndpointTypes(bound("default", "customers"), token.TypePAT)
	if e, ok := errs.As(err); !ok || e.Code != errs.CodeInvalidScope || !strings.Contains(e.Error(), "customers") {
		t.Fatalf("a personal token on customers: %v", err)
	}
}
