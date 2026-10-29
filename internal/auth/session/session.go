package session

import (
	"context"
	"errors"
	"net/http"

	"github.com/guided-traffic/trove/internal/auth/principal"
)

// Info is what a valid session yields. It holds no secret.
type Info struct {
	ID string
	// Principal is the signed-in principal, checked active by the validator.
	Principal principal.Principal
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
