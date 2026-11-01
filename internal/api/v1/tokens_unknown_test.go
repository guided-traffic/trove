package v1

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/token"
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
