//go:build e2e

package e2e

// The addition of the full run of Test and Release on the commit that closes the phase (R-ANS-028, R-ANS-032): the Ansible clients with
// Valkey stopped. The workflow starts it only when it is dispatched with closing=true; a run of it without TROVE_E2E_ANSIBLE_CLOSING=1 is a
// failure, never a skip, so it cannot go missing from the closing run (deploy/dev/e2e.sh ansible-closing).

import (
	"os"
	"testing"
)

func requireAnsibleClosingRun(t testing.TB) {
	t.Helper()
	if os.Getenv("TROVE_E2E_ANSIBLE_CLOSING") != "1" {
		t.Fatal("this test belongs to the closing run: start it with deploy/dev/e2e.sh ansible-closing (the closing=true dispatch of the workflow)")
	}
}

// TestAnsibleClosing: with Valkey stopped, a publish, the polling of its import task and installs through a local repository, and an install
// through a remote over the counting Galaxy test upstream, succeed on both clients (R-ANS-028). Nothing of the format is read from Valkey:
// the rows are PostgreSQL and S3, and the flight of a remote file falls back to one upstream request per replica (R-ARCH-033).
func TestAnsibleClosing(t *testing.T) {
	requireAnsibleClosingRun(t)
	fx := ansibleFx(t)
	s := ansibleStub(t)
	s.resetAll(t)
	t.Cleanup(func() { s.resetAll(t) })

	local := fx.localRepo(t, "cllocal")
	remote := fx.remoteRepo(t, "clremote", nil)
	env := tokenEnvVar + "=" + fx.token

	withValkeyStopped(t, func() {
		servingReadyDegraded(t, true)
		for _, img := range ansibleImages {
			t.Run("R-ANS-028 "+img.name+" publishes and installs through a local repository with Valkey stopped", func(t *testing.T) {
				c := fx.client(t, img)
				col := collection{ns: "e2e" + fx.run, name: img.name + "closing", version: "1.0.0"}
				cfg := ansibleCfg(ansibleRepoURL(t, local))
				c.must(withCfg(cfg, col.publishScript()), env)
				c.must(withCfg(cfg, "ansible-galaxy collection install "+col.full()+" -p coll\ntest -s coll/ansible_collections/"+col.ns+"/"+col.name+"/MANIFEST.json"), env)
			})
			t.Run("R-ANS-028 "+img.name+" installs through a remote with Valkey stopped", func(t *testing.T) {
				c := fx.client(t, img)
				c.must(withCfg(ansibleCfg(ansibleRepoURL(t, remote)), "ansible-galaxy collection install acme.tools -p coll\ntest -s coll/ansible_collections/acme/tools/MANIFEST.json"), env)
			})
		}
	})
}
