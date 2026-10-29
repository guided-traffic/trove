// Package auth is the authentication stage (R-ARCH-005 step 6, R-AUTH-001,
// R-AUTH-002, R-EP-010): it selects the one credential of a request, validates
// it through package token or package session, applies the rules of the
// endpoint the request arrived on, and yields a [Result] or a rejection.
//
// Handlers and package authz never see a credential, only the result, which the
// composition root converts to httpserver.Principal (the chain stores that
// type; this package is below the edge layer and cannot import it).
//
// Sub-packages: principal (the principal table), token, session, oidc. The
// implementation of [Authenticator] belongs to work package WP1.2, with the
// endpoint rules (R-EP-010, R-EP-011) filled in by WP1.9; the session and the
// login flow behind it are WP1.1.
package auth
