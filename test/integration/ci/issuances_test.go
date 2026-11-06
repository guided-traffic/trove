//go:build integration

package ci

// The issuance API (R-CI-020, Q-CI-26): the records newest first with the filters and cursor paging of R-API-010,
// one record with its usage summary from the audit store, the bulk revoke's rules, who may read and revoke, and a
// project Owner's view of the tokens its project's mappings contributed to.

import (
	"net/url"
	"testing"
	"time"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/internal/ci/claims"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

const issuancesPath = "/api/v1/ci/issuances"

// items are the records of a list answer.
func items(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, it := range body["items"].([]any) {
		out = append(out, it.(map[string]any))
	}
	return out
}

// R-CI-020: the records of the reference pipeline's exchanges, the filters, the paging, one record with its usage
// and the bulk revoke's answer and refusals.
func TestR_CI_020_TheIssuanceRecordsAndTheBulkRevoke(t *testing.T) {
	// The database gauges are read every second, so the gauge follows the revocation within the test.
	e := newCIEnvWith(t, options{env: []string{"TROVE_METRICS_DB_GAUGE_INTERVAL=1s"}}, "acme-release", "acme-read")
	rel, read := e.maps["acme-release"], e.maps["acme-read"]
	auditor := e.stack.User("auditor")
	e.stack.Bind("auditor", auditor)
	auditorTok := e.stack.Token(auditor, scope.Scope{Inherit: true, System: []string{"tokens:read"}})
	a := e.web("web-a")
	publisher := e.issue(a, e.bundle("4711", "acme/web/frontend", rel.contribution(nil, grant([]string{"it/npm-internal"}, []string{"@acme/*"}, claims.Read, claims.Deploy))), job{"700", "9001"}, 10*time.Minute)
	time.Sleep(5 * time.Millisecond) // the records sort by issued_at
	second := e.issue(a, e.bundle("4711", "acme/web/frontend", read.contribution(nil)), job{"700", "9002"}, 10*time.Minute)
	time.Sleep(5 * time.Millisecond)
	other := e.issue(a, e.bundle("4712", "acme/mobile/app", read.contribution(nil)), job{"701", "9003"}, 10*time.Minute)
	mustStatus(t, "publish", e.publish(a, "npm-internal", publisher.secret, "@acme/frontend", "1.0.0"), 201)

	t.Run("R-CI-020 GET ?jobId= returns the job's record, and the publish's audit entry carries its token_id", func(t *testing.T) {
		r := e.do(a, "GET", issuancesPath+"?jobId=9001", nil)
		wantStatus(t, r, 200, "")
		got := items(t, r.JSON(t))
		if len(got) != 1 || got[0]["tokenId"] != publisher.id || got[0]["active"] != true || got[0]["projectPath"] != "acme/web/frontend" {
			t.Fatalf("records %v", got)
		}
		if n := e.number(`SELECT count(*) FROM audit_event WHERE action = 'npm.publish' AND repository = 'it/npm-internal' AND token_id = $1::uuid`, publisher.id); n != 1 {
			t.Errorf("%d publish entries with the token", n)
		}
	})
	t.Run("R-CI-020 the list is newest first and pages by cursor", func(t *testing.T) {
		r := e.do(a, "GET", issuancesPath+"?limit=2", nil)
		wantStatus(t, r, 200, "")
		page := r.JSON(t)
		first := items(t, page)
		if len(first) != 2 || first[0]["tokenId"] != other.id || first[1]["tokenId"] != second.id || page["nextCursor"] == nil {
			t.Fatalf("first page %v", page)
		}
		next := e.do(a, "GET", issuancesPath+"?limit=2&cursor="+url.QueryEscape(page["nextCursor"].(string)), nil)
		wantStatus(t, next, 200, "")
		if rest := items(t, next.JSON(t)); len(rest) != 1 || rest[0]["tokenId"] != publisher.id {
			t.Fatalf("second page %v", rest)
		}
	})
	t.Run("R-CI-020 the filters", func(t *testing.T) {
		for query, want := range map[string]int{
			"issuer=" + ciIssuerName: 3, "issuer=gitlab-other": 0, "projectId=4711": 2, "projectPath=acme/mobile/app": 1,
			"pipelineId=700": 2, "mapping=acme-read": 2, "active=true": 3, "active=false": 0,
			"from=" + url.QueryEscape(time.Now().Add(time.Hour).UTC().Format(time.RFC3339)): 0,
			"to=" + url.QueryEscape(time.Now().Add(time.Hour).UTC().Format(time.RFC3339)):   3,
		} {
			r := e.do(a, "GET", issuancesPath+"?"+query, nil)
			wantStatus(t, r, 200, "")
			if got := len(items(t, r.JSON(t))); got != want {
				t.Errorf("%s: %d records, want %d", query, got, want)
			}
		}
		wantStatus(t, e.do(a, "GET", issuancesPath+"?active=maybe", nil), 400, "")
	})
	t.Run("R-CI-020 one record with its usage summary from the audit store", func(t *testing.T) {
		r := e.do(a, "GET", issuancesPath+"/"+publisher.id, nil)
		wantStatus(t, r, 200, "")
		body := r.JSON(t)
		use := body["usage"].(map[string]any)
		deployed := use["packagesDeployed"].([]any)
		if use["requests"].(float64) < 1 || use["lastUsedAt"] == nil || len(deployed) != 1 || deployed[0] != "it/npm-internal/@acme/frontend@1.0.0" {
			t.Fatalf("usage %v", use)
		}
		rec := body["issuance"].(map[string]any)
		if g := rec["grants"].(map[string]any); g["issuer"] != ciIssuerName || len(g["contributions"].([]any)) != 1 {
			t.Errorf("grants %v", g)
		}
		wantStatus(t, e.do(a, "GET", issuancesPath+"/0190a4c2-0000-7000-8000-00000000ffff", nil), 404, "")
	})
	t.Run("R-AUTHZ-011 the auditor reads the records, a user without tokens:read and a CI token are refused", func(t *testing.T) {
		wantStatus(t, e.as(a, auditorTok, "GET", issuancesPath, nil), 200, "")
		wantStatus(t, e.as(a, auditorTok, "POST", issuancesPath+"/revoke", map[string]any{"issuer": ciIssuerName, "jobId": "9002"}), 403, "")
		wantStatus(t, e.as(a, e.alicePAT, "GET", issuancesPath, nil), 403, "")
		wantStatus(t, e.as(a, publisher.secret, "GET", issuancesPath, nil), 403, "ci_token_restricted")
	})
	t.Run("R-CI-020 the bulk revoke needs an issuer and a filter and answers the number revoked", func(t *testing.T) {
		wantStatus(t, e.do(a, "POST", issuancesPath+"/revoke", map[string]any{"issuer": ciIssuerName}), 422, "")
		wantStatus(t, e.do(a, "POST", issuancesPath+"/revoke", map[string]any{"projectId": "4711"}), 422, "")
		wantStatus(t, e.do(a, "POST", issuancesPath+"/revoke", map[string]any{"issuer": "gitlab-other", "projectId": "4711"}), 404, "")
		r := e.do(a, "POST", issuancesPath+"/revoke", map[string]any{"issuer": ciIssuerName, "projectId": "4711", "mapping": "acme-read"})
		wantStatus(t, r, 200, "")
		if n := r.JSON(t)["revoked"]; n != float64(1) {
			t.Fatalf("revoked %v", n)
		}
		again := e.do(a, "POST", issuancesPath+"/revoke", map[string]any{"issuer": ciIssuerName, "projectId": "4711", "mapping": "acme-read"})
		if n := again.JSON(t)["revoked"]; n != float64(0) {
			t.Errorf("a repeated revoke revoked %v", n)
		}
		if e.alive(a, second.secret) != 401 || e.alive(a, publisher.secret) != 200 || e.alive(a, other.secret) != 200 {
			t.Error("the bulk revoke ended the wrong tokens")
		}
		if got := items(t, e.do(a, "GET", issuancesPath+"?active=false", nil).JSON(t)); len(got) != 1 || got[0]["tokenId"] != second.id || got[0]["revokedAt"] == nil {
			t.Errorf("inactive %v", got)
		}
		filter := e.text(`SELECT details->'filter'->>'mapping' FROM audit_event WHERE action = 'ci.token.revoked' AND details->>'cause' = 'bulk'`)
		if filter != "acme-read" {
			t.Errorf("the bulk event's filter %q", filter)
		}
	})
	t.Run("R-CI-020 trove_ci_tokens_active counts the unexpired, unrevoked tokens per issuer", func(t *testing.T) {
		within(t, "the gauge after the revocation", 5*time.Second, func() bool {
			return testenv.MetricValue(a.Metrics(t), `trove_ci_tokens_active{issuer="`+ciIssuerName+`"}`) == 2
		})
	})
}

// Q-CI-26: a project Owner lists the records of tokens whose mappings include one of its project's, and
// sees its project's mappings only; an Owner of another project sees none.
func TestQ_CI_26_AProjectOwnerListsTheTokensOfItsMappings(t *testing.T) {
	e := newCIEnv(t, false, "acme-read")
	sys := e.maps["acme-read"]
	carol := e.stack.User("carol")
	carolTok := e.stack.Token(carol, scope.Scope{Inherit: true, Admin: true})
	a := e.web("web-a")
	wantStatus(t, e.do(a, "POST", "/api/v1/projects", map[string]any{"key": "web", "displayName": "Web"}), 201, "")
	wantStatus(t, e.do(a, "POST", "/api/v1/projects", map[string]any{"key": "data", "displayName": "Data"}), 201, "")
	e.addOwner("web", e.bobID)
	epoch := e.addOwner("data", carol.ID)
	webID := e.text(`SELECT id::text FROM project WHERE key = 'web'`)
	// The project mapping as W3's store writes one (tier project:web, its project's ID).
	pid := e.text(`INSERT INTO principal (id, kind, name) VALUES (gen_random_uuid(), 'ci_mapping', 'gitlab-main/web/web-publish') RETURNING id::text`)
	mid := e.text(`INSERT INTO ci_mappings (id, issuer_id, principal_id, tier, project_id, name, priority, match, grants, created_by)
	        VALUES (gen_random_uuid(), $1::uuid, $2::uuid, 'project:web', $3::uuid, 'web-publish', 1, '{}'::jsonb, '{}'::jsonb, 'test') RETURNING id::text`, e.issuerID, pid, webID)
	web := ciMapping{id: mid, principalID: pid, name: "web-publish", project: "web"}
	e.maps[web.project+"/"+web.name] = web
	both := e.bundle("4711", "acme/web/frontend", sys.contribution(nil),
		claims.FrozenMapping{Ref: web.ref(), ID: web.id, Tier: "project:web", Project: &claims.FrozenProject{Key: "web", ID: webID}})
	tok := e.issue(a, both, job{"1", "77"}, 10*time.Minute)
	e.exec(`UPDATE ci_token_issuances SET user_id = '7', user_login = 'alice', jti = 'jti-1', requested_scope = 'read',
	        claims = claims || '{"job_id":"77","user_id":"7","user_login":"alice","jti":"jti-1","runner_id":3,"runner_environment":"self-hosted"}'::jsonb
	 WHERE token_id = $1::uuid`, tok.id)
	e.issue(a, e.bundle("4712", "acme/other", sys.contribution(nil)), job{"1", "78"}, 10*time.Minute)

	r := e.as(a, e.bobPAT, "GET", "/api/v1/projects/web/ci/issuances", nil, "Trove-Authz-Min-Epoch", epoch)
	wantStatus(t, r, 200, "")
	got := items(t, r.JSON(t))
	if len(got) != 1 || got[0]["tokenId"] != tok.id {
		t.Fatalf("records %v", got)
	}
	names := got[0]["mappingNames"].([]any)
	contributions := got[0]["grants"].(map[string]any)["contributions"].([]any)
	if len(names) != 1 || names[0] != "web/web-publish" || len(contributions) != 1 {
		t.Errorf("the record shows another tier's mappings: %v %v", names, contributions)
	}
	// Q-CI-52: the scope strings list every tier's items and the person and the client are not the project's business.
	for _, field := range []string{"requestedScope", "userId", "userLogin", "jti", "clientIp", "userAgent"} {
		if v, present := got[0][field]; present && v != nil {
			t.Errorf("the project's view shows %s = %v", field, v)
		}
	}
	if got[0]["grantedScope"] != "" {
		t.Errorf("the project's view shows grantedScope = %v", got[0]["grantedScope"])
	}
	claimsOf, _ := got[0]["claims"].(map[string]any)
	for _, name := range []string{"user_id", "user_login", "jti", "runner_id", "runner_environment"} {
		if _, present := claimsOf[name]; present {
			t.Errorf("the project's claim snapshot holds %s", name)
		}
	}
	if claimsOf["project_id"] != "4711" || claimsOf["job_id"] != "77" {
		t.Errorf("the project's claim snapshot lost the job's identity: %v", claimsOf)
	}
	if full := e.do(a, "GET", issuancesPath+"/"+tok.id, nil).JSON(t)["issuance"].(map[string]any); full["grantedScope"] == nil || full["grantedScope"] == "" {
		t.Errorf("the system view lost grantedScope: %v", full["grantedScope"])
	}
	if r := e.as(a, carolTok, "GET", "/api/v1/projects/web/ci/issuances", nil, "Trove-Authz-Min-Epoch", epoch); r.Status != 404 {
		t.Errorf("another project's Owner got %d", r.Status)
	}
	if r := e.as(a, carolTok, "GET", "/api/v1/projects/data/ci/issuances", nil, "Trove-Authz-Min-Epoch", epoch); r.Status != 200 || len(items(t, r.JSON(t))) != 0 {
		t.Errorf("the other project lists %d %s", r.Status, r.Body)
	}
}

// R-CI-020, Q-CI-52: the state of a record is one value in the list, in the read and in the filter. A token row goes
// with the principal of a deleted mapping (api_token.principal_id cascades), so the record outlives its token: it reads
// as not active with the state `removed`, the filter ?active=false lists it and ?active=true leaves it out. A revoked
// and an expired token read as `revoked` and `expired`.
func TestR_CI_020_ARecordWhoseTokenWentWithItsMappingIsNotActive(t *testing.T) {
	e := newCIEnv(t, false, "acme-read", "acme-gone", "acme-late")
	a := e.web("web-a")
	kept := e.issue(a, e.bundle("4711", "acme/web/frontend", e.maps["acme-read"].contribution(nil)), job{"1", "10"}, 10*time.Minute)
	time.Sleep(5 * time.Millisecond)
	gone := e.issue(a, e.bundle("4711", "acme/web/frontend", e.maps["acme-gone"].contribution(nil)), job{"1", "11"}, 10*time.Minute)
	time.Sleep(5 * time.Millisecond)
	late := e.issue(a, e.bundle("4711", "acme/web/frontend", e.maps["acme-late"].contribution(nil)), job{"1", "12"}, 10*time.Minute)
	wantStatus(t, e.do(a, "POST", issuancesPath+"/revoke", map[string]any{"issuer": ciIssuerName, "jobId": "10"}), 200, "")
	// The helper's row carries a condition the store would not write; the delete decodes it.
	e.exec(`UPDATE ci_mappings SET match = '{"claim":"project_id","equals":"4711"}'::jsonb WHERE id = $1::uuid`, e.maps["acme-gone"].id)
	wantStatus(t, e.do(a, "DELETE", mappingsOf(ciIssuerName)+"/acme-gone", nil), 204, "")
	e.exec(`UPDATE ci_token_issuances SET expires_at = now() - interval '1 minute' WHERE token_id = $1::uuid`, late.id)
	e.exec(`UPDATE api_token SET expires_at = now() - interval '1 minute' WHERE id = $1::uuid`, late.id)
	if n := e.number(`SELECT count(*) FROM api_token WHERE id = $1::uuid`, gone.id); n != 0 {
		t.Fatalf("the token row of the deleted mapping is still there (%d)", n)
	}

	state := map[string]string{kept.id: "revoked", gone.id: "removed", late.id: "expired"}
	for id, want := range state {
		r := e.do(a, "GET", issuancesPath+"/"+id, nil)
		wantStatus(t, r, 200, "")
		rec := r.JSON(t)["issuance"].(map[string]any)
		if rec["state"] != want || rec["active"] != false {
			t.Errorf("record %s reads as state %v active %v, want %s and false", id, rec["state"], rec["active"], want)
		}
	}
	listed := func(query string) map[string]map[string]any {
		r := e.do(a, "GET", issuancesPath+query, nil)
		wantStatus(t, r, 200, "")
		out := map[string]map[string]any{}
		for _, it := range items(t, r.JSON(t)) {
			out[it["tokenId"].(string)] = it
		}
		return out
	}
	if got := listed("?active=true"); len(got) != 0 {
		t.Errorf("?active=true lists %v", got)
	}
	inactive := listed("?active=false")
	if len(inactive) != 3 {
		t.Fatalf("?active=false lists %d records, want 3", len(inactive))
	}
	for id, want := range state {
		if rec := inactive[id]; rec["state"] != want || rec["active"] != false {
			t.Errorf("the list shows %s as %v/%v, want %s/false", id, rec["state"], rec["active"], want)
		}
	}
	if rec := inactive[gone.id]; rec["revokedAt"] != nil {
		t.Errorf("a record whose token row is gone shows revokedAt %v", rec["revokedAt"])
	}
}
