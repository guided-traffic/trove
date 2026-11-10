//go:build e2e

package e2e

// The mirror mode of the Ansible remote with the real clients (R-ANS-032, R-ANS-021, R-REPO-050, R-REPO-051): a pass stores the collections
// the pattern acme.* selects from the counting Galaxy test upstream, the test upstream's connections are cut, and both clients install from
// the mirror alone, a collection with its dependency included. A collection the pattern does not select is not there.

import (
	"strings"
	"testing"
	"time"

	"github.com/guided-traffic/trove/test/ansiblestub"
)

func TestAnsibleMirror(t *testing.T) {
	fx := ansibleFx(t)
	s := ansibleStub(t)
	s.resetAll(t)
	t.Cleanup(func() { s.resetAll(t) })

	key := "ansible-mirror-" + fx.run
	host := ansibleStubHost(t)
	body := map[string]any{
		"key": key, "class": "remote", "format": "ansible", "requireAuthentication": true,
		"remote": map[string]any{
			"url":  "https://" + host + "/api/",
			"mode": "mirror",
			"extraHosts": []any{
				map[string]any{"pattern": ansibleStubSibling(host, "github"), "sendCredentials": false},
				map[string]any{"pattern": ansibleStubSibling(host, "codeload"), "sendCredentials": false},
			},
			"mirror": map[string]any{"concurrency": 2},
		},
		"includePatterns": []string{"acme.*"},
	}
	fx.adm.do("POST", reposRoute, body).want(t, "create "+key, 201)
	t.Cleanup(func() { deleteRepository(t, fx.adm, key) })
	ansibleReadableOnEveryReplica(t, fx.token, key)

	e := &mirrorEnv{adm: fx.adm}
	e.run_(t, key, false)
	done := e.awaitComplete(t, key, 10*time.Minute)
	sum := done.LastCompletePass
	if sum.Outcome != "complete" || sum.PackagesFailed != 0 || sum.Packages == 0 {
		t.Fatalf("the pass ended %+v, want complete with the acme collections and no failed package", *sum)
	}
	t.Logf("the pass ended %s: %d packages, %d items, %d bytes, method %s", sum.Outcome, sum.Packages, sum.ItemsStored, sum.BytesStored, sum.Method)

	// The upstream goes away: its connections are cut at once (the switch `down: reset`). Scaling its Deployment to zero would leave the
	// Service without an endpoint, and a kind node drops those connections without an answer.
	s.ansibleMode(t, ansiblestub.Mode{Down: ansiblestub.DownReset})
	delivered := func() int {
		c := s.ansibleCounts(t)
		n := 0
		for _, kind := range []string{
			ansiblestub.ClassRoot, ansiblestub.ClassIndex, ansiblestub.ClassCollection, ansiblestub.ClassVersions, ansiblestub.ClassVersion,
			ansiblestub.ClassArtifact, ansiblestub.ClassRole, ansiblestub.ClassRoleArchive, ansiblestub.ClassOther,
		} {
			n += c.Delivered(kind)
		}
		return n
	}
	before := delivered()
	cfg := ansibleCfg(ansibleRepoURL(t, key))
	env := tokenEnvVar + "=" + fx.token

	for _, img := range ansibleImages {
		t.Run("R-ANS-021 R-REPO-050 R-ANS-032 "+img.name+" installs acme.tools and its dependency from the mirror with the upstream cut", func(t *testing.T) {
			c := fx.client(t, img)
			out := c.must(withCfg(cfg, "ansible-galaxy collection install acme.tools -p coll >/dev/null\n"+manifestVersions("acme.tools", "acme.base")), env)
			if !strings.Contains(out, "acme.tools 2.0.0") || !strings.Contains(out, "acme.base 1.2.0") {
				t.Errorf("the installed versions:\n%s", out)
			}
			out = c.must(withCfg(cfg, "ansible-galaxy collection install acme.tools:1.0.0 -p coll >/dev/null\n"+manifestVersions("acme.tools")), env)
			if !strings.Contains(out, "acme.tools 1.0.0") {
				t.Errorf("an older version of the mirror:\n%s", out)
			}
		})

		t.Run("R-ANS-021 R-ANS-032 "+img.name+" a collection the pattern does not select is not in the mirror", func(t *testing.T) {
			c := fx.client(t, img)
			if out, err := c.sh(withCfg(cfg, "ansible-galaxy collection install other.stuff -p coll"), env); err == nil {
				t.Errorf("other.stuff installed from a mirror of acme.*:\n%s", tailLines(out, 20))
			}
		})
	}

	if got := delivered(); got != before {
		t.Fatalf("the test upstream delivered %d answers after its connections were cut, %d before: a client was served by pull-through, not by the mirror", got, before)
	}
}
