//go:build integration

package ci

// The issuance API (R-CI-020, Q-CI-26): the records newest first with the filters and cursor paging of R-API-010,
// one record with its usage summary from the audit store, the bulk revoke's rules, who may read and revoke, and a
// project administrator's view of the tokens its project's mappings contributed to.

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
	publisher := e.issue(a, e.bundle("4711", "acme/web/frontend", rel.contribution(nil, grant([]string{"npm-internal"}, []string{"@acme/*"}, claims.Read, claims.Deploy))), job{"700", "9001"}, 10*time.Minute)
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
		if n := e.number(`SELECT count(*) FROM audit_event WHERE action = 'npm.publish' AND repository = 'npm-internal' AND token_id = $1::uuid`, publisher.id); n != 1 {
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
		if use["requests"].(float64) < 1 || use["lastUsedAt"] == nil || len(deployed) != 1 || deployed[0] != "npm-internal/@acme/frontend@1.0.0" {
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

// Q-CI-26: a project administrator lists the records of tokens whose mappings include one of its project's, and
// sees its project's mappings only; an administrator of another project sees none.
func TestQ_CI_26_AProjectAdministratorListsTheTokensOfItsMappings(t *testing.T) {
	e := newCIEnv(t, false, "acme-read")
	sys := e.maps["acme-read"]
	carol := e.stack.User("carol")
	carolTok := e.stack.Token(carol, scope.Scope{Inherit: true, Admin: true})
	a := e.web("web-a")
	wantStatus(t, e.do(a, "POST", "/api/v1/projects", map[string]any{"key": "web", "displayName": "Web"}), 201, "")
	wantStatus(t, e.do(a, "POST", "/api/v1/projects", map[string]any{"key": "data", "displayName": "Data"}), 201, "")
	wantStatus(t, e.do(a, "PUT", "/api/v1/projects/web/admins/user%3Abob", nil), 201, "")
	added := e.do(a, "PUT", "/api/v1/projects/data/admins/user%3Acarol", nil)
	wantStatus(t, added, 201, "")
	epoch := added.Header.Get("Trove-Authz-Epoch")
	webID := e.text(`SELECT id::text FROM project WHERE key = 'web'`)
	// The project mapping as W3's store writes one (tier project:web, its project's ID).
	pid := e.text(`INSERT INTO principal (id, kind, name) VALUES (gen_random_uuid(), 'ci_mapping', 'gitlab-main/web-publish') RETURNING id::text`)
	mid := e.text(`INSERT INTO ci_mappings (id, issuer_id, principal_id, tier, project_id, name, priority, match, grants, created_by)
	        VALUES (gen_random_uuid(), $1::uuid, $2::uuid, 'project:web', $3::uuid, 'web-publish', 1, '{}'::jsonb, '{}'::jsonb, 'test') RETURNING id::text`, e.issuerID, pid, webID)
	web := ciMapping{id: mid, principalID: pid, name: "web-publish"}
	e.maps[web.name] = web
	both := e.bundle("4711", "acme/web/frontend", sys.contribution(nil),
		claims.FrozenMapping{Ref: web.ref(), ID: web.id, Tier: "project:web", Project: &claims.FrozenProject{Key: "web", ID: webID}})
	tok := e.issue(a, both, job{"1", "77"}, 10*time.Minute)
	e.issue(a, e.bundle("4712", "acme/other", sys.contribution(nil)), job{"1", "78"}, 10*time.Minute)

	r := e.as(a, e.bobPAT, "GET", "/api/v1/projects/web/ci/issuances", nil, "Trove-Authz-Min-Epoch", epoch)
	wantStatus(t, r, 200, "")
	got := items(t, r.JSON(t))
	if len(got) != 1 || got[0]["tokenId"] != tok.id {
		t.Fatalf("records %v", got)
	}
	names := got[0]["mappingNames"].([]any)
	contributions := got[0]["grants"].(map[string]any)["contributions"].([]any)
	if len(names) != 1 || names[0] != "web-publish" || len(contributions) != 1 {
		t.Errorf("the record shows another tier's mappings: %v %v", names, contributions)
	}
	if r := e.as(a, carolTok, "GET", "/api/v1/projects/web/ci/issuances", nil, "Trove-Authz-Min-Epoch", epoch); r.Status != 404 {
		t.Errorf("another project's administrator got %d", r.Status)
	}
	if r := e.as(a, carolTok, "GET", "/api/v1/projects/data/ci/issuances", nil, "Trove-Authz-Min-Epoch", epoch); r.Status != 200 || len(items(t, r.JSON(t))) != 0 {
		t.Errorf("the other project lists %d %s", r.Status, r.Body)
	}
}
