// Package ci is the domain of the GitLab CI OIDC federation (docs/spec/gitlab-ci-oidc.md, beta
// phase 5): CI issuers and their signing keys, identity mappings, the token exchange, the issuance
// records and the revocation of CI tokens.
//
// This file set is the contract of the work packages of docs/developer/ci-oidc.md. It holds the
// domain types (types.go, issuance.go, exchange.go), the service interfaces that the work packages
// implement (services.go) and the typed not-implemented answer of the stubs the composition root
// wires until they are built (stubs.go). The pure parts live in two leaves below it:
//
//   - internal/ci/claims: the condition tree, glob, templates, the grant specification and its
//     rendering, the scope grammar of the exchange and the save-time rules of a mapping (W2).
//   - internal/ci/subject: the validation of a subject token over the steps of R-CI-004 (W2).
//   - internal/ci/reason: the reason names of R-CI-008 and the management codes, shared by all.
//
// What a CI token carries to its use, and where the authorization evaluator reads it, is the seam of
// Q-CI-17: [claims.Frozen] is written to api_token.ci_context by the exchange's transaction and
// travels with the token cache entry to authz.Subject.CI; no use-time request queries PostgreSQL for
// it (R-AUTHZ-015).
package ci
