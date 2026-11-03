package bastionmain

import (
	"context"
	"errors"
	"time"

	"github.com/guided-traffic/trove/internal/audit"
	"github.com/guided-traffic/trove/internal/auth/token"
)

// BastionValidator is the part of the token service the authenticator calls (*token.Service).
type BastionValidator interface {
	AuthenticateBastion(ctx context.Context, raw string, req token.Request) (token.BastionInfo, error)
	NoteUse(id, clientAddr string)
}

// throttleWindow is the Retry-After of a throttled answer: the window of the failed-credential limiter
// (R-AUTH-038).
const throttleWindow = time.Minute

// Authenticator implements [TokenAuthenticator] over the token service (R-BAS-004, R-BAS-005). The syntax,
// the replica's token cache with the freshness gate and the revocation bound of R-AUTH-023, the throttle asked
// after a cache miss, the single-flight lookup and the rejection log are the token service's; this type maps
// its verdicts to the errors of the seam and records the use of the token (lastUsedAt, R-AUTH-031).
type Authenticator struct {
	v BastionValidator
}

var _ TokenAuthenticator = (*Authenticator)(nil)

// NewAuthenticator creates the authenticator.
func NewAuthenticator(v BastionValidator) *Authenticator {
	return &Authenticator{v: v}
}

// Authenticate implements [TokenAuthenticator]. A token that is malformed, of another type, unknown, revoked or
// expired, or whose registration is disabled or gone is [ErrTokenInvalid], whichever it is (R-AUTH-002); a token
// the replica does not hold while the sender's address is throttled is [ErrThrottled]; a store that does not
// answer is the store's error, never a verdict on the token. token.rejected names the request's source, and the
// last use of an accepted token records the address the request came from: the Bastion's own, not the caller's
// it forwards for (R-BAS-017).
func (a *Authenticator) Authenticate(ctx context.Context, raw string, req AuthRequest) (BastionToken, error) {
	treq := token.Request{Source: audit.Source{ClientAddr: req.ClientAddr, UserAgent: req.UserAgent}, RetryAfter: throttleWindow}
	if req.Throttle != nil {
		treq.Throttle = req.Throttle
	}
	info, err := a.v.AuthenticateBastion(ctx, raw, treq)
	if err != nil {
		switch {
		case errors.Is(err, token.ErrThrottled):
			return BastionToken{}, ErrThrottled
		case errors.Is(err, token.ErrMalformed), errors.Is(err, token.ErrUnknown), errors.Is(err, token.ErrRevoked),
			errors.Is(err, token.ErrExpired), errors.Is(err, token.ErrOwner):
			return BastionToken{}, ErrTokenInvalid
		}
		return BastionToken{}, err
	}
	a.v.NoteUse(info.TokenID, req.ClientAddr)
	return BastionToken{TokenID: info.TokenID, Hint: info.Hint, BastionID: info.BastionID, Name: info.Name, ExpiresAt: info.ExpiresAt}, nil
}
