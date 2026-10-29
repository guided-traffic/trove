package token

import (
	"context"
	"errors"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
)

// Type is a token type, from the prefix (R-AUTH-022). The values match
// httpserver.TokenType.
type Type string

// The token types and their prefixes.
const (
	TypePAT Type = "pat" // trv_pat_
	TypeSAT Type = "sat" // trv_sat_
	TypeCIT Type = "cit" // trv_cit_
)

// Patterns is an include and exclude list of globs (R-AUTHZ-005).
type Patterns struct {
	Include []string
	Exclude []string
}

// ScopeGrant is one element of the repository part of a scope.
type ScopeGrant struct {
	Repositories []string
	Packages     Patterns
	Paths        Patterns
	Actions      []string
}

// Scope is the validated scope of a token (R-AUTH-024). It narrows the owner's
// rights and never widens them. Builds join with P1.
type Scope struct {
	Inherit      bool
	Admin        bool
	Repositories []ScopeGrant
	System       []string
	// Endpoints binds the token to endpoint names (R-EP-011); empty means unbound.
	Endpoints []string
}

// Info is what a successful validation yields: the token row's facts and the
// active owner. It holds no secret, no hash and no token value.
type Info struct {
	ID    string
	Type  Type
	Scope Scope
	// Owner is the owner principal, checked active by the validator.
	Owner     principal.Principal
	ExpiresAt *time.Time
}

// Rejection reasons the validator reports (R-AUTH-023 step 3); the stage maps
// every one to 401 invalid_token. The distinction exists for audit only.
var (
	ErrMalformed = errors.New("token: malformed")
	ErrUnknown   = errors.New("token: unknown")
	ErrRevoked   = errors.New("token: revoked")
	ErrExpired   = errors.New("token: expired")
	ErrOwner     = errors.New("token: owner not active")
)

// Validator resolves a presented token (R-AUTH-023). The implementation checks
// the syntax without database access, the replica's cache, the failed-credential
// throttle, then the database.
type Validator interface {
	// Validate returns the token's facts, or one of the Err values above.
	Validate(ctx context.Context, raw string) (Info, error)
}
