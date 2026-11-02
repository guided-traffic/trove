// Package claims is the pure claim engine of the CI exchange (R-CI-010 to R-CI-014): normalized
// claims, the condition tree, glob and regular-expression matching, the template language, the grant
// specification and its rendering, the scope request grammar of the exchange (R-CI-007, Q-CI-2), the
// cross-project classification (R-CI-012), the save-time rules of a mapping and the frozen bundle a
// CI token carries (Q-CI-17).
//
// It does no I/O: every function is a function of its arguments, so the exchange, the dry run
// (R-CI-019) and the save of a mapping evaluate one implementation, and it holds no state
// (R-ARCH-032; a regular expression is compiled at save and again per evaluation). It is a leaf
// (internal/archtest/layers.go): the domain packages internal/ci, internal/authz and
// internal/auth/token import it, and it imports only the standard library and internal/ci/reason.
//
// The JSON forms of its types (conditions, grants, options) are the wire forms of the management API
// in lowerCamelCase (R-CI-002) and also the stored forms of ci_mappings.match, .grants and .options,
// so a mapping read from PostgreSQL decodes with the same types the API decodes a request with; a
// [Node] decodes through [ParseCondition] wherever it is decoded.
//
// Where the files are: claims.go (normalization, CrossProject, Snapshot), condition.go (the tree,
// its parse and its limits), evaluate.go (Evaluate and the trace), glob.go, regex.go, template.go,
// grants.go (Spec, Render, ApplyCaps, Frozen), scope.go (ParseScope, Narrow, FormatScope, Freeze),
// selector.go (the selector syntax of R-AUTHZ-004), check.go (Check and the warnings).
package claims
