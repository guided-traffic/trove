package authz

import "testing"

// TestR_EP_018_TheViewDecidesAPackageOnItsDocumentPath: an exposure rule's path patterns on a format with package names
// are matched against the path of the package's document by the view a format filters a document with, as the ceiling
// matches them against the path a protocol request states and a listing row's document path (R-EP-012, R-AUTHZ-005,
// Q-AUTHZ-251). An include that names a package's folder exposes the package, its versions and its tags; an exclude hides
// them; an item that names a package and no path fails a rule with path patterns.
func TestR_EP_018_TheViewDecidesAPackageOnItsDocumentPath(t *testing.T) {
	f := exposureFixture()
	f.repo("npm-pinc", "local", "npm", "").repo("npm-pexc", "local", "npm", "")
	f.policy(policyDef{name: "paths-read", selectors: []string{"npm-pinc", "npm-pexc"}, entries: map[string][]string{idBot: {"read"}}})
	f.exposure(ruleDef{endpoint: "customers", name: "pinc", repo: "npm-pinc", dims: paths("open-pkg/**")})
	f.exposure(ruleDef{endpoint: "customers", name: "pexc", repo: "npm-pexc", dims: ruleDims{Paths: PatternLists{Exclude: []string{"secret-pkg/**"}}}})
	e := newTestEngine(t, f, func(o *Options) { o.Ceiling = exposureCeiling() })
	cl, x := exposureCeiling(), e.Index()
	for _, c := range []struct {
		repo, pkg string
		want      bool
	}{
		{"npm-pinc", "open-pkg", true},
		{"npm-pinc", "secret-pkg", false},
		{"npm-pexc", "open-pkg", true},
		{"npm-pexc", "secret-pkg", false},
	} {
		t.Run("R-EP-018 "+c.repo+" "+c.pkg, func(t *testing.T) {
			v := cl.View(x, "customers", qr(c.repo))
			if got := v.Package(c.pkg); got != c.want {
				t.Fatalf("Package = %v, want %v", got, c.want)
			}
			if got := v.Version(c.pkg, "1.0.0"); got != c.want {
				t.Fatalf("Version = %v, want %v", got, c.want)
			}
			if got := v.Tag(c.pkg, "latest", "1.0.0"); got != c.want {
				t.Fatalf("Tag = %v, want %v", got, c.want)
			}
			// The ceiling decides the protocol's document on the same path, and a listing row through the read scope.
			doc := e.packagePath(x.repo(qr(c.repo)).Format, c.pkg)
			d := e.Decide(t.Context(), onEP(botSub, "customers"), Coordinate{Repository: qr(c.repo), Package: c.pkg, Path: doc, Kind: KindMetadata, Actions: []Action{Read}})
			if d.Allowed() != c.want {
				t.Fatalf("the ceiling decides the document %v, the view %v", d.Allowed(), c.want)
			}
		})
	}
	t.Run("R-EP-012 an item that names a package and no path fails a rule with path patterns", func(t *testing.T) {
		for _, name := range []string{"pinc", "pexc"} {
			rules := x.exposure["customers"].byRepo[x.repo(qr("npm-"+name)).ID]
			if admitsItem(rules, exposureItem{pkg: "open-pkg"}, nil) {
				t.Errorf("rule %s admits a package without a path", name)
			}
		}
	})
}
