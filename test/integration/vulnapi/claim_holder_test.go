//go:build integration

package vulnapi

import (
	"encoding/json"
	"testing"

	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_VULN_010_TheClaimHolderIsTheRulesNotAPatternMatch (A5, Q-VULN-70): a claim of a local repository that sets
// matchPublicAdvisories always serves routing only (R-VULN-010), so the claimed-name matches name no holder for it, and a
// name a second repository's claim covers is shown with that repository.
func TestR_VULN_010_TheClaimHolderIsTheRulesNotAPatternMatch(t *testing.T) {
	e := newEnv(t)
	e.st.Repository("npm-public", "local", "npm", `{"local":{"packageOwnership":"off"},"vulnerabilities":{"matchPublicAdvisories":"always"}}`)
	e.st.Exec(`INSERT INTO namespace_claims (repo_id, pattern, source, scope) SELECT id, '@pub/*', 'manual', 'members' FROM repositories WHERE key = 'npm-public'`)
	e.st.Exec(`INSERT INTO namespace_claims (repo_id, pattern, source, scope) SELECT id, '@acme/*', 'manual', 'members' FROM repositories WHERE key = 'npm-local'`)
	for _, n := range []string{"@pub/x", "@acme/y"} {
		e.st.Exec(`INSERT INTO vuln_claimed_match (ecosystem, advisory_id, name, kind) VALUES ('npm', 'MAL-2026-1300', $1, 'malicious')`, n)
	}
	web2 := e.st.Start(t, testenv.ReplicaOptions{Name: "web2", Env: []string{"TROVE_API_RATE_LIMITS_ENABLED=false"}}) // loads the repository and the claims
	a := e.get(web2, "/api/v1/vulnerabilities/claimed-name-matches", e.tokRita)
	if a.Status != 200 {
		t.Fatalf("%d %s", a.Status, a.Body)
	}
	var p struct {
		Items []struct {
			Name            string
			ClaimRepository *string
		}
	}
	if err := json.Unmarshal(a.Body, &p); err != nil {
		t.Fatal(err)
	}
	holder := map[string]string{}
	for _, it := range p.Items {
		holder[it.Name] = "<none>"
		if it.ClaimRepository != nil {
			holder[it.Name] = *it.ClaimRepository
		}
	}
	if holder["@acme/y"] != "it/npm-local" {
		t.Errorf("@acme/y is held by %q", holder["@acme/y"])
	}
	if holder["@pub/x"] != "<none>" {
		t.Errorf("a claim of a repository that declares public packages is shown as the holder: %q", holder["@pub/x"])
	}
}
