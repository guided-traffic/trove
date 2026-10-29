package principal

import (
	"errors"

	"github.com/guided-traffic/trove/internal/errs"
	"github.com/guided-traffic/trove/internal/meta/postgres"
)

// Refusals of the Provisioner and the Manager. The login flow maps each to the
// reason of its audit event and the code of its error page (R-AUTH-007,
// R-AUTH-008).
var (
	// ErrUserDisabled: the user is disabled and not re-enabled by this login.
	ErrUserDisabled = errors.New("principal: user disabled")
	// ErrUsernameTaken: another user holds the mapped username.
	ErrUsernameTaken = errors.New("principal: username taken")
	// ErrNotProvisioned: the identity is unknown and jit is off.
	ErrNotProvisioned = errors.New("principal: not provisioned")
	// ErrUsernameInvalid: the mapped username violates the name rule or is reserved.
	ErrUsernameInvalid = errors.New("principal: username invalid")
	// ErrBreakGlass: the operation does not apply to the break-glass account; the
	// API answers 409 with the code of the login area.
	ErrBreakGlass = errors.New("principal: break-glass account")
)

// DBErr maps a database failure: an outage becomes the 503 of R-ARCH-023, any
// other error an internal error that is logged and not rendered. A nil error
// stays nil and an *errs.Error stays as it is.
func DBErr(op string, err error) error {
	if err == nil {
		return nil
	}
	if _, ok := errs.As(err); ok {
		return err
	}
	if postgres.IsUnavailable(err) {
		return postgres.Unavailable(err)
	}
	return errs.E(errs.Internal, errs.CodeInternal, "The request failed.", errs.WithOp(op), errs.WithCause(err))
}

// NotFound is the 404 for a principal-table object.
func NotFound(what string) error {
	return errs.E(errs.NotFound, errs.CodeNotFound, what+" does not exist.")
}
