package ci

import (
	"errors"
	"testing"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/ci/reason"
)

// Step 1 of R-CI-007: the parameters, before the subject token costs a signature check (R-CI-008's reasons of the
// request, Q-CI-41).
func TestR_CI_007_TheParametersOfARequest(t *testing.T) {
	x := newTestExchange(t)
	ok := func() ExchangeRequest {
		return ExchangeRequest{GrantType: GrantTypeTokenExchange, SubjectToken: "a.b.c", SubjectTokenType: TokenTypeJWT}
	}
	n := func(v int64) *int64 { return &v }
	for _, c := range []struct {
		name string
		edit func(*ExchangeRequest)
		want Reason
	}{
		{"a complete request", func(*ExchangeRequest) {}, ""},
		{"no grant_type", func(r *ExchangeRequest) { r.GrantType = "" }, reason.MalformedRequest},
		{"another grant_type", func(r *ExchangeRequest) { r.GrantType = "password" }, reason.GrantTypeUnsupported},
		{"Q-CI-41 an empty subject_token counts as omitted", func(r *ExchangeRequest) { r.SubjectToken = "" }, reason.MalformedRequest},
		{"no subject_token_type", func(r *ExchangeRequest) { r.SubjectTokenType = "" }, reason.MalformedRequest},
		{"the job-token type (P2)", func(r *ExchangeRequest) { r.SubjectTokenType = TokenTypeGitLabJobToken }, reason.UnsupportedSubjectTokenType},
		{"an access token requested", func(r *ExchangeRequest) { r.RequestedTokenType = TokenTypeAccessToken }, ""},
		{"a refresh token requested", func(r *ExchangeRequest) { r.RequestedTokenType = "urn:ietf:params:oauth:token-type:refresh_token" }, reason.UnsupportedRequestedTokenType},
		{"an actor token type alone", func(r *ExchangeRequest) { r.ActorTokenType = TokenTypeJWT }, reason.ActorTokenNotSupported},
		{"the base URL as audience, with a slash", func(r *ExchangeRequest) { r.Audience = []string{"https://trove.test/"} }, ""},
		{"another resource", func(r *ExchangeRequest) { r.Resource = []string{"https://trove.test.evil"} }, reason.AudienceNotThisServer},
		{"ttl zero", func(r *ExchangeRequest) { r.TTLSeconds = n(0) }, reason.MalformedRequest},
		{"Q-CI-41 ttl below ci.token.min_ttl", func(r *ExchangeRequest) { r.TTLSeconds = n(29) }, reason.TTLBelowMinimum},
		{"ttl at the floor", func(r *ExchangeRequest) { r.TTLSeconds = n(30) }, ""},
		{"a huge ttl", func(r *ExchangeRequest) { r.TTLSeconds = n(1 << 62) }, ""},
		{"a scope with manage", func(r *ExchangeRequest) { r.Scope = "manage:npm-internal" }, reason.ScopeMalformed},
		{"a scope with two spaces", func(r *ExchangeRequest) { r.Scope = "read  deploy" }, reason.ScopeMalformed},
	} {
		t.Run(c.name, func(t *testing.T) {
			req := ok()
			c.edit(&req)
			_, ee := x.s.checkRequest(req)
			var got Reason
			if ee != nil {
				got = ee.Reason
			}
			if got != c.want {
				t.Fatalf("reason %q, want %q (%v)", got, c.want, ee)
			}
		})
	}
}

// R-CI-021: an address in exempt_cidrs skips the per-address limiter; every other address spends it, and a refusal
// is counted and recorded like a denial.
func TestR_CI_021_TheAddressLimiterAndItsExemptNetworks(t *testing.T) {
	x := newTestExchange(t)
	x.s.o.Config.Exchange.RateLimit.ExemptCIDRs = []string{"10.20.0.0/16", "192.0.2.7"}
	s, err := NewExchange(x.s.o)
	if err != nil {
		t.Fatal(err)
	}
	lim := &countingLimiter{allow: 1}
	s.o.AddressLimiter = lim
	for _, addr := range []string{"10.20.3.4", "192.0.2.7", "::ffff:10.20.0.1"} {
		if err := s.CheckAddress(t.Context(), audit.Source{ClientAddr: addr}); err != nil {
			t.Errorf("%s is exempt: %v", addr, err)
		}
	}
	if len(lim.buckets) != 0 {
		t.Fatalf("an exempt address spent the budget: %v", lim.buckets)
	}
	if err := s.CheckAddress(t.Context(), audit.Source{ClientAddr: "198.51.100.1"}); err != nil {
		t.Fatal(err)
	}
	err = s.CheckAddress(t.Context(), audit.Source{ClientAddr: "198.51.100.1"})
	var ee *ExchangeError
	if !errors.As(err, &ee) || ee.Reason != reason.RateLimited || ee.RetryAfter <= 0 {
		t.Fatalf("err = %v", err)
	}
	bad := x.s.o
	bad.Config.Exchange.RateLimit.ExemptCIDRs = []string{"not-a-network"}
	if _, err := NewExchange(bad); err == nil {
		t.Error("an exempt entry that is no address or CIDR is accepted")
	}
}
