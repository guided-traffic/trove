//go:build integration && ci_pending

package ci

// The acceptance criteria of R-CI-001 and R-CI-002 that need a work package that is not built when W1
// lands. They are written and compile under the build tag ci_pending, so they are not run by
// `make test-integration` and are never skipped silently: the list below is the one the contract page
// tracks (docs/developer/ci-oidc.md "Pending criteria"). The work package that makes a test pass removes
// the tag from it (moves it to a file of its own) in the change that builds it.
//
//	Test                                                       Needs
//	TestR_CI_002_DisablingAnIssuerRevokesItsTokens              W5 (Revoker.RevokeIssuerTx), W4 (the exchange issues tokens)
//	TestR_CI_002_DeletingAnIssuerRevokesItsTokens               W5, W4
//	TestR_CI_002_CascadeDeletesTheMappingsAndTheirPrincipals    W3 (Mappings.DeleteForIssuerTx)
//	TestR_CI_001_ARemovedConfigurationIssuerKeepsItsRecords     W5 (the disable of an issuer with issuance records revokes)
//	TestR_CI_001_ExchangeAnswersIssuerDisabledForARemovedIssuer W4

import (
	"testing"

	"github.com/guided-traffic/trove/test/oidcstub"
)

// R-CI-002: after a PUT with enabled false and the current If-Match, a CI token issued earlier through
// the issuer gets 401 on every web replica within R-AUTH-023's bound of 2 s, with Valkey running or stopped.
func TestR_CI_002_DisablingAnIssuerRevokesItsTokens(t *testing.T) {
	for _, valkey := range []bool{true, false} {
		d := startDouble(t, oidcstub.Config{})
		e := newEnv(t, options{valkey: valkey})
		a, b := e.web("web-a"), e.web("web-b")
		wantStatus(t, e.do(a, "POST", issuersPath, d.body("gitlab-main")), 201, "")
		// W4: exchange a minted ID token through a mapping (W3) for a CI token and use it on both replicas.
		t.Skip("pending W3, W4 and W5: no exchange issues a token yet") // listed in the header, tracked on the contract page
		_, _ = a, b
	}
}

// R-CI-002: DELETE revokes the unexpired tokens of the issuer in the same transaction.
func TestR_CI_002_DeletingAnIssuerRevokesItsTokens(t *testing.T) {
	t.Skip("pending W4 and W5: no exchange issues a token yet")
}

// R-CI-002: ?cascade=true deletes the mappings and their principals in the transaction, one
// ci.mapping.deleted each.
func TestR_CI_002_CascadeDeletesTheMappingsAndTheirPrincipals(t *testing.T) {
	d := startDouble(t, oidcstub.Config{})
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	wantStatus(t, e.do(a, "POST", issuersPath, d.body("gitlab-main")), 201, "")
	e.seedMapping("gitlab-main", "publish")
	wantStatus(t, e.do(a, "DELETE", issuersPath+"/gitlab-main?cascade=true", nil), 204, "")
	if e.number("SELECT count(*) FROM ci_mappings") != 0 || e.number("SELECT count(*) FROM principal WHERE kind = 'ci_mapping'") != 0 {
		t.Error("the mappings or their principals remain")
	}
	if e.events("ci.mapping.deleted") != 1 || e.events("ci.issuer.deleted") != 1 {
		t.Errorf("events: %d mapping deletions, %d issuer deletions", e.events("ci.mapping.deleted"), e.events("ci.issuer.deleted"))
	}
}

// R-CI-001: removed from ci.issuers, the next trove migrate disables the issuer and its issuance
// records stay.
func TestR_CI_001_ARemovedConfigurationIssuerKeepsItsRecords(t *testing.T) {
	d := startDouble(t, oidcstub.Config{})
	e := newEnv(t, options{valkey: true})
	if err := e.declare(declared(d, "gitlab-main")); err != nil {
		t.Fatal(err)
	}
	e.seedIssuance("gitlab-main")
	if err := e.declare(); err != nil {
		t.Fatal(err)
	}
	if e.number("SELECT count(*) FROM ci_token_issuances") != 1 || e.number("SELECT count(*) FROM ci_issuers WHERE NOT enabled AND removed_from_configuration") != 1 {
		t.Error("the issuer is not disabled or its records are gone")
	}
}

// R-CI-001: an exchange with an ID token of a removed issuer answers issuer_disabled.
func TestR_CI_001_ExchangeAnswersIssuerDisabledForARemovedIssuer(t *testing.T) {
	t.Skip("pending W4: the exchange is not built")
}
