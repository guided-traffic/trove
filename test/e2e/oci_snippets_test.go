//go:build e2e

package e2e

// The rendered setup snippets of R-OCI-030, run by the clients they are written for, the way the npm matrix runs the snippets of R-NPM-041:
// the configure snippet signs the client in, the publish snippet pushes an image, the install snippet pulls it again. The snippets are
// those the setup API answers for the repository (credentials=env, so the token is the reference $TROVE_TOKEN the shell expands), run
// unchanged by `sh -c`.

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// setupSnippets are the snippets of the client setup of a repository by id.
func (fx *ociFixture) setupSnippets(t testing.TB, key, client, pkg, version string) map[string]string {
	t.Helper()
	q := url.Values{"client": {client}, "credentials": {"env"}, "package": {pkg}, "version": {version}}
	r := fx.adm.get(repoAPI(key)+"/client-setup?"+q.Encode()).want(t, "client setup of "+key+" for "+client, 200)
	var setup struct {
		Snippets []struct {
			ID      string `json:"id"`
			Tab     string `json:"tab"`
			Content string `json:"content"`
		} `json:"snippets"`
	}
	r.JSON(t, &setup)
	out := map[string]string{}
	for _, s := range setup.Snippets {
		out[s.ID] = s.Content
	}
	return out
}

func TestOCISnippets(t *testing.T) {
	fx := ociFx(t)
	host := ociRegistryHost(t)

	// one local repository per publish snippet: a tag that exists is not moved (R-OCI-013), and the snippets all publish myapp:1.0
	for _, tc := range []struct {
		name, tool, publishID string
		buildx                bool
	}{
		{"docker", ociDocker, "docker.publish", false},
		{"buildx", ociDocker, "docker.publish.buildx", true},
		{"podman", ociPodman, "podman.publish", false},
	} {
		t.Run("R-OCI-030 the "+tc.publishID+" snippet pushes, the install snippet pulls it again with "+tc.tool, func(t *testing.T) {
			key := "oci-snip-" + tc.name + "-" + fx.run
			fx.adm.do(http.MethodPost, reposRoute, map[string]any{"key": key, "class": "local", "format": "oci", "requireAuthentication": true}).want(t, "create "+key, 201)
			t.Cleanup(func() { deleteRepository(t, fx.adm, key) })
			ensurePolicy(t, fx.adm, "e2e-oci-snip-"+tc.name+"-"+fx.run, []string{key}, [2]any{"sa:" + ociPublisher, []string{"read", "deploy", "delete"}})
			ociReadableOnEveryReplica(t, fx.token, key)

			snippets := fx.setupSnippets(t, key, tc.tool, "myapp", "1.0")
			prefix := host + "/" + repoRef(key)
			configure := tc.tool + ".configure.shell"
			for _, id := range []string{configure, tc.publishID, tc.tool + ".install"} {
				content := snippets[id]
				if content == "" {
					t.Fatalf("the setup of %s for %s has no snippet %s: %v", key, tc.tool, id, snippets)
				}
				if !strings.Contains(content, prefix) {
					t.Errorf("snippet %s does not name the image prefix %s:\n%s", id, prefix, content)
				}
			}

			c := fx.client(t, 0, tc.tool, "snippet-"+tc.name)
			// the publish snippets push the image myapp:1.0 of the client (docker tag myapp:1.0 …) or build the working directory
			c.write("Dockerfile", []byte("FROM scratch\nCOPY payload /payload\n"))
			c.write("payload", []byte("snippet "+tc.name+" "+fx.run))
			if !tc.buildx {
				c.exec("build", "--tag", "myapp:1.0", ".")
			}
			run := func(cl *ociClient, id string) {
				t.Helper()
				if out, err := cl.shell(snippets[id]); err != nil {
					t.Fatalf("snippet %s failed: %v\n%s\n%s", id, err, snippets[id], tailLines(out, 25))
				}
			}
			run(c, configure)
			run(c, tc.publishID)
			digest := fx.digestOf(t, key, "myapp", "1.0")

			// the install snippet pulls the pushed image on another client
			other := fx.client(t, 1, tc.tool, "snippet-install-"+tc.name)
			run(other, configure)
			run(other, tc.tool+".install")
			if !other.holds(prefix+"/myapp:1.0", digest) {
				t.Fatalf("%s holds %q after the install snippet, want the digest %s of the publish", tc.tool, other.digests(prefix+"/myapp:1.0"), digest)
			}
		})
	}
}
