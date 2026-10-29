package httpserver

// This file holds the seam between the authentication stage (R-ARCH-005 step 6)
// and everything behind it: the value the chain stores in the request context.
// It is a plain value type of the edge layer. The domain packages (internal/auth,
// internal/authz) are below this layer and cannot import it; they define their
// own richer types, and the composition root and internal/api/v1 convert at the
// seam (docs/developer/m1-contracts.md).

// PrincipalKind is what kind of identity a request was authenticated as
// (R-AUTHZ-002). The values are stable: they appear in audit events.
type PrincipalKind string

// The principal kinds.
const (
	// KindAnonymous is the caller without a credential (R-AUTH-037).
	KindAnonymous PrincipalKind = "anonymous"
	// KindUser is a user of the local table, signed in through OIDC or a token of
	// a user (R-AUTH-016).
	KindUser PrincipalKind = "user"
	// KindServiceAccount is a service account (R-AUTH-020).
	KindServiceAccount PrincipalKind = "service_account"
	// KindCI is a CI identity, the owner of a CI token (R-AUTH-022). CI tokens
	// arrive with the CI exchange; the kind is reserved from M1 on.
	KindCI PrincipalKind = "ci"
	// KindBreakGlass is the break-glass administrator (R-AUTH-014). Authorization
	// grants it the administrator role without a role binding (R-AUTHZ-010).
	KindBreakGlass PrincipalKind = "break_glass"
)

// AuthMethod is the credential source that proved the principal (R-AUTH-001).
type AuthMethod string

// The credential sources, in the order the stage tries them.
const (
	// MethodNone is the anonymous request.
	MethodNone AuthMethod = ""
	// MethodAPIKey is the NuGet API key header (R-AUTH-033); reserved for M2.
	MethodAPIKey AuthMethod = "api_key"
	// MethodBearer is "Authorization: Bearer <token>".
	MethodBearer AuthMethod = "bearer"
	// MethodBasic is "Authorization: Basic base64(<any>:<token>)".
	MethodBasic AuthMethod = "basic"
	// MethodSession is the session cookie (R-AUTH-001 item 3).
	MethodSession AuthMethod = "session"
)

// TokenType is the type of a trove token, from its prefix (R-AUTH-022).
type TokenType string

// The token types.
const (
	TokenPAT TokenType = "pat" // trv_pat_, personal access token
	TokenSAT TokenType = "sat" // trv_sat_, service-account token
	TokenCIT TokenType = "cit" // trv_cit_, CI token
)

// GroupRef names a group the caller belongs to.
type GroupRef struct {
	// ID is the immutable ID in the principal table (R-AUTHZ-002).
	ID   string
	Name string
}

// Patterns is an include and exclude list of globs (R-AUTHZ-005).
type Patterns struct {
	Include []string
	Exclude []string
}

// ScopeGrant is one element of a token scope's repository part (R-AUTH-024).
type ScopeGrant struct {
	// Repositories are keys or patterns.
	Repositories []string
	Packages     Patterns
	Paths        Patterns
	// Actions are the repository actions of R-AUTHZ-006.
	Actions []string
}

// Scope is the ceiling a token puts on its owner's rights: effective rights are
// the owner's grants at use time intersected with the scope (R-AUTH-024,
// R-AUTHZ-013). It never grants anything. Builds join with P1.
type Scope struct {
	// Inherit admits every repository right of the owner.
	Inherit bool
	// Admin lets the token exercise the owner's system roles.
	Admin bool
	// Repositories are the restricted grants when Inherit is false.
	Repositories []ScopeGrant
	// System lists system capabilities (R-AUTHZ-010) from the closed list of R-AUTH-025.
	System []string
}

// Credential is what proved the principal. The zero value is the anonymous
// request. It holds IDs and states, never a token value, hash or session secret
// (R-AUTH-041).
type Credential struct {
	Method AuthMethod
	// SessionID is the ID of the UI session (MethodSession).
	SessionID string
	// TokenID and TokenType identify the token (MethodBearer, MethodBasic, MethodAPIKey).
	TokenID   string
	TokenType TokenType
	// Scope is the token's ceiling. Nil means there is none: sessions and the
	// break-glass session are bounded by the principal's own rights only.
	Scope *Scope
	// Endpoints are the endpoint names the token is valid on (R-EP-011); empty
	// means unbound. The stage has already refused a token on another endpoint,
	// so the field is for audit and for the decision point.
	Endpoints []string
	// BasicUsername is the username of a Basic credential, recorded for
	// diagnostics and otherwise ignored (R-AUTH-001).
	BasicUsername string
}

// Principal is the authenticated caller, or the anonymous one. It is the seam
// between the authentication stage and the handlers: handlers and the
// authorization step receive only this and never parse credentials themselves
// (R-AUTH-001).
//
// Whether the caller holds the system administrator capability is not a field.
// Authorization decides it from the principal's role bindings, the credential's
// scope and the principal kind (R-AUTHZ-010, R-AUTHZ-013).
type Principal struct {
	// Anonymous is true when the request carried no credentials. Existing code
	// tests this field; it equals Kind == KindAnonymous for principals the
	// authentication stage builds.
	Anonymous bool
	// Kind is the principal kind; empty for a Principal built by a test that only
	// sets Name.
	Kind PrincipalKind
	// ID is the immutable ID of the principal table row; empty when anonymous.
	ID string
	// Name is the username, service-account name or CI principal reference
	// (R-AUTHZ-002); empty when anonymous.
	Name string
	// Groups are the groups of the principal as the stage resolved them from the
	// principal cache at request time: local memberships plus the synchronized
	// ones (R-AUTHZ-002, R-AUTH-023). Tokens carry no group lists. Empty for
	// anonymous and CI principals, whose principal set is not group-derived by
	// membership of a user.
	Groups []GroupRef
	// Mappings are the CI identity mapping references ("ci:<issuer>/<mapping>")
	// recorded on a CI token at exchange (R-AUTHZ-002); reserved for M4.
	Mappings []string
	// Credential is what proved the principal.
	Credential Credential
	// MinEpoch is the value of the request header Trove-Authz-Min-Epoch of an
	// authenticated request, 0 when absent or anonymous (R-AUTHZ-032). The
	// composition root fills it after authentication succeeded.
	MinEpoch int64
}

// AnonymousPrincipal returns the caller without a credential.
func AnonymousPrincipal() Principal {
	return Principal{Anonymous: true, Kind: KindAnonymous}
}
