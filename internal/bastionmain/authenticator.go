package bastionmain

import (
	"context"
	"errors"

	"github.com/guided-traffic/trove/internal/auth/token"
)

// BastionValidator is the part of the token service the authenticator calls (*token.Service).
type BastionValidator interface {
	AuthenticateBastion(ctx context.Context, raw string, req token.Request) (token.BastionInfo, error)
	NoteUse(id, clientAddr string)
}

// Authenticator implements [TokenAuthenticator] over the token service (R-BAS-004, R-BAS-005). The syntax,
// the replica's token cache with the freshness gate and the revocation bound of R-AUTH-023, the single-flight
// lookup and the rejection log are the token service's; this type maps its verdicts to the one error of the
// seam and records the use of the token (lastUsedAt, R-AUTH-031).
type Authenticator struct {
	v    BastionValidator
	addr func(context.Context) string
}

var _ TokenAuthenticator = (*Authenticator)(nil)

// NewAuthenticator creates the authenticator. clientAddr names the address of the request in ctx for the
// token's last-used address; nil records none.
func NewAuthenticator(v BastionValidator, clientAddr func(context.Context) string) *Authenticator {
	return &Authenticator{v: v, addr: clientAddr}
}

// Authenticate implements [TokenAuthenticator]. A token that is malformed, of another type, unknown, revoked or
// expired, or whose registration is disabled or gone is [ErrTokenInvalid], whichever it is (R-AUTH-002); a store
// that does not answer is the store's error, never a verdict on the token.
func (a *Authenticator) Authenticate(ctx context.Context, raw string) (BastionToken, error) {
	info, err := a.v.AuthenticateBastion(ctx, raw, token.Request{})
	if err != nil {
		switch {
		case errors.Is(err, token.ErrMalformed), errors.Is(err, token.ErrUnknown), errors.Is(err, token.ErrRevoked),
			errors.Is(err, token.ErrExpired), errors.Is(err, token.ErrOwner):
			return BastionToken{}, ErrTokenInvalid
		}
		return BastionToken{}, err
	}
	addr := ""
	if a.addr != nil {
		addr = a.addr(ctx)
	}
	a.v.NoteUse(info.TokenID, addr)
	return BastionToken{TokenID: info.TokenID, Hint: info.Hint, BastionID: info.BastionID, Name: info.Name, ExpiresAt: info.ExpiresAt}, nil
}
