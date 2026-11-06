//go:build integration

package ci

// The criteria of the mappings, the issuers and the use time that needed the exchange (formerly pending_test.go
// behind ci_pending): R-CI-007's re-check against a replica whose copies lag (R-CI-019's mapping created on A), R-CI-009's
// first match per tier and its render failure, R-CI-012's fork pipelines, R-CI-013's policy binding at use time,
// R-CI-017's revocation of exchanged tokens, R-CI-001's removed issuer, R-CI-020's issuance record of an exchange.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/oidcstub"
)

// mrClaims are the claims of a merge request pipeline of the default project whose source branch is protected
// (R-CI-012: ref is the source branch, ref_protected describes it).
func mrClaims() map[string]any {
	return map[string]any{
		"pipeline_source": "merge_request_event", "job_source": "merge_request_event", "ref": "release-1", "ref_type": "branch",
		"ref_path": "refs/heads/release-1", "ref_protected": "true",
	}
}

// protectedPublish is R-CI-009's p20: protected refs of namespace 4711 publish their npm scope.
func protectedPublish(name string, priority int) map[string]any {
	return mapping(name, priority, all(eq("namespace_id", "4711"), eq("ref_protected", true)),
		grants(inline("it/npm-internal", []string{"@{{ namespace_path | slug }}/*"}, "read", "deploy")))
}

// mrReadOnly is R-CI-009's p10: merge request pipelines of namespace 4711 only read.
func mrReadOnly(name string, priority int) map[string]any {
	return mapping(name, priority, all(eq("namespace_id", "4711"), eq("pipeline_source", "merge_request_event")),
		grants(inline("it/npm-internal", []string{"@{{ namespace_path | slug }}/*"}, "read")))
}

// TestR_CI_007_AReplicaWithALaggingCopyIssuesWhatIsCommitted is R-CI-007's three criteria with replica B's change-log
// poll and its Valkey subscription blocked (Valkey stopped, B's change log stopped): a mapping created on A that
// shadows a broader one (R-CI-019) and a disabled mapping. The audience removed from the issuer has its own test below.
func TestR_CI_007_AReplicaWithALaggingCopyIssuesWhatIsCommitted(t *testing.T) {
	x := newXEnv(t, options{valkey: false})
	a, b := x.web("web-a"), x.web("web-b")
	x.issuer(a, map[string]any{"allowedAudiences": []string{troveAudience, "https://vault.example.com"}})
	x.addMapping(a, protectedPublish("acme-protected-publish", 100))
	x.addMapping(a, releaseMapping("acme-release-publish", 50))
	// The copies on B hold the issuer and this mapping set before B stops listening.
	wantToken(t, x.exchange(b, x.mint(oidcstub.MintOptions{Claims: mrClaims()})))
	wantToken(t, x.exchange(b, x.mint(oidcstub.MintOptions{Claims: map[string]any{"aud": "https://vault.example.com"}})))
	if err := b.App.Changes().Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	stale := func() float64 {
		return metric(t, b, fmt.Sprintf(`trove_ci_mapping_set_stale_total{issuer=%q}`, xIssuer))
	}

	t.Run("R-CI-019 a mapping created on A at priority 10 governs the next exchange on B: the merge request pipeline gets the read-only grants", func(t *testing.T) {
		before := stale()
		wantStatus(t, x.do(a, "POST", mappingsOf(xIssuer), mrReadOnly("acme-mr-readonly", 10)), 201, "")
		g := wantToken(t, x.exchange(b, x.mint(oidcstub.MintOptions{Claims: mrClaims()})))
		if len(g.Mappings) != 1 || g.Mappings[0] != "acme-mr-readonly" || g.Scope != "read:npm-internal" {
			t.Errorf("B issued %v with scope %q", g.Mappings, g.Scope)
		}
		if got := stale() - before; got != 1 {
			t.Errorf("trove_ci_mapping_set_stale_total counted %v reloads on B, want 1", got)
		}
	})

	t.Run("R-CI-007 a mapping disabled on A issues no token with its grants on B", func(t *testing.T) {
		g := x.do(a, "GET", mappingsOf(xIssuer)+"/acme-release-publish", nil)
		body := g.JSON(t)
		body["enabled"] = false
		wantStatus(t, x.do(a, "PUT", mappingsOf(xIssuer)+"/acme-release-publish", body, "If-Match", etag(g)), 200, "")
		// The protected tag now falls through to the later protected-ref mapping, never the disabled one.
		got := x.exchange(b, x.mint(oidcstub.MintOptions{}))
		if got.Status == 200 {
			if tok := wantToken(t, got); len(tok.Mappings) != 1 || tok.Mappings[0] != "acme-protected-publish" {
				t.Errorf("B issued %v", tok.Mappings)
			}
		} else {
			wantOAuth(t, got, 400, "invalid_request", "no_matching_mapping")
		}
	})
}

// TestR_CI_007_AnAudienceRemovedOnAIsRefusedOnBFromTheRowReadInTheTransaction is R-CI-007's third criterion on its own:
// B holds the issuer's copy that still allows the second audience (a first exchange loaded it), B's change-log poll is
// stopped and Valkey is down, so nothing but the exchange's own transaction can tell B that the audience is gone. The
// refusal therefore comes from the issuer row B re-reads under FOR SHARE and validates the held subject token against
// again (R-CI-004 steps 5 to 9); the stale counter shows that this path ran, and no token is issued.
func TestR_CI_007_AnAudienceRemovedOnAIsRefusedOnBFromTheRowReadInTheTransaction(t *testing.T) {
	x := newXEnv(t, options{valkey: false})
	a, b := x.web("web-a"), x.web("web-b")
	x.issuer(a, map[string]any{"allowedAudiences": []string{troveAudience, "https://vault.example.com"}})
	x.addMapping(a, releaseMapping("acme-release-publish", 50))
	vault := map[string]any{"aud": "https://vault.example.com"}
	// B's copy of the issuer is loaded by a first exchange with the second audience, and nothing reloads it after.
	wantToken(t, x.exchange(b, x.mint(oidcstub.MintOptions{Claims: vault})))
	if err := b.App.Changes().Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	stale := func() float64 {
		return metric(t, b, fmt.Sprintf(`trove_ci_mapping_set_stale_total{issuer=%q}`, xIssuer))
	}
	g := x.do(a, "GET", issuersPath+"/"+xIssuer, nil)
	body := g.JSON(t)
	body["allowedAudiences"] = []string{troveAudience}
	wantStatus(t, x.do(a, "PUT", issuersPath+"/"+xIssuer, body, "If-Match", etag(g)), 200, "")

	before, tokens := stale(), x.number("SELECT count(*) FROM api_token WHERE type = 'cit'")
	wantOAuth(t, x.exchange(b, x.mint(oidcstub.MintOptions{Claims: vault})), 400, "invalid_request", "audience_invalid")
	if got := stale() - before; got != 1 {
		t.Errorf("the transaction re-validated %v times on B, want 1: the refusal did not come from the re-read row", got)
	}
	if n := x.number("SELECT count(*) FROM api_token WHERE type = 'cit'"); n != tokens {
		t.Errorf("a token was issued: %d -> %d", tokens, n)
	}
}

func TestR_CI_009_AMergeRequestPipelineOfAProtectedBranchCannotPublish(t *testing.T) {
	x := newXEnv(t, options{valkey: true})
	a := x.web("web-a")
	x.issuer(a)
	x.addMapping(a, mrReadOnly("p10", 10))
	x.addMapping(a, protectedPublish("p20", 20))
	t.Run("R-CI-009 a push on a protected tag gets p20's grants and publishes", func(t *testing.T) {
		g := wantToken(t, x.exchange(a, x.mint(oidcstub.MintOptions{})))
		if g.Mappings[0] != "p20" {
			t.Fatalf("mappings %v", g.Mappings)
		}
		pub := x.stack.Do(a, "PUT", npmPath("npm-internal", "@acme-billing/invoice-client"), g.Token, npmBody(t, "@acme-billing/invoice-client", "1.0.0"))
		if pub.Status != 200 && pub.Status != 201 {
			t.Errorf("publish: %d %s", pub.Status, pub.Body)
		}
	})
	t.Run("R-CI-009 a merge request pipeline whose source branch is protected matches both and gets only p10's: its publish is 403", func(t *testing.T) {
		g := wantToken(t, x.exchange(a, x.mint(oidcstub.MintOptions{Claims: mrClaims()})))
		if len(g.Mappings) != 1 || g.Mappings[0] != "p10" || g.Scope != "read:npm-internal" {
			t.Fatalf("mappings %v, scope %q", g.Mappings, g.Scope)
		}
		if pub := x.stack.Do(a, "PUT", npmPath("npm-internal", "@acme-billing/invoice-client"), g.Token, npmBody(t, "@acme-billing/invoice-client", "1.0.1")); pub.Status != 403 {
			t.Errorf("publish of the merge request pipeline: %d", pub.Status)
		}
	})
}

func TestR_CI_009_AnAbsentClaimFailsTheExchangeEvenIfALaterMappingMatches(t *testing.T) {
	x := newXEnv(t, options{valkey: true})
	a := x.web("web-a")
	x.issuer(a)
	x.addMapping(a, mapping("by-environment", 10, all(eq("namespace_id", "4711")),
		grants(inline("it/npm-internal", []string{"@{{ environment | slug }}/*"}, "read"))))
	x.addMapping(a, releaseMapping("acme-release-publish", 100))
	wantToken(t, x.exchange(a, x.mint(oidcstub.MintOptions{})))
	r := x.exchange(a, x.mint(oidcstub.MintOptions{Remove: []string{"environment", "environment_protected", "deployment_tier", "environment_action"}}))
	wantOAuth(t, r, 400, "invalid_request", "grant_render_failed")
	if d := r.JSON(t)["error_description"].(string); !strings.Contains(d, "by-environment") {
		t.Errorf("the description does not name the mapping: %q", d)
	}
}

// forkInParent is the claim set of the recorded fork merge request pipeline run in the parent project
// (test/oidcstub/testdata/id-token-merge-request-fork-in-parent.json), with this double's issuer, trove's audience
// and the times of now.
func (x *xenv) forkInParent(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("../../oidcstub/testdata/id-token-merge-request-fork-in-parent.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Claims map[string]any `json:"claims"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	f.Claims["iss"], f.Claims["aud"] = x.d.stub.Issuer(), troveAudience
	f.Claims["iat"], f.Claims["nbf"], f.Claims["exp"] = now, now-5, now+3600
	b, _ := json.Marshal(f.Claims)
	return b
}

func TestR_CI_012_ForkPipelines(t *testing.T) {
	x := newXEnv(t, options{valkey: true})
	a := x.web("web-a")
	x.issuer(a)
	x.addMapping(a, releaseMapping("acme-release-publish", 100))

	t.Run("R-CI-012 a synthetic token with project_id 900 and job_project_id 42 is cross_project_source_denied", func(t *testing.T) {
		wantOAuth(t, x.exchange(a, x.mint(oidcstub.MintOptions{Claims: map[string]any{"project_id": "900", "job_project_id": "42"}})),
			400, "invalid_request", "cross_project_source_denied")
	})
	t.Run("R-CI-012 with a matched mapping that opts in to read_only, the scope holds only read items", func(t *testing.T) {
		x.addMapping(a, mapping("fork-read", 10, all(eq("job_project_id", "42")),
			grants(inline("it/npm-internal", []string{"@acme-billing/*"}, "read")), map[string]any{"options": map[string]any{"crossProjectSource": "read_only"}}))
		g := wantToken(t, x.exchange(a, x.mint(oidcstub.MintOptions{Claims: map[string]any{"project_id": "900", "job_project_id": "42"}})))
		for _, it := range strings.Fields(g.Scope) {
			if !strings.HasPrefix(it, "read:") {
				t.Errorf("a cross-project token holds %q (scope %q)", it, g.Scope)
			}
		}
		if g.Mappings[0] != "fork-read" {
			t.Errorf("mappings %v", g.Mappings)
		}
	})
	t.Run("R-CI-012 the recorded fork merge request pipeline run in the parent cannot publish to npm-internal", func(t *testing.T) {
		// The parent's mapping pins the project the job runs in, the parent (job_project_id 1), and would publish.
		x.addMapping(a, mapping("parent-publish", 20, all(eq("job_project_id", "1"), eq("ref_protected", false)),
			grants(inline("it/npm-internal", []string{"*"}, "read", "deploy")), map[string]any{"options": map[string]any{"allowUnprotectedRefs": true}}))
		tok := x.mint(oidcstub.MintOptions{RawPayload: x.forkInParent(t)})
		wantOAuth(t, x.exchange(a, tok), 400, "invalid_request", "cross_project_source_denied")
	})
}

func TestR_CI_013_RemovingTheMappingFromAPolicyFailsAnIssuedToken(t *testing.T) {
	x := newXEnv(t, options{valkey: true})
	a := x.web("web-a")
	x.issuer(a)
	x.addMapping(a, releaseMapping("acme-release-publish", 100))
	g := wantToken(t, x.exchange(a, x.mint(oidcstub.MintOptions{})))
	pub := x.stack.Do(a, "PUT", npmPath("npm-internal", "@acme-billing/invoice-client"), g.Token, npmBody(t, "@acme-billing/invoice-client", "1.0.0"))
	if pub.Status != 200 && pub.Status != 201 {
		t.Fatalf("publish: %d", pub.Status)
	}
	if got := x.stack.Do(a, "GET", npmPath("npm-all", "@acme-billing/invoice-client"), g.Token, nil); got.Status != 200 {
		t.Fatalf("install through the policy: %d", got.Status)
	}
	// The mapping's PUT without the policy removes its entry from the policy (Q-CI-35); the token's policy grants
	// are live, so the next install through npm-all fails without a new exchange (Q-CI-39: 404 under hide_existence).
	m := x.do(a, "GET", mappingsOf(xIssuer)+"/acme-release-publish", nil)
	body := m.JSON(t)
	body["grants"].(map[string]any)["policies"] = []string{}
	wantStatus(t, x.do(a, "PUT", mappingsOf(xIssuer)+"/acme-release-publish", body, "If-Match", etag(m)), 200, "")
	x.settled()
	if got := x.stack.Do(a, "GET", npmPath("npm-all", "@acme-billing/invoice-client"), g.Token, nil); got.Status != 404 && got.Status != 403 {
		t.Errorf("install after the removal: %d", got.Status)
	}
	// The frozen inline grant of the same token still reads its own package in npm-internal.
	if got := x.stack.Do(a, "GET", npmPath("npm-internal", "@acme-billing/invoice-client"), g.Token, nil); got.Status != 200 {
		t.Errorf("the frozen inline grant: %d", got.Status)
	}
}

func TestR_CI_017_DisablingAMappingRevokesItsTokensOnEveryReplica(t *testing.T) {
	for _, valkey := range []bool{true, false} {
		t.Run(fmt.Sprintf("valkey %v", valkey), func(t *testing.T) {
			x := newXEnv(t, options{valkey: valkey})
			a, b, c := x.web("web-a"), x.web("web-b"), x.web("web-c")
			x.issuer(a)
			x.addMapping(a, releaseMapping("acme-release-publish", 100))
			ce := ciEnvOf(x.env, xIssuer)
			g := wantToken(t, x.exchange(a, x.mint(oidcstub.MintOptions{})))
			for _, r := range []*testenv.Replica{b, c} {
				if s := ce.alive(r, g.Token); s != 200 {
					t.Fatalf("%s answered %d to a fresh token", r.Name, s)
				}
			}
			m := x.do(a, "GET", mappingsOf(xIssuer)+"/acme-release-publish", nil)
			body := m.JSON(t)
			body["enabled"] = false
			start := time.Now()
			wantStatus(t, x.do(a, "PUT", mappingsOf(xIssuer)+"/acme-release-publish", body, "If-Match", etag(m)), 200, "")
			ce.refusedEverywhere(t, start, g.Token, b, c)
		})
	}
}

func TestR_CI_009_DeletingAMappingRevokesItsTokens(t *testing.T) {
	x := newXEnv(t, options{valkey: true})
	a, b := x.web("web-a"), x.web("web-b")
	x.issuer(a)
	x.addMapping(a, releaseMapping("acme-release-publish", 100))
	ce := ciEnvOf(x.env, xIssuer)
	g := wantToken(t, x.exchange(a, x.mint(oidcstub.MintOptions{})))
	start := time.Now()
	wantStatus(t, x.do(a, "DELETE", mappingsOf(xIssuer)+"/acme-release-publish", nil), 204, "")
	ce.refusedEverywhere(t, start, g.Token, a, b)
	if n := x.number(`SELECT count(*) FROM audit_event WHERE action = 'ci.token.revoked' AND details->>'cause' = 'mapping'`); n != 1 {
		t.Errorf("%d ci.token.revoked events of the mapping", n)
	}
}

func TestR_CI_001_ExchangeAnswersIssuerDisabledForARemovedIssuer(t *testing.T) {
	x := newXEnv(t, options{valkey: true})
	a := x.web("web-a")
	if err := x.declare(declared(x.d, xIssuer)); err != nil {
		t.Fatal(err)
	}
	within(t, "the replica seeing the declared issuer", 5*time.Second, func() bool {
		_, ok, _ := a.App.CIIssuers().Resolve(t.Context(), x.d.stub.Issuer())
		return ok
	})
	x.addMapping(a, releaseMapping("acme-release-publish", 100))
	wantToken(t, x.exchange(a, x.mint(oidcstub.MintOptions{})))
	// The next migration no longer declares it: disabled with removed_from_configuration (Q-CI-16).
	if err := x.declare(); err != nil {
		t.Fatal(err)
	}
	within(t, "the replica seeing the removal", 5*time.Second, func() bool {
		is, ok, _ := a.App.CIIssuers().Resolve(t.Context(), x.d.stub.Issuer())
		return ok && !is.Enabled
	})
	wantOAuth(t, x.exchange(a, x.mint(oidcstub.MintOptions{})), 400, "invalid_request", "issuer_disabled")
}

func TestR_CI_020_TheIssuanceRecordOfAnExchange(t *testing.T) {
	x := newXEnv(t, options{valkey: true})
	a := x.web("web-a")
	x.issuer(a)
	x.addMapping(a, releaseMapping("acme-release-publish", 100))
	g := wantToken(t, x.exchangeFrom(a, "198.51.100.30", x.mint(oidcstub.MintOptions{}), "scope", "read:npm-internal deploy:npm-internal"))

	t.Run("R-CI-020 GET /ci/issuances?job_id= returns the record with the verified job identity and the allowlisted snapshot", func(t *testing.T) {
		r := x.do(a, "GET", "/api/v1/ci/issuances?issuer="+xIssuer+"&jobId=123456", nil)
		wantStatus(t, r, 200, "")
		items, _ := r.JSON(t)["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("%d records\n%s", len(items), r.Body)
		}
		rec, _ := json.Marshal(items[0])
		for _, want := range []string{`"projectId":"1234"`, `"pipelineId":"98765"`, `"ref":"v1.4.0"`, `"acme-release-publish"`, `"read:npm-internal deploy:npm-internal"`, `"198.51.100.30"`} {
			if !strings.Contains(string(rec), want) {
				t.Errorf("the record lacks %s: %s", want, rec)
			}
		}
		if strings.Contains(string(rec), "jdoe@example.com") || strings.Contains(string(rec), "user_email") {
			t.Errorf("the record holds the user's e-mail: %s", rec)
		}
	})

	t.Run("R-CI-020 the record's expiry is the token row's, its grants the token's bundle", func(t *testing.T) {
		if n := x.number(`SELECT count(*) FROM ci_token_issuances i JOIN api_token t ON t.id = i.token_id WHERE i.expires_at = t.expires_at AND i.grants = t.ci_context`); n != 1 {
			t.Errorf("%d records agree with their token row", n)
		}
	})

	t.Run("R-CI-020 ci.exchange.succeeded records the token, the mappings, the snapshot, the scope and the TTL in the transaction", func(t *testing.T) {
		d := x.eventDetails("ci.exchange.succeeded")
		if d["issuer"] != xIssuer || d["grantedScope"] != g.Scope || d["ttlSeconds"] != float64(g.ExpiresIn) || d["tokenId"] == nil {
			t.Errorf("ci.exchange.succeeded records %v", d)
		}
		if c, _ := d["claims"].(map[string]any); c["project_id"] != "1234" || c["user_email"] != nil {
			t.Errorf("the snapshot %v", c)
		}
		if n := x.number(`SELECT count(*) FROM audit_event WHERE action = 'token.created' AND details->>'createdVia' = 'ci-exchange'`); n != 1 {
			t.Errorf("%d token.created of the exchange", n)
		}
	})

	t.Run("R-CI-020 the exchanges are counted, a successful one without reason", func(t *testing.T) {
		if v := metric(t, a, exchanges(xIssuer, "success", "")); v != 1 {
			t.Errorf("%s = %v", exchanges(xIssuer, "success", ""), v)
		}
	})
}
