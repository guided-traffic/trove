package bastionmain

import (
	"context"
	"errors"
	"time"
)

// BastionToken is what the validation of a Bastion token yields (R-BAS-004, R-BAS-005): the token and the
// enabled registration it belongs to. It holds no secret and no hash.
type BastionToken struct {
	// TokenID is the token's ID and Hint its public hint (trv_bst_ and the first four characters).
	TokenID string
	Hint    string
	// BastionID is the registration's principal ID and Name its name.
	BastionID string
	Name      string
	// ExpiresAt is the token's expiry; a Bastion token always expires.
	ExpiresAt time.Time
}

// ErrTokenInvalid is the one error of [TokenAuthenticator.Authenticate] for every way a token fails:
// malformed, unknown, expired, revoked, of another type, or its registration disabled or gone. The caller
// does not tell which (R-AUTH-002); the answer is 401 bastion_token_invalid.
var ErrTokenInvalid = errors.New("bastionmain: invalid Bastion token")

// TokenAuthenticator turns a raw Bastion token into its enabled registration (R-BAS-004, R-BAS-005). The
// implementation of work package 1 follows the format, storage, validation, caching and revocation bound of
// R-AUTH-022, R-AUTH-023 and R-AUTH-029: the syntax and checksum without database access, the replica's
// token cache, then the database; a revoked token or a disabled registration is refused within the
// revocation bound.
//
// Only the recognition stage (work package 2) calls it, and only for a token that arrives in
// Trove-Bastion-Authorization or on an interface route. Every other route refuses the type before any
// database lookup (R-BAS-005), which internal/auth/token does in its validation (built).
type TokenAuthenticator interface {
	// Authenticate returns the registration of the token, or [ErrTokenInvalid]. Any other error is a failure
	// of the store (503), not a verdict on the token.
	Authenticate(ctx context.Context, raw string) (BastionToken, error)
}
