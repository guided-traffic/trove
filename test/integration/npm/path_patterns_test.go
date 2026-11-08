//go:build integration

package npm

import (
	"net/http"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The path dimension of a resource set on the npm protocol (R-AUTHZ-005, Q-AUTHZ-250): a protocol request is
// decided on the canonical path of what it serves or writes (R-NPM-003: the package document at the name, a
// tarball at <name>/-/<file>), exactly as /files/ decides the same file, so a grant or a token scope that
// excludes the paths of one package refuses that package on every route, and one that includes the paths of
// a package serves that package through the protocol.
//
//	npm-path: local, packageOwnership off; open-pkg 1.0.0 and secret-pkg 1.0.0
//
//	excluder: read and deploy, paths.exclude secret-pkg/**
//	includer: read, paths.include open-pkg/**
//	scoped:   read without patterns, a token whose scope excludes secret-pkg/**

type pathEnv struct {
	*env
	r                                 *testenv.Replica
	setup, excluder, includer, scoped string
}

func newPathEnv(t *testing.T) *pathEnv {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "npmpath", Env: []string{"TROVE_VULNERABILITIES_SOURCES_OSV_MODE=off"}})
	e := &pathEnv{env: &env{t: t, st: st}}
	st.Repository("npm-path", "local", "npm", `{"local":{"packageOwnership":"off"}}`)
	repos := []string{"npm-path"}
	setup, excluder, includer, scoped := st.User("setup"), st.User("excluder"), st.User("includer"), st.User("scoped")
	st.Policy("path-setup", []string{"read", "deploy", "delete"}, repos, setup)
	st.Policy("path-excluder", []string{"read", "deploy"}, repos, excluder)
	st.Exec(`INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) SELECT id, 'path', 'exclude', 'secret-pkg/**' FROM authz_policy WHERE name = 'path-excluder'`)
	st.Policy("path-includer", []string{"read"}, repos, includer)
	st.Exec(`INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) SELECT id, 'path', 'include', 'open-pkg/**' FROM authz_policy WHERE name = 'path-includer'`)
	st.Policy("path-scoped", []string{"read"}, repos, scoped)
	e.setup = st.Token(setup, scope.Default())
	e.excluder = st.Token(excluder, scope.Default())
	e.includer = st.Token(includer, scope.Default())
	e.scoped = st.Token(scoped, scope.Scope{Repositories: []scope.Grant{{
		Repositories: []string{"it/npm-path"}, Paths: scope.Patterns{Exclude: []string{"secret-pkg/**"}}, Actions: []string{"read"},
	}}})
	e.r = e.replica("web-path")
	e.mustPublish(e.r, "npm-path", e.setup, pkg{Name: "open-pkg", Version: "1.0.0"})
	e.mustPublish(e.r, "npm-path", e.setup, pkg{Name: "secret-pkg", Version: "1.0.0"})
	return e
}

// pathReads are the protocol reads of one package, by the name a failure shows.
func pathReads(name string) map[string]string {
	return map[string]string{
		"package document":       name,
		"version document":       name + "/1.0.0",
		"dist-tag document":      name + "/latest",
		"tarball":                name + "/-/" + name + "-1.0.0.tgz",
		"dist-tags of a package": "-/package/" + name + "/dist-tags",
	}
}

// expect asserts the status of every protocol read of the package for the caller.
func (e *pathEnv) expect(t *testing.T, who, token, name string, status int) {
	t.Helper()
	for what, path := range pathReads(name) {
		if a := e.get(e.r, "npm-path", path, token); a.Status != status {
			t.Errorf("%s, %s of %s: %d, want %d\n%s", who, what, name, a.Status, status, a.Body)
		}
	}
}

func TestR_AUTHZ_005_PathPatternsDecideTheNPMProtocolOnCanonicalPaths(t *testing.T) {
	e := newPathEnv(t)

	t.Run("R-AUTHZ-005 a path exclude in a grant hides the excluded package on every protocol read and serves the others", func(t *testing.T) {
		e.expect(t, "excluder", e.excluder, "secret-pkg", http.StatusNotFound)
		e.expect(t, "excluder", e.excluder, "open-pkg", http.StatusOK)
		// The raw address decides the same file on the same path (R-EP-014): the protocol may not be a way around it.
		if a := e.st.Do(e.r, "GET", "/files/it/npm-path/secret-pkg/-/secret-pkg-1.0.0.tgz", e.excluder, nil); a.Status != http.StatusNotFound {
			t.Errorf("excluder, /files/ of the excluded tarball: %d, want 404", a.Status)
		}
	})

	t.Run("R-AUTHZ-005 a path exclude in a token scope hides the excluded package on every protocol read", func(t *testing.T) {
		e.expect(t, "scoped token", e.scoped, "secret-pkg", http.StatusNotFound)
		e.expect(t, "scoped token", e.scoped, "open-pkg", http.StatusOK)
	})

	t.Run("R-AUTHZ-005 a path include serves the included package through the protocol and hides the others", func(t *testing.T) {
		e.expect(t, "includer", e.includer, "open-pkg", http.StatusOK)
		e.expect(t, "includer", e.includer, "secret-pkg", http.StatusNotFound)
		if a := e.st.Do(e.r, "GET", "/files/it/npm-path/open-pkg/-/open-pkg-1.0.0.tgz", e.includer, nil); a.Status != http.StatusOK {
			t.Errorf("includer, /files/ of the included tarball: %d, want 200", a.Status)
		}
	})

	t.Run("R-AUTHZ-023 a check is decided on the package of its path, and a path of another package answers false", func(t *testing.T) {
		checks := []map[string]string{
			{"repository": "it/npm-path", "package": "open-pkg", "action": "read"},
			{"repository": "it/npm-path", "package": "secret-pkg", "action": "read"},
			{"repository": "it/npm-path", "path": "open-pkg/-/open-pkg-1.0.0.tgz", "action": "read"},
			{"repository": "it/npm-path", "path": "secret-pkg/-/secret-pkg-1.0.0.tgz", "action": "read"},
			{"repository": "it/npm-path", "package": "open-pkg", "path": "open-pkg/-/open-pkg-1.0.0.tgz", "action": "read"},
			{"repository": "it/npm-path", "package": "secret-pkg", "path": "open-pkg/-/open-pkg-1.0.0.tgz", "action": "read"},
			{"repository": "it/npm-path", "package": "open-pkg", "path": "open-pkg/%2e%2e/secret-pkg", "action": "read"},
		}
		want := []bool{true, false, true, false, true, false, false}
		a := e.st.Do(e.r, "POST", "/api/v1/authz/check", e.excluder, map[string]any{"checks": checks})
		if a.Status != http.StatusOK {
			t.Fatalf("check: %d %s", a.Status, a.Body)
		}
		var res struct {
			Results []struct {
				Allowed bool `json:"allowed"`
			} `json:"results"`
		}
		if err := jsonUnmarshal(a.Body, &res); err != nil || len(res.Results) != len(want) {
			t.Fatalf("%v: %s", err, a.Body)
		}
		for i, w := range want {
			if res.Results[i].Allowed != w {
				t.Errorf("check %v: allowed %v, want %v", checks[i], res.Results[i].Allowed, w)
			}
		}
	})

	t.Run("R-AUTHZ-005 a write to an excluded package is refused and one to another package is stored", func(t *testing.T) {
		if a := e.publish(e.r, "npm-path", e.excluder, pkg{Name: "secret-pkg", Version: "2.0.0"}); a.Status != http.StatusForbidden {
			t.Errorf("publish of the excluded package: %d, want 403\n%s", a.Status, a.Body)
		}
		if n := e.versions("npm-path", "secret-pkg"); n != 1 {
			t.Errorf("secret-pkg holds %d versions after the refused publish, want 1", n)
		}
		if a := e.send(e.r, "PUT", "npm-path", "-/package/secret-pkg/dist-tags/beta", e.excluder, "\"1.0.0\""); a.Status != http.StatusForbidden {
			t.Errorf("dist-tag add on the excluded package: %d, want 403\n%s", a.Status, a.Body)
		}
		if a := e.publish(e.r, "npm-path", e.excluder, pkg{Name: "open-pkg", Version: "2.0.0"}); a.Status != http.StatusCreated {
			t.Errorf("publish of another package: %d, want 201\n%s", a.Status, a.Body)
		}
	})
}
