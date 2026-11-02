//go:build integration

package ci

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/oidcstub"
)

const issuersPath = "/api/v1/ci/issuers"

// seedMapping writes a mapping row of an issuer the way W3's store will (the principal of kind ci_mapping
// and the row), so the issuer's delete sees mappings. The store of the mappings is not W1's.
func (e *env) seedMapping(issuer, name string) {
	e.t.Helper()
	e.exec(`WITH p AS (INSERT INTO principal (id, kind, name) VALUES (gen_random_uuid(), 'ci_mapping', $1 || '/' || $2) RETURNING id)
	        INSERT INTO ci_mappings (id, issuer_id, principal_id, name, priority, match, grants, created_by)
	        SELECT gen_random_uuid(), i.id, p.id, $2, 1, '{}'::jsonb, '{}'::jsonb, 'test' FROM p, ci_issuers i WHERE i.name = $1`, issuer, name)
}

// seedIssuance writes the record of an exchange (R-CI-020), which is what shows that an issuer issued
// tokens.
func (e *env) seedIssuance(issuer string) {
	e.t.Helper()
	e.exec(`INSERT INTO ci_token_issuances (token_id, issuer_id, mapping_ids, mapping_names, subject_kind, project_id, job_id, claims, grants,
	                                        granted_scope, issued_at, expires_at)
	        SELECT gen_random_uuid(), i.id, '{}', '{}', 'id_token', '7', '9', '{}'::jsonb, '{}'::jsonb, 'read', now(), now() + interval '1 hour'
	          FROM ci_issuers i WHERE i.name = $1`, issuer)
}

func TestR_CI_002_TheIssuerLifecycle(t *testing.T) {
	d := startDouble(t, oidcstub.Config{})
	e := newEnv(t, options{valkey: true})
	a, b := e.web("web-a"), e.web("web-b")

	t.Run("R-CI-002 create answers 201 with Location and ETag, the defaults filled in and the keys fetched", func(t *testing.T) {
		before := e.epoch("ci_mappings")
		r := e.do(a, "POST", issuersPath, d.body("gitlab-main", map[string]any{"description": "the instance"}))
		wantStatus(t, r, 201, "")
		if loc := r.Header.Get("Location"); !strings.HasSuffix(loc, issuersPath+"/gitlab-main") {
			t.Errorf("Location = %q", loc)
		}
		if etag(r) != `"1"` {
			t.Errorf("ETag = %q", etag(r))
		}
		got := r.JSON(t)
		if got["source"] != "configuration" && got["source"] != "api" {
			t.Errorf("source = %v", got["source"])
		}
		for member, want := range map[string]any{
			"source": "api", "enabled": true, "exclusiveAudience": true, "allowMissingTyp": true, "clockSkewSeconds": float64(60),
			"singleUseSubjectTokens": false, "useOutboundProxy": false, "removedFromConfiguration": false, "version": float64(1),
		} {
			if got[member] != want {
				t.Errorf("%s = %v, want %v", member, got[member], want)
			}
		}
		auds, _ := got["allowedAudiences"].([]any)
		if fmt.Sprint(auds) != "[http://trove.test npm:trove.test]" {
			t.Errorf("allowedAudiences = %v", auds)
		}
		if e.epoch("ci_mappings") != before+1 {
			t.Errorf("the change area ci_mappings moved %d, want 1", e.epoch("ci_mappings")-before)
		}
		if e.events("ci.issuer.created") != 1 || e.events("ci.keys.refreshed") != 1 {
			t.Errorf("events: created %d, keys refreshed %d", e.events("ci.issuer.created"), e.events("ci.keys.refreshed"))
		}
		k := e.do(a, "GET", issuersPath+"/gitlab-main/keys", nil)
		wantStatus(t, k, 200, "")
		keys, _ := k.JSON(t)["keys"].([]any)
		if len(keys) < 1 || keys[0].(map[string]any)["kty"] != "RSA" {
			t.Errorf("keys = %v", keys)
		}
	})

	t.Run("R-CI-002 the other replica resolves the issuer within the poll interval", func(t *testing.T) {
		within(t, "web-b resolving gitlab-main", 3*time.Second, func() bool {
			_, ok, err := b.App.CIIssuers().Resolve(t.Context(), d.stub.Issuer())
			return err == nil && ok
		})
	})

	t.Run("R-CI-002 a create of the same name or the same issuer URL is 409", func(t *testing.T) {
		wantStatus(t, e.do(a, "POST", issuersPath, d.body("gitlab-main")), 409, "already_exists")
		other := d.body("gitlab-twin")
		wantStatus(t, e.do(a, "POST", issuersPath, other), 409, "already_exists")
		if n := e.number("SELECT count(*) FROM ci_issuers"); n != 1 {
			t.Errorf("%d issuers", n)
		}
	})

	t.Run("R-CI-002 a read carries the ETag, mapping count and key summary; If-None-Match answers 304", func(t *testing.T) {
		r := e.do(b, "GET", issuersPath+"/gitlab-main", nil)
		wantStatus(t, r, 200, "")
		got := r.JSON(t)
		keys, _ := got["keys"].(map[string]any)
		if got["mappingCount"] != float64(0) || keys["usable"] != float64(gitlabKeys) || keys["ignored"] != float64(0) || keys["fetchedAt"] == nil {
			t.Errorf("mappingCount %v, keys %v", got["mappingCount"], keys)
		}
		if n := e.as(b, e.opsPAT, "GET", issuersPath+"/gitlab-main", nil, "If-None-Match", etag(r)); n.Status != 304 {
			t.Errorf("If-None-Match answered %d", n.Status)
		}
		wantStatus(t, e.do(a, "GET", issuersPath+"/nope", nil), 404, "not_found")
		list := e.do(a, "GET", issuersPath, nil)
		wantStatus(t, list, 200, "")
		if items, _ := list.JSON(t)["items"].([]any); len(items) != 1 {
			t.Errorf("%d issuers listed", len(items))
		}
	})

	t.Run("R-CI-002 a PUT without If-Match is 428 and leaves the version; with the tag of a GET it answers the next ETag", func(t *testing.T) {
		cur := e.do(a, "GET", issuersPath+"/gitlab-main", nil)
		body := cur.JSON(t)
		wantStatus(t, e.do(a, "PUT", issuersPath+"/gitlab-main", body), 428, "precondition_required")
		if v := e.number("SELECT version FROM ci_issuers WHERE name = 'gitlab-main'"); v != 1 {
			t.Fatalf("the version moved to %d", v)
		}
		body["description"] = "second"
		r := e.do(b, "PUT", issuersPath+"/gitlab-main", body, "If-Match", etag(cur))
		wantStatus(t, r, 200, "")
		if etag(r) != `"2"` || r.JSON(t)["description"] != "second" {
			t.Errorf("ETag %q, description %v", etag(r), r.JSON(t)["description"])
		}
	})

	t.Run("R-CI-002 a stale If-Match is 412 with currentEtag; * is the deliberate blind write", func(t *testing.T) {
		body := e.do(a, "GET", issuersPath+"/gitlab-main", nil).JSON(t) // version 2
		r := e.do(a, "PUT", issuersPath+"/gitlab-main", body, "If-Match", `"1"`)
		wantStatus(t, r, 412, "precondition_failed")
		if r.JSON(t)["currentEtag"] != `"2"` {
			t.Errorf("currentEtag = %v", r.JSON(t)["currentEtag"])
		}
		blind := e.do(a, "PUT", issuersPath+"/gitlab-main", body, "If-Match", "*")
		wantStatus(t, blind, 200, "")
		if etag(blind) != `"3"` {
			t.Errorf("ETag = %q", etag(blind))
		}
	})

	t.Run("R-CI-002 name, issuer and kind are immutable; the read-only members of a GET body are ignored", func(t *testing.T) {
		body := e.do(a, "GET", issuersPath+"/gitlab-main", nil).JSON(t)
		changed := map[string]any{}
		for k, v := range body {
			changed[k] = v
		}
		changed["issuer"] = "https://other.example.com"
		r := e.do(a, "PUT", issuersPath+"/gitlab-main", changed, "If-Match", "*")
		wantStatus(t, r, 422, "")
		if f := problemFields(t, r); len(f) != 1 || f[0] != "/issuer immutable" {
			t.Errorf("fields = %v", f)
		}
		changed["issuer"], changed["name"] = body["issuer"], "renamed"
		wantStatus(t, e.do(a, "PUT", issuersPath+"/gitlab-main", changed, "If-Match", "*"), 422, "")
		body["version"], body["createdAt"] = 999, "2001-01-01T00:00:00Z"
		wantStatus(t, e.do(a, "PUT", issuersPath+"/gitlab-main", body, "If-Match", "*"), 200, "")
	})

	t.Run("R-CI-002 a PUT is one event chosen by enabled, with the record before and after and the journal entry", func(t *testing.T) {
		body := e.do(a, "GET", issuersPath+"/gitlab-main", nil).JSON(t)
		upd, dis, ena := e.events("ci.issuer.updated"), e.events("ci.issuer.disabled"), e.events("ci.issuer.enabled")
		epoch := e.epoch("ci_mappings")
		body["enabled"] = false
		wantStatus(t, e.do(a, "PUT", issuersPath+"/gitlab-main", body, "If-Match", "*"), 200, "")
		if e.events("ci.issuer.disabled") != dis+1 || e.events("ci.issuer.updated") != upd || e.events("ci.issuer.enabled") != ena {
			t.Errorf("a disable wrote the wrong event")
		}
		if got := e.text(`SELECT details->'before'->>'enabled' || ',' || (details->'after'->>'enabled') FROM audit_event WHERE action = 'ci.issuer.disabled' ORDER BY occurred_at DESC LIMIT 1`); got != "true,false" {
			t.Errorf("before and after enabled = %q", got)
		}
		if e.number("SELECT count(*) FROM revocation_outbox WHERE event = 'ci.issuer.disabled'") != 1 {
			t.Error("the disable is not in the revocation journal")
		}
		body["enabled"] = true
		wantStatus(t, e.do(a, "PUT", issuersPath+"/gitlab-main", body, "If-Match", "*"), 200, "")
		body["description"] = "again"
		wantStatus(t, e.do(a, "PUT", issuersPath+"/gitlab-main", body, "If-Match", "*"), 200, "")
		if e.events("ci.issuer.enabled") != ena+1 || e.events("ci.issuer.updated") != upd+1 {
			t.Errorf("enable %d, update %d", e.events("ci.issuer.enabled")-ena, e.events("ci.issuer.updated")-upd)
		}
		if e.number("SELECT count(*) FROM revocation_outbox WHERE event = 'ci.issuer.enabled'") != 0 {
			t.Error("an enable is journaled")
		}
		if e.epoch("ci_mappings") != epoch+3 {
			t.Errorf("three writes moved ci_mappings by %d", e.epoch("ci_mappings")-epoch)
		}
	})

	t.Run("R-CI-002 a delete is refused while mappings exist and goes through without them", func(t *testing.T) {
		e.seedMapping("gitlab-main", "publish")
		r := e.do(a, "DELETE", issuersPath+"/gitlab-main", nil)
		wantStatus(t, r, 409, "conflict")
		if !strings.Contains(r.JSON(t)["detail"].(string), "1 mappings") {
			t.Errorf("detail = %v", r.JSON(t)["detail"])
		}
		wantStatus(t, e.do(a, "DELETE", issuersPath+"/gitlab-main", nil, "If-Match", `"1"`), 412, "precondition_failed")
		e.exec("DELETE FROM ci_mappings")
		e.exec("DELETE FROM principal WHERE kind = 'ci_mapping'")
		wantStatus(t, e.do(b, "DELETE", issuersPath+"/gitlab-main", nil), 204, "")
		wantStatus(t, e.do(a, "GET", issuersPath+"/gitlab-main", nil), 404, "not_found")
		if e.events("ci.issuer.deleted") != 1 || e.number("SELECT count(*) FROM revocation_outbox WHERE event = 'ci.issuer.deleted'") != 1 {
			t.Error("the delete is not audited and journaled")
		}
		if e.number("SELECT count(*) FROM ci_issuer_keys") != 0 {
			t.Error("the key row stays after the delete")
		}
		within(t, "web-b dropping the deleted issuer", 3*time.Second, func() bool {
			_, ok, err := b.App.CIIssuers().Resolve(t.Context(), d.stub.Issuer())
			return err == nil && !ok
		})
	})
}

func TestR_CI_002_OnlyAnAdministratorMayUseTheOperations(t *testing.T) {
	d := startDouble(t, oidcstub.Config{})
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	wantStatus(t, e.do(a, "POST", issuersPath, d.body("gitlab-main")), 201, "")
	for _, op := range []struct{ method, path string }{
		{"GET", issuersPath},
		{"POST", issuersPath},
		{"GET", issuersPath + "/gitlab-main"},
		{"PUT", issuersPath + "/gitlab-main"},
		{"DELETE", issuersPath + "/gitlab-main"},
		{"GET", issuersPath + "/gitlab-main/keys"},
		{"POST", issuersPath + "/gitlab-main/keys/refresh"},
	} {
		r := e.as(a, e.alicePAT, op.method, op.path, map[string]any{}, "If-Match", "*")
		if r.Status != 403 {
			t.Errorf("%s %s answered %d to a user without a role", op.method, op.path, r.Status)
		}
		if n := e.as(a, "", op.method, op.path, nil); n.Status != 401 {
			t.Errorf("%s %s answered %d without a credential", op.method, op.path, n.Status)
		}
	}
	if n := e.number("SELECT count(*) FROM ci_issuers"); n != 1 {
		t.Errorf("a refused call changed the issuers: %d", n)
	}
}

func TestR_CI_002_TheCreateRefusals(t *testing.T) {
	d := startDouble(t, oidcstub.Config{})
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	post := func(body map[string]any) testenv.Answer { return e.do(a, "POST", issuersPath, body) }
	noValidate := map[string]any{"validate": false}

	t.Run("R-CI-001 a second issuer with the URL of the first is 409, also for gitlab.com", func(t *testing.T) {
		wantStatus(t, post(map[string]any{"name": "gitlab-com", "kind": "gitlab", "issuer": "https://gitlab.com", "validate": false}), 201, "")
		r := post(map[string]any{"name": "gitlab-com-2", "kind": "gitlab", "issuer": "https://gitlab.com", "validate": false})
		wantStatus(t, r, 409, "already_exists")
		if !strings.Contains(r.JSON(t)["detail"].(string), "gitlab-com") {
			t.Errorf("the 409 does not name the issuer that has the URL: %v", r.JSON(t)["detail"])
		}
	})

	for name, tc := range map[string]struct {
		body   map[string]any
		status int
		code   string
		field  string
	}{
		"R-CI-001 HS256 is no algorithm":        {map[string]any{"allowedAlgorithms": []string{"HS256"}}, 422, "", "/allowedAlgorithms algorithm_not_allowed"},
		"R-CI-001 none is no algorithm":         {map[string]any{"allowedAlgorithms": []string{"none"}}, 422, "", "/allowedAlgorithms algorithm_not_allowed"},
		"R-CI-001 an http issuer is refused":    {map[string]any{"issuer": "http://gitlab.example.com"}, 422, "", "/issuer invalid_issuer"},
		"R-CI-001 a path claim cannot be a pin": {map[string]any{"pinClaims": []string{"project_path"}}, 422, "pin_claim_not_immutable", "/pinClaims pin_claim_not_immutable"},
		"R-CI-001 gitlab.com is a public issuer": {
			map[string]any{"issuer": "https://gitlab.com", "publicIssuer": false}, 422, "public_issuer_required", "/publicIssuer public_issuer_required",
		},
		"R-CI-002 the kind is a closed enum":            {map[string]any{"kind": "GitHub"}, 422, "", "/kind invalid_kind"},
		"R-CI-002 github is not available":              {map[string]any{"kind": "github"}, 422, "kind_not_available", "/kind kind_not_available"},
		"R-CI-002 generic is not available":             {map[string]any{"kind": "generic"}, 422, "kind_not_available", "/kind kind_not_available"},
		"R-CI-001 the fallback is not built":            {map[string]any{"jobTokenFallback": map[string]any{"enabled": true}}, 422, "job_token_fallback_not_available", "/jobTokenFallback/enabled job_token_fallback_not_available"},
		"R-CI-001 a bundle variable outside the prefix": {map[string]any{"caBundleRef": "env:GITLAB_CA"}, 422, "", "/caBundleRef invalid_ca_bundle_ref"},
		"R-CI-001 an unknown member":                    {map[string]any{"colour": "red"}, 400, "", ""},
	} {
		t.Run(name, func(t *testing.T) {
			body := map[string]any{"name": "refused", "kind": "gitlab", "issuer": "https://gitlab.example.com", "validate": false}
			for k, v := range tc.body {
				body[k] = v
			}
			r := post(body)
			wantStatus(t, r, tc.status, tc.code)
			if tc.field != "" {
				found := false
				for _, f := range problemFields(t, r) {
					found = found || f == tc.field
				}
				if !found {
					t.Errorf("fields = %v, want %q", problemFields(t, r), tc.field)
				}
			}
		})
	}
	if n := e.number("SELECT count(*) FROM ci_issuers WHERE name = 'refused'"); n != 0 {
		t.Errorf("%d refused issuers were stored", n)
	}

	t.Run("R-CI-002 validation: discovery, issuer, keys and algorithms, nothing stored on failure", func(t *testing.T) {
		before := e.epoch("ci_mappings")
		for _, tc := range []struct {
			name  string
			fault oidcstub.Fault
			code  string
			match string
		}{
			{"an unreachable issuer", oidcstub.Fault{Unreachable: true}, "discovery_unreachable", "connection_failed"},
			{"an HTTP error", oidcstub.Fault{Status: 500, Target: oidcstub.TargetDiscovery}, "discovery_unreachable", "http_status status=500"},
			{"a discovery document of another issuer", oidcstub.Fault{IssuerMismatch: "https://elsewhere.example.com"}, "issuer_mismatch", "issuer_mismatch"},
			{"no common algorithm", oidcstub.Fault{NoCommonAlgorithm: true}, "algorithms_not_offered", "algorithms_not_offered"},
			{"a key URL at the metadata address", oidcstub.Fault{JWKSURI: "https://169.254.169.254/keys"}, "discovery_unreachable", "egress_denied host=169.254.169.254"},
			{"a body that is no JSON", oidcstub.Fault{NotJSON: true}, "discovery_unreachable", "invalid_response"},
		} {
			if err := d.stub.SetFault(tc.fault); err != nil {
				t.Fatal(err)
			}
			r := post(d.body("validated"))
			wantStatus(t, r, 422, tc.code)
			if detail := r.JSON(t)["detail"].(string); !strings.Contains(detail, tc.match) || strings.Contains(detail, oidcstub.NonJSONMarker) {
				t.Errorf("%s: detail %q", tc.name, detail)
			}
		}
		if err := d.stub.SetFault(oidcstub.Fault{}); err != nil {
			t.Fatal(err)
		}
		if n := e.number("SELECT count(*) FROM ci_issuers WHERE name = 'validated'"); n != 0 || e.epoch("ci_mappings") != before {
			t.Errorf("a failed validation stored %d issuers and moved the epoch by %d", n, e.epoch("ci_mappings")-before)
		}
		// validate: false stores the issuer without contacting it, and the key status is empty.
		d.stub.Reset()
		wantStatus(t, post(d.body("lazy", noValidate)), 201, "")
		if c := d.stub.Counts(); c.Discovery != 0 || c.JWKS != 0 {
			t.Errorf("validate false contacted the issuer: %+v", c)
		}
		k := e.do(a, "GET", issuersPath+"/lazy/keys", nil)
		wantStatus(t, k, 200, "")
		if keys, _ := k.JSON(t)["keys"].([]any); len(keys) != 0 || k.JSON(t)["fetchedAt"] != nil {
			t.Errorf("an issuer that was never fetched lists keys: %s", k.Body)
		}
	})

	t.Run("R-CI-002 the key set is empty of keys until a fetch has stored one", func(t *testing.T) {
		// The criterion of the restore (R-CI-003): ci_issuer_keys is transient, so a restored installation
		// has issuers and no key rows. The row of "lazy" does not exist; the first fetch creates it.
		if n := e.number("SELECT count(*) FROM ci_issuer_keys k JOIN ci_issuers i ON i.id = k.issuer_id WHERE i.name = 'lazy'"); n != 0 {
			t.Fatalf("a key row exists: %d", n)
		}
		r := e.do(a, "POST", issuersPath+"/lazy/keys/refresh", nil)
		wantStatus(t, r, 200, "")
		if keys, _ := r.JSON(t)["keys"].([]any); len(keys) != gitlabKeys {
			t.Errorf("keys after the first fetch: %s", r.Body)
		}
	})
}

func TestR_CI_001_TheDefaultAudiencesFollowTheBaseURL(t *testing.T) {
	d := startDouble(t, oidcstub.Config{})
	e := newEnv(t, options{valkey: true, env: []string{"TROVE_SERVER_BASE_URL=https://packages.example.com:8443"}})
	a := e.web("web-a")
	r := e.do(a, "POST", issuersPath, d.body("gitlab-main", map[string]any{"validate": false}))
	wantStatus(t, r, 201, "")
	if got := fmt.Sprint(r.JSON(t)["allowedAudiences"]); got != "[https://packages.example.com:8443 npm:packages.example.com]" {
		t.Errorf("allowedAudiences = %s", got)
	}
	g := e.do(a, "GET", issuersPath+"/gitlab-main", nil)
	if got := fmt.Sprint(g.JSON(t)["allowedAudiences"]); got != "[https://packages.example.com:8443 npm:packages.example.com]" {
		t.Errorf("the read says %s", got)
	}
}

func TestR_CI_005_AnIssuerIsSelectedByTheExactIssClaim(t *testing.T) {
	// W1's part of the first criterion: the issuer of a token is selected by the exact iss, and the keys it
	// is verified with are that issuer's only. The mappings of the other issuer never come into play
	// because the selection is exact and per issuer (the evaluation is W3's and W4's).
	one, two := startDouble(t, oidcstub.Config{}), startDouble(t, oidcstub.Config{})
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	wantStatus(t, e.do(a, "POST", issuersPath, one.body("gitlab-com")), 201, "")
	wantStatus(t, e.do(a, "POST", issuersPath, two.body("gitlab-internal")), 201, "")
	isOne, isTwo := issuerOf(t, a, one.stub.Issuer()), issuerOf(t, a, two.stub.Issuer())
	if isOne.Name != "gitlab-com" || isTwo.Name != "gitlab-internal" {
		t.Fatalf("selected %q and %q", isOne.Name, isTwo.Name)
	}
	for _, iss := range []string{one.stub.Issuer() + "/", strings.ToUpper(one.stub.Issuer()), one.stub.Issuer() + "x", ""} {
		if _, ok, err := a.App.CIIssuers().Resolve(t.Context(), iss); err != nil || ok {
			t.Errorf("Resolve(%q) = %v, %v: the comparison is exact", iss, ok, err)
		}
	}
	// A token of one instance does not verify with the keys of the other.
	tok := mint(t, one, oidcstub.MintOptions{})
	if err := keyLookup(t, a, isOne, tok); err != nil {
		t.Errorf("with its own issuer's keys: %v", err)
	}
	if err := keyLookup(t, a, isTwo, tok); err == nil {
		t.Error("a token of one instance verified with the keys of another")
	}
}

func TestQ_CI_26_AProjectAdministratorListsTheIssuersReadOnly(t *testing.T) {
	d := startDouble(t, oidcstub.Config{})
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	wantStatus(t, e.do(a, "POST", issuersPath, d.body("gitlab-main")), 201, "")
	wantStatus(t, e.do(a, "POST", "/api/v1/projects", map[string]any{"key": "web", "displayName": "Web"}), 201, "")
	wantStatus(t, e.do(a, "POST", "/api/v1/projects", map[string]any{"key": "data", "displayName": "Data"}), 201, "")
	added := e.do(a, "PUT", "/api/v1/projects/web/admins/user%3Abob", nil)
	wantStatus(t, added, 201, "")
	epoch := added.Header.Get("Trove-Authz-Epoch")

	r := e.as(a, e.bobPAT, "GET", "/api/v1/projects/web/ci/issuers", nil, "Trove-Authz-Min-Epoch", epoch)
	wantStatus(t, r, 200, "")
	items, _ := r.JSON(t)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("items = %v", items)
	}
	item := items[0].(map[string]any)
	if len(item) != 4 || item["name"] != "gitlab-main" || item["kind"] != "gitlab" || item["issuer"] != d.stub.Issuer() || item["publicIssuer"] != false {
		t.Errorf("a project administrator sees %v: name, kind, issuer and publicIssuer only", item)
	}
	if n := e.as(a, e.bobPAT, "GET", "/api/v1/projects/data/ci/issuers", nil); n.Status != 404 {
		t.Errorf("another project's route answered %d", n.Status)
	}
	if n := e.as(a, e.alicePAT, "GET", "/api/v1/projects/web/ci/issuers", nil); n.Status != 404 && n.Status != 403 {
		t.Errorf("a user who administers nothing got %d", n.Status)
	}
	// Read-only: the issuer routes stay closed to the project administrator.
	for _, op := range []struct{ method, path string }{{"POST", issuersPath}, {"PUT", issuersPath + "/gitlab-main"}, {"DELETE", issuersPath + "/gitlab-main"}, {"GET", issuersPath}} {
		if w := e.as(a, e.bobPAT, op.method, op.path, map[string]any{}, "If-Match", "*"); w.Status != 403 {
			t.Errorf("%s %s answered %d to a project administrator", op.method, op.path, w.Status)
		}
	}
}

func TestR_CI_002_AnIssuerThatIssuedTokensIsRevokedThroughTheRevoker(t *testing.T) {
	// An issuer with an issuance record is disabled and deleted through the revoker (W5), in the transaction of
	// the write: the revocation finds the tokens through the records. The record seeded here has no token row (a
	// token removed by its retention task), so nothing is left to revoke and both writes succeed; the tokens'
	// side is revoke_test.go's (TestR_CI_002_DisablingAnIssuerRevokesItsTokens and …DeletingAnIssuer…).
	d := startDouble(t, oidcstub.Config{})
	e := newEnv(t, options{valkey: true})
	a := e.web("web-a")
	wantStatus(t, e.do(a, "POST", issuersPath, d.body("gitlab-main")), 201, "")
	e.seedIssuance("gitlab-main")
	body := e.do(a, "GET", issuersPath+"/gitlab-main", nil).JSON(t)
	body["enabled"] = false
	wantStatus(t, e.do(a, "PUT", issuersPath+"/gitlab-main", body, "If-Match", "*"), 200, "")
	if e.number("SELECT count(*) FROM ci_issuers WHERE NOT enabled") != 1 || e.events("ci.token.revoked") != 0 {
		t.Error("the issuer is not disabled, or a revocation without tokens wrote an event")
	}
	wantStatus(t, e.do(a, "DELETE", issuersPath+"/gitlab-main", nil), 204, "")
	if e.number("SELECT count(*) FROM ci_token_issuances") != 0 {
		t.Error("the records of a deleted issuer remain (Q-CI-20: they cascade)")
	}
}
