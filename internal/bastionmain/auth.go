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

// ErrThrottled is the error of [TokenAuthenticator.Authenticate] for a token the replica does not hold in its cache
// while the address that sent it is in throttled mode (R-AUTH-038, Q-BAS-65): the token was not looked up. The
// answer is 429 rate_limited.
var ErrThrottled = errors.New("bastionmain: the address that sent the Bastion token is throttled")

// AuthRequest is what the authenticator learns of the request besides the token.
type AuthRequest struct {
	// Throttle is the failed-credential throttle of the address that sent the token, nil for none. It is asked
	// only when the token is not in the replica's token cache, as the token service asks for every other
	// credential (R-AUTH-023, R-AUTH-038): a known Bastion token costs no read of the shared counters.
	Throttle Throttle
	// ClientAddr and UserAgent are the source of the request as the chain resolved it before the Bastion's
	// forwarded headers count: the address the Bastion, or whoever sent the token, connected from (R-BAS-017).
	// token.rejected names it, and the last use of the token records the address (R-AUTH-031).
	ClientAddr string
	UserAgent  string
}

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
	// Authenticate returns the registration of the token, [ErrTokenInvalid], or [ErrThrottled]. Any other
	// error is a failure of the store (503), not a verdict on the token.
	Authenticate(ctx context.Context, raw string, req AuthRequest) (BastionToken, error)
}
