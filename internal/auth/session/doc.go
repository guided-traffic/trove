// Package session is the UI session contract (R-AUTH-010 to R-AUTH-013): the
// session cookie __Host-trv_session, the session row in PostgreSQL, the CSRF
// check and logout. The cookie holds an opaque secret whose SHA-256 is stored;
// a session lives in the ui_session table, not in a replica (R-AUTH-042).
//
// The implementation belongs to work package WP1.1. The authentication stage in
// package auth calls only [Validator].
package session
