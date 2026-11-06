//go:build e2e

package e2e

// The harness of the project tests (R-AUTHZ-026, beta phase 5a): an administrator creates a project and makes bob, a user
// without any system role, its administrator; everything inside the project is then done with bob's session, the way a
// team's administrator does it without a system administrator. The format tests are npm_project_test.go and
// nuget_project_test.go; docs/developer/e2e.md describes the whole.

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"testing"
	"time"
)

// partnerProject is one project of a test: the project, its administrator bob (a session on the browser's jar), and the
// names every object of the project carries (`<key>-` is the prefix R-AUTHZ-026 demands).
type partnerProject struct {
	t     testing.TB
	adm   *api     // the system administrator's token, which sets the project up and removes it
	bob   *browser // the project administrator's session
	key   string
	sa    string   // the project service account, `<key>-ci`
	repos []string // every repository the project administrator created, removed with the project
}

// newPartnerProject creates the project `<label>-<run>` with both switches on and bob as its administrator, and waits
// until every api replica knows bob as that administrator. The project, its service account and its repositories go with the
// test.
func newPartnerProject(t *testing.T, adm *api, label string) *partnerProject {
	t.Helper()
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	p := &partnerProject{t: t, adm: adm, key: label + "-" + hex.EncodeToString(b)}
	p.sa = p.key + "-ci"

	p.bob = newBrowser(t)
	p.bob.oidcLogin("bob", secretValue(t, "trove-keycloak", "bob-password"))

	adm.do(http.MethodPost, "/api/v1/projects", map[string]any{
		"key": p.key, "displayName": "Partner " + p.key, "managePolicies": true, "manageRepositories": true,
	}).want(t, "create the project "+p.key, 201)
	t.Cleanup(p.remove)
	adm.do(http.MethodPut, "/api/v1/projects/"+p.key+"/admins/user%3Abob", nil).want(t, "make bob the administrator of "+p.key, 200, 201)

	// the grant reaches the replicas within the bound of R-AUTHZ-028: wait until nine requests, which the scatter profile
	// spreads over the replicas, all list the project in bob's `projects` and show no system role
	type me struct {
		Roles    []string `json:"roles"`
		Projects []struct {
			Key string `json:"key"`
		} `json:"projects"`
	}
	seen := map[string]bool{}
	waitFor(t, "every api replica to list the project among bob's administered projects", time.Minute, 500*time.Millisecond, func() bool {
		clear(seen)
		for range 9 {
			r := p.bob.get("/api/v1/authz/me")
			var m me
			if r.Status != 200 || jsonUnmarshal(string(r.Body), &m) != nil || len(m.Roles) != 0 {
				return false
			}
			found := false
			for _, x := range m.Projects {
				found = found || x.Key == p.key
			}
			if !found {
				return false
			}
			seen[r.Replica()] = true
		}
		return len(seen) >= 2
	})
	return p
}

// remove deletes what the test left: the service account (with its tokens), the repositories, then the project, which
// has to be empty (project_not_empty).
func (p *partnerProject) remove() {
	p.adm.do(http.MethodDelete, "/api/v1/service-accounts/"+p.sa, nil)
	for i := len(p.repos) - 1; i >= 0; i-- {
		deleteRepository(p.t, p.adm, p.repos[i])
	}
	// A deleted repository stays in its project until a worker has removed it (the project counts every lifecycle), so the
	// delete of the project waits for that.
	waitFor(p.t, "the project "+p.key+" to be empty and deleted", 2*time.Minute, 2*time.Second, func() bool {
		r := p.adm.do(http.MethodDelete, "/api/v1/projects/"+p.key, nil)
		return r.Status == 204 || r.Status == 200 || r.Status == 404
	})
}

// repository creates a project repository with the session of the project administrator; the key is `<project>-<suffix>`.
func (p *partnerProject) repository(body map[string]any, suffix string) string {
	p.t.Helper()
	key := p.key + "-" + suffix
	body["key"], body["project"] = key, p.key
	p.bob.do(http.MethodPost, reposRoute, body).want(p.t, "the project administrator creates "+key, 201)
	p.repos = append(p.repos, key)
	return key
}

// serviceAccount creates the project's service account `<key>-ci`.
func (p *partnerProject) serviceAccount() {
	p.t.Helper()
	p.bob.do(http.MethodPost, "/api/v1/service-accounts", map[string]any{
		"name": p.sa, "description": "partner of " + p.key, "project": p.key,
	}).want(p.t, "the project administrator creates "+p.sa, 201)
}

// policy writes a project policy that grants the project's service account the actions on the repositories.
func (p *partnerProject) policy(suffix string, repos []string, actions ...string) {
	p.t.Helper()
	p.bob.do(http.MethodPost, "/api/v1/permission-policies", map[string]any{
		"name": p.key + "-" + suffix, "description": "end-to-end tests", "project": p.key,
		"resources": map[string]any{"repositories": repos},
		"entries":   []map[string]any{{"principal": "sa:" + p.sa, "actions": actions}},
	}).want(p.t, "the project administrator writes the policy "+suffix, 201)
}

// token issues a token of the project service account, bound to no endpoint, that carries the account's grants.
func (p *partnerProject) token() createdToken {
	p.t.Helper()
	r := p.bob.do(http.MethodPost, "/api/v1/service-accounts/"+p.sa+"/tokens", map[string]any{
		"description": "partner token", "ttlSeconds": 3600, "scope": map[string]any{"inherit": true},
	}).want(p.t, "the project administrator issues a token of "+p.sa, 201)
	var c createdToken
	r.JSON(p.t, &c)
	return c
}

// revoke revokes the token with the project administrator's session and waits until the replicas refuse it (the bound of
// R-AUTHZ-028; nine requests reach at least two replicas in the scatter profile).
func (p *partnerProject) revoke(tok createdToken) {
	p.t.Helper()
	p.bob.do(http.MethodDelete, "/api/v1/tokens/"+tok.ID, nil).want(p.t, "the project administrator revokes the token", 200, 204)
	seen := map[string]bool{}
	defer awaitTokenRefusedOnPackagesPods(p.t, tok.Token)
	waitFor(p.t, "the revoked token is refused by every api replica", time.Minute, time.Second, func() bool {
		clear(seen)
		for range 9 {
			r, err := newAPI(p.t).withToken(tok.Token).send(http.MethodGet, "/api/v1/authz/me", nil)
			if err != nil || r.Status != 401 {
				return false
			}
			seen[r.Replica()] = true
		}
		return len(seen) >= 2
	})
}
