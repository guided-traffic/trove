package session

import (
	"errors"

	"github.com/guided-traffic/trove/internal/errs"
)

// Problem maps an error of [Store.Validate] or [Store.CheckCSRF] to the error
// the authentication stage answers with: 401 session_expired (R-AUTH-002) or
// 403 csrf_failed (R-AUTH-012). Any other error, a database that does not
// answer included, is returned as it is (503, R-AUTH-001). The stage adds the
// WWW-Authenticate challenge of its endpoint family.
func Problem(err error) error {
	switch {
	case errors.Is(err, ErrInvalid):
		return errs.E(errs.Unauthenticated, errs.CodeSessionExpired, "The session has ended; sign in again.", errs.WithCause(err))
	case errors.Is(err, ErrCSRF):
		return errs.E(errs.Forbidden, errs.CodeCSRFFailed,
			"A cookie-authenticated request that changes state needs the X-CSRF-Token header and a same-origin request.", errs.WithCause(err))
	}
	return err
}

// ReauthenticationRequired is the 401 of R-AUTH-011 for an action that mints a
// long-lived credential through a session whose interactive authentication is
// older than auth.sessions.recent_auth_window.
func ReauthenticationRequired() error {
	return errs.E(errs.Unauthenticated, errs.CodeReauthenticationRequired,
		"This action needs a recent sign-in; re-authenticate at the identity provider and retry.")
}
