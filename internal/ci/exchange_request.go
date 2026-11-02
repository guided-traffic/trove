package ci

import (
	"errors"
	"strings"
	"time"

	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/ci/reason"
)

// Step 1 of R-CI-007: the parameters of the request, before anything of the subject token is read. The
// checks are pure and cost no signature check, so a request that cannot succeed by its own parameters is
// refused for the price of its parse.

// params is a request that passed step 1.
type params struct {
	items []claims.ScopeItem
	ttl   *time.Duration
}

// refuse is an *ExchangeError without a cause.
func refuse(r Reason, description string) *ExchangeError {
	return &ExchangeError{Reason: r, Description: description}
}

// checkRequest validates the parameters (R-CI-007 step 1, R-CI-008): the grant type, the subject token and its
// type, the requested token type, the actor token, audience and resource, ttl and the grammar of scope. A
// parameter sent without a value counts as omitted (RFC 6749 §3.1), so an empty subject_token is a missing
// required parameter, malformed_request (Q-CI-41). The descriptions name parameters, never their values.
func (s *ExchangeService) checkRequest(req ExchangeRequest) (params, *ExchangeError) {
	switch {
	case req.GrantType == "":
		return params{}, refuse(reason.MalformedRequest, "the request has no grant_type")
	case req.GrantType != GrantTypeTokenExchange:
		return params{}, refuse(reason.GrantTypeUnsupported, "the grant_type is not "+GrantTypeTokenExchange)
	case req.SubjectToken == "":
		return params{}, refuse(reason.MalformedRequest, "the request has no subject_token (a parameter without a value counts as omitted, RFC 6749 section 3.1)")
	case req.SubjectTokenType == "":
		return params{}, refuse(reason.MalformedRequest, "the request has no subject_token_type")
	case req.SubjectTokenType == TokenTypeGitLabJobToken:
		return params{}, refuse(reason.UnsupportedSubjectTokenType, "the CI_JOB_TOKEN fallback is not available; request an ID token with id_tokens: and send it as "+TokenTypeIDToken)
	case req.SubjectTokenType != TokenTypeIDToken && req.SubjectTokenType != TokenTypeJWT:
		return params{}, refuse(reason.UnsupportedSubjectTokenType, "the subject_token_type is not "+TokenTypeIDToken+" or "+TokenTypeJWT)
	case req.RequestedTokenType != "" && req.RequestedTokenType != TokenTypeAccessToken:
		return params{}, refuse(reason.UnsupportedRequestedTokenType, "the requested_token_type is not "+TokenTypeAccessToken)
	case req.ActorToken != "" || req.ActorTokenType != "":
		return params{}, refuse(reason.ActorTokenNotSupported, "actor_token and actor_token_type are not supported")
	}
	for _, v := range append(append([]string(nil), req.Audience...), req.Resource...) {
		if !s.thisServer(v) {
			return params{}, refuse(reason.AudienceNotThisServer, "audience and resource must equal the server's base URL")
		}
	}
	var p params
	if req.TTLSeconds != nil {
		n := *req.TTLSeconds
		if n < 1 {
			return params{}, refuse(reason.MalformedRequest, "ttl is a whole number of seconds, at least 1")
		}
		d := time.Duration(min(n, int64((7*24*time.Hour)/time.Second))) * time.Second
		if d < s.o.Config.Token.MinTTL {
			return params{}, refuse(reason.TTLBelowMinimum, "the requested ttl is below ci.token.min_ttl ("+seconds(s.o.Config.Token.MinTTL)+")")
		}
		p.ttl = &d
	}
	items, err := claims.ParseScope(req.Scope)
	if err != nil {
		return params{}, fromClaims(err)
	}
	p.items = items
	return p, nil
}

// thisServer reports whether an audience or resource value names this installation: server.base_url, with or
// without a trailing slash.
func (s *ExchangeService) thisServer(v string) bool {
	base := strings.TrimSuffix(s.o.BaseURL, "/")
	return base != "" && strings.TrimSuffix(v, "/") == base
}

// fromClaims converts a refusal of the claim engine (scope, render) into the exchange's error.
func fromClaims(err error) *ExchangeError {
	var ce *claims.Error
	if errors.As(err, &ce) {
		return refuse(ce.Reason, ce.Detail)
	}
	return &ExchangeError{Reason: reason.Internal, Description: "the request could not be evaluated", Err: err}
}
