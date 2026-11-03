//go:build integration

package ci

// The revocation of CI tokens (R-CI-017, R-CI-020, R-AUTH-029, R-CI-002's revocation through an issuer): every cause
// ends the tokens in the transaction that decides it, records the change of the area auth, so every replica
// refuses them within R-AUTH-023's bound of 2 s whatever Valkey does, and writes ci.token.revoked with the cause and
// its journal entry. The tokens are issued with IssueCITx (use_test.go); the mapping store is W3's, so a mapping's
// revocation is driven here through the revoker the store calls, in a transaction of the replica.

import (
	"fmt"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/ci"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/internal/meta"
	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/oidcstub"
)

// revocationBound is auth.revocation_bound, the default of R-AUTH-023.
const revocationBound = 2 * time.Second

// alive is the status of GET /api/v1/tokens/current with the token: 200 while it is valid, 401 once revoked.
func (e *ciEnv) alive(r *testenv.Replica, secret string) int {
	e.t.Helper()
	return e.stack.Do(r, "GET", "/api/v1/tokens/current", secret, nil).Status
}

// refusedEverywhere waits until every replica answers 401 for the token and fails when that took longer than the
// revocation bound after start.
func (e *ciEnv) refusedEverywhere(t *testing.T, start time.Time, secret string, reps ...*testenv.Replica) {
	t.Helper()
	for _, r := range reps {
		within(t, r.Name+" refusing the revoked token", revocationBound+3*time.Second, func() bool { return e.alive(r, secret) == 401 })
	}
	if took := time.Since(start); took > revocationBound {
		t.Errorf("refused on every replica after %s, bound %s", took, revocationBound)
	}
}

// ciEnvOf adds the CI fixture to an installation whose issuer the API created.
func ciEnvOf(e *env, issuer string) *ciEnv {
	return &ciEnv{env: e, issuerID: e.text("SELECT id::text FROM ci_issuers WHERE name = $1", issuer), maps: map[string]ciMapping{}}
}

// R-CI-017: a revocation through replica A is in force on B and C within 2 s, with Valkey running and with Valkey
// stopped, for the bulk revoke, a mapping's revocation (the call the mapping store makes when a mapping is disabled
// or deleted) and an administrator's revocation of one token.
func TestR_CI_017_ARevocationThroughOneReplicaIsRefusedOnTheOthersWithin2s(t *testing.T) {
	for _, valkey := range []bool{true, false} {
		t.Run(fmt.Sprintf("valkey %v", valkey), func(t *testing.T) {
			e := newCIEnv(t, valkey, "acme-release", "acme-read")
			rel, read := e.maps["acme-release"], e.maps["acme-read"]
			a, b, c := e.web("web-a"), e.web("web-b"), e.web("web-c")
			use := func(m ciMapping, j job, extra ...claims.FrozenMapping) ciToken {
				tok := e.issue(a, e.bundle("4711", "acme/web/frontend", append([]claims.FrozenMapping{m.contribution(nil)}, extra...)...), j, 10*time.Minute)
				for _, r := range []*testenv.Replica{b, c} {
					if s := e.alive(r, tok.secret); s != 200 {
						t.Fatalf("%s answered %d to a fresh token", r.Name, s)
					}
				}
				return tok
			}

			t.Run("R-CI-020 the bulk revoke", func(t *testing.T) {
				tok, other := use(rel, job{"700", "1"}), use(rel, job{"701", "2"})
				start := time.Now()
				r := e.do(a, "POST", "/api/v1/ci/issuances/revoke", map[string]any{"issuer": ciIssuerName, "pipelineId": "700"})
				wantStatus(t, r, 200, "")
				if n := r.JSON(t)["revoked"]; n != float64(1) {
					t.Fatalf("revoked %v", n)
				}
				e.refusedEverywhere(t, start, tok.secret, b, c)
				if e.alive(b, other.secret) != 200 {
					t.Error("the token of another pipeline was revoked")
				}
			})

			// A system mapping (Q-CI-47): its disable ends the tokens it owns and those it contributes to; a project mapping's
			// ends only the tokens it owns (tiers_test.go).
			t.Run("R-CI-017 disabling a system mapping ends its tokens and those it contributes to", func(t *testing.T) {
				owned, contributed, other := use(read, job{"800", "3"}), use(rel, job{"801", "4"}, read.contribution(nil)), use(rel, job{"802", "5"})
				start := time.Now()
				_, db := a.App.CITokenIssuer()
				var done ci.Revoked
				err := db.InTx(t.Context(), func(tx meta.Tx) error {
					var err error
					done, err = a.App.CIRevoker().RevokeMappingTx(t.Context(), tx, ci.Actor{Kind: "user", ID: e.bobID, Name: "bob"},
						ci.Mapping{ID: read.id, PrincipalID: read.principalID, IssuerID: e.issuerID, Issuer: ciIssuerName, Name: read.name, Tier: ci.TierSystem}, ci.CauseMapping)
					return err
				})
				if err != nil || len(done.TokenIDs) != 2 {
					t.Fatalf("revoked %v, %v", done.TokenIDs, err)
				}
				e.refusedEverywhere(t, start, owned.secret, b, c)
				e.refusedEverywhere(t, start, contributed.secret, b, c)
				if e.alive(c, other.secret) != 200 {
					t.Error("a token the mapping does not contribute to was revoked")
				}
				if got := e.text(`SELECT revocation_reason FROM api_token WHERE id = $1::uuid`, owned.id); got != "mapping_deleted" {
					t.Errorf("reason %q", got)
				}
				if got := e.text(`SELECT (details->>'mapping') || ' ' || (details->>'cause') FROM audit_event WHERE action = 'ci.token.revoked' AND details->>'cause' = 'mapping'`); got != "acme-read mapping" {
					t.Errorf("event %q", got)
				}
			})

			t.Run("R-AUTH-029 an administrator revokes one CI token by ID", func(t *testing.T) {
				tok := use(rel, job{"900", "6"})
				start := time.Now()
				wantStatus(t, e.do(a, "DELETE", "/api/v1/tokens/"+tok.id, nil), 204, "")
				e.refusedEverywhere(t, start, tok.secret, b, c)
				if n := e.number(`SELECT count(*) FROM audit_event WHERE action = 'ci.token.revoked' AND details->>'cause' = 'single' AND details->'tokenIds' ? $1`, tok.id); n != 1 {
					t.Errorf("%d single events", n)
				}
			})
		})
	}
}

// R-CI-017, R-AUTH-029: DELETE /api/v1/tokens/current with a CI token answers 204, the token gets 401 on the next
// npm view, and the audit trail shows token.revoked with reason owner and ci.token.revoked with cause self.
func TestR_CI_017_DeleteTokensCurrentEndsTheJobsToken(t *testing.T) {
	e := newCIEnv(t, false, "acme-release")
	m := e.maps["acme-release"]
	ops := e.stack.User("publisher")
	e.stack.Policy("publishers", []string{"read", "deploy"}, []string{"npm-internal"}, ops)
	a, b := e.web("web-a"), e.web("web-b")
	tok := e.issue(a, e.bundle("4711", "acme/web/frontend", m.contribution(nil, grant([]string{"npm-internal"}, []string{"@acme/*"}, claims.Read, claims.Deploy))), job{"1", "2"}, 10*time.Minute)
	mustStatus(t, "publish", e.publish(a, "npm-internal", tok.secret, "@acme/frontend", "1.0.0"), 201)
	mustStatus(t, "npm view before", e.install(b, "npm-internal", tok.secret, "@acme/frontend"), 200)
	current := e.stack.Do(a, "GET", "/api/v1/tokens/current", tok.secret, nil)
	mustStatus(t, "GET current", current, 200)
	if current.JSON(t)["type"] != "cit" {
		t.Errorf("current %s", current.Body)
	}
	start := time.Now()
	mustStatus(t, "DELETE current", e.stack.Do(a, "DELETE", "/api/v1/tokens/current", tok.secret, nil), 204)
	within(t, "b refusing npm view", revocationBound+3*time.Second, func() bool { return e.install(b, "npm-internal", tok.secret, "@acme/frontend").Status == 401 })
	if took := time.Since(start); took > revocationBound {
		t.Errorf("refused after %s", took)
	}
	if got := e.text(`SELECT reason FROM audit_event WHERE action = 'token.revoked' AND resource_id = $1`, tok.id); got != "owner" {
		t.Errorf("token.revoked reason %q", got)
	}
	if n := e.number(`SELECT count(*) FROM audit_event WHERE action = 'ci.token.revoked' AND details->>'cause' = 'self' AND details->'tokenIds' ? $1`, tok.id); n != 1 {
		t.Errorf("%d self events", n)
	}
	if n := e.number(`SELECT count(*) FROM revocation_outbox WHERE event = 'ci.token.revoked' AND entry->'tokenIds' ? $1 AND entry->>'cause' = 'self'`, tok.id); n != 1 {
		t.Errorf("%d journal entries", n)
	}
}

// R-CI-002: after a PUT with enabled false and the current If-Match, a CI token issued earlier through the issuer
// gets 401 on every web replica within R-AUTH-023's bound of 2 s, with Valkey running or stopped. The token is issued
// with IssueCITx instead of an exchange (W4).
func TestR_CI_002_DisablingAnIssuerRevokesItsTokens(t *testing.T) {
	for _, valkey := range []bool{true, false} {
		t.Run(fmt.Sprintf("valkey %v", valkey), func(t *testing.T) {
			d := startDouble(t, oidcstub.Config{})
			base := newEnv(t, options{valkey: valkey})
			a, b := base.web("web-a"), base.web("web-b")
			wantStatus(t, base.do(a, "POST", issuersPath, d.body("gitlab-main")), 201, "")
			e := ciEnvOf(base, "gitlab-main")
			m := e.ciMappingRow("acme-release", 1)
			e.maps[m.name] = m
			tok := e.issue(a, e.bundle("4711", "acme/web/frontend", m.contribution(nil)), job{"1", "2"}, 10*time.Minute)
			if e.alive(b, tok.secret) != 200 {
				t.Fatal("the token does not work")
			}
			got := e.do(a, "GET", issuersPath+"/gitlab-main", nil)
			body := got.JSON(t)
			body["enabled"] = false
			start := time.Now()
			wantStatus(t, e.do(a, "PUT", issuersPath+"/gitlab-main", body, "If-Match", etag(got)), 200, "")
			e.refusedEverywhere(t, start, tok.secret, a, b)
			if n := e.number(`SELECT count(*) FROM audit_event WHERE action = 'ci.token.revoked' AND details->>'cause' = 'issuer' AND details->>'issuer' = 'gitlab-main'`); n != 1 {
				t.Errorf("%d issuer events", n)
			}
		})
	}
}

// R-CI-002: DELETE revokes the unexpired tokens of the issuer in the same transaction. The token's owner is a
// mapping principal without a mapping row, so the delete needs no cascade (W3 builds the cascade).
func TestR_CI_002_DeletingAnIssuerRevokesItsTokens(t *testing.T) {
	d := startDouble(t, oidcstub.Config{})
	base := newEnv(t, options{valkey: true})
	a, b := base.web("web-a"), base.web("web-b")
	wantStatus(t, base.do(a, "POST", issuersPath, d.body("gitlab-main")), 201, "")
	e := ciEnvOf(base, "gitlab-main")
	pid := e.text(`INSERT INTO principal (id, kind, name) VALUES (gen_random_uuid(), 'ci_mapping', 'gitlab-main/acme-release') RETURNING id::text`)
	m := ciMapping{id: "0190a4c2-0000-7000-8000-00000000aaaa", principalID: pid, name: "acme-release"}
	e.maps[m.name] = m
	tok := e.issue(a, e.bundle("4711", "acme/web/frontend", m.contribution(nil)), job{"1", "2"}, 10*time.Minute)
	start := time.Now()
	wantStatus(t, e.do(a, "DELETE", issuersPath+"/gitlab-main", nil), 204, "")
	e.refusedEverywhere(t, start, tok.secret, a, b)
	if got := e.text(`SELECT revocation_reason FROM api_token WHERE id = $1::uuid`, tok.id); got != "admin" {
		t.Errorf("reason %q", got)
	}
}

// R-CI-002: DELETE with ?cascade=true revokes the issuer's tokens before its mappings and their principals go. A
// token row belongs to its mapping's principal (ON DELETE CASCADE): deleting the principals first would take the rows
// with them without a ci.token.revoked event, a journal entry or a change of the area auth, and a replica's cached token
// would stay usable beyond R-AUTH-023's bound.
func TestR_CI_002_ACascadeDeleteRevokesTheTokensBeforeTheMappingsGo(t *testing.T) {
	d := startDouble(t, oidcstub.Config{})
	base := newEnv(t, options{valkey: true})
	a, b := base.web("web-a"), base.web("web-b")
	wantStatus(t, base.do(a, "POST", issuersPath, d.body(ciIssuerName)), 201, "")
	base.seedMapping(ciIssuerName, "acme-release")
	e := ciEnvOf(base, ciIssuerName)
	m := ciMapping{
		id:          e.text(`SELECT id::text FROM ci_mappings WHERE name = 'acme-release'`),
		principalID: e.text(`SELECT principal_id::text FROM ci_mappings WHERE name = 'acme-release'`),
		name:        "acme-release",
	}
	e.maps[m.name] = m
	tok := e.issue(a, e.bundle("4711", "acme/web/frontend", m.contribution(nil)), job{"1", "2"}, 10*time.Minute)
	if got := e.alive(b, tok.secret); got != 200 {
		t.Fatalf("the token before the deletion: %d", got)
	}
	start := time.Now()
	wantStatus(t, e.do(a, "DELETE", issuersPath+"/"+ciIssuerName+"?cascade=true", nil), 204, "")
	e.refusedEverywhere(t, start, tok.secret, a, b)
	if n := e.events("ci.token.revoked"); n != 1 {
		t.Errorf("%d ci.token.revoked events, want 1", n)
	}
}

// R-CI-001: removed from ci.issuers, the next trove migrate disables the issuer and its issuance records stay; the
// tokens it issued are revoked in that transaction.
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
