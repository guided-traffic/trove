// Package oidc is the browser login against the configured OpenID Connect
// providers: discovery, the authorization code flow with PKCE, the login flow
// row, the callback, logout at the provider and the break-glass sign-in beside
// it (R-AUTH-003 to R-AUTH-009, R-AUTH-013, R-AUTH-014).
//
// It depends on package principal (the [principal.Provisioner] it hands claims
// to) and on package session (it creates the session after a successful
// callback). It owns no shared interface beyond those two.
package oidc
