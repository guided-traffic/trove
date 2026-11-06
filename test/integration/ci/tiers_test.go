//go:build integration

package ci

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/integration/testenv"
	"github.com/guided-traffic/trove/test/oidcstub"
)

// The tiers of one token stay apart (R-CI-009, R-CI-016, R-CI-017, Q-CI-26, Q-CI-47): a project tier's mapping
// changes what its own tier gives a job and nothing of what the system tier or another project's tier gives.

// tierEnv is an exchange installation with the system mapping acme-release-publish and the projects web, data and
// shop, each with one npm repository of its own (web-npm, data-npm, shop-npm), of which bob is Owner.
type tierEnv struct {
	*xenv
	a, b  *testenv.Replica
	epoch string
}

func newTierEnv(t *testing.T) *tierEnv {
	t.Helper()
	// ci.token.min_ttl of 1 s lets a project mapping save the maxTtlSeconds 1 of the audit's finding.
	x := newXEnv(t, options{valkey: true, env: []string{"TROVE_CI_TOKEN_MIN_TTL=1s"}})
	e := &tierEnv{xenv: x, a: x.web("web-a"), b: x.web("web-b")}
	x.issuer(e.a)
	x.addMapping(e.a, releaseMapping("acme-release-publish", 100))
	for _, key := range []string{"web", "data", "shop"} {
		wantStatus(t, x.do(e.a, "POST", "/api/v1/projects", map[string]any{"key": key, "displayName": key}), 201, "")
		x.stack.Repository(key+"/"+key+"-npm", "local", "npm", "{}")
		e.epoch = x.addOwner(key, x.bobID)
	}
	return e
}

// bob sends a request as the administrator of the three projects.
func (e *tierEnv) bob(method, path string, body any, hdr ...string) testenv.Answer {
	e.t.Helper()
	return e.as(e.a, e.bobPAT, method, path, body, append([]string{"Trove-Authz-Min-Epoch", e.epoch}, hdr...)...)
}

// projectReader is a project mapping that reads the project's repository for the double's project 1234.
func projectReader(project, name string, extra ...map[string]any) map[string]any {
	return mapping(name, 10, all(eq("project_id", "1234")), grants(inline(project+"-npm", []string{"*"}, "read")),
		append([]map[string]any{{"issuer": xIssuer}}, extra...)...)
}

// addProjectMapping saves a project mapping as bob and waits for every replica's index.
func (e *tierEnv) addProjectMapping(project string, body map[string]any) {
	e.t.Helper()
	wantStatus(e.t, e.bob("POST", projectMappings(project), body), 201, "")
	e.settled()
}

// reads reports whether the token reads the repository on the replica (whoami answers 404 without a grant).
func (e *tierEnv) reads(r *testenv.Replica, token, repo string) bool {
	e.t.Helper()
	return e.stack.Do(r, "GET", npmBase(repo)+"-/whoami", token, nil).Status == 200
}

// alive is the status of GET /api/v1/tokens/current with the token: 200 while it is valid, 401 once revoked.
func (e *tierEnv) alive(r *testenv.Replica, token string) int {
	e.t.Helper()
	return e.stack.Do(r, "GET", "/api/v1/tokens/current", token, nil).Status
}

// setEnabled replaces a project mapping with enabled set as bob.
func (e *tierEnv) setEnabled(project, name string, enabled bool) {
	e.t.Helper()
	path := projectMappings(project) + "/" + xIssuer + "/" + name
	m := e.bob("GET", path, nil)
	wantStatus(e.t, m, 200, "")
	body := m.JSON(e.t)
	body["enabled"] = enabled
	wantStatus(e.t, e.bob("PUT", path, body, "If-Match", etag(m)), 200, "")
	e.settled()
}

func TestQ_CI_47_AProjectTierDoesNotChangeWhatAnotherTierGives(t *testing.T) {
	e := newTierEnv(t)
	a, b := e.a, e.b
	// data-short ends its own contribution after 1 s, shop-broken's template names a claim the token lacks, and
	// web-reader reads web-npm for as long as the token lives.
	e.addProjectMapping("data", projectReader("data", "data-short", map[string]any{"maxTtlSeconds": 1}))
	e.addProjectMapping("shop", mapping("shop-broken", 10, all(eq("project_id", "1234")),
		grants(inline("shop-npm", []string{"@{{ environment | slug }}/*"}, "read")), map[string]any{"issuer": xIssuer}))
	e.addProjectMapping("web", projectReader("web", "web-reader"))
	noEnvironment := oidcstub.MintOptions{Remove: []string{"environment", "environment_protected", "deployment_tier", "environment_action"}}

	g := wantToken(t, e.exchange(a, e.mint(noEnvironment)))
	t.Run("R-CI-016 a project mapping's maxTtlSeconds of 1 s bounds its own contribution, not the token", func(t *testing.T) {
		if g.ExpiresIn < 890 || g.ExpiresIn > 900 {
			t.Fatalf("expires_in %d: the system tier's lifetime (ci.token.default_ttl) is the token's", g.ExpiresIn)
		}
		var bundle struct {
			Contributions []struct {
				Ref       string  `json:"ref"`
				ExpiresAt *string `json:"expiresAt"`
			} `json:"contributions"`
		}
		raw := e.text("SELECT grants::text FROM ci_token_issuances WHERE mapping_names @> '{data/data-short}'")
		if err := json.Unmarshal([]byte(raw), &bundle); err != nil {
			t.Fatal(err)
		}
		for _, c := range bundle.Contributions {
			if (c.Ref == "ci:"+xIssuer+"/data/data-short") != (c.ExpiresAt != nil) {
				t.Errorf("contribution %s expires at %v", c.Ref, c.ExpiresAt)
			}
		}
		within(t, "the end of data-short's contribution", 5*time.Second, func() bool { return !e.reads(b, g.Token, "data/data-npm") })
		for _, repo := range []string{"npm-internal", "web/web-npm"} {
			if !e.reads(b, g.Token, repo) {
				t.Errorf("after data-short's contribution ended the token no longer reads %s", repo)
			}
		}
	})
	t.Run("R-CI-009 a project tier whose grants do not render drops only its own contribution", func(t *testing.T) {
		if want := []string{"acme-release-publish", "data/data-short", "web/web-reader"}; !slices.Equal(g.Mappings, want) {
			t.Fatalf("mappings %v, want %v", g.Mappings, want)
		}
		if e.reads(a, g.Token, "shop/shop-npm") {
			t.Error("the dropped tier's repository is readable")
		}
	})
	t.Run("R-CI-017 disabling a project mapping leaves the system tier's token alive and ends the contribution", func(t *testing.T) {
		start := time.Now()
		e.setEnabled("web", "web-reader", false)
		for _, r := range []*testenv.Replica{a, b} {
			within(t, r.Name+" dropping web-reader's contribution", 10*time.Second, func() bool { return !e.reads(r, g.Token, "web/web-npm") })
			if s := e.alive(r, g.Token); s != 200 {
				t.Errorf("%s answered %d: the token of the system tier was revoked", r.Name, s)
			}
			if !e.reads(r, g.Token, "npm-internal") {
				t.Errorf("%s: the system tier's grants went with the project's mapping", r.Name)
			}
		}
		t.Logf("dropped on both replicas after %s", time.Since(start))
		if n := e.number(`SELECT count(*) FROM api_token WHERE type = 'cit' AND revoked_at IS NOT NULL`); n != 0 {
			t.Errorf("%d CI tokens revoked by a project mapping that owns none", n)
		}
	})
	t.Run("R-CI-017 deleting a project mapping leaves the system tier's token alive and ends the contribution", func(t *testing.T) {
		e.setEnabled("web", "web-reader", true)
		g2 := wantToken(t, e.exchange(a, e.mint(noEnvironment)))
		if !e.reads(b, g2.Token, "web/web-npm") {
			t.Fatal("the new token does not read web-npm")
		}
		wantStatus(t, e.bob("DELETE", projectMappings("web")+"/"+xIssuer+"/web-reader", nil), 204, "")
		e.settled()
		for _, r := range []*testenv.Replica{a, b} {
			within(t, r.Name+" dropping the deleted mapping's contribution", 10*time.Second, func() bool { return !e.reads(r, g2.Token, "web/web-npm") })
			if s := e.alive(r, g2.Token); s != 200 || !e.reads(r, g2.Token, "npm-internal") {
				t.Errorf("%s answered %d: the system tier's token went with the project's mapping", r.Name, s)
			}
		}
	})
}

// Q-CI-47: a token in which no system mapping contributes belongs to its project mapping, whose disable revokes it.
func TestQ_CI_47_AProjectMappingThatOwnsATokenRevokesIt(t *testing.T) {
	e := newTierEnv(t)
	e.addProjectMapping("web", projectReader("web", "web-reader"))
	ce := ciEnvOf(e.env, xIssuer)
	// An unprotected ref: the system mapping does not match, the project's does.
	g := wantToken(t, e.exchange(e.a, e.mint(oidcstub.MintOptions{Claims: map[string]any{"ref_protected": "false"}})))
	if !slices.Equal(g.Mappings, []string{"web/web-reader"}) {
		t.Fatalf("mappings %v", g.Mappings)
	}
	owner := e.text(`SELECT p.name FROM api_token t JOIN principal p ON p.id = t.principal_id JOIN ci_token_issuances s ON s.token_id = t.id WHERE s.mapping_names = '{web/web-reader}'`)
	if owner != xIssuer+"/web/web-reader" {
		t.Fatalf("the token belongs to %s", owner)
	}
	path := projectMappings("web") + "/" + xIssuer + "/web-reader"
	m := e.bob("GET", path, nil)
	body := m.JSON(t)
	body["enabled"] = false
	start := time.Now()
	wantStatus(t, e.bob("PUT", path, body, "If-Match", etag(m)), 200, "")
	ce.refusedEverywhere(t, start, g.Token, e.a, e.b)
}
