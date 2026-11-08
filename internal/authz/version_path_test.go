package authz

import (
	"context"
	"testing"

	"github.com/guided-traffic/trove/internal/auth/scope"
)

// TestR_AUTHZ_005_AVersionIsDecidedOnItsFilesPath: a version is decided on the package document's path and on the path of
// the version's file (catalog.Entry.VersionPath), so a path pattern finer than a package's folder that hides one version's
// file hides the version from every listing, while the package and the other versions stay (R-AUTHZ-005, Q-AUTHZ-255).
// VersionsByPath says which subjects' grants decide versions one by one, and VersionIndependent is false for them.
func TestR_AUTHZ_005_AVersionIsDecidedOnItsFilesPath(t *testing.T) {
	f := newFixture().repo("npm-local", "local", "npm", "").repo("nuget-local", "local", "nuget", "")
	repos := []string{"npm-local", "nuget-local"}
	f.policy(policyDef{name: "file-excluder", selectors: repos, pathExc: []string{"hidden/-/hidden-2.0.0.tgz", "acme.secret/2.0.0/**"}, entries: map[string][]string{idAlice: {"read"}}})
	f.policy(policyDef{name: "names", selectors: repos, pkgInc: []string{"hidden", "acme.secret"}, entries: map[string][]string{idBob: {"read"}}})
	f.policy(policyDef{name: "whole", selectors: repos, entries: map[string][]string{idCarol: {"read"}}})
	f.policy(policyDef{name: "whole-and-files", selectors: repos, pathExc: []string{"hidden/-/hidden-2.0.0.tgz"}, entries: map[string][]string{idCarol: {"read"}}})
	e := newTestEngine(t, f, nil)
	ctx := context.Background()
	alice, bob, carol := userSub(idAlice, "alice"), userSub(idBob, "bob"), userSub(idCarol, "carol")
	scoped := userSub(idCarol, "carol")
	scoped.Scope = &scope.Scope{Repositories: []scope.Grant{{
		Repositories: []string{inst("npm-local"), inst("nuget-local")}, Paths: scope.Patterns{Exclude: []string{"hidden/-/hidden-2.0.0.tgz", "acme.secret/2.0.0/**"}}, Actions: []string{"read"},
	}}}
	keys := qrs([]string{"npm-local", "nuget-local"})

	for _, tc := range []struct {
		who                 string
		sub                 Subject
		byPath, independent bool
		// hides says whether the second version is hidden.
		hides bool
	}{
		{"a grant that excludes one version's file", alice, true, false, true},
		{"a grant narrowed by names only", bob, false, true, false},
		{"a grant without patterns beside one with a path exclude", carol, false, true, false},
		{"a token scope that excludes one version's file", scoped, true, false, true},
	} {
		t.Run("R-AUTHZ-005 "+tc.who, func(t *testing.T) {
			s, err := e.ReadScopeOn(ctx, e.Index(), tc.sub, qr(""), keys)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range []struct{ repo, pkg, first, second string }{
				{"npm-local", "hidden", "1.0.0", "2.0.0"},
				{"nuget-local", "acme.secret", "1.0.0", "2.0.0"},
			} {
				if got := s.VersionsByPath(qr(c.repo)); got != tc.byPath {
					t.Errorf("VersionsByPath(%s) = %v, want %v", c.repo, got, tc.byPath)
				}
				if !s.MayRead(qr(c.repo), c.pkg, "") || !s.MayRead(qr(c.repo), c.pkg, c.first) {
					t.Errorf("%s: the package or its first version is hidden", c.repo)
				}
				if got := s.MayRead(qr(c.repo), c.pkg, c.second); got == tc.hides {
					t.Errorf("%s: MayRead(%s, %s) = %v, want %v", c.repo, c.pkg, c.second, got, !tc.hides)
				}
			}
			vi, ok := s.(VersionIndependent)
			if !ok || vi.VersionIndependent() != tc.independent {
				t.Errorf("VersionIndependent = %v, want %v", ok && vi.VersionIndependent(), tc.independent)
			}
			if !s.VersionsByPath(qr("unasked")) {
				t.Error("a key the scope did not classify is not read by path")
			}
		})
	}

	t.Run("R-AUTHZ-005 a listing row agrees with the decision on the version's file", func(t *testing.T) {
		s, err := e.ReadScopeOn(ctx, e.Index(), alice, qr(""), keys)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range []string{"1.0.0", "2.0.0"} {
			d := e.Decide(ctx, alice, Coordinate{Repository: qr("npm-local"), Package: "hidden", Version: v, Path: "hidden/-/hidden-" + v + ".tgz", Kind: KindArtifact, Actions: []Action{Read}})
			if got := s.MayRead(qr("npm-local"), "hidden", v); got != d.Allowed() {
				t.Errorf("MayRead(hidden, %s) = %v, the tarball's decision %v", v, got, d.Allowed())
			}
		}
	})

	t.Run("R-AUTHZ-005 a version the format names no file for fails a path set", func(t *testing.T) {
		s, err := e.ReadScopeOn(ctx, e.Index(), alice, qr(""), keys)
		if err != nil {
			t.Fatal(err)
		}
		if s.MayRead(qr("npm-local"), "hidden", "a/b") {
			t.Error("a version without a file path was read")
		}
		c, ok := e.atVersion(e.Index(), Coordinate{Repository: qr("npm-local"), Package: "hidden", Version: "1.0.0", Path: "hidden"})
		if ok || c.Path != "hidden" {
			t.Errorf("a stated path was replaced: %q %v", c.Path, ok)
		}
	})
}

// TestR_EP_018_TheViewDecidesAVersionOnItsFilesPath: an exposure rule whose path pattern names one version's file hides the
// version from the view a format filters its documents with, as the ceiling hides the file (R-EP-012, Q-EP-85).
func TestR_EP_018_TheViewDecidesAVersionOnItsFilesPath(t *testing.T) {
	f := exposureFixture()
	f.repo("npm-pver", "local", "npm", "")
	f.policy(policyDef{name: "pver-read", selectors: []string{"npm-pver"}, entries: map[string][]string{idBot: {"read"}}})
	f.exposure(ruleDef{endpoint: "customers", name: "pver", repo: "npm-pver", dims: ruleDims{Paths: PatternLists{Exclude: []string{"tool/-/tool-2.0.0.tgz"}}}})
	e := newTestEngine(t, f, func(o *Options) { o.Ceiling = exposureCeiling() })
	v := exposureCeiling().View(e.Index(), "customers", qr("npm-pver"))
	if !v.Package("tool") || !v.Version("tool", "1.0.0") {
		t.Fatal("the package or its first version is hidden")
	}
	if v.Version("tool", "2.0.0") || v.Tag("tool", "latest", "2.0.0") {
		t.Fatal("the version whose file the rule excludes is exposed")
	}
	d := e.Decide(t.Context(), onEP(botSub, "customers"), Coordinate{Repository: qr("npm-pver"), Package: "tool", Version: "2.0.0", Path: "tool/-/tool-2.0.0.tgz", Kind: KindArtifact, Actions: []Action{Read}})
	if d.Allowed() {
		t.Fatal("the ceiling serves the file the view hides")
	}
}
