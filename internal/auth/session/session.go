package session

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/guided-traffic/trove/internal/auth/principal"
)

// Auth methods of a session row (ui_session.auth_method).
const (
	MethodOIDC       = "oidc"
	MethodBreakGlass = "break_glass"
)

// Info is what a valid session yields. It holds no secret: the CSRF hash it
// carries for [Validator.CheckCSRF] is unexported and is a SHA-256, not the token.
type Info struct {
	ID string
	// Principal is the signed-in principal, checked active by the validator. A
	// break-glass session has Kind principal.KindBreakGlass (R-AUTHZ-010).
	Principal principal.Principal
	// AuthMethod is MethodOIDC or MethodBreakGlass; ProviderID names the provider
	// of an OIDC session.
	AuthMethod string
	ProviderID string
	// AuthenticatedAt is the last interactive authentication (R-AUTH-011), as the
	// row stores it.
	AuthenticatedAt time.Time

	csrf []byte
	// authLocal is AuthenticatedAt on this replica's monotonic clock, derived
	// from the database's age of the value, so the recent-authentication check
	// compares no two clocks.
	authLocal time.Time
}

// AuthenticatedWithin reports whether the last interactive authentication is at
// most d ago (R-AUTH-011 recent authentication).
func (i Info) AuthenticatedWithin(d time.Duration) bool {
	if i.authLocal.IsZero() {
		return false
	}
	return time.Since(i.authLocal) <= d
}

// ErrInvalid covers an unknown, expired, ended or evicted session and a
// disabled principal. The stage answers 401 and the cookie is not retried
// against a lower-ranked source (R-AUTH-001).
var ErrInvalid = errors.New("session: invalid")

// ErrCSRF is returned when the CSRF rules of R-AUTH-012 refuse a request.
var ErrCSRF = errors.New("session: csrf check failed")

// Validator resolves the session cookie of a request.
type Validator interface {
	// Validate resolves the cookie value (cache auth_sessions, then ui_session).
	Validate(ctx context.Context, cookie string) (Info, error)
	// CheckCSRF applies R-AUTH-012 to a request that carries the session: the
	// rules depend on method and path family, so the validator sees the request.
	CheckCSRF(r *http.Request, s Info) error
}
