//go:build integration

package npm

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The action map of R-AUTHZ-007 for npm, one case per row: a principal who holds exactly the action
// of the row (plus read, which a package client needs to see the package at all) succeeds, and a
// principal who lacks it fails. Ownership is switched off in the repository, so what is shown is the
// action and nothing else; ownership has its own tests (R-AUTHZ-018). The grants are seeded before the
// replica starts, because its grant index is built from what exists then.

// actEnv is the installation of the action-map tests.
type actEnv struct {
	*env
	setup, readOnly, readDeploy, readDelete, noRead string
	n                                               int
}

func newActEnv(t *testing.T) (*actEnv, *testenv.Replica) {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "npm"})
	e := &actEnv{env: &env{t: t, st: st}}
	st.Repository("npm-act", "local", "npm", "{\"local\":{\"packageOwnership\":\"off\"}}")
	grant := func(name string, actions ...string) string {
		p := st.User(name)
		st.Policy("act-"+name, actions, []string{"npm-act"}, p)
		return st.Token(p, scope.Default())
	}
	e.setup = grant("setup", "read", "deploy", "delete")
	e.readOnly = grant("reader", "read")
	e.readDeploy = grant("deployer", "read", "deploy")
	e.readDelete = grant("deleter", "read", "delete")
	e.noRead = grant("blind", "deploy", "delete") // everything but read
	return e, e.replica("web-a")
}

// fresh publishes 1.0.0 and 1.1.0 of a new package with the setup principal and tags 1.0.0 as stable.
func (e *actEnv) fresh(r *testenv.Replica) string {
	e.t.Helper()
	e.n++
	name := fmt.Sprintf("act-%d", e.n)
	e.mustPublish(r, "npm-act", e.setup, pkg{Name: name, Version: "1.0.0"})
	e.mustPublish(r, "npm-act", e.setup, pkg{Name: name, Version: "1.1.0"})
	if a := e.send(r, "PUT", "npm-act", "-/package/"+name+"/dist-tags/stable", e.setup, "\"1.0.0\""); a.Status != 200 {
		e.t.Fatalf("tag: %d %s", a.Status, a.Body)
	}
	return name
}

func succeeded(a testenv.Answer) bool { return a.Status >= 200 && a.Status < 300 }

func TestR_AUTHZ_007_NPMActionMap(t *testing.T) {
	e, r := newActEnv(t)
	// A case: the operation, who holds exactly its action, who lacks it and what the refusal is.
	cases := []struct {
		row       string
		with, not string
		refused   int
		do        func(name, tok string) testenv.Answer
	}{
		{"package document: read", e.readOnly, e.noRead, 404, func(n, tok string) testenv.Answer {
			return e.get(r, "npm-act", n, tok)
		}},
		{"tarball download: read", e.readOnly, e.noRead, 404, func(n, tok string) testenv.Answer {
			return e.get(r, "npm-act", n+"/-/"+n+"-1.0.0.tgz", tok)
		}},
		{"npm publish of a new version: deploy", e.readDeploy, e.readDelete, 403, func(n, tok string) testenv.Answer {
			return e.publish(r, "npm-act", tok, pkg{Name: n, Version: "2.0.0"})
		}},
		{"npm publish of the first version of a name: deploy", e.readDeploy, e.readDelete, 403, func(n, tok string) testenv.Answer {
			return e.publish(r, "npm-act", tok, pkg{Name: n + "-new", Version: "1.0.0"})
		}},
		{"npm unpublish name: delete", e.readDelete, e.readDeploy, 403, func(n, tok string) testenv.Answer {
			rev, _ := e.revOf(r, n)
			return e.send(r, "DELETE", "npm-act", n+"/-rev/"+rev, tok, nil)
		}},
		{"npm unpublish name@version: delete", e.readDelete, e.readDeploy, 403, func(n, tok string) testenv.Answer {
			rev, doc := e.revOf(r, n)
			delete(doc["versions"].(map[string]any), "1.1.0")
			put := e.send(r, "PUT", "npm-act", n+"/-rev/"+rev, tok, doc)
			if !succeeded(put) {
				return put
			}
			newRev, _ := put.JSON(t)["rev"].(string)
			return e.send(r, "DELETE", "npm-act", n+"/-/"+n+"-1.1.0.tgz/-rev/"+newRev, tok, nil)
		}},
		{"npm dist-tag add: deploy", e.readDeploy, e.readDelete, 403, func(n, tok string) testenv.Answer {
			return e.send(r, "PUT", "npm-act", "-/package/"+n+"/dist-tags/beta", tok, "\"1.1.0\"")
		}},
		{"npm dist-tag rm: deploy", e.readDeploy, e.readDelete, 403, func(n, tok string) testenv.Answer {
			return e.send(r, "DELETE", "npm-act", "-/package/"+n+"/dist-tags/stable", tok, nil)
		}},
	}
	for _, c := range cases {
		t.Run("R-AUTHZ-007 "+c.row, func(t *testing.T) {
			if a := c.do(e.fresh(r), c.with); !succeeded(a) {
				t.Fatalf("with the action: %d %s", a.Status, a.Body)
			}
			a := c.do(e.fresh(r), c.not)
			if a.Status != c.refused {
				t.Fatalf("without the action: %d %s, want %d", a.Status, a.Body, c.refused)
			}
			code(t, a)
		})
	}

	// The revisioned PUT is the unpublish of npm (R-NPM-017): it removes the versions the document lacks and
	// needs delete, nothing else. The dist-tags of its document are not a write of tags (they follow the rule
	// of an unpublish), so a holder of delete only is not refused for them and a holder of deploy only is.
	t.Run("R-AUTHZ-007 the revisioned package document PUT needs delete and writes no tag", func(t *testing.T) {
		put := func(tok string) (testenv.Answer, string) {
			n := e.fresh(r)
			rev, doc := e.revOf(r, n)
			delete(doc["versions"].(map[string]any), "1.1.0")
			doc["dist-tags"].(map[string]any)["moved"] = "1.0.0"
			return e.send(r, "PUT", "npm-act", n+"/-rev/"+rev, tok, doc), n
		}
		if a, _ := put(e.readDeploy); a.Status != http.StatusForbidden {
			t.Fatalf("deploy without delete: %d %s", a.Status, a.Body)
		}
		a, n := put(e.readDelete)
		if a.Status != http.StatusOK {
			t.Fatalf("delete without deploy: %d %s", a.Status, a.Body)
		}
		if _, ok := tagsOfDoc(document(t, e.get(r, "npm-act", n, e.setup)))["moved"]; ok {
			t.Fatal("the dist-tags of an unpublish document were written")
		}
	})

	t.Run("R-AUTHZ-007 npm publish of an existing version: refused by the format, whatever the actions", func(t *testing.T) {
		n := e.fresh(r)
		a := e.publish(r, "npm-act", e.setup, pkg{Name: n, Version: "1.0.0", Files: map[string]string{"x.js": "x"}})
		if a.Status != 409 || code(t, a) != "npm_version_exists" {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})

	t.Run("R-AUTHZ-007 npm whoami: an authenticated subject who can see the repository, no further action", func(t *testing.T) {
		a := e.get(r, "npm-act", "-/whoami", e.readOnly)
		if a.Status != 200 || a.JSON(t)["username"] != "reader" {
			t.Fatalf("with read: %d %s", a.Status, a.Body)
		}
		// an authenticated subject who may not read the repository does not learn that it exists (R-AUTHZ-016)
		if a := e.get(r, "npm-act", "-/whoami", e.st.Token(e.st.User("nobody"), scope.Default())); a.Status != 404 {
			t.Fatalf("without any grant: %d %s", a.Status, a.Body)
		}
		if a := e.get(r, "npm-act", "-/whoami", ""); a.Status != 401 {
			t.Fatalf("anonymous: %d %s", a.Status, a.Body)
		}
	})

	// The rows below name operations the npm format does not implement yet: with every action held, the
	// answer is the 404 of an unimplemented endpoint (R-NPM-043), so the action map has nothing to show.
	// The assertion fails the day one is built, and then gains the with/without pair of the other rows.
	t.Run("R-AUTHZ-007 npm deprecate is not built: 404 with every action", func(t *testing.T) {
		n := e.fresh(r)
		_, doc := e.revOf(r, n)
		doc["versions"].(map[string]any)["1.0.0"].(map[string]any)["deprecated"] = "use 1.1.0"
		if a := e.send(r, "PUT", "npm-act", n, e.setup, doc); a.Status != 404 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})
	t.Run("R-AUTHZ-007 npm search is not built: 404 with every action", func(t *testing.T) {
		if a := e.get(r, "npm-act", "-/v1/search?text=act", e.setup); a.Status != 404 {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})
	t.Run("R-AUTHZ-007 npm ping answers as whoami does: a subject who can see the repository, 404 for one who cannot, 401 for none", func(t *testing.T) {
		if a := e.get(r, "npm-act", "-/ping", e.readOnly); a.Status != 200 {
			t.Fatalf("with read: %d %s", a.Status, a.Body)
		}
		if a := e.get(r, "npm-act", "-/ping", e.st.Token(e.st.User("nobody2"), scope.Default())); a.Status != 404 {
			t.Fatalf("without any grant: %d %s", a.Status, a.Body)
		}
		if a := e.get(r, "npm-act", "-/ping", ""); a.Status != 401 {
			t.Fatalf("anonymous: %d %s", a.Status, a.Body)
		}
	})
}

// revOf reads the _rev and the document the way npm does before it writes, as the setup principal.
func (e *actEnv) revOf(r *testenv.Replica, name string) (string, map[string]any) {
	doc := document(e.t, e.get(r, "npm-act", name+"?write=true", e.setup))
	rev, _ := doc["_rev"].(string)
	return rev, doc
}
