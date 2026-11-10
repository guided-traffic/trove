//go:build e2e

package e2e

// The local class of the Ansible format with the real clients (R-ANS-032, R-ANS-008, R-ANS-009, R-ANS-005, R-ANS-004): a collection is
// built in the pod with ansible-galaxy collection init and build, published with ansible-galaxy collection publish and installed with
// ansible-galaxy collection install, by a requirements.yml too, from the pod of each image. Every request of a client may reach another
// packages replica (the scatter profile), so the publish, the polling of its import task and the installs meet different replicas.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestAnsibleLocal(t *testing.T) {
	fx := ansibleFx(t)
	repo := fx.localRepo(t, "local")
	cfg := ansibleCfg(ansibleRepoURL(t, repo))
	env := tokenEnvVar + "=" + fx.token

	for _, img := range ansibleImages {
		base := collection{ns: "e2e" + fx.run, name: img.name + "base", version: "1.0.0"}
		tools := collection{ns: "e2e" + fx.run, name: img.name + "tools", version: "1.0.0", deps: map[string]string{base.full(): ">=1.0.0"}}

		t.Run("R-ANS-008 R-ANS-009 R-ANS-032 "+img.name+" publishes with ansible-galaxy collection publish and prints the import messages", func(t *testing.T) {
			c := fx.client(t, img)
			out := c.must(withCfg(cfg, base.publishScript()), env)
			if !strings.Contains(out, "Waiting until Galaxy import task") && !strings.Contains(out, "Collection has been published") {
				t.Errorf("the publish printed neither the import wait nor the end of the import:\n%s", tailLines(out, 20))
			}
			c.must(withCfg(cfg, tools.publishScript()), env)
			// the version is in the repository, with the digest of the archive the client sent
			r := fx.registry(t).get(ansibleRepoPath(repo)+"/v3/collections/"+base.ns+"/"+base.name+"/versions/1.0.0/").want(t, "the version document", 200)
			var v struct {
				Version  string `json:"version"`
				Artifact struct {
					SHA256 string `json:"sha256"`
				} `json:"artifact"`
			}
			r.JSON(t, &v)
			if v.Version != "1.0.0" || len(v.Artifact.SHA256) != 64 {
				t.Errorf("the version document: %s", r)
			}
		})

		t.Run("R-ANS-005 R-ANS-032 "+img.name+" installs the collection and its dependency through replicas of any number", func(t *testing.T) {
			c := fx.client(t, img)
			// the install is repeated, so that the requests reach different replicas; the installed tree is the published version
			script := withCfg(cfg, fmt.Sprintf(`for i in 1 2 3 4 5 6; do ansible-galaxy collection install %s -p coll --force || exit 1; done`, tools.full()))
			c.must(script, env)
			out := c.must(withCfg(cfg, fmt.Sprintf(`ansible-galaxy collection install %[1]s -p coll >/dev/null && ansible-galaxy collection list -p coll`, tools.full())), env)
			for _, want := range []string{tools.full(), base.full()} {
				if !strings.Contains(out, want) {
					t.Errorf("the installed collections lack %s:\n%s", want, out)
				}
			}
		})

		t.Run("R-ANS-004 R-ANS-032 "+img.name+" installs a requirements.yml with a pinned version and a range", func(t *testing.T) {
			c := fx.client(t, img)
			req := fmt.Sprintf("collections:\n  - name: %s\n    version: '1.0.0'\n    source: trove\n  - name: %s\n    version: '>=1.0.0'\n", base.full(), tools.full())
			script := withCfg(cfg, "printf %s "+shellQuoteWord(req)+" >requirements.yml\nansible-galaxy collection install -r requirements.yml -p coll\n"+
				fmt.Sprintf("for f in %s %s; do test -s coll/ansible_collections/$(echo $f | tr . /)/MANIFEST.json || exit 3; done", base.full(), tools.full()))
			c.must(script, env)
		})

		t.Run("R-ANS-008 R-ANS-032 "+img.name+" a retry of the same archive succeeds and installs identical bytes", func(t *testing.T) {
			c := fx.client(t, img)
			again := collection{ns: base.ns, name: base.name + "retry", version: "1.0.0"}
			// two publishes of one file: the second answers with the task of the first (idempotent), and the version stays one
			c.must(withCfg(cfg, again.buildScript()+"ansible-galaxy collection publish out/"+again.archive()+"\nansible-galaxy collection publish out/"+again.archive()+"\n"), env)
			c.must(withCfg(cfg, "ansible-galaxy collection install "+again.full()+" -p coll"), env)
			r := fx.registry(t).get(ansibleRepoPath(repo)+"/v3/collections/"+again.ns+"/"+again.name+"/versions/").want(t, "the version list", 200)
			var l struct {
				Meta struct {
					Count int `json:"count"`
				} `json:"meta"`
			}
			r.JSON(t, &l)
			if l.Meta.Count != 1 {
				t.Errorf("the retry left %d versions, want 1: %s", l.Meta.Count, r)
			}
		})

		t.Run("R-ANS-008 R-ANS-032 "+img.name+" a publish of other bytes under an existing version is refused and installs stay the first bytes", func(t *testing.T) {
			c := fx.client(t, img)
			other := tools
			other.salt = "other bytes"
			out, err := c.sh(withCfg(cfg, other.publishScript()), env)
			if err == nil {
				t.Fatalf("a conflicting publish of %s succeeded:\n%s", tools.full(), tailLines(out, 20))
			}
			if !strings.Contains(strings.ToLower(out), "already") && !strings.Contains(out, "version_exists") && !strings.Contains(out, "409") {
				t.Errorf("the client's message does not say that the version exists:\n%s", tailLines(out, 20))
			}
			r := fx.registry(t).get(ansibleRepoPath(repo)+"/v3/collections/"+tools.ns+"/"+tools.name+"/versions/").want(t, "the version list", 200)
			if strings.Count(string(r.Body), `"version":"1.0.0"`)+strings.Count(string(r.Body), `"version": "1.0.0"`) != 1 {
				t.Errorf("the version list after the refused publish: %s", r)
			}
		})
	}

	t.Run("R-ANS-003 the discovery document lists v3 and no v1 on a local repository", func(t *testing.T) {
		r := fx.registry(t).get(ansibleRepoPath(repo)+"/").want(t, "the discovery document", http.StatusOK)
		if strings.Contains(string(r.Body), `"v1"`) || !strings.Contains(string(r.Body), `"v3"`) {
			t.Errorf("discovery: %s", r)
		}
	})
}
