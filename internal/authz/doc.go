// Package authz is the decision point (R-AUTHZ-001 to R-AUTHZ-032): deny by
// default, the principal set of a request, repository actions on a coordinate,
// system capabilities, token scope and endpoint exposure as ceilings, and the
// denial semantics of R-AUTHZ-016.
//
// Two entry points exist and nothing else decides:
//
//   - [Authorizer.Decide] for a repository action on a coordinate;
//   - [Authorizer.Require] for a system capability of a management operation.
//
// Both take a [Subject], which authz builds nothing of itself: internal/api/v1
// converts the request's httpserver.Principal (this package is below the edge
// layer and cannot import it). Evaluation reads the compiled grant index in
// memory and never queries PostgreSQL or Valkey in the steady state
// (R-AUTHZ-015, R-AUTHZ-028).
//
// The evaluator, the policy store, the index with its invalidation on the
// change area authz, and the decision audit belong to work package WP1.3. The
// exposure ceiling (R-EP-013) is compiled into the same index by WP1.9.
package authz
