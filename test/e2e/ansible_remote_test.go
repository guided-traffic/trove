//go:build e2e

package e2e

// The remote class of the Ansible format with the real clients (R-ANS-032, R-ANS-016 to R-ANS-018, R-ANS-020, R-ANS-015): a remote over
// the counting Galaxy test upstream (deploy/dev/ansiblestub.yaml, test/ansiblestub). Collections come through the Galaxy v3 documents the
// remote rewrites, roles through its translation of the v1 API and the pull-through of the GitHub archive. What the upstream saw is the
// stub's count.

import (
	"fmt"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/ansiblestub"
)

// netrcScript writes the ~/.netrc line of the configure snippet for the edge's host, with the token of the publisher.
const netrcScript = `printf 'machine localhost login trove password %s\n' "$TROVE_TOKEN" >"$HOME/.netrc"
chmod 600 "$HOME/.netrc"
`

func TestAnsibleRemote(t *testing.T) {
	fx := ansibleFx(t)
	s := ansibleStub(t)
	s.resetAll(t)
	s.ansibleMode(t, ansiblestub.Mode{})
	t.Cleanup(func() { s.resetAll(t) })

	repo := fx.remoteRepo(t, "remote", nil)
	cfg := ansibleCfg(ansibleRepoURL(t, repo))
	env := tokenEnvVar + "=" + fx.token
	var afterCollections, afterRoles int

	for i, img := range ansibleImages {
		t.Run("R-ANS-016 R-ANS-017 R-ANS-018 R-ANS-032 "+img.name+" installs acme.tools and its dependency acme.base through the remote", func(t *testing.T) {
			c := fx.client(t, img)
			out := c.must(withCfg(cfg, "ansible-galaxy collection install acme.tools -p coll >/dev/null\n"+manifestVersions("acme.tools", "acme.base")), env)
			// the newest stable version of acme.tools is 2.0.0 (2.1.0-rc.1 is a pre-release); acme.base is its dependency
			if !strings.Contains(out, "acme.tools 2.0.0") || !strings.Contains(out, "acme.base 1.2.0") {
				t.Errorf("the installed versions:\n%s", out)
			}
			if i == 0 {
				afterCollections = s.ansibleCounts(t).Total()
				if afterCollections == 0 {
					t.Fatal("the test upstream saw no request after the first client installed through the remote")
				}
			}
		})
	}

	t.Run("R-ANS-017 R-ANS-018 R-ANS-032 the second client costs the upstream no request, however the replicas were reached", func(t *testing.T) {
		if got := s.ansibleCounts(t).Total(); got != afterCollections {
			t.Fatalf("the test upstream saw %d requests after both clients, %d after the first: %+v", got, afterCollections, s.ansibleCounts(t).Entries)
		}
	})

	t.Run("R-ANS-018 R-ANS-032 the remote serves a pinned version and the upstream's version list in the client's order", func(t *testing.T) {
		c := fx.client(t, ansibleCore221)
		out := c.must(withCfg(cfg, "ansible-galaxy collection install acme.tools:1.1.0 -p coll >/dev/null\n"+manifestVersions("acme.tools")), env)
		if !strings.Contains(out, "acme.tools 1.1.0") {
			t.Errorf("the installed version:\n%s", out)
		}
	})

	for _, img := range ansibleImages {
		t.Run("R-ANS-020 R-ANS-015 R-ANS-032 "+img.name+" installs a role and a collection of one requirements.yml, the role with the netrc line", func(t *testing.T) {
			c := fx.client(t, img)
			req := "roles:\n  - name: geerlingguy.pip\n    version: 2.0.0\ncollections:\n  - name: acme.signed\n"
			script := withCfg(cfg, netrcScript+"printf %s "+shellQuoteWord(req)+" >requirements.yml\n"+
				"ansible-galaxy role install -r requirements.yml -p roles\n"+
				"ansible-galaxy collection install -r requirements.yml -p coll\n"+
				"test -s roles/geerlingguy.pip/meta/main.yml && test -s coll/ansible_collections/acme/signed/MANIFEST.json")
			c.must(script, env)
			if img.name == ansibleImages[0].name {
				afterRoles = s.ansibleCounts(t).Total()
			}
		})
	}

	t.Run("R-ANS-020 R-ANS-032 the second client's role install costs the upstream no request", func(t *testing.T) {
		if got := s.ansibleCounts(t).Total(); got != afterRoles {
			t.Fatalf("the test upstream saw %d requests after both role installs, %d after the first: %+v", got, afterRoles, s.ansibleCounts(t).Entries)
		}
	})

	t.Run("R-ANS-015 R-ANS-032 a role archive without the netrc line fails with the client's 401", func(t *testing.T) {
		c := fx.client(t, ansibleCore221)
		// a role of the stub that no earlier test installed: the archive is not in the cache, and the download carries no token header
		out, err := c.sh(withCfg(cfg, "ansible-galaxy role install geerlingguy.pip,1.1.0 -p roles"), env)
		if err == nil {
			t.Fatalf("a role install without a netrc line succeeded:\n%s", tailLines(out, 20))
		}
		if !unauthorized.MatchString(out) {
			t.Errorf("the client's message does not name the authentication failure:\n%s", tailLines(out, 20))
		}
	})

	t.Run("R-ANS-020 R-ANS-032 the remote drops the role version on a host the operator did not allow", func(t *testing.T) {
		// acme.rolex has 1.0.0 on the GitHub stand-in and 0.9.0 on the unlisted host, which the remote does not allow
		reg := fx.registry(t)
		lookup := reg.get(ansibleRepoPath(repo)+"/v1/roles/?owner__username=acme&name=rolex").want(t, "the role lookup", 200)
		var roles struct {
			Results []struct {
				ID int `json:"id"`
			} `json:"results"`
		}
		lookup.JSON(t, &roles)
		if len(roles.Results) != 1 {
			t.Fatalf("the role lookup: %s", lookup)
		}
		vs := reg.get(fmt.Sprintf("%s/v1/roles/%d/versions/", ansibleRepoPath(repo), roles.Results[0].ID)).want(t, "the role versions", 200)
		if strings.Contains(string(vs.Body), "0.9.0") || !strings.Contains(string(vs.Body), "1.0.0") {
			t.Errorf("the versions offered for acme.rolex: %s", vs)
		}
	})
}

// manifestVersions is a script that prints "<collection> <version>" for each collection installed below ./coll.
func manifestVersions(names ...string) string {
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = shellQuoteWord(n)
	}
	return `python3 -c 'import json,sys
for n in sys.argv[1:]:
    ns, name = n.split(".")
    print(n, json.load(open("coll/ansible_collections/%s/%s/MANIFEST.json" % (ns, name)))["collection_info"]["version"])' ` + strings.Join(quoted, " ")
}
