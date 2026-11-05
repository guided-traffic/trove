//go:build e2e

package e2e

// The browser journey of the mirror panel (R-REPO-059, R-UI-021) in the profile of the mirror set, where a pool of processes claims the
// queue `mirror`: web/e2e/tests/14-mirror.spec.ts, which TestE1_11Journeys also runs among the others in the scatter profile.

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestMirror_R_REPO_059_BrowserJourney runs the journey of the Mirror tab and of the upstream settings of a remote (status, pause, resume,
// run a pass, run a full pass, with axe in both schemes) against the cluster of the mirror profile. The suite needs `npm ci` in web/ and
// `npx playwright install --with-deps chromium` (CI does both in the job e2e-mirror); without them the test fails and says what to run.
func TestMirror_R_REPO_059_BrowserJourney(t *testing.T) {
	dir := filepath.Join(cfg.repo, "web", "e2e")
	if _, err := os.Stat(filepath.Join(cfg.repo, "web", "node_modules", ".bin", "playwright")); err != nil {
		t.Fatal("R-REPO-059: the Playwright suite is not installed; run `npm ci` in web/ and `npx playwright install --with-deps chromium`")
	}
	if os.Getenv("TROVE_E2E_NPMSTUB_HOST") == "" {
		t.Fatal("TROVE_E2E_NPMSTUB_HOST is not set: the journey types the address of the npm test upstream into the wizard (run the set through deploy/dev/e2e.sh)")
	}
	e := mirrorFx(t)
	// the journeys sign in with the break-glass password and as alice and bob; a lockout left by an earlier test would fail the first
	e.adm.do(http.MethodPost, "/api/v1/users/admin/unlock", nil).want(t, "unlock break-glass", 204)
	cmd := exec.Command("npx", "playwright", "test", "tests/14-mirror.spec.ts")
	cmd.Dir = dir
	reportFile := filepath.Join(cfg.repo, ".out", "playwright", "mirror-results.json")
	_ = os.Remove(reportFile)
	cmd.Env = append(os.Environ(),
		"TROVE_E2E_EDGE_URL="+cfg.edgeURL,
		"TROVE_E2E_KEYCLOAK_ADDR="+keycloakAddr(t),
		"NODE_EXTRA_CA_CERTS="+cfg.caFile,
		"TROVE_E2E_ALICE_PASSWORD="+secretValue(t, "trove-keycloak", "alice-password"),
		"TROVE_E2E_BOB_PASSWORD="+secretValue(t, "trove-keycloak", "bob-password"),
		"TROVE_E2E_BREAKGLASS_PASSWORD="+secretValue(t, "trove-break-glass", "password"),
		"TROVE_E2E_ADMIN_TOKEN="+adminToken(t),
		"PLAYWRIGHT_JSON_OUTPUT_NAME="+reportFile,
	)
	out, err := cmd.CombinedOutput()
	_ = os.WriteFile(filepath.Join(cfg.out, "playwright-mirror.log"), out, 0o644)
	specs := playwrightSpecs(t, reportFile)
	for _, sp := range specs {
		t.Run(sp.Title, func(t *testing.T) {
			if !sp.OK {
				t.Errorf("journey %s failed (see playwright-mirror.log and .out/playwright/report)", sp.File)
			}
		})
	}
	if err != nil {
		t.Fatalf("playwright: %v\n%s", err, tail(string(out), 6000))
	}
	if len(specs) == 0 {
		t.Fatalf("playwright ran no journey:\n%s", tail(string(out), 2000))
	}
	t.Logf("playwright: %d journeys passed\n%s", len(specs), tail(string(out), 1500))
}
