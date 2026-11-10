//go:build e2e

package e2e

// The setup snippets of the Ansible format, pasted (R-ANS-032, R-ANS-025): what the client setup API renders for a repository is executed
// unchanged in the pod of each image, with the token in $TROVE_TOKEN as the page's credentials=env mode spells it: the ansible.cfg, the
// variable of the token, the netrc line, the install command, the requirements.yml and the publish command.

import (
	"strings"
	"testing"
)

func TestAnsibleSnippets(t *testing.T) {
	fx := ansibleFx(t)
	repo := fx.localRepo(t, "snippets")

	for _, img := range ansibleImages {
		t.Run("R-ANS-025 R-ANS-032 "+img.name+" publishes with the publish snippet and installs with the configure and install snippets", func(t *testing.T) {
			c := fx.client(t, img)
			snippets := fx.setupSnippets(t, repo, "acme.tools", "1.0.0")
			for _, id := range []string{
				"ansible-galaxy.configure.ansible-cfg", "ansible-galaxy.configure.token-env", "ansible-galaxy.configure.netrc",
				"ansible-galaxy.install.collection", "ansible-galaxy.install.requirements", "ansible-galaxy.publish.collection",
			} {
				if snippets[id] == "" {
					t.Fatalf("the setup of %s has no snippet %s: %v", repo, id, sortedKeys(snippets))
				}
			}
			cfgSnippet := snippets["ansible-galaxy.configure.ansible-cfg"]
			if !strings.Contains(cfgSnippet, "url="+ansibleRepoURL(t, repo)) || strings.Contains(strings.ToLower(cfgSnippet), "token") {
				t.Errorf("the ansible.cfg snippet names another address or holds a token:\n%s", cfgSnippet)
			}

			// the publish snippet names ./acme-tools-1.0.0.tar.gz: build the archive under that name, as the page tells the reader to
			publish := snippets["ansible-galaxy.publish.collection"]
			named := collection{ns: "acme", name: "tools", version: "1.0.0", salt: img.name}
			// the pasted snippets in the order of the page: cfg, token variable, netrc, then the commands
			script := "printf %s " + shellQuoteWord(cfgSnippet) + " >ansible.cfg\n" +
				snippets["ansible-galaxy.configure.token-env"] + "\n" +
				snippets["ansible-galaxy.configure.netrc"] + "\n" +
				named.buildScript() + "cp out/" + named.archive() + " ./" + named.archive() + "\n" + publish + "\n" +
				"mkdir -p coll\n" + strings.TrimSpace(snippets["ansible-galaxy.install.collection"]) + " -p coll\n" +
				"test -s coll/ansible_collections/acme/tools/MANIFEST.json"
			// a second image meets the version of the first: the same salt would be a retry, another salt a conflict, so the repository is
			// the first image's and the second only installs
			if img.name != ansibleImages[0].name {
				script = "printf %s " + shellQuoteWord(cfgSnippet) + " >ansible.cfg\n" + snippets["ansible-galaxy.configure.token-env"] + "\n" +
					"mkdir -p coll\n" + strings.TrimSpace(snippets["ansible-galaxy.install.collection"]) + " -p coll\n" +
					"test -s coll/ansible_collections/acme/tools/MANIFEST.json"
			}
			c.must(script)
		})

		t.Run("R-ANS-025 R-ANS-032 "+img.name+" installs with the requirements.yml snippet", func(t *testing.T) {
			c := fx.client(t, img)
			snippets := fx.setupSnippets(t, repo, "acme.tools", "1.0.0")
			req := snippets["ansible-galaxy.install.requirements"]
			script := "printf %s " + shellQuoteWord(snippets["ansible-galaxy.configure.ansible-cfg"]) + " >ansible.cfg\n" +
				snippets["ansible-galaxy.configure.token-env"] + "\n" +
				"printf %s " + shellQuoteWord(req) + " >requirements.yml\n" +
				"ansible-galaxy collection install -r requirements.yml -p coll\n" +
				"test -s coll/ansible_collections/acme/tools/MANIFEST.json"
			c.must(script)
		})
	}
}
