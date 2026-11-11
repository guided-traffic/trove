//go:build e2e

package e2e

// The virtual class of the Ansible format with the real clients (R-ANS-032, R-ANS-022 to R-ANS-024, R-ANS-025): one address over a local
// and a remote member (the counting Galaxy test upstream). A collection published through the virtual repository lands in the deployment
// target and installs through the virtual address; a collection only the remote serves installs through the same address, a dependency
// included; the first publish of a name into the local member claims it, so the remote's versions of that name are not offered
// (R-REPO-023); a version that a path pattern of a grant hides stays hidden from the list and from the install of the principal it is
// granted to.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/ansiblestub"
)

func TestAnsibleVirtual(t *testing.T) {
	fx := ansibleFx(t)
	s := ansibleStub(t)
	s.resetAll(t)
	s.ansibleMode(t, ansiblestub.Mode{})
	t.Cleanup(func() { s.resetAll(t) })

	local := fx.localRepo(t, "vlocal")
	remote := fx.remoteRepo(t, "vremote", nil)
	// a name a remote member serves may be published into the local member only by a holder of manage (R-REPO-048)
	ensurePolicy(t, fx.adm, "e2e-ansible-manage-"+fx.run, []string{local}, [2]any{"sa:" + ansiblePublisher, []string{"manage"}})

	virtual := "ansible-virtual-" + fx.run
	fx.createRepo(t, virtual, map[string]any{
		"key": virtual, "class": "virtual", "format": "ansible",
		"virtual": map[string]any{"members": []string{local, remote}, "deployTarget": local},
	})
	env := tokenEnvVar + "=" + fx.token
	cfg := ansibleCfg(ansibleRepoURL(t, virtual))

	// acme.tools 3.0.0 is published into the local member, which claims the name: the remote serves 1.0.0, 1.1.0 and 2.0.0 of it
	newest := collection{ns: "acme", name: "tools", version: "3.0.0"}
	fx.client(t, ansibleCore221).must(withCfg(ansibleCfg(ansibleRepoURL(t, local)), newest.publishScript()), env)

	// a principal whose grant on the three repositories excludes the archive acme.base 1.2.0 (a path pattern of the grant)
	narrow := ansiblePublisher + "-narrow"
	ensureSA(t, fx.adm, narrow)
	fx.adm.do(http.MethodPost, "/api/v1/permission-policies", map[string]any{
		"name": narrow + "-" + fx.run, "description": "end-to-end tests",
		"resources": map[string]any{
			"repositories": qualifyAll([]string{local, remote, virtual}),
			"paths":        map[string]any{"include": []string{"**"}, "exclude": []string{"collections/acme/base/acme-base-1.2.0.tar.gz"}},
		},
		"entries": []map[string]any{{"principal": "sa:" + narrow, "actions": []string{"read"}}},
	}).want(t, "the policy of the narrow principal", http.StatusCreated)
	t.Cleanup(func() { fx.adm.do(http.MethodDelete, "/api/v1/permission-policies/"+narrow+"-"+fx.run, nil) })
	narrowToken := freshSAToken(t, fx.adm, narrow, 4*3600, map[string]any{"inherit": true}).Token
	ansibleReadableOnEveryReplica(t, narrowToken, virtual)

	t.Run("R-REPO-023 R-ANS-022 R-ANS-032 the name the local member published first is claimed by it: the remote's versions of it are not offered", func(t *testing.T) {
		r := fx.registry(t).get(ansibleRepoPath(virtual)+"/v3/collections/acme/tools/versions/?limit=100").want(t, "the version list", http.StatusOK)
		if !strings.Contains(string(r.Body), `"count":1`) || !strings.Contains(string(r.Body), "3.0.0") || strings.Contains(string(r.Body), "2.0.0") {
			t.Fatalf("the list of a claimed name: %s", r)
		}
	})

	for _, img := range ansibleImages {
		own := collection{ns: "e2e" + fx.run, name: img.name + "virt", version: "1.0.0"}

		t.Run("R-ANS-024 R-ANS-032 "+img.name+" publishes through the virtual repository into its deployment target and installs it from the virtual address", func(t *testing.T) {
			c := fx.client(t, img)
			c.must(withCfg(cfg, own.publishScript()), env)
			// the version is in the local member, and the install through the virtual address returns it
			fx.registry(t).get(ansibleRepoPath(local)+"/v3/collections/"+own.ns+"/"+own.name+"/versions/1.0.0/").want(t, "the version in the deployment target", http.StatusOK)
			out := c.must(withCfg(cfg, "ansible-galaxy collection install "+own.full()+" -p coll >/dev/null\n"+manifestVersions(own.full())), env)
			if !strings.Contains(out, own.full()+" 1.0.0") {
				t.Errorf("the installed version:\n%s", out)
			}
		})

		t.Run("R-ANS-022 R-ANS-032 "+img.name+" installs the local member's collection and the remote member's one, its dependency included, through one address", func(t *testing.T) {
			c := fx.client(t, img)
			out := c.must(withCfg(cfg, "ansible-galaxy collection install acme.tools other.stuff -p coll >/dev/null\n"+manifestVersions("acme.tools", "other.stuff")), env)
			if !strings.Contains(out, "acme.tools 3.0.0") || !strings.Contains(out, "other.stuff 1.0.0") {
				t.Errorf("the installed versions (the local 3.0.0 and the remote one):\n%s", out)
			}
			out = c.must(withCfg(cfg, "ansible-galaxy collection install acme.signed acme.base -p coll >/dev/null\n"+manifestVersions("acme.signed", "acme.base")), env)
			if !strings.Contains(out, "acme.base 1.2.0") {
				t.Errorf("the remote member's collection:\n%s", out)
			}
			if out, err := c.sh(withCfg(cfg, "ansible-galaxy collection install acme.tools:2.0.0 -p coll"), env); err == nil {
				t.Errorf("a version of the claimed name that only the remote has installed:\n%s", tailLines(out, 20))
			}
		})

		t.Run("R-ANS-023 R-AUTHZ-005 R-ANS-032 "+img.name+" a version a path pattern of the grant hides stays hidden from the list and from the install", func(t *testing.T) {
			c := fx.client(t, img)
			narrowEnv := tokenEnvVar + "=" + narrowToken
			out := c.must(withCfg(cfg, "ansible-galaxy collection install acme.base -p coll >/dev/null\n"+manifestVersions("acme.base")), narrowEnv)
			if !strings.Contains(out, "acme.base 1.0.0") {
				t.Errorf("acme.base for the principal whose grant excludes 1.2.0:\n%s", out)
			}
			if out, err := c.sh(withCfg(cfg, "ansible-galaxy collection install acme.base:1.2.0 -p coll"), narrowEnv); err == nil {
				t.Errorf("the hidden version installed:\n%s", tailLines(out, 20))
			}
			r := newAPI(t).withToken(narrowToken).get(ansibleRepoPath(virtual)+"/v3/collections/acme/base/versions/?limit=100").want(t, "the version list", http.StatusOK)
			if strings.Contains(string(r.Body), "1.2.0") || !strings.Contains(string(r.Body), "1.0.0") {
				t.Errorf("the list for the narrow principal: %s", r)
			}
			// the publisher, without the pattern, still sees it
			r = fx.registry(t).get(ansibleRepoPath(virtual)+"/v3/collections/acme/base/versions/?limit=100").want(t, "the version list", http.StatusOK)
			if !strings.Contains(string(r.Body), "1.2.0") {
				t.Errorf("the list for the publisher: %s", r)
			}
		})
	}
}
