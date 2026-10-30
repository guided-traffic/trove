// Package authz is the decision point (R-AUTHZ-001 to R-AUTHZ-032): deny by
// default, the principal set of a request, repository actions on a coordinate,
// system capabilities, token scope and endpoint exposure as ceilings, and the
// denial semantics of R-AUTHZ-016.
//
// Two entry points exist and nothing else decides:
//
//   - [Decider.DecideOn] ([Authorizer.Decide] for a job) for a repository action
//     on a coordinate;
//   - [Decider.RequireOn] ([Authorizer.Require] for a job) for a system
//     capability of a management operation.
//
// A request takes its index once with [Decider.Begin] and asks every question of
// it, so all of them are answered on one index. Every question takes a [Subject],
// which authz builds nothing of itself: internal/api/edge converts the request's
// httpserver.Principal and the listener's endpoint (this package is below the
// edge layer and cannot import it), and the architecture test refuses a Subject
// built anywhere else, because the endpoint it carries selects the exposure
// ceiling of every decision. Evaluation reads the compiled grant index in
// memory and never queries PostgreSQL or Valkey in the steady state
// (R-AUTHZ-015, R-AUTHZ-028).
//
// The evaluator, the policy store, the index with its invalidation on the
// change area authz, and the decision audit belong to work package WP1.3. The
// exposure ceiling (R-EP-013) is compiled into the same index by WP1.9.
package authz
