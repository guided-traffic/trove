package v1

import "net/http"

// The token exchange, POST /api/v1/auth/oidc/token (R-CI-007, R-CI-008, R-CI-021), work package W4.
//
// What the handler must do, so that the endpoint stays what the specification says:
//
//   - It requires and parses no trove credential, session or CSRF token. The authentication stage
//     already answers the anonymous principal for this route whatever the request carries
//     (internal/auth.isExchangeRoute), the declaration is {public: true}, and the route belongs to
//     the surface family ci_exchange (httpserver.FamilyOf), so an endpoint serves it only when its
//     surfaces name ci_exchange. The general API rate limit (limitedPath in ratelimit.go) must skip
//     the route: the exchange has the limiters of R-CI-021 of its own.
//   - [ci.Exchanger.CheckAddress] runs first, before the body is read; then the body limit
//     ci.exchange.max_body_bytes, the content type (form or JSON, anything else 400
//     unsupported_content_type), a repeated parameter (400 malformed_request), then
//     [ci.Exchanger.Exchange].
//   - Every failure is the OAuth error response of RFC 6749 section 5.2, not problem details: the
//     status and `error` from [ci.Reason.OAuth], `reason` unless ci.exchange.expose_reason is false,
//     `requestId` equal to X-Request-Id, `Retry-After` on 429 and 503, Cache-Control no-store.
//     The response and the log hold no subject token and no trv_cit_ value.
//
// Until W4, the stub answers 501 ci_not_implemented as problem details.

// ExchangeCiToken implements POST /api/v1/auth/oidc/token.
func (s *Server) ExchangeCiToken(w http.ResponseWriter, r *http.Request) {
	ciPending(w, r, s.o.CI.Exchanger != nil, "The CI token exchange", "W4")
}
