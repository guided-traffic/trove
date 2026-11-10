//go:build e2e

package e2e

// The digest of a collection archive through a remote (R-ANS-032, R-ANS-018, Q-ANS-13): the upstream announces a SHA-256 in the version
// document, and trove compares it with the bytes it fetched whatever the policy says. The stub's wrong_digest switch serves other bytes than
// the document announces; the client must not get them, and a repository that refused them is not poisoned for the install that follows.

import (
	"strings"
	"testing"

	"github.com/guided-traffic/trove/test/ansiblestub"
)

func TestAnsibleDigest(t *testing.T) {
	fx := ansibleFx(t)
	s := ansibleStub(t)
	s.resetAll(t)
	t.Cleanup(func() { s.resetAll(t) })

	for _, img := range ansibleImages {
		t.Run("R-ANS-018 R-ANS-032 "+img.name+" is refused the archive whose bytes do not match the announced digest, and installs once the upstream is right", func(t *testing.T) {
			repo := fx.remoteRepo(t, "digest"+img.name, nil)
			cfg := ansibleCfg(ansibleRepoURL(t, repo))
			env := tokenEnvVar + "=" + fx.token
			c := fx.client(t, img)

			s.resetAll(t)
			s.ansibleMode(t, ansiblestub.Mode{WrongDigest: []string{"acme.base"}})
			out, err := c.sh(withCfg(cfg, "ansible-galaxy collection install acme.base:1.0.0 -p coll\n"), env)
			if err == nil {
				t.Fatalf("the install of an archive with the wrong digest succeeded:\n%s", tailLines(out, 20))
			}
			if s.ansibleCounts(t).OfKind(ansiblestub.ClassArtifact, "GET") == 0 {
				t.Errorf("the remote never asked the upstream for the archive: %+v", s.ansibleCounts(t).Entries)
			}
			if _, err := c.sh(withCfg(cfg, "test -e coll/ansible_collections/acme/base/MANIFEST.json"), env); err == nil {
				t.Errorf("the client has a collection although the install failed")
			}

			// the upstream is right again: nothing of the refused bytes stays in the repository
			s.ansibleMode(t, ansiblestub.Mode{})
			out = c.must(withCfg(cfg, "ansible-galaxy collection install acme.base:1.0.0 -p coll >/dev/null\n"+manifestVersions("acme.base")), env)
			if want := "acme.base 1.0.0"; !strings.Contains(out, want) {
				t.Errorf("the install after the upstream was right again printed %q, want %q", out, want)
			}
		})
	}
}
