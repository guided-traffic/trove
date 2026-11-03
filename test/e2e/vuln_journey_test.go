//go:build e2e

package e2e

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestVuln_R_VULN_022_Journeys runs the browser journeys of the report, the finding, the triage and the package page
// (web/e2e/vuln, playwright.vuln.config.ts) against the vuln profile with axe (E4b.6): three web replicas, every request to
// another one, so a journey that passes has read its own writes on another replica. The suite needs `npm ci` in web/ and
// `npx playwright install --with-deps chromium` on the host (the job e2e-vuln does both); without them this test fails and
// says what to run, it does not skip.
func TestVuln_R_VULN_022_Journeys(t *testing.T) {
	dir := filepath.Join(cfg.repo, "web", "e2e")
	if _, err := os.Stat(filepath.Join(cfg.repo, "web", "node_modules", ".bin", "playwright")); err != nil {
		t.Fatal("R-VULN-022: the Playwright suite is not installed; run `npm ci` in web/ and `npx playwright install --with-deps chromium`")
	}
	e := vulnFx(t)
	// The journeys sign in with the break-glass password and as bob; a lockout left by an earlier test would fail the first of
	// them for a reason that is not the UI.
	e.adm.do(http.MethodPost, "/api/v1/users/admin/unlock", nil).want(t, "unlock break-glass", 204)
	// TROVE_E2E_PLAYWRIGHT_ARGS narrows a run while a journey is written; a set of CI never sets it.
	args := append([]string{"playwright", "test", "--config", "playwright.vuln.config.ts"}, strings.Fields(os.Getenv("TROVE_E2E_PLAYWRIGHT_ARGS"))...)
	cmd := exec.Command("npx", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"TROVE_E2E_VULN=1",
		"TROVE_E2E_EDGE_URL="+cfg.edgeURL,
		"TROVE_E2E_KEYCLOAK_ADDR="+keycloakAddr(t),
		"NODE_EXTRA_CA_CERTS="+cfg.caFile,
		"TROVE_E2E_ALICE_PASSWORD="+secretValue(t, "trove-keycloak", "alice-password"),
		"TROVE_E2E_BOB_PASSWORD="+secretValue(t, "trove-keycloak", "bob-password"),
		"TROVE_E2E_BREAKGLASS_PASSWORD="+secretValue(t, "trove-break-glass", "password"),
		"TROVE_E2E_ADMIN_TOKEN="+adminToken(t),
	)
	reportFile := filepath.Join(cfg.repo, ".out", "playwright", "vuln-results.json")
	_ = os.Remove(reportFile)
	cmd.Env = append(cmd.Env, "PLAYWRIGHT_JSON_OUTPUT_NAME="+reportFile)
	out, err := cmd.CombinedOutput()
	_ = os.WriteFile(filepath.Join(cfg.out, "playwright-vuln.log"), out, 0o644)
	specs := playwrightSpecs(t, reportFile)
	for _, sp := range specs {
		t.Run(sp.Title, func(t *testing.T) {
			if !sp.OK {
				t.Errorf("journey %s failed (see playwright-vuln.log and .out/playwright/report)", sp.File)
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
