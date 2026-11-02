//go:build integration && ci_pending

package ci

// The acceptance criteria of R-CI-001 and R-CI-002 that need a work package that is not built yet. They
// are written and compile under the build tag ci_pending, so they are not run by `make test-integration`
// and are never skipped silently: the list below is the one the contract page tracks
// (docs/developer/ci-oidc.md "Pending criteria"). The work package that makes a test pass removes the tag
// from it (moves it to a file of its own) in the change that builds it. W5 moved the revocation criteria
// of R-CI-001 and R-CI-002 to revoke_test.go, with tokens issued through IssueCITx.
//
//	Test                                                       Needs
//	TestR_CI_001_ExchangeAnswersIssuerDisabledForARemovedIssuer W4
//	TestR_CI_009_AMergeRequestPipelineOfAProtectedBranchCannotPublish               W4, W5
//	TestR_CI_009_AnAbsentClaimFailsTheExchangeEvenIfALaterMappingMatches            W4
//	TestR_CI_019_AMappingCreatedOnAIsUsedByTheNextExchangeOnBWithPropagationBlocked W4
//	TestR_CI_017_DisablingAMappingRevokesItsTokensOnEveryReplica                    W4, W5
//	TestR_CI_009_DeletingAMappingRevokesItsTokens                                   W4, W5
//	TestR_CI_013_RemovingTheMappingFromAPolicyFailsAnIssuedToken                    W4, W5

import "testing"

// R-CI-001: an exchange with an ID token of a removed issuer answers issuer_disabled.
func TestR_CI_001_ExchangeAnswersIssuerDisabledForARemovedIssuer(t *testing.T) {
	t.Skip("pending W4: the exchange is not built")
}

// The criteria of the mappings (work package W3: R-CI-009, R-CI-013, R-CI-017, R-CI-019) that need the exchange
// (W4) or the use-time evaluator and the revoker (W5). The parts of these criteria that W3 owns (the first
// match, the render failure, the cache of the mapping set, the dry run) are tested without the exchange in
// mappings_test.go and dryrun_test.go.

// R-CI-009: a merge request pipeline whose source branch is protected matches p10 and p20 and gets only p10's
// grants; its `npm publish` gets 403.
func TestR_CI_009_AMergeRequestPipelineOfAProtectedBranchCannotPublish(t *testing.T) {
	t.Skip("pending W4 and W5: the exchange issues the token and the evaluator judges its publish")
}

// R-CI-009: a matched mapping whose template references an absent claim fails the exchange with
// grant_render_failed, even if a later mapping would match.
func TestR_CI_009_AnAbsentClaimFailsTheExchangeEvenIfALaterMappingMatches(t *testing.T) {
	t.Skip("pending W4: the exchange is not built")
}

// R-CI-019: a mapping created through replica A is used by the next exchange on replica B, also while B's
// change-log poll and Valkey subscription are blocked (the mapping-set check of R-CI-007).
func TestR_CI_019_AMappingCreatedOnAIsUsedByTheNextExchangeOnBWithPropagationBlocked(t *testing.T) {
	t.Skip("pending W4: the mapping-set check is the exchange's")
}

// R-CI-017: disabling a mapping through replica A makes its unexpired tokens fail with 401 on replicas B and C
// within 2 s, with Valkey running and with Valkey stopped; a token that escaped the sweep is refused with
// principal_disabled.
func TestR_CI_017_DisablingAMappingRevokesItsTokensOnEveryReplica(t *testing.T) {
	t.Skip("pending W4 and W5: no exchange issues a token and no revoker revokes it yet")
}

// R-CI-009: deleting a mapping revokes its unexpired tokens in the same transaction.
func TestR_CI_009_DeletingAMappingRevokesItsTokens(t *testing.T) {
	t.Skip("pending W4 and W5: no exchange issues a token and no revoker revokes it yet")
}

// R-CI-013: removing the mapping from the policy makes the next `npm install` with an already issued token fail
// with 403, without a new exchange.
func TestR_CI_013_RemovingTheMappingFromAPolicyFailsAnIssuedToken(t *testing.T) {
	t.Skip("pending W4 and W5: the evaluator reads the policy bindings of the token's mappings")
}
