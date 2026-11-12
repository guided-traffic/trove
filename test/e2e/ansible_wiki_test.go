//go:build e2e

package e2e

// The wiki row of R-ANS-046 (R-AUTHZ-034, T-18): a collection whose repository has public artifacts and a private wiki is installed with the
// real client without a credential, while the documentation route refuses the anonymous caller and answers the holder of a grant.

import (
	"net/http"
	"strings"
	"testing"
)

func TestAnsibleDocsWiki_R_ANS_046(t *testing.T) {
	fx := ansibleFx(t)
	key := "ansible-wiki-" + fx.run
	createRepoAt(t, fx.adm, map[string]any{"key": key, "class": "local", "format": "ansible"}, map[string]any{"artifacts": "public", "wiki": "private"})
	t.Cleanup(func() { deleteRepository(t, fx.adm, key) })
	ansibleReadableOnEveryReplica(t, fx.token, key)

	img := ansibleImages[0]
	col := collection{ns: "e2e" + fx.run, name: img.name + "wiki", version: "1.0.0"}
	c := fx.client(t, img)
	c.must(withCfg(ansibleCfg(ansibleRepoURL(t, key)), col.publishScript()), tokenEnvVar+"="+fx.token)
	docs := ansibleRepoPath(key) + "/v3/collections/" + col.ns + "/" + col.name + "/versions/1.0.0/docs-blob/"
	anon := func() *api { return newAPI(t) }

	t.Run("R-AUTHZ-034 R-ANS-046 the real client installs the collection without a credential", func(t *testing.T) {
		awaitStatusOnEveryReplica(t, "the anonymous read of the collection on every replica", anon,
			ansibleRepoPath(key)+"/v3/collections/"+col.ns+"/"+col.name+"/versions/1.0.0/", 200)
		out := c.must(withCfg(ansibleCfg(ansibleRepoURL(t, key)), "ansible-galaxy collection install "+col.full()+" -p coll && ansible-galaxy collection list -p coll"), "TROVE_TOKEN=")
		if !strings.Contains(out, col.full()) {
			t.Errorf("the anonymous install did not install %s:\n%s", col.full(), out)
		}
	})

	t.Run("R-AUTHZ-034 R-ANS-046 the documentation of the version is refused to the anonymous caller and served to a grant", func(t *testing.T) {
		r := anon().do(http.MethodGet, docs, nil).want(t, "the anonymous documentation request", 401)
		if !strings.Contains(strings.ToLower(r.Header.Get("WWW-Authenticate")), "token") && r.Header.Get("WWW-Authenticate") == "" {
			t.Errorf("the refusal carries no challenge: %v", r.Header)
		}
		fx.registry(t).get(docs).want(t, "the documentation with the publisher's grant", 200)
	})
}
