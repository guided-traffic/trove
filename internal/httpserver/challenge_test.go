package httpserver

import (
	"net/http/httptest"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

// TestR_AUTH_002_ARefusedReadOnAPackageRouteCarriesTheBasicChallenge: the decision point answers a caller without a credential with the
// generic 401; the route's family gives it the challenge package clients answer with their configured credentials, and a browser the Bearer
// one (R-AUTH-002, Q-AUTH-173).
func TestR_AUTH_002_ARefusedReadOnAPackageRouteCarriesTheBasicChallenge(t *testing.T) {
	generic := errs.E(errs.Unauthenticated, errs.CodeUnauthenticated, "This endpoint requires an authenticated caller.", errs.WithChallenge("Bearer"))
	cases := []struct {
		name, path string
		header     map[string]string
		want       string
	}{
		{"a package route", "/api/npm/web/npm-all/lodash", nil, `Basic realm="trove"`},
		{"a raw file", "/files/web/generic/x.bin", nil, `Basic realm="trove"`},
		{"the management API", "/api/v1/projects", nil, "Bearer"},
		{"another route", "/ui/", nil, "Bearer"},
		{"a browser on a package route", "/api/npm/web/npm-all/lodash", map[string]string{"Sec-Fetch-Mode": "navigate"}, "Bearer"},
	}
	for _, c := range cases {
		t.Run("R-AUTH-002 "+c.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", c.path, nil)
			for k, v := range c.header {
				r.Header.Set(k, v)
			}
			e, ok := errs.As(packageChallenge(r, generic))
			if !ok || e.Challenge != c.want {
				t.Errorf("challenge = %q, want %q", e.Challenge, c.want)
			}
		})
	}
	t.Run("R-AUTH-002 another error is left as it is", func(t *testing.T) {
		own := errs.E(errs.Unauthenticated, errs.CodeInvalidToken, "x", errs.WithChallenge(`Bearer realm="trove", error="invalid_token"`))
		r := httptest.NewRequest("GET", "/api/npm/web/npm-all/lodash", nil)
		if e, _ := errs.As(packageChallenge(r, own)); e.Challenge != `Bearer realm="trove", error="invalid_token"` {
			t.Errorf("challenge = %q", e.Challenge)
		}
		forbidden := errs.E(errs.Forbidden, errs.CodeForbidden, "x")
		if packageChallenge(r, forbidden) != error(forbidden) {
			t.Error("a 403 was rewritten")
		}
	})
}
