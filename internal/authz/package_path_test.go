package authz

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
)

// TestR_AUTHZ_005_APathPatternDecidesAPackageOnItsPath: the path dimension of a resource set is matched against the
// canonical path of the coordinate (R-AUTHZ-005). A coordinate that names a package and no path does not match a path
// set with any pattern, include or exclude: nothing shows that what it stands for lies outside the excluded paths. The
// decision point's own questions about a package as a whole (a listing row, a check of the caller) carry the path of
// the package's document, so a path pattern narrows them as it narrows the document a client fetches.
func TestR_AUTHZ_005_APathPatternDecidesAPackageOnItsPath(t *testing.T) {
	f := newFixture().repo("npm-local", "local", "npm", "").repo("nuget-local", "local", "nuget", "")
	repos := []string{"npm-local", "nuget-local"}
	f.policy(policyDef{name: "excluder", selectors: repos, pathExc: []string{"secret/**", "acme.secret/**"}, entries: map[string][]string{idAlice: {"read", "deploy"}}})
	f.policy(policyDef{name: "includer", selectors: repos, pathInc: []string{"open/**", "acme.open/**"}, entries: map[string][]string{idBob: {"read"}}})
	f.policy(policyDef{name: "everything", selectors: repos, entries: map[string][]string{idCarol: {"read"}}})
	e := newTestEngine(t, f, nil)
	ctx := context.Background()
	alice, bob := userSub(idAlice, "alice"), userSub(idBob, "bob")
	carol := userSub(idCarol, "carol")
	carol.Scope = &scope.Scope{Repositories: []scope.Grant{{
		Repositories: []string{inst("npm-local"), inst("nuget-local")}, Paths: scope.Patterns{Exclude: []string{"secret/**", "acme.secret/**"}}, Actions: []string{"read"},
	}}}
	readAt := func(repo, pkg, path string) Coordinate {
		return Coordinate{Repository: qr(repo), Package: pkg, Path: path, Kind: KindArtifact, Actions: []Action{Read}}
	}
	deployAt := func(repo, pkg, path string) Coordinate {
		return Coordinate{Repository: qr(repo), Package: pkg, Path: path, Kind: KindWrite, Actions: []Action{Deploy}}
	}

	for _, tc := range []struct {
		name string
		sub  Subject
		c    Coordinate
		want bool
	}{
		{"an exclude refuses the excluded package's file", alice, readAt("npm-local", "secret", "secret/-/secret-1.0.0.tgz"), false},
		{"an exclude refuses the excluded package's document", alice, readAt("npm-local", "secret", "secret"), false},
		{"an exclude admits another package's file", alice, readAt("npm-local", "open", "open/-/open-1.0.0.tgz"), true},
		{"an exclude refuses a write to the excluded package", alice, deployAt("npm-local", "secret", "secret"), false},
		{"an exclude admits a write to another package", alice, deployAt("npm-local", "open", "open/-/open-2.0.0.tgz"), true},
		{"an exclude refuses a package named without a path", alice, readAt("npm-local", "open", ""), false},
		{"an exclude refuses a NuGet package's flat container file", alice, readAt("nuget-local", "acme.secret", "acme.secret/1.0.0/acme.secret.1.0.0.nupkg"), false},
		{"an exclude refuses a NuGet package's registration", alice, readAt("nuget-local", "acme.secret", "acme.secret/.registration/index.json"), false},
		{"an include admits the included package's file", bob, readAt("npm-local", "open", "open/-/open-1.0.0.tgz"), true},
		{"an include admits the included package's document", bob, readAt("npm-local", "open", "open"), true},
		{"an include refuses another package", bob, readAt("npm-local", "secret", "secret"), false},
		{"an include refuses a package named without a path", bob, readAt("npm-local", "open", ""), false},
		{"a token scope's exclude refuses the excluded package", carol, readAt("npm-local", "secret", "secret/-/secret-1.0.0.tgz"), false},
		{"a token scope's exclude admits another package", carol, readAt("npm-local", "open", "open/-/open-1.0.0.tgz"), true},
		{"a token scope's exclude refuses a package named without a path", carol, readAt("npm-local", "open", ""), false},
		{"a repository-level coordinate matches whatever the path patterns say", alice, Coordinate{Repository: qr("npm-local"), Kind: KindSearch, Actions: []Action{Read}}, true},
		{"a repository-level coordinate matches an include", bob, Coordinate{Repository: qr("npm-local"), Kind: KindSearch, Actions: []Action{Read}}, true},
	} {
		t.Run("R-AUTHZ-005 "+tc.name, func(t *testing.T) {
			if d := e.Decide(ctx, tc.sub, tc.c); (d.Outcome == Allow) != tc.want {
				t.Fatalf("Decide(%+v) = %+v, want allowed %v", tc.c, d, tc.want)
			}
		})
	}

	t.Run("R-AUTHZ-016 a listing decides a row on the path of its package document", func(t *testing.T) {
		keys := []string{"npm-local", "nuget-local"}
		for _, tc := range []struct {
			who        string
			sub        Subject
			repo, pkg  string
			want       bool
			wantShape  ReadShape
			shapeOfKey string
		}{
			{"excluder", alice, "npm-local", "secret", false, ReadRows, "npm-local"},
			{"excluder", alice, "npm-local", "open", true, ReadRows, "npm-local"},
			{"excluder", alice, "nuget-local", "acme.secret", false, ReadRows, "nuget-local"},
			{"excluder", alice, "nuget-local", "acme.open", true, ReadRows, "nuget-local"},
			{"includer", bob, "npm-local", "open", true, ReadRows, "npm-local"},
			{"includer", bob, "npm-local", "secret", false, ReadRows, "npm-local"},
			{"includer", bob, "nuget-local", "acme.open", true, ReadRows, "nuget-local"},
			{"scoped token", carol, "npm-local", "secret", false, ReadRows, "npm-local"},
			{"scoped token", carol, "npm-local", "open", true, ReadRows, "npm-local"},
		} {
			rs, err := e.ReadScopeOn(ctx, e.Index(), tc.sub, qr(""), qrs(keys))
			if err != nil {
				t.Fatal(err)
			}
			if got := rs.Shape(qr(tc.shapeOfKey)); got != tc.wantShape {
				t.Errorf("%s: shape of %s = %v, want %v: a path pattern narrows the rows", tc.who, tc.shapeOfKey, got, tc.wantShape)
			}
			if got := rs.MayRead(qr(tc.repo), tc.pkg, ""); got != tc.want {
				t.Errorf("%s: MayRead(%s, %s) = %v, want %v", tc.who, tc.repo, tc.pkg, got, tc.want)
			}
			if got := rs.MayRead(qr(tc.repo), tc.pkg, "1.0.0"); got != tc.want {
				t.Errorf("%s: MayRead(%s, %s, 1.0.0) = %v, want %v", tc.who, tc.repo, tc.pkg, got, tc.want)
			}
		}
	})

	t.Run("R-AUTHZ-023 a check that names a package and no path is decided on the path of its package document", func(t *testing.T) {
		got, err := e.CheckOn(e.Index(), alice, []CheckItem{
			{Repository: qr("npm-local"), Package: "secret", Action: Read},
			{Repository: qr("npm-local"), Package: "open", Action: Read},
			{Repository: qr("npm-local"), Package: "open", Path: "secret/-/secret-1.0.0.tgz", Action: Read},
		})
		if err != nil {
			t.Fatal(err)
		}
		if got[0] || !got[1] || got[2] {
			t.Fatalf("CheckOn = %v, want [false true false]", got)
		}
	})

	t.Run("R-AUTHZ-005 a format without package documents leaves a package question without a path, which fails closed", func(t *testing.T) {
		c := e.atPackage(e.Index(), Coordinate{Repository: qr("npm-local"), Format: "unknown-format", Package: "open"})
		if c.Path != "" {
			t.Fatalf("path = %q", c.Path)
		}
		if got := e.atPackage(e.Index(), Coordinate{Repository: qr("nuget-local"), Package: "acme.open"}).Path; got != "acme.open/index.json" {
			t.Fatalf("the NuGet document path = %q", got)
		}
		if got := e.atPackage(e.Index(), Coordinate{Repository: qr("npm-local"), Package: "open", Path: "open/-/open-1.0.0.tgz"}).Path; got != "open/-/open-1.0.0.tgz" {
			t.Fatalf("a stated path was replaced: %q", got)
		}
	})
}
