//go:build e2e

package e2e

// An external user sees nothing that is only internal (R-AUTHZ-030, R-AUTH-044, T-18): with an internal project, an internal and a public
// file repository, an internal user reads the file of the internal repository and lists the internal project, and the same user flagged
// external gets 404 for the file, finds neither the repository nor the project in the lists and still reads the public file; clearing the
// flag gives everything back. The set access-long runs it with the browser journeys.

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
)

func TestExternalUser_R_AUTHZ_030_SeesNothingInternal(t *testing.T) {
	adm := adminAPI(t)
	useScatter(t)
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	suffix := hex.EncodeToString(b)
	project, intRepo, pubRepo := "ext-"+suffix, "ext-int-"+suffix, "ext-pub-"+suffix
	adm.do(http.MethodPost, "/api/v1/projects", map[string]any{"key": project, "displayName": "External user " + suffix, "visibility": "internal"}).
		want(t, "create the internal project", 201)
	t.Cleanup(func() { adm.do(http.MethodDelete, "/api/v1/projects/"+project, nil) })
	createRepoAt(t, adm, map[string]any{"key": intRepo, "class": "local", "format": "generic"}, map[string]any{"artifacts": "internal", "wiki": "internal"})
	createRepoAt(t, adm, map[string]any{"key": pubRepo, "class": "local", "format": "generic"}, map[string]any{"artifacts": "public", "wiki": "public"})
	t.Cleanup(func() {
		deleteRepository(t, adm, intRepo)
		deleteRepository(t, adm, pubRepo)
	})
	for _, key := range []string{intRepo, pubRepo} {
		adm.do(http.MethodPut, filesAddr(key)+"/a.txt", "x", "Content-Type", "text/plain").want(t, "upload to "+key, 200, 201)
	}
	tok := userToken(t, "bob")
	t.Cleanup(func() { setExternal(t, adm, "bob", false) })
	bob := func() *api { return newAPI(t).withToken(tok) }
	intFile, pubFile := filesAddr(intRepo)+"/a.txt", filesAddr(pubRepo)+"/a.txt"

	t.Run("R-AUTHZ-030 an internal user reads the internal file and lists the internal project", func(t *testing.T) {
		awaitStatusOnEveryReplica(t, "the internal read on every replica", bob, intFile, 200)
		r := bob().get("/api/v1/projects?limit=200").want(t, "the project list", 200)
		if !strings.Contains(string(r.Body), `"`+project+`"`) {
			t.Fatalf("an internal user's project list lacks the internal project %s:\n%s", project, r)
		}
	})

	setExternal(t, adm, "bob", true)
	t.Run("R-AUTHZ-030 R-AUTH-044 an external user gets 404, lists neither project nor repository, and reads the public file", func(t *testing.T) {
		awaitStatusOnEveryReplica(t, "the external flag on every replica", bob, intFile, 404)
		bob().get(pubFile).want(t, "the public file", 200)
		if r := bob().get("/api/v1/projects?limit=200").want(t, "the project list", 200); strings.Contains(string(r.Body), `"`+project+`"`) {
			t.Fatalf("an external user's project list names the internal project:\n%s", r)
		}
		if r := bob().get("/api/v1/repositories?limit=200").want(t, "the repository list", 200); strings.Contains(string(r.Body), `"`+intRepo+`"`) {
			t.Fatalf("an external user's repository list names %s:\n%s", intRepo, r)
		}
		bob().get("/api/v1/projects/"+project).want(t, "the internal project itself", 404)
	})

	setExternal(t, adm, "bob", false)
	t.Run("R-AUTH-044 cleared, the flag gives the internal content back", func(t *testing.T) {
		awaitStatusOnEveryReplica(t, "the cleared flag on every replica", bob, intFile, 200)
	})
}
