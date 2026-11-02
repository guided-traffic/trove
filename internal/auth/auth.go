package auth

import (
	"net/http"

	"github.com/guided-traffic/trove/internal/auth/principal"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/auth/token"
	"github.com/guided-traffic/trove/internal/ci/claims"
)

// Method is the credential source that proved the principal (R-AUTH-001). The
// values match httpserver.AuthMethod. The stage evaluates only the first source
// present, in this order: [MethodAPIKey] (on the routes of
// [EndpointRules.APIKeyRoute] only), then the Authorization header
// ([MethodBearer] or [MethodBasic]), then [MethodSession], then none.
type Method string

// The credential sources.
const (
	MethodNone Method = ""
	// MethodAPIKey is a trove token in [HeaderAPIKey] (R-AUTH-033).
	MethodAPIKey  Method = "api_key"
	MethodBearer  Method = "bearer"
	MethodBasic   Method = "basic"
	MethodSession Method = "session"
)

// HeaderAPIKey is the NuGet API-key header (R-AUTH-001 item 1, R-AUTH-033). The
// stage examines it on the routes that declare it while
// auth.clients.nuget_api_key_header is on ([Options.APIKeyHeader]).
const HeaderAPIKey = "X-NuGet-ApiKey" //nolint:gosec // G101: a header name, not a credential

// Credential is what proved the principal: IDs and states only.
type Credential struct {
	Method        Method
	SessionID     string
	TokenID       string
	TokenType     token.Type
	Scope         *scope.Scope // nil: no ceiling (sessions)
	BasicUsername string
}

// Result is what the stage yields for an authenticated or anonymous request. A
// rejection is the error return of [Authenticator.Authenticate].
type Result struct {
	// Anonymous is true when the request carries no credential and the endpoint
	// and R-AUTH-037 allow anonymous access.
	Anonymous  bool
	Principal  principal.Principal
	Groups     []principal.GroupRef
	Credential Credential
	// CI is the frozen bundle of a CI token (token.Info.CI); nil for every other credential
	// (R-AUTHZ-014, Q-CI-17).
	CI *claims.Frozen
}

// EndpointRules is what the stage needs to know about the endpoint of the
// request (R-EP-010). The composition root builds it from the endpoint of the
// request context; the defaults are those of the default endpoint.
type EndpointRules struct {
	// Name is the endpoint name; "default" for the default endpoint.
	Name string
	// AcceptsSession is true when the endpoint's surface holds ui.
	AcceptsSession bool
	// TokenTypes are the accepted token types (auth.token_types).
	TokenTypes []token.Type
	// BoundTokensOnly refuses unbound tokens (auth.bound_tokens_only).
	BoundTokensOnly bool
	// Anonymous is true when the endpoint allows anonymous requests
	// (auth.anonymous), subject to R-AUTH-037.
	Anonymous bool
	// PublicRoute reports whether the request matches a management API operation
	// that is declared public (or public by a switch): such a request without a
	// credential reaches its handler as the anonymous principal whatever the
	// anonymous switches say, because the operation's own declaration decides
	// (R-AUTH-037 "Always unauthenticated", R-API-008). Nil: none is.
	PublicRoute func(*http.Request) bool
	// APIKeyRoute reports whether the request matches a package-protocol route that
	// declares the API-key header (formats.Route.APIKey: NuGet's push, delete, relist
	// and symbol push, R-NUGET-012). Only there is [HeaderAPIKey] examined, before
	// Authorization (R-AUTH-001 item 1, R-AUTH-033), and there the session cookie is
	// never accepted, whatever the method (Q-AUTH-187). The composition root hands
	// the dispatcher's own match of the route, so the stage and the dispatcher agree
	// on it (Q-AUTH-186). The stage asks it only when a credential depends on the
	// answer. Nil: no route is.
	APIKeyRoute func(*http.Request) bool
	// Refused, when set, is told of every request the endpoint's rules refused
	// (R-EP-010): a token type the endpoint does not accept, a token bound
	// elsewhere or an unbound token where only bound ones are accepted, and a
	// request without a credential on an endpoint without anonymous access. The
	// composition root counts it in trove_endpoint_credentials_refused_total and
	// records token.rejected with reason endpoint_not_accepted (R-EP-009). The
	// answer is the 401 of an unknown token or of a missing credential either way.
	Refused func(r *http.Request, f Refusal)
}

// The reasons of an endpoint's refusal, the values of the reason label of
// trove_endpoint_credentials_refused_total (R-EP-009).
const (
	RefusedTokenType    = "token_type"
	RefusedTokenBinding = "token_binding"
	RefusedAnonymous    = "anonymous"
)

// Refusal is a request the endpoint's credential rules refused (R-EP-010).
type Refusal struct {
	// Reason is one of the Refused* values.
	Reason string
	// TokenType is the type of a refused token; TokenID its ID when the refusal
	// came after the lookup (a binding refusal). A type refusal makes no lookup,
	// so it has no ID.
	TokenType token.Type
	TokenID   string
}

func (ep EndpointRules) refused(r *http.Request, f Refusal) {
	if ep.Refused != nil {
		ep.Refused(r, f)
	}
}

// Authenticator is the authentication stage. An error it returns is an
// *errs.Error of kind Unauthenticated (401 with the challenge of R-AUTH-002,
// code unauthenticated or invalid_token); a credential that is present but
// invalid never falls through to a lower-ranked source or to anonymous.
type Authenticator interface {
	Authenticate(r *http.Request, ep EndpointRules) (Result, error)
}
