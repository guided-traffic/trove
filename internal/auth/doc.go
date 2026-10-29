// Package auth is the authentication stage (R-ARCH-005 step 6, R-AUTH-001,
// R-AUTH-002, R-EP-010): it selects the one credential of a request, validates
// it through package token or package session, applies the rules of the
// endpoint the request arrived on, and yields a [Result] or a rejection.
//
// Handlers and package authz never see a credential, only the result, which the
// composition root converts to httpserver.Principal (the chain stores that
// type; this package is below the edge layer and cannot import it).
//
// Sub-packages: principal (the principal table), token, session (the UI
// session) and oidc (the login flow behind it).
package auth
