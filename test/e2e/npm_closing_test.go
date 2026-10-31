//go:build e2e

package e2e

// The additions of the full run of Test and Release on the commit that closes the phase (roadmap M2, E2.1, E2.2 and
// R-NPM-045): the scenario list and the cold-cache install again with Valkey stopped, and the read-only subset against
// the public registry. The workflow starts them only when it is dispatched with closing=true (Q-NPM-46); a run of
// them without TROVE_E2E_NPM_CLOSING=1 is a failure, never a skip, so they cannot go missing from the closing run.

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
)

func requireClosingRun(t testing.TB) {
	t.Helper()
	if os.Getenv("TROVE_E2E_NPM_CLOSING") != "1" {
		t.Fatal("this test belongs to the closing run: start it with `deploy/dev/e2e.sh npm-closing` (the workflow's closing=true dispatch)")
	}
}

func TestNPMClosing(t *testing.T) {
	requireClosingRun(t)
	fx := npmFx(t)
	requireScatter(t, fx)

	// E2.1, R-NPM-046: the scenario list again, with Valkey and the Sentinels stopped. Every replica stays ready and
	// reports Valkey as degraded; no client operation fails.
	t.Run("R-NPM-045 R-NPM-046 E2.1 the scenario list with Valkey stopped", func(t *testing.T) {
		withValkeyStopped(t, func() {
			webReadyDegraded(t, true)
			runNPMMatrix(t, "matrix-valkey-stopped")
		})
		webReadyDegraded(t, false)
	})

	// E2.2: with Valkey stopped the upstream records at most one request per replica for each document and tarball.
	t.Run("R-NPM-046 E2.2 the cold-cache install with Valkey stopped", func(t *testing.T) {
		withValkeyStopped(t, func() {
			webReadyDegraded(t, true)
			coldInstall(t, fx, true)
		})
		webReadyDegraded(t, false)
	})

	// R-NPM-045: the read-only subset against the public registry, for every gate row: documents and tarballs the real
	// registry serves, through a remote repository and a virtual one over it. Nothing is published, nothing changed.
	t.Run("R-NPM-045 the read-only subset against the public registry", func(t *testing.T) {
		remote, all := "npm-public-"+fx.run+"-remote", "npm-public-"+fx.run+"-all"
		fx.adm.do(http.MethodPost, "/api/v1/repositories", map[string]any{
			"key": remote, "class": "remote", "format": "npm",
			"remote": map[string]any{"url": "https://registry.npmjs.org/"},
		}).want(t, "create "+remote, 201)
		fx.adm.do(http.MethodPost, "/api/v1/repositories", map[string]any{
			"key": all, "class": "virtual", "format": "npm", "requireAuthentication": true,
			"virtual": map[string]any{"members": []string{npmLocalKey, remote}, "deployTarget": npmLocalKey},
		}).want(t, "create "+all, 201)
		ensurePolicy(t, fx.adm, "e2e-npm-public-"+fx.run, []string{remote, all}, [2]any{"sa:" + npmPublisher, []string{"read"}})
		// the shadowing check of every later first publish into npm-local asks this remote too; it goes with the test
		t.Cleanup(func() {
			deleteRepository(t, fx.adm, all)
			deleteRepository(t, fx.adm, remote)
		})
		for i, row := range npmGateRows {
			t.Run(row, func(t *testing.T) {
				t.Parallel()
				c := fx.clientOn(t, row, i%npmClientCount(t), all, "public")
				dir := c.project("public", nil)
				c.configure(dir)
				// small, stable packages that no other test publishes into npm-local (a name npm-local holds is claimed by it and never read from the remote, R-NPM-033)
				specs := []string{"ms@2.1.3", "@types/ms@0.7.34"}
				c.mustInstall(dir, specs...)
				c.assertMarkersLoad(t, dir, "ms")
				c.wipeClientState(dir)
				c.must(dir, c.frozenInstall()...)
				c.assertMarkersLoad(t, dir, "ms")
				if got := strings.TrimSpace(c.viewVersion(dir, "ms@2.1.3")); !strings.Contains(got, "2.1.3") {
					t.Fatalf("%s: view of ms@2.1.3 printed %q", row, got)
				}
			})
		}
	})
}

// assertMarkersLoad requires that Node.js can load the package.
func (c *npmClient) assertMarkersLoad(t *testing.T, dir, name string) {
	t.Helper()
	if r := c.exec(dir, nil, "node", "-e", fmt.Sprintf("require(%q)", name)); r.Code != 0 {
		t.Fatalf("require(%q) in %s: %s", name, dir, r.Out)
	}
}

// viewVersion prints the version of a package spec through the row's own view command.
func (c *npmClient) viewVersion(dir, spec string) string {
	c.t.Helper()
	switch c.family() {
	case "yarn4":
		return c.must(dir, "npm", "info", spec, "--fields", "version", "--json").Out
	case "yarn1":
		return c.must(dir, "info", spec, "version").Out
	default:
		return c.must(dir, "view", spec, "version").Out
	}
}
