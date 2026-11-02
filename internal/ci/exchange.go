package ci

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
)

// The token exchange of work package W4 (R-CI-007, R-CI-008, R-CI-016, R-CI-018, R-CI-020, R-CI-021). The
// files of the exchange:
//
//	exchange.go          the request, the result, the error and the interface
//	exchange_service.go  the service, its options and the order of R-CI-007
//	exchange_request.go  step 1: the parameters
//	exchange_tx.go       the one transaction: the re-read, the restore cut-off, the jti, steps 4 to 10
//	exchange_scope.go    the scope narrowing and the answer's `scope`
//	exchange_denials.go  the per-replica aggregation of the denial events (R-CI-021)
//	ttl.go               the lifetime formula (R-CI-016), shared with the dry run

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
// values. SubjectToken and ActorToken are credentials: they are never logged, never put in an error
// and never stored; LogValue and String leave them out.
type ExchangeRequest struct {
	GrantType          string
	SubjectToken       string
	SubjectTokenType   string
	RequestedTokenType string
	Scope              string
	// Audience and Resource are the values of the parameters; each must equal the configured base URL.
	// The handler refuses a parameter sent twice, so each holds at most one value.
	Audience, Resource []string
	// ActorToken and ActorTokenType are carried only to be refused (actor_token_not_supported).
	ActorToken, ActorTokenType string
	CIIssuer                   string
	// TTLSeconds is the trove extension `ttl`; nil when the request sent none.
	TTLSeconds *int64

	// Source is the client address (trusted-proxy handling, R-ARCH-006), the user agent and the
	// X-Request-Id, which the error body repeats as requestId (R-CI-008).
	Source audit.Source
}

// LogValue is the request as a log line may show it: the parameter names that are set, never a token.
func (r ExchangeRequest) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("grant_type", r.GrantType), slog.Bool("subject_token", r.SubjectToken != ""),
		slog.String("subject_token_type", r.SubjectTokenType), slog.String("scope", r.Scope),
		slog.Bool("actor_token", r.ActorToken != ""), slog.String("ci_issuer", r.CIIssuer),
	)
}

// String is LogValue's text, so a %v never prints a token.
func (r ExchangeRequest) String() string { return r.LogValue().String() }

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

// String leaves the token out.
func (r ExchangeResult) String() string {
	return "exchange result (ci_issuer " + r.CIIssuer + ", expires_in " + strconv.FormatInt(r.ExpiresIn, 10) + ")"
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

// TraceEntry, Selection and Select are in select.go (work package W3); TTLInput and ComputeTTL in ttl.go.

// Exchanger is the token exchange (R-CI-007, R-CI-008, R-CI-016, R-CI-018, R-CI-020, R-CI-021),
// built by work package W4 ([ExchangeService]). The handler of POST /api/v1/auth/oidc/token calls
// CheckAddress before it reads the body, Refused for a refusal of its own decoding and Exchange after it
// parsed the body.
type Exchanger interface {
	// CheckAddress is the limiter ci_exchange_address (R-CI-021): per client address (src.ClientAddr),
	// cluster scope, addresses in ci.exchange.rate_limit.exempt_cidrs skip it. It makes no database
	// access. The error is an *ExchangeError with reason rate_limited and a Retry-After; the refusal is
	// counted and recorded like every denial.
	CheckAddress(ctx context.Context, src audit.Source) error
	// Refused records a refusal the handler decided before Exchange (the body limit, the content type, a
	// malformed body, a parameter sent twice): the metric and the aggregated ci.exchange.denied event.
	Refused(ctx context.Context, src audit.Source, err *ExchangeError)
	// Exchange runs R-CI-007's processing order. The request parameters are validated first, then the
	// subject token (subject.Parse, the issuer from the in-process copy, Verify with the key source
	// of the issuer), then the limiter ci_exchange_subject (after validation, before the
	// transaction). One transaction then: re-read the issuer and every matched mapping FOR SHARE and
	// compare versions and mappings_version (Q-CI-15, evaluate again from PostgreSQL once, then
	// configuration_changed), restore cut-off, the jti upsert (R-CI-018), the token row through
	// token.Service.IssueCITx, the issuance record, and the audit event ci.exchange.succeeded. Steps 4
	// to 9 inside it call no Valkey, issuer or other service (R-ARCH-019). A failed transaction leaves
	// no trace of the jti. Every error is an *ExchangeError; a denial writes ci.exchange.denied
	// (aggregated per replica, R-CI-021), before the signature is verified with only the unverified iss
	// and kid.
	Exchange(ctx context.Context, req ExchangeRequest) (ExchangeResult, error)
}
