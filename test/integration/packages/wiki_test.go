//go:build integration

package packages

import (
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// TestR_AUTHZ_034_TheReadmeIsWikiContent: a repository whose artifacts are public and whose wiki is internal serves its packages to
// a caller who reads by the level alone and its README to nobody who is external: the package API drops `hasReadme` and answers
// the README operation with the 404 of a version without one, and the npm document leaves `readme` out (R-AUTHZ-034, R-API-058,
// R-NPM-010). The caller who holds a grant reads the wiki with it.
func TestR_AUTHZ_034_TheReadmeIsWikiContent(t *testing.T) {
	e := newEnv(t)
	e.publish("npm-local", spec{Name: "wikipkg", Version: "1.0.0", Readme: "# Wiki package\n\nthe readme text\n"})
	// Public artifacts and an internal wiki, set before the replica that serves the test starts so that its grant index holds them.
	e.st.Exec("UPDATE project SET visibility = 'public'")
	e.st.Exec("UPDATE repositories SET visibility_artifacts = 'public', visibility_wiki = 'internal' WHERE key = 'npm-local'")
	e.st.Exec("UPDATE app_user SET external = true WHERE principal_id = $1::uuid", e.dave.ID)
	other := e.st.User("frank") // an internal user without a grant
	tokFrank := e.st.Token(other, scope.Default())
	e.r = e.st.Start(t, testenv.ReplicaOptions{Name: "web2"})

	id := e.idOf(e.tokB, "wikipkg")
	version := func(token string) map[string]any {
		t.Helper()
		a := e.get(token, "/api/v1/packages/"+id+"/versions/1.0.0")
		if a.Status != 200 {
			t.Fatalf("version: %d %s", a.Status, a.Body)
		}
		return a.JSON(t)
	}
	readme := func(token string) int {
		t.Helper()
		return e.get(token, "/api/v1/packages/"+id+"/versions/1.0.0/readme").Status
	}
	document := func(token string) map[string]any {
		t.Helper()
		a := e.get(token, "/api/npm/it/npm-local/wikipkg")
		if a.Status != 200 {
			t.Fatalf("npm document: %d %s", a.Status, a.Body)
		}
		return a.JSON(t)
	}

	t.Run("a caller with a grant reads the wiki", func(t *testing.T) {
		if version(e.tokB)["hasReadme"] != true || readme(e.tokB) != 200 || document(e.tokB)["readme"] == nil {
			t.Error("bob holds a grant on the repository and reads no README")
		}
	})
	t.Run("an internal user without a grant reads the wiki by the level", func(t *testing.T) {
		if version(tokFrank)["hasReadme"] != true || readme(tokFrank) != 200 || document(tokFrank)["readme"] == nil {
			t.Error("an internal user reads the artifacts and not the internal wiki")
		}
	})
	t.Run("an external user reads the artifacts and not the wiki", func(t *testing.T) {
		v := version(e.tokD)
		if v["hasReadme"] != false {
			t.Errorf("hasReadme = %v for a caller without the wiki", v["hasReadme"])
		}
		if got := readme(e.tokD); got != 404 {
			t.Errorf("the README answers %d, want the 404 of a version without one", got)
		}
		doc := document(e.tokD)
		if _, ok := doc["readme"]; ok {
			t.Error("the npm document carries the README")
		}
		if _, ok := doc["readmeFilename"]; ok {
			t.Error("the npm document carries readmeFilename")
		}
		if doc["name"] != "wikipkg" {
			t.Errorf("the rest of the document changed: %v", doc)
		}
	})
	t.Run("a request without a credential reads the artifacts and not the wiki", func(t *testing.T) {
		if readme("") != 404 {
			t.Error("an anonymous caller reads the README")
		}
		if _, ok := document("")["readme"]; ok {
			t.Error("the npm document for an anonymous caller carries the README")
		}
	})
	t.Run("the validators of the two answers differ", func(t *testing.T) {
		with := e.get(e.tokB, "/api/npm/it/npm-local/wikipkg").Header.Get("ETag")
		without := e.get(e.tokD, "/api/npm/it/npm-local/wikipkg").Header.Get("ETag")
		if with == "" || with == without {
			t.Errorf("etags %q and %q", with, without)
		}
		if got := e.get(e.tokD, "/api/npm/it/npm-local/wikipkg", "If-None-Match", with).Status; got != 200 {
			t.Errorf("the validator of the document with the README is a %d for the caller without it", got)
		}
	})
}
