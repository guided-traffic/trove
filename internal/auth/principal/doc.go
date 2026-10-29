// Package principal is the store contract of the principal table: users, groups
// and service accounts, their status and their group memberships (R-AUTH-016,
// R-AUTH-018, R-AUTH-020, R-AUTHZ-002). Policies, role bindings and owner
// records reference a principal by its immutable ID, never by name.
//
// It sits at the bottom of the auth tree: token, session, oidc, the
// authentication stage in package auth and package authz all import it, and it
// imports none of them. It also holds the implementation: queries, the cache
// auth_principals, the change area auth, the provisioner of the login and the
// manager behind the management API.
package principal
