// Package token is the token contract of R-AUTH-022 to R-AUTH-032: the three
// token types and their prefixes, the scope structure (R-AUTH-024) that the
// authorization decision point applies as a ceiling (R-AUTHZ-013), and the
// validator the authentication stage calls.
//
// The scope types live here, below package authz, so that authz imports them
// without importing the authentication stage. The format, storage (SHA-256 of
// the 57-character string, never the value), the cache auth_tokens, rotation,
// revocation and expiry notices belong to work package WP1.2.
package token
