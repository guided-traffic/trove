// Package claims is the pure claim engine of the CI exchange (R-CI-010 to R-CI-014): normalized
// claims, the condition tree, glob and regular-expression matching, the template language, the grant
// specification and its rendering, the scope request grammar of the exchange (R-CI-007, Q-CI-2), the
// cross-project classification (R-CI-012) and the save-time rules of a mapping.
//
// It does no I/O and holds no state: every function is a function of its arguments, so the exchange,
// the dry run (R-CI-019) and the save of a mapping evaluate one implementation. It is a leaf
// (internal/archtest/layers.go): the domain packages internal/ci, internal/authz and
// internal/auth/token import it, and it imports only the standard library and internal/ci/reason.
//
// The JSON forms of its types (conditions, grants, options) are the wire forms of the management API
// in lowerCamelCase (R-CI-002) and also the stored forms of ci_mappings.match, .grants and .options,
// so a mapping read from PostgreSQL decodes with the same types the API decodes a request with.
//
// The bodies are stubs until work package W2 of docs/developer/ci-oidc.md builds them; a stub
// panics with "not implemented: W2". The types, the signatures and the doc comments are the contract
// the other work packages build against.
package claims
