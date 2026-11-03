//go:build integration

package transfer

import "testing"

// TestR_VULN_002_AProjectAdministratorDoesNotWeakenTheScanning: manageRepositories lets a project administrator change its
// repositories, but not lift download blocks by the member `vulnerabilities` (Q-VULN-69): the weakening writes answer 403
// vulnerability_weakening_refused, the strengthening ones succeed, and an administrator of trove may weaken.
func TestR_VULN_002_AProjectAdministratorDoesNotWeakenTheScanning(t *testing.T) {
	e := newWebEnv(t)
	e.breakGlass()
	e.project("web", true, true)
	e.projectAdmin("wanda", "web")
	a := e.newReplica("a", webOpts{})
	res := a.postJSON("admin", "/api/v1/repositories", map[string]any{
		"key": "web-local", "class": "local", "format": "npm", "project": "web",
	})
	if res.status != 201 {
		t.Fatalf("create: %d %s", res.status, res.body)
	}
	for _, weaken := range []string{
		`{"vulnerabilities":{"enabled":false}}`,
		`{"vulnerabilities":{"blockMalicious":false}}`,
		`{"vulnerabilities":{"matchPublicAdvisories":"never"}}`,
	} {
		res := patchRepo(a, "wanda", "web-local", weaken)
		if res.status != 403 || res.json(t)["code"] != "vulnerability_weakening_refused" {
			t.Errorf("%s: %d %s", weaken, res.status, res.body)
		}
	}
	if res := patchRepo(a, "wanda", "web-local", `{"vulnerabilities":{"onMissingData":"block","matchPublicAdvisories":"always"}}`); res.status != 200 {
		t.Fatalf("strengthening: %d %s", res.status, res.body)
	}
	// Back to auto and allow is weakening now.
	if res := patchRepo(a, "wanda", "web-local", `{"vulnerabilities":{"onMissingData":"allow"}}`); res.status != 403 {
		t.Errorf("onMissingData block to allow: %d %s", res.status, res.body)
	}
	if res := patchRepo(a, "admin", "web-local", `{"vulnerabilities":{"blockMalicious":false}}`); res.status != 200 {
		t.Fatalf("an administrator of trove weakens: %d %s", res.status, res.body)
	}
	if n := e.number(`SELECT count(*) FROM audit_event WHERE action = 'repository.updated' AND details->>'block' = 'lifted'`); n != 1 {
		t.Errorf("repository.updated events with block lifted = %d, want 1", n)
	}
}
