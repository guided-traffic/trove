//go:build integration

package registry

import (
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/registrytoken"
	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// A caller whose grant has path patterns obtains a registry token for the images the patterns admit (R-OCI-006, R-AUTHZ-005): the
// token service asks the decision point on the path of the image's package document, so an include list and an exclude list decide
// a name as the handlers do. The token is a ceiling and every request is decided again (R-OCI-005).
func TestR_OCI_006_APathScopedCallerGetsARegistryTokenForTheImagesItsPatternsAdmit(t *testing.T) {
	var including, excluding string
	e := newEnv(t, func(e *env, _ *testenv.ReplicaOptions) {
		inc, exc := e.st.User("pathinc"), e.st.User("pathexc")
		e.st.Policy("oci-path-include", []string{"read"}, []string{"web/docker-local"}, inc)
		e.st.Exec(`INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) SELECT id, 'path', 'include', 'team/**' FROM authz_policy WHERE name = 'oci-path-include'`)
		e.st.Policy("oci-path-exclude", []string{"read"}, []string{"web/docker-local"}, exc)
		e.st.Exec(`INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) SELECT id, 'path', 'exclude', 'secret/**' FROM authz_policy WHERE name = 'oci-path-exclude'`)
		including, excluding = e.st.Token(inc, scope.Default()), e.st.Token(exc, scope.Default())
	})
	for _, n := range []string{"team/app", "other/app", "secret/app"} {
		if a := e.push(bearer(e.admin), "web/docker-local/"+n, "1", newImage(n)); a.Code != http.StatusCreated {
			t.Fatalf("push %s: %d %s", n, a.Code, a.Body.String())
		}
	}
	pull := func(rgt, name string) int {
		return e.send("default", req{method: "GET", path: "/v2/web/docker-local/" + name + "/manifests/1", auth: bearer(rgt), header: map[string]string{"Accept": manifestType}}).Code
	}
	grant := func(names ...string) registrytoken.Grant {
		var g registrytoken.Grant
		for _, n := range names {
			g.Entries = append(g.Entries, registrytoken.Entry{Repo: local, Name: n, Actions: registrytoken.Pull})
		}
		return g
	}
	t.Run("an include list pulls the image it admits and is refused the others", func(t *testing.T) {
		rgt := e.mint("default", "", including, grant("team/app", "other/app"), false)
		if c := pull(rgt, "team/app"); c != http.StatusOK {
			t.Fatalf("team/app: %d", c)
		}
		if c := pull(rgt, "other/app"); c == http.StatusOK {
			t.Fatalf("other/app was served")
		}
	})
	t.Run("an exclude list pulls the images it does not name", func(t *testing.T) {
		rgt := e.mint("default", "", excluding, grant("team/app", "secret/app"), false)
		if c := pull(rgt, "team/app"); c != http.StatusOK {
			t.Fatalf("team/app: %d", c)
		}
		if c := pull(rgt, "secret/app"); c == http.StatusOK {
			t.Fatalf("secret/app was served")
		}
	})
}
