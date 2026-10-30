package tarball

import "testing"

func TestSplitName(t *testing.T) {
	cases := []struct {
		name        string
		dir         bool
		first, inst string
		ok          bool
	}{
		{"package/package.json", false, "package", "package.json", true},
		{"package/", true, "package", "", true},
		{"package", true, "package", "", true},
		{"package/lib/", true, "package", "lib", true},
		{"package/lib/a.js", false, "package", "lib/a.js", true},
		{"package/lib/", false, "", "", false},
		{"package//", true, "", "", false},
		{"./package/package.json", false, "", "", false},
		{"package/./a", false, "", "", false},
		{"package/../a", false, "", "", false},
		{"/package/a", false, "", "", false},
		{"", false, "", "", false},
		{"package/a\\b", false, "", "", false},
		{"package/a\x00b", false, "", "", false},
		{"package/\xc3\x28", false, "", "", false},
	}
	for _, c := range cases {
		t.Run("R-NPM-007 "+c.name, func(t *testing.T) {
			first, inst, err := splitName(c.name, c.dir, 1024)
			if (err == nil) != c.ok || first != c.first || inst != c.inst {
				t.Fatalf("splitName(%q, %v) = %q, %q, %v", c.name, c.dir, first, inst, err)
			}
		})
	}
}
