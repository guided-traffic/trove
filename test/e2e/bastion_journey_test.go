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

// TestBastion_R_BAS_007_Journey runs the browser journey of the Bastions screen (web/e2e/bastion/14-bastions.spec.ts): the
// administrator sees the last contact of the e2e Bastion's replicas, registers a Bastion, issues a token that expires in 20
// days, copies it once and sees the warning mark. The suite needs `npm ci` in web/ and `npx playwright install --with-deps
// chromium` on the host (the job e2e-bastion does both); without them this test fails and says what to run, it does not skip.
func TestBastion_R_BAS_007_Journey(t *testing.T) {
	dir := filepath.Join(cfg.repo, "web", "e2e")
	if _, err := os.Stat(filepath.Join(cfg.repo, "web", "node_modules", ".bin", "playwright")); err != nil {
		t.Fatal("R-BAS-007: the Playwright suite is not installed; run `npm ci` in web/ and `npx playwright install --with-deps chromium`")
	}
	env := basFx(t)
	env.adm.do(http.MethodPost, "/api/v1/users/admin/unlock", nil).want(t, "unlock break-glass", 204)
	cmd := exec.Command("npx", "playwright", "test", "--config", "playwright.bastion.config.ts")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"TROVE_E2E_EDGE_URL="+cfg.edgeURL,
		"NODE_EXTRA_CA_CERTS="+cfg.caFile,
		"TROVE_E2E_ALICE_PASSWORD="+secretValue(t, "trove-keycloak", "alice-password"),
		"TROVE_E2E_BOB_PASSWORD="+secretValue(t, "trove-keycloak", "bob-password"),
		"TROVE_E2E_BREAKGLASS_PASSWORD="+secretValue(t, "trove-break-glass", "password"),
		"TROVE_E2E_ADMIN_TOKEN="+adminToken(t),
		"TROVE_E2E_BASTION_REGISTRATION="+env.vol.reg,
		"TROVE_E2E_BASTION_REPLICAS="+strings.Join(podNames(basReadyPods(t, env.vol)), ","),
	)
	reportFile := filepath.Join(cfg.repo, ".out", "playwright", "bastion-results.json")
	_ = os.Remove(reportFile)
	cmd.Env = append(cmd.Env, "PLAYWRIGHT_JSON_OUTPUT_NAME="+reportFile)
	out, err := cmd.CombinedOutput()
	_ = os.WriteFile(filepath.Join(cfg.out, "playwright-bastion.log"), out, 0o644)
	specs := playwrightSpecs(t, reportFile)
	for _, sp := range specs {
		t.Run(sp.Title, func(t *testing.T) {
			if !sp.OK {
				t.Errorf("journey %s failed (see playwright-bastion.log and .out/playwright/report)", sp.File)
			}
		})
	}
	if err != nil {
		t.Fatalf("playwright: %v\n%s", err, tail(string(out), 6000))
	}
	if len(specs) == 0 {
		t.Fatalf("playwright ran no journey:\n%s", tail(string(out), 2000))
	}
}
