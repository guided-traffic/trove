package authz

import (
	"testing"

	"github.com/guided-traffic/trove/internal/formats/catalog"
)

func TestR_AUTHZ_005_Patterns(t *testing.T) {
	type c struct {
		name              string
		dim               dimension
		names             catalog.Names
		include, exclude  []string
		value             string
		present           bool
		want              bool
		wantCompileFailed bool
	}
	cases := []c{
		{name: "name glob scope matches a scoped package", dim: dimPackage, include: []string{"@acme/*"}, value: "@acme/ui", present: true, want: true},
		{name: "name glob scope does not match another scope", dim: dimPackage, include: []string{"@acme/*"}, value: "@acme-tools/ui", present: true},
		{name: "exclude beats include inside one set", dim: dimPackage, include: []string{"@acme/*"}, exclude: []string{"@acme/secret-*"}, value: "@acme/secret-keys", present: true},
		{name: "exclude leaves the rest of the include", dim: dimPackage, include: []string{"@acme/*"}, exclude: []string{"@acme/secret-*"}, value: "@acme/ui", present: true, want: true},
		{name: "NuGet prefix matches any spelling", dim: dimPackage, names: lowerNames{}, include: []string{"Acme.*"}, value: "ACME.Core", present: true, want: true},
		{name: "NuGet prefix needs the dot", dim: dimPackage, names: lowerNames{}, include: []string{"Acme.*"}, value: "AcmeCore", present: true},
		{name: "NuGet exact ID in any spelling", dim: dimPackage, names: lowerNames{}, include: []string{"acme.core"}, value: "Acme.CORE", present: true, want: true},
		{name: "npm names compare case-sensitively", dim: dimPackage, include: []string{"acme-*"}, value: "ACME-ui", present: true},
		{name: "star is one unscoped name and never crosses a slash", dim: dimPackage, include: []string{"*"}, value: "@acme/ui", present: true},
		{name: "star matches an unscoped name", dim: dimPackage, include: []string{"*"}, value: "left-pad", present: true, want: true},
		{name: "@*/* matches every scoped name", dim: dimPackage, include: []string{"@*/*"}, value: "@a/b", present: true, want: true},
		{name: "@*/* does not match an unscoped name", dim: dimPackage, include: []string{"@*/*"}, value: "b", present: true},
		{name: "double star matches every package", dim: dimPackage, include: []string{"**"}, value: "@a/b", present: true, want: true},
		{name: "question mark is one character except slash", dim: dimPackage, include: []string{"a?c"}, value: "abc", present: true, want: true},
		{name: "question mark does not match a slash", dim: dimPackage, include: []string{"a?c"}, value: "a/c", present: true},
		{name: "a backslash makes a star literal", dim: dimPackage, include: []string{`a\*b`}, value: "a*b", present: true, want: true},
		{name: "a literal star does not match another character", dim: dimPackage, include: []string{`a\*b`}, value: "ab", present: true},
		{name: "a literal star does not widen to a run", dim: dimPackage, include: []string{`@acme/ui\*`}, value: "@acme/ui-kit", present: true},
		{name: "a backslash escapes any character", dim: dimPackage, include: []string{`\a`}, value: "a", present: true, want: true},
		{name: "an empty include is unrestricted without a value", dim: dimPackage, value: "", want: true},
		{name: "an empty include is unrestricted with a value", dim: dimPackage, value: "x", present: true, want: true},
		{name: "an empty include still applies the exclude", dim: dimPackage, exclude: []string{"x*"}, value: "xy", present: true},
		{name: "an empty include with an exclude passes a coordinate without a value", dim: dimPackage, exclude: []string{"x*"}, value: "", want: true},
		{name: "a non-empty include never matches a coordinate without a value", dim: dimPackage, include: []string{"@acme/*"}, value: ""},
		{name: "path double star matches nested files", dim: dimPath, include: []string{"releases/**"}, value: "releases/1.0/app.zip", present: true, want: true},
		{name: "path double star matches one level", dim: dimPath, include: []string{"releases/**"}, value: "releases/app.zip", present: true, want: true},
		{name: "path double star does not match a sibling directory", dim: dimPath, include: []string{"releases/**"}, value: "snapshots/app.zip", present: true},
		{name: "path star stays inside a segment", dim: dimPath, include: []string{"releases/*"}, value: "releases/1.0/app.zip", present: true},
		{name: "path star matches a file in the directory", dim: dimPath, include: []string{"releases/*"}, value: "releases/app.zip", present: true, want: true},
		{name: "path double star in the middle", dim: dimPath, include: []string{"a/**/c"}, value: "a/b/x/c", present: true, want: true},
		{name: "path double star in the middle matches zero segments", dim: dimPath, include: []string{"a/**/c"}, value: "a/c", present: true, want: true},
		{name: "leading path double star", dim: dimPath, include: []string{"**/app.zip"}, value: "x/y/app.zip", present: true, want: true},
		{name: "leading path double star matches the top level", dim: dimPath, include: []string{"**/app.zip"}, value: "app.zip", present: true, want: true},
		{name: "a lone path double star matches any path", dim: dimPath, include: []string{"**"}, value: "a/b/c", present: true, want: true},
		{name: "path question mark is one character inside a segment", dim: dimPath, include: []string{"v?/x"}, value: "v1/x", present: true, want: true},
		{name: "path exclude beats include", dim: dimPath, include: []string{"releases/**"}, exclude: []string{"releases/internal/**"}, value: "releases/internal/a", present: true},
		{name: "a name grant does not match a path without a package", dim: dimPackage, include: []string{"@acme/*"}, value: "", present: false},
		{name: "Q-AUTHZ-160 an exclude-only name set of a format with names fails a coordinate without a package", dim: dimPackage, names: exactNames{}, exclude: []string{"@secret/*"}, value: ""},
		{name: "Q-AUTHZ-160 an exclude-only name set of a format with names still passes a package outside the excludes", dim: dimPackage, names: exactNames{}, exclude: []string{"@secret/*"}, value: "@acme/ui", present: true, want: true},
		{name: "Q-AUTHZ-160 an exclude-only name set of a format with names refuses an excluded package", dim: dimPackage, names: exactNames{}, exclude: []string{"@secret/*"}, value: "@secret/x", present: true},
		{name: "Q-AUTHZ-160 a name set without patterns passes a coordinate without a package", dim: dimPackage, names: exactNames{}, value: "", want: true},
		{name: "a trailing backslash is invalid", dim: dimPackage, include: []string{`abc\`}, wantCompileFailed: true},
		{name: "an empty pattern is invalid", dim: dimPackage, include: []string{""}, wantCompileFailed: true},
		{name: "a path pattern with an empty segment is invalid", dim: dimPath, include: []string{"a//b"}, wantCompileFailed: true},
		{name: "a double star inside a path segment is invalid", dim: dimPath, include: []string{"a**b/c"}, wantCompileFailed: true},
		{name: "an escaped double star inside a path segment is a literal", dim: dimPath, include: []string{`a\*\*b/c`}, value: "a**b/c", present: true, want: true},
	}
	for _, tc := range cases {
		t.Run("R-AUTHZ-005 "+tc.name, func(t *testing.T) {
			g, err := compileSet(tc.dim, tc.include, tc.exclude, tc.names)
			if tc.wantCompileFailed {
				if err == nil {
					t.Fatal("the pattern compiled")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := g.match(tc.value, tc.present); got != tc.want {
				t.Fatalf("match(%q, %v) = %v, want %v", tc.value, tc.present, got, tc.want)
			}
		})
	}

	t.Run("R-AUTHZ-005 limits: 1024 characters and 256 patterns", func(t *testing.T) {
		long := make([]byte, 1025)
		for i := range long {
			long[i] = 'a'
		}
		if ValidatePackagePattern(string(long)) == nil {
			t.Error("a pattern of 1025 characters was valid")
		}
		if ValidatePackagePattern(string(long[:1024])) != nil {
			t.Error("a pattern of 1024 characters was invalid")
		}
		if ValidatePatternCount(make([]string, 257)) == nil || ValidatePatternCount(make([]string, 256)) != nil {
			t.Error("the count limit is 256")
		}
	})

	t.Run("R-AUTHZ-005 a repository-level coordinate matches whatever the patterns say", func(t *testing.T) {
		pkg, _ := compileSet(dimPackage, []string{"@acme/*"}, nil, nil)
		path, _ := compileSet(dimPath, []string{"releases/**"}, nil, nil)
		rs := resourceSet{packages: pkg, paths: path}
		if !rs.matches(true, "", "") || rs.matches(false, "", "") {
			t.Fatal("repository-level coordinates match every grant that covers the repository")
		}
	})
}

func TestR_AUTHZ_004_Selectors(t *testing.T) {
	repos := []repoView{
		{ID: 1, Key: "npm-internal", Class: "local", Format: "npm"},
		{ID: 2, Key: "npm-team-web", Class: "local", Format: "npm"},
		{ID: 3, Key: "npmjs", Class: "remote", Format: "npm"},
		{ID: 4, Key: "npm-all", Class: "virtual", Format: "npm"},
		{ID: 5, Key: "files-internal", Class: "local", Format: "generic"},
	}
	cases := []struct {
		sel  string
		want []string
	}{
		{"npm-internal", []string{"npm-internal"}},
		{"npm-team-*", []string{"npm-team-web"}},
		{"npm-*", []string{"npm-internal", "npm-team-web", "npm-all"}},
		{"npm-????????", []string{"npm-internal", "npm-team-web"}},
		{"@any-local", []string{"npm-internal", "npm-team-web", "files-internal"}},
		{"@any-remote", []string{"npmjs"}},
		{"@any-virtual", []string{"npm-all"}},
		{"@any", []string{"npm-internal", "npm-team-web", "npmjs", "npm-all", "files-internal"}},
		{"@any-local:npm", []string{"npm-internal", "npm-team-web"}},
		{"@any-local:generic", []string{"files-internal"}},
		{"npm-*:npm", []string{"npm-internal", "npm-team-web", "npm-all"}},
		{"*:generic", []string{"files-internal"}},
	}
	for _, tc := range cases {
		t.Run("R-AUTHZ-004 selector "+tc.sel, func(t *testing.T) {
			s, err := parseSelector(tc.sel)
			if err != nil {
				t.Fatal(err)
			}
			s.repoID = 0
			if s.kind == selKey {
				for _, r := range repos {
					if r.Key == s.key {
						s.repoID = r.ID
					}
				}
			}
			var got []string
			for _, r := range repos {
				if s.matches(r) {
					got = append(got, r.Key)
				}
			}
			if len(got) != len(tc.want) {
				t.Fatalf("covers %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("covers %v, want %v", got, tc.want)
				}
			}
		})
	}
	for _, bad := range []string{"", "@any-everything", "Npm-Internal", "npm-internal:npm", "npm-*:", "a b", "@any:"} {
		t.Run("R-AUTHZ-004 selector "+bad+" is invalid", func(t *testing.T) {
			if _, err := parseSelector(bad); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestR_AUTHZ_001_Presets(t *testing.T) {
	cases := map[string][]string{
		"read":     {"read"},
		"write":    {"annotate", "deploy", "read"},
		"maintain": {"annotate", "delete", "deploy", "overwrite", "read"},
		"full":     {"annotate", "delete", "deploy", "manage", "overwrite", "read"},
	}
	for preset, want := range cases {
		t.Run("R-AUTHZ-001 preset "+preset+" expands to explicit actions", func(t *testing.T) {
			set, bad := ParseActions([]string{preset})
			if len(bad) != 0 {
				t.Fatal(bad)
			}
			got := set.Strings()
			if len(got) != len(want) {
				t.Fatalf("%v", got)
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("%v, want %v", got, want)
				}
			}
		})
	}
	t.Run("R-AUTHZ-001 no action implies another", func(t *testing.T) {
		for _, a := range Actions() {
			for _, b := range Actions() {
				if a != b && SetOf(a).Has(b) {
					t.Errorf("%s implies %s", a, b)
				}
			}
		}
	})
	t.Run("R-AUTHZ-021 an unknown action is reported by name", func(t *testing.T) {
		if _, bad := ParseActions([]string{"read", "publish", "admin"}); len(bad) != 2 {
			t.Fatalf("bad = %v", bad)
		}
	})
}
