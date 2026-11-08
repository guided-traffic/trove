//go:build integration

package nuget

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
	"github.com/guided-traffic/trove/test/integration/testenv"
)

// The path dimension of a resource set on the NuGet protocol (R-AUTHZ-005, Q-AUTHZ-250): a protocol request
// is decided on the canonical path of what it serves or writes (R-NUGET-005), exactly as /files/ decides the
// same file, so a grant or a token scope that excludes the paths of one ID refuses that ID on every route,
// and one that includes the paths of an ID serves that ID through the protocol.
//
//	nuget-path: local, packageOwnership off; Acme.Open 1.0.0 and Acme.Secret 1.0.0
//
//	excluder: read and deploy, paths.exclude acme.secret/**
//	includer: read, paths.include acme.open/**
//	scoped:   read without patterns, a token whose scope excludes acme.secret/**

type pathEnv struct {
	*wenv
	r                                        *testenv.Replica
	setup, excluder, includer, scoped, upper string
}

func newPathEnv(t *testing.T) *pathEnv {
	t.Helper()
	st := testenv.NewStack(t, testenv.StackOptions{Name: "nugetpath"})
	e := &pathEnv{wenv: &wenv{t: t, st: st}}
	st.Repository("nuget-path", "local", "nuget", `{"local":{"packageOwnership":"off"}}`)
	repos := []string{"nuget-path"}
	setup, excluder, includer, scoped := st.User("setup"), st.User("excluder"), st.User("includer"), st.User("scoped")
	st.Policy("path-setup", []string{"read", "deploy", "delete"}, repos, setup)
	st.Policy("path-excluder", []string{"read", "deploy"}, repos, excluder)
	st.Exec(`INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) SELECT id, 'path', 'exclude', 'acme.secret/**' FROM authz_policy WHERE name = 'path-excluder'`)
	st.Policy("path-includer", []string{"read"}, repos, includer)
	st.Exec(`INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) SELECT id, 'path', 'include', 'acme.open/**' FROM authz_policy WHERE name = 'path-includer'`)
	st.Policy("path-scoped", []string{"read"}, repos, scoped)
	// A policy whose exclude is written in upper case, as a broad selector brings one to a NuGet repository created after the
	// policy was saved, where the write-time refusal could not see the repository: it is folded where it is compiled
	// (Q-AUTHZ-254). The row is written directly, since the policy API refuses it for a repository that exists.
	upper := st.User("upper")
	st.Policy("path-upper", []string{"read"}, repos, upper)
	st.Exec(`INSERT INTO authz_policy_pattern (policy_id, dimension, mode, pattern) SELECT id, 'path', 'exclude', 'Acme.Secret/**' FROM authz_policy WHERE name = 'path-upper'`)
	e.upper = st.Token(upper, scope.Default())
	e.setup = st.Token(setup, scope.Default())
	e.excluder = st.Token(excluder, scope.Default())
	e.includer = st.Token(includer, scope.Default())
	e.scoped = st.Token(scoped, scope.Scope{Repositories: []scope.Grant{{
		Repositories: []string{"it/nuget-path"}, Paths: scope.Patterns{Exclude: []string{"acme.secret/**"}}, Actions: []string{"read"},
	}}})
	e.r = e.replica("web-path")
	e.mustPush(e.r, "nuget-path", e.setup, nupkg(t, pkgSpec{id: "Acme.Open", version: "1.0.0"}))
	e.mustPush(e.r, "nuget-path", e.setup, nupkg(t, pkgSpec{id: "Acme.Secret", version: "1.0.0"}))
	return e
}

// reads are the protocol reads of one ID, by the name a failure shows.
func pathReads(id string) map[string]string {
	return map[string]string{
		"flat container version list":   "v3/flatcontainer/" + id + "/index.json",
		"flat container package":        "v3/flatcontainer/" + id + "/1.0.0/" + id + ".1.0.0.nupkg",
		"flat container nuspec":         "v3/flatcontainer/" + id + "/1.0.0/" + id + ".nuspec",
		"registration index":            "v3/registration-semver2/" + id + "/index.json",
		"registration leaf":             "v3/registration-semver2/" + id + "/1.0.0.json",
		"registration index, SemVer 1":  "v3/registration/" + id + "/index.json",
		"registration index, gzip hive": "v3/registration-gz/" + id + "/index.json",
	}
}

func (e *pathEnv) get(path, token string) testenv.Answer {
	e.t.Helper()
	return e.st.Do(e.r, "GET", "/api/nuget/it/nuget-path/"+path, token, nil)
}

// searchIDs are the lower-case IDs a search for "acme" lists to the caller, and the count it reports.
func (e *pathEnv) searchIDs(token string) ([]string, int) {
	e.t.Helper()
	a := e.get("v3/search?q=acme&take=100", token)
	if a.Status != http.StatusOK {
		e.t.Fatalf("search: %d %s", a.Status, a.Body)
	}
	var f found
	if err := json.Unmarshal(a.Body, &f); err != nil {
		e.t.Fatalf("%v: %s", err, a.Body)
	}
	ids := f.ids()
	for i := range ids {
		ids[i] = strings.ToLower(ids[i])
	}
	slices.Sort(ids)
	return ids, f.TotalHits
}

// expect asserts the status of every protocol read of the ID for the caller.
func (e *pathEnv) expect(t *testing.T, who, token, id string, status int) {
	t.Helper()
	for what, path := range pathReads(id) {
		if a := e.get(path, token); a.Status != status {
			t.Errorf("%s, %s of %s: %d, want %d\n%s", who, what, id, a.Status, status, a.Body)
		}
	}
}

func TestR_AUTHZ_005_PathPatternsDecideTheNuGetProtocolOnCanonicalPaths(t *testing.T) {
	e := newPathEnv(t)

	t.Run("R-AUTHZ-005 a path exclude in a grant hides the excluded ID on every protocol read and serves the others", func(t *testing.T) {
		e.expect(t, "excluder", e.excluder, "acme.secret", http.StatusNotFound)
		e.expect(t, "excluder", e.excluder, "acme.open", http.StatusOK)
		// The raw address decides the same file on the same path (R-EP-014): the protocol may not be a way around it.
		if a := e.st.Do(e.r, "GET", "/files/it/nuget-path/acme.secret/1.0.0/acme.secret.1.0.0.nupkg", e.excluder, nil); a.Status != http.StatusNotFound {
			t.Errorf("excluder, /files/ of the excluded package: %d, want 404", a.Status)
		}
	})

	t.Run("R-AUTHZ-005 a path exclude in a token scope hides the excluded ID on every protocol read", func(t *testing.T) {
		e.expect(t, "scoped token", e.scoped, "acme.secret", http.StatusNotFound)
		e.expect(t, "scoped token", e.scoped, "acme.open", http.StatusOK)
	})

	t.Run("R-AUTHZ-005 an exclude written in upper case hides the ID it names on NuGet", func(t *testing.T) {
		e.expect(t, "upper", e.upper, "acme.secret", http.StatusNotFound)
		e.expect(t, "upper", e.upper, "acme.open", http.StatusOK)
		if ids, total := e.searchIDs(e.upper); !slices.Equal(ids, []string{"acme.open"}) || total != 1 {
			t.Errorf("upper: search lists %v (totalHits %d), want [acme.open]", ids, total)
		}
	})

	t.Run("R-AUTHZ-005 a path include serves the included ID through the protocol and hides the others", func(t *testing.T) {
		e.expect(t, "includer", e.includer, "acme.open", http.StatusOK)
		e.expect(t, "includer", e.includer, "acme.secret", http.StatusNotFound)
		if a := e.st.Do(e.r, "GET", "/files/it/nuget-path/acme.open/1.0.0/acme.open.1.0.0.nupkg", e.includer, nil); a.Status != http.StatusOK {
			t.Errorf("includer, /files/ of the included package: %d, want 200", a.Status)
		}
	})

	t.Run("R-AUTHZ-005 the search lists and counts only the IDs whose package document the caller may read", func(t *testing.T) {
		for _, c := range []struct {
			who, token string
			want       []string
		}{
			{"excluder", e.excluder, []string{"acme.open"}},
			{"includer", e.includer, []string{"acme.open"}},
			{"scoped token", e.scoped, []string{"acme.open"}},
			{"setup", e.setup, []string{"acme.open", "acme.secret"}},
		} {
			ids, total := e.searchIDs(c.token)
			if !slices.Equal(ids, c.want) || total != len(c.want) {
				t.Errorf("%s: search lists %v (totalHits %d), want %v", c.who, ids, total, c.want)
			}
		}
	})

	t.Run("R-AUTHZ-005 a token scope whose path pattern has an upper-case letter over a NuGet repository is refused", func(t *testing.T) {
		create := func(pattern string) testenv.Answer {
			return e.st.Do(e.r, "POST", "/api/v1/tokens", e.setup, map[string]any{
				"description": "scoped", "ttlSeconds": 600,
				"scope": map[string]any{"repositories": []any{map[string]any{
					"repositories": []string{"it/nuget-path"}, "paths": map[string]any{"exclude": []string{pattern}}, "actions": []string{"read"},
				}}},
			})
		}
		if a := create("Acme.Secret/**"); a.Status != http.StatusBadRequest || !strings.Contains(string(a.Body), "invalid_scope") {
			t.Errorf("upper-case exclude: %d %s, want 400 invalid_scope", a.Status, a.Body)
		}
		if a := create("acme.secret/**"); a.Status != http.StatusCreated {
			t.Errorf("lower-case exclude: %d %s, want 201", a.Status, a.Body)
		}
	})

	t.Run("R-AUTHZ-005 a push of an excluded ID is refused and one of another ID is stored", func(t *testing.T) {
		if a := e.push(e.r, "nuget-path", e.excluder, nupkg(t, pkgSpec{id: "Acme.Secret", version: "2.0.0"})); a.Status != http.StatusForbidden {
			t.Errorf("push of the excluded ID: %d, want 403\n%s", a.Status, a.Body)
		}
		if n := e.versions("nuget-path", "acme.secret"); n != 1 {
			t.Errorf("acme.secret holds %d versions after the refused push, want 1", n)
		}
		if a := e.push(e.r, "nuget-path", e.excluder, nupkg(t, pkgSpec{id: "Acme.Open", version: "2.0.0"})); a.Status != http.StatusCreated {
			t.Errorf("push of another ID: %d, want 201\n%s", a.Status, a.Body)
		}
	})
}
