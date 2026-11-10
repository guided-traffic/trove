//go:build e2e

package e2e

// The credentials of the Ansible clients (R-ANS-032, R-ANS-006, R-ANS-015): the token in ansible.cfg, in the environment variable of the
// server and on the command line (the scheme Token), a username and the token as the password (HTTP Basic), a wrong token and no token on a
// repository that requires authentication. The roles' netrc is in ansible_remote_test.go, because only a remote serves roles.

import (
	"regexp"
	"testing"
)

// unauthorized matches how the clients word a 401 of the Galaxy API.
var unauthorized = regexp.MustCompile(`(?i)401|unauthori[sz]ed|not authenticated|authentication credentials`)

func TestAnsibleAuth(t *testing.T) {
	fx := ansibleFx(t)
	repo := fx.localRepo(t, "auth")
	url := ansibleRepoURL(t, repo)

	for _, img := range ansibleImages {
		pub := collection{ns: "e2e" + fx.run, name: img.name + "auth", version: "1.0.0"}

		t.Run("R-ANS-006 R-ANS-032 "+img.name+" publishes with the token on the command line", func(t *testing.T) {
			c := fx.client(t, img)
			// --token belongs to --server: ansible-core applies it to the server named on the command line, not to those of ansible.cfg
			c.must(pub.buildScript() + "ansible-galaxy collection publish out/" + pub.archive() + " --server " + shellQuoteWord(url) + ` --token "$TROVE_TOKEN"`)
		})

		for name, tc := range map[string]struct {
			cfg string
			env []string
		}{
			"token= in ansible.cfg":                         {cfg: ansibleCfg(url, "token="+fx.token)},
			"the environment variable of the server":        {cfg: ansibleCfg(url), env: []string{tokenEnvVar + "=" + fx.token}},
			"a username and the token as the password":      {cfg: ansibleCfg(url, "username=e2e", "password="+fx.token)},
			"the environment variable beats a wrong token=": {cfg: ansibleCfg(url, "token=trv_pat_wrong"), env: []string{tokenEnvVar + "=" + fx.token}},
		} {
			t.Run("R-ANS-006 R-ANS-032 "+img.name+" installs with "+name, func(t *testing.T) {
				c := fx.client(t, img)
				c.must(withCfg(tc.cfg, "ansible-galaxy collection install "+pub.full()+" -p coll\ntest -s coll/ansible_collections/"+pub.ns+"/"+pub.name+"/MANIFEST.json"), tc.env...)
			})
		}

		t.Run("R-ANS-006 R-ANS-032 "+img.name+" a wrong token is refused with the 401 of the server", func(t *testing.T) {
			c := fx.client(t, img)
			out, err := c.sh(withCfg(ansibleCfg(url, "token=trv_pat_00000000000000000000000000000000"), "ansible-galaxy collection install "+pub.full()+" -p coll"))
			if err == nil {
				t.Fatalf("an install with a wrong token succeeded:\n%s", tailLines(out, 20))
			}
			if !unauthorized.MatchString(out) {
				t.Errorf("the client's message does not name the authentication failure:\n%s", tailLines(out, 20))
			}
		})

		t.Run("R-ANS-003 R-ANS-032 "+img.name+" no token on a repository that requires one fails at the first request", func(t *testing.T) {
			c := fx.client(t, img)
			out, err := c.sh(withCfg(ansibleCfg(url), "ansible-galaxy collection install "+pub.full()+" -p coll"))
			if err == nil {
				t.Fatalf("an install without a token succeeded:\n%s", tailLines(out, 20))
			}
			if !unauthorized.MatchString(out) {
				t.Errorf("the client's message does not name the authentication failure:\n%s", tailLines(out, 20))
			}
		})

		t.Run("R-ANS-008 R-ANS-032 "+img.name+" a publish without a token is refused", func(t *testing.T) {
			c := fx.client(t, img)
			again := collection{ns: pub.ns, name: pub.name + "anon", version: "1.0.0"}
			if out, err := c.sh(withCfg(ansibleCfg(url), again.publishScript())); err == nil {
				t.Fatalf("a publish without a token succeeded:\n%s", tailLines(out, 20))
			}
		})
	}
}
