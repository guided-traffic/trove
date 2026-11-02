//go:build integration

package nuget

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The action map of R-AUTHZ-007 for NuGet, one case per row: a principal who holds exactly the action
// of the row (plus read, which a package client needs to see the feed at all) succeeds, and a principal
// who lacks it fails. Ownership is switched off in the repositories, so what is shown is the action and
// nothing else; ownership has its own tests (R-AUTHZ-018). The grants are seeded before the replica
// starts, because its grant index is built from what exists then.

// actEnv is the installation of the action-map tests.
type actEnv struct {
	*wenv
	setup, readOnly, readDeploy, readDelete, noRead string
	n                                               int
}

func newActEnv(t *testing.T) (*actEnv, *testenv.Replica) {
	t.Helper()
	var opts testenv.StackOptions
	opts.Name = "nugeta"
	st := testenv.NewStack(t, opts)
	e := new(actEnv)
	e.wenv = new(wenv)
	e.t, e.st = t, st
	st.Repository("nuget-act", "local", "nuget", "{\"local\":{\"packageOwnership\":\"off\"}}")
	st.Repository("nuget-act-hard", "local", "nuget", "{\"local\":{\"packageOwnership\":\"off\"},\"nuget\":{\"deleteMode\":\"delete\"}}")
	repos := []string{"nuget-act", "nuget-act-hard"}
	grant := func(name string, actions ...string) string {
		p := st.User(name)
		st.Policy("act-"+name, actions, repos, p)
		return st.Token(p, scope.Default())
	}
	e.setup = grant("setup", "read", "deploy", "delete")
	e.readOnly = grant("reader", "read")
	e.readDeploy = grant("deployer", "read", "deploy")
	e.readDelete = grant("deleter", "read", "delete")
	e.noRead = grant("blind", "deploy", "delete") // everything but read
	return e, e.replica("web-a")
}

// fresh pushes 1.0.0 and 1.1.0 of a new package ID with the setup principal and returns the ID.
func (e *actEnv) fresh(r *testenv.Replica, repo string) string {
	e.t.Helper()
	e.n++
	id := fmt.Sprintf("Act.P%d", e.n)
	e.mustPush(r, repo, e.setup, nupkg(e.t, pkgSpec{id: id, version: "1.0.0"}))
	e.mustPush(r, repo, e.setup, nupkg(e.t, pkgSpec{id: id, version: "1.1.0"}))
	return id
}

func succeeded(a testenv.Answer) bool { return a.Status/100 == 2 }

func TestR_AUTHZ_007_NuGetActionMap(t *testing.T) {
	e, r := newActEnv(t)
	feed := "nuget-act"
	// A case: the operation, who holds exactly its action, who lacks it and what the refusal is.
	cases := []struct {
		row       string
		repo      string
		with, not string
		refused   int
		do        func(id, lower, tok string) testenv.Answer
	}{
		{"service index: read", feed, e.readOnly, e.noRead, 404, func(id, lower, tok string) testenv.Answer {
			return e.get(r, feed, "v3/index.json", tok)
		}},
		{"flat container version list: read", feed, e.readOnly, e.noRead, 404, func(id, lower, tok string) testenv.Answer {
			return e.get(r, feed, "v3/flatcontainer/"+lower+"/index.json", tok)
		}},
		{"flat container package download: read", feed, e.readOnly, e.noRead, 404, func(id, lower, tok string) testenv.Answer {
			return e.get(r, feed, "v3/flatcontainer/"+lower+"/1.0.0/"+lower+".1.0.0.nupkg", tok)
		}},
		{"flat container nuspec: read", feed, e.readOnly, e.noRead, 404, func(id, lower, tok string) testenv.Answer {
			return e.get(r, feed, "v3/flatcontainer/"+lower+"/1.0.0/"+lower+".nuspec", tok)
		}},
		{"registration index: read", feed, e.readOnly, e.noRead, 404, func(id, lower, tok string) testenv.Answer {
			return e.get(r, feed, "v3/registration-semver2/"+lower+"/index.json", tok)
		}},
		{"registration leaf: read", feed, e.readOnly, e.noRead, 404, func(id, lower, tok string) testenv.Answer {
			return e.get(r, feed, "v3/registration-semver2/"+lower+"/1.0.0.json", tok)
		}},
		{"search: read", feed, e.readOnly, e.noRead, 404, func(id, lower, tok string) testenv.Answer {
			return e.get(r, feed, "v3/search?q=act", tok)
		}},
		{"dotnet nuget push of a new version: deploy", feed, e.readDeploy, e.readDelete, 403, func(id, lower, tok string) testenv.Answer {
			return e.push(r, feed, tok, nupkg(t, pkgSpec{id: id, version: "2.0.0"}))
		}},
		{"dotnet nuget push of the first version of an ID: deploy", feed, e.readDeploy, e.readDelete, 403, func(id, lower, tok string) testenv.Answer {
			return e.push(r, feed, tok, nupkg(t, pkgSpec{id: id + ".New", version: "1.0.0"}))
		}},
		{"dotnet nuget delete that unlists: delete", feed, e.readDelete, e.readDeploy, 403, func(id, lower, tok string) testenv.Answer {
			return e.send(r, "DELETE", feed, id+"/1.0.0", tok)
		}},
		{"dotnet nuget delete that deletes: delete", "nuget-act-hard", e.readDelete, e.readDeploy, 403, func(id, lower, tok string) testenv.Answer {
			return e.send(r, "DELETE", "nuget-act-hard", id+"/1.0.0", tok)
		}},
		{"relist: deploy", feed, e.readDeploy, e.readDelete, 403, func(id, lower, tok string) testenv.Answer {
			return e.send(r, "POST", feed, id+"/1.0.0", tok)
		}},
		{"symbol package push: deploy", feed, e.readDeploy, e.readDelete, 403, func(id, lower, tok string) testenv.Answer {
			return e.pushSymbols(r, feed, tok, snupkg(t, id, "1.0.0", "", nil))
		}},
	}
	for _, c := range cases {
		t.Run("R-AUTHZ-007 "+c.row, func(t *testing.T) {
			id := e.fresh(r, c.repo)
			if c.row == "relist: deploy" {
				if a := e.send(r, "DELETE", c.repo, id+"/1.0.0", e.setup); a.Status != http.StatusNoContent {
					t.Fatalf("unlist: %d %s", a.Status, a.Body)
				}
			}
			if a := c.do(id, lower(id), c.with); !succeeded(a) {
				t.Fatalf("with the action: %d %s", a.Status, a.Body)
			}
			id = e.fresh(r, c.repo)
			if c.row == "relist: deploy" {
				if a := e.send(r, "DELETE", c.repo, id+"/1.0.0", e.setup); a.Status != http.StatusNoContent {
					t.Fatalf("unlist: %d %s", a.Status, a.Body)
				}
			}
			a := c.do(id, lower(id), c.not)
			if a.Status != c.refused {
				t.Fatalf("without the action: %d %s, want %d", a.Status, a.Body, c.refused)
			}
		})
	}

	t.Run("R-AUTHZ-007 the refusal of a delete changes nothing", func(t *testing.T) {
		id := e.fresh(r, feed)
		if a := e.send(r, "DELETE", feed, id+"/1.0.0", e.readDeploy); a.Status != http.StatusForbidden {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
		if e.listed(feed, lower(id), "1.0.0") != "true" {
			t.Fatal("a refused delete unlisted the version")
		}
	})

	t.Run("R-AUTHZ-007 push of an existing version: refused by the format, whatever the actions", func(t *testing.T) {
		id := e.fresh(r, feed)
		a := e.push(r, feed, e.setup, nupkg(t, pkgSpec{id: id, version: "1.0.0", description: "other bytes"}))
		if a.Status != http.StatusConflict || code(t, a) != "nuget_version_exists" {
			t.Fatalf("existing: %d %s", a.Status, a.Body)
		}
	})
	t.Run("R-AUTHZ-007 push of a deleted version: refused by the format, whatever the actions", func(t *testing.T) {
		id := e.fresh(r, "nuget-act-hard")
		if a := e.send(r, "DELETE", "nuget-act-hard", id+"/1.0.0", e.setup); a.Status != http.StatusNoContent {
			t.Fatalf("delete: %d %s", a.Status, a.Body)
		}
		a := e.push(r, "nuget-act-hard", e.setup, nupkg(t, pkgSpec{id: id, version: "1.0.0"}))
		if a.Status != http.StatusConflict || code(t, a) != "nuget_version_deleted" {
			t.Fatalf("deleted: %d %s", a.Status, a.Body)
		}
	})

	// Autocomplete is not built (P1): with every action held, the feed answers 501 nuget_not_implemented for a resource it does not
	// have, so the action map has nothing to show. The assertion fails the day it is built and then gains
	// the with/without pair of the search row.
	t.Run("R-AUTHZ-007 autocomplete is not built: 501 nuget_not_implemented with every action", func(t *testing.T) {
		if a := e.get(r, feed, "v3/autocomplete?q=act", e.setup); a.Status != http.StatusNotImplemented || code(t, a) != "nuget_not_implemented" {
			t.Fatalf("%d %s", a.Status, a.Body)
		}
	})
}

func lower(s string) string { return strings.ToLower(s) }

// get sends a GET of a path below the registry URL of a repository.
func (e *actEnv) get(r *testenv.Replica, repo, path, token string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(r, "GET", "/api/nuget/"+repo+"/"+path, token, nil)
}
