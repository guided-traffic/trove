//go:build integration

package npm

import (
	"encoding/json"
	"testing"
)

// TestR_AUTHZ_034_TheReadmeOfAVersionManifestIsWikiContent: a repository with public artifacts and an internal wiki serves the packument
// and the version document to a caller without a credential, with readme and readmeFilename out of the top level and out of every
// version manifest; a caller with a grant reads the wiki and gets them (R-AUTHZ-034, Q-NPM-88, R-NPM-010, R-NPM-014).
func TestR_AUTHZ_034_TheReadmeOfAVersionManifestIsWikiContent(t *testing.T) {
	e := newEnv(t)
	pub := e.replica("web-1")
	e.mustPublish(pub, "npm-local", e.tokA, pkg{Name: "wikipkg", Version: "1.0.0", Manifest: func(m map[string]any) { m["readmeFilename"] = "README.md" }})
	// The levels are set before the replica that serves the test starts, so that its grant index holds them.
	e.st.Exec("UPDATE project SET visibility = 'public'")
	e.st.Exec("UPDATE repositories SET visibility_artifacts = 'public', visibility_wiki = 'internal' WHERE key = 'npm-local'")
	r := e.replica("web-2")

	manifestOf := func(token, path string) (top, manifest map[string]any) {
		t.Helper()
		a := e.get(r, "npm-local", path, token)
		if a.Status != 200 {
			t.Fatalf("%s: %d %s", path, a.Status, a.Body)
		}
		var doc map[string]any
		if err := json.Unmarshal(a.Body, &doc); err != nil {
			t.Fatal(err)
		}
		if vs, ok := doc["versions"].(map[string]any); ok {
			return doc, vs["1.0.0"].(map[string]any)
		}
		return doc, doc
	}
	for _, path := range []string{"wikipkg", "wikipkg/1.0.0"} {
		top, m := manifestOf("", path)
		for _, k := range []string{"readme", "readmeFilename"} {
			if _, ok := top[k]; ok {
				t.Errorf("%s: an anonymous caller got %s at the top level", path, k)
			}
			if _, ok := m[k]; ok {
				t.Errorf("%s: an anonymous caller got %s in the version manifest", path, k)
			}
		}
		if m["version"] != "1.0.0" {
			t.Errorf("%s: the rest of the manifest changed: %v", path, m)
		}
		_, withGrant := manifestOf(e.tokC, path)
		if withGrant["readmeFilename"] != "README.md" {
			t.Errorf("%s: a caller with a grant lost readmeFilename of the manifest: %v", path, withGrant)
		}
		anon := e.get(r, "npm-local", path, "").Header.Get("ETag")
		granted := e.get(r, "npm-local", path, e.tokC).Header.Get("ETag")
		if anon == "" || anon == granted {
			t.Errorf("%s: the validators %q and %q do not tell the two answers apart", path, anon, granted)
		}
	}
}
