package ci

import (
	"context"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/ci/claims"
)

// The OAuth values of the exchange (R-CI-007).
const (
	GrantTypeTokenExchange = "urn:ietf:params:oauth:grant-type:token-exchange" //nolint:gosec // G101: an OAuth URN, not a credential
	TokenTypeIDToken       = "urn:ietf:params:oauth:token-type:id_token"       //nolint:gosec // G101: an OAuth URN, not a credential
	TokenTypeJWT           = "urn:ietf:params:oauth:token-type:jwt"            //nolint:gosec // G101: an OAuth URN, not a credential
	TokenTypeAccessToken   = "urn:ietf:params:oauth:token-type:access_token"   //nolint:gosec // G101: an OAuth URN, not a credential
	// TokenTypeGitLabJobToken is the subject token type of the P2 fallback (R-CI-022); the MVP answers
	// it with unsupported_subject_token_type.
	TokenTypeGitLabJobToken = "urn:trove:token-type:gitlab-job-token" //nolint:gosec // G101: an OAuth URN, not a credential
)

// ExchangeRequest is a parsed exchange request (R-CI-007): the handler decodes the form or JSON body
// (content type, repeated parameters, the body limit) and fills this; the service validates the
// values. SubjectToken is a credential: it is never logged, never put in an error and never
// stored.
type ExchangeRequest struct {
	GrantType          string
	SubjectToken       string
	SubjectTokenType   string
	RequestedTokenType string
	Scope              string
	// Audience and Resource are the values of the parameters; each must equal the configured base URL.
	Audience, Resource []string
	// ActorToken and ActorTokenType are carried only to be refused (actor_token_not_supported).
	ActorToken, ActorTokenType string
	CIIssuer                   string
	// TTLSeconds is the trove extension `ttl`; nil when the request sent none.
	TTLSeconds *int64

	// Source is the client address (trusted-proxy handling, R-ARCH-006) and user agent; RequestID is
	// the X-Request-Id that the error body repeats as requestId (R-CI-008).
	Source    audit.Source
	RequestID string
}

// ExchangeResult is the 200 body of the exchange (R-CI-007). AccessToken is the one place the
// token value appears.
type ExchangeResult struct {
	AccessToken     string
	IssuedTokenType string
	TokenType       string
	ExpiresIn       int64
	Scope           string
	CIIssuer        string
	Mappings        []string
}

// ExchangeError is every failure of the exchange (R-CI-008): the reason, a description that holds no
// claim value and no token material, and the Retry-After of a 429 or 503. The handler renders it as
// the OAuth error response with the status and `error` of [Reason.OAuth]. Err is the cause for the
// log, never for the body.
type ExchangeError struct {
	Reason      Reason
	Description string
	RetryAfter  time.Duration
	Err         error
}

// Error implements error.
func (e *ExchangeError) Error() string { return string(e.Reason) + ": " + e.Description }

// Unwrap returns the cause.
func (e *ExchangeError) Unwrap() error { return e.Err }

// TraceEntry is one mapping in the decision trace of an evaluation (R-CI-019): the first failed leaf
// as the spec's sketch shows it, and every leaf for the claim tester (R-UI-036).
type TraceEntry struct {
	Tier    string
	Mapping string
	Matched bool
	// Contributes is false for a matched mapping whose cross_project_source is deny on a
	// cross-project token (R-CI-012): it is not one of the token's mappings.
	Contributes bool
	FailedLeaf  string
	Detail      string
	Leaves      []claims.LeafResult
}

// Selection is the result of steps 4 to 7 of the exchange on one mapping set (R-CI-007): the
// classification, the trace, the contributing mappings, their rendered grants and the caps. It is a
// pure function of the mapping set and the claims, so the dry run (W3) and the exchange (W4) share it.
type Selection struct {
	CrossProject bool
	Trace        []TraceEntry
	// Mappings are the contributing mappings, in evaluation order; exactly one in the MVP (one tier).
	Mappings []Mapping
	// Rendered is the union of what the contributing mappings render (policies, groups, inline).
	Rendered claims.Rendered
	Caps     []claims.Cap
	// Failure is no_matching_mapping, cross_project_source_denied or grant_render_failed, "" on a
	// success; Detail names the mapping or the claim and holds no claim value.
	Failure Reason
	Detail  string
}

// Select evaluates the mapping set for a subject token of the given kind (R-CI-009): the enabled
// mappings whose subject_kinds include it, per tier in ascending priority, the first match per tier;
// the cross-project classification and the per-mapping opt-in (R-CI-012); the rendering of the
// matched mappings' grants (R-CI-013, R-CI-014) without falling through on a render failure; the cap
// `read` for a read_only opt-in. It records the trace of every mapping it looked at.
func Select(_ MappingSet, _ string, _ claims.Claims) Selection {
	panic("not implemented: W3")
}

// TTLInput is what the lifetime of a CI token depends on (R-CI-016).
type TTLInput struct {
	// Requested is the `ttl` parameter; nil takes Default.
	Requested *time.Duration
	// Default, Max, Min are ci.token.default_ttl, ci.token.max_ttl and ci.token.min_ttl.
	Default, Max, Min time.Duration
	// IssuerMax is the issuer's max_ttl; MappingMax the smallest max_ttl of the contributing mappings.
	// Nil is unbounded.
	IssuerMax, MappingMax *time.Duration
	// SubjectExpiresAt is the `exp` of the subject token; Now is the exchange's clock.
	SubjectExpiresAt, Now time.Time
}

// ComputeTTL is the formula of R-CI-016: the minimum of the requested or default ttl, the mapping and
// issuer bounds, ci.token.max_ttl and the remaining lifetime of the subject token. A result below Min
// is the reason subject_token_expires_too_soon.
func ComputeTTL(_ TTLInput) (time.Duration, Reason) {
	panic("not implemented: W4")
}

// Exchanger is the token exchange (R-CI-007, R-CI-008, R-CI-016, R-CI-018, R-CI-020, R-CI-021),
// implemented by W4. The handler of POST /api/v1/auth/oidc/token calls CheckAddress before it reads
// the body and Exchange after it parsed it.
type Exchanger interface {
	// CheckAddress is the limiter ci_exchange_address (R-CI-021): per client address, cluster scope,
	// addresses in ci.exchange.rate_limit.exempt_cidrs skip it. It makes no database access. The error
	// is an *ExchangeError with reason rate_limited and a Retry-After.
	CheckAddress(ctx context.Context, clientAddr string) error
	// Exchange runs R-CI-007's processing order. The request parameters are validated first, then the
	// subject token (subject.Parse, the issuer from the in-process copy, Verify with the key source
	// of the issuer), then the limiter ci_exchange_subject (after validation, before the
	// transaction). One transaction then: re-read the issuer and every matched mapping FOR SHARE and
	// compare versions and mappings_version (Q-CI-15, evaluate again from PostgreSQL once, then
	// configuration_changed), restore cut-off, the jti upsert (R-CI-018), the token row through
	// token.Service.IssueCITx, the issuance record, and the audit event ci.exchange.succeeded. Steps 4
	// to 9 inside it call no Valkey, issuer or other service (R-ARCH-019). A failed transaction leaves
	// no trace of the jti. Every error is an *ExchangeError; a denial after verification writes
	// ci.exchange.denied (aggregated per replica, R-CI-021) and before it only the unverified iss and
	// kid.
	Exchange(ctx context.Context, req ExchangeRequest) (ExchangeResult, error)
}
