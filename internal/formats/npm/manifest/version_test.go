package manifest

import "testing"

// The expectations were produced by semver.clean(v, true) of the semver package
// that ships with npm.
func TestCleanVersion(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"1.2.3", "1.2.3", true},
		{"v1.2.3", "1.2.3", true},
		{" v1.2.3 ", "1.2.3", true},
		{"=1.2.3", "1.2.3", true},
		{"==v1.2.3", "1.2.3", true},
		{"v=1.2.3", "1.2.3", true},
		{"v 1.2.3", "1.2.3", true},
		{"\t1.2.3\n", "1.2.3", true},
		{"1.2.3+build.5", "1.2.3", true},
		{"1.2.3-beta.1+b", "1.2.3-beta.1", true},
		{"01.02.03", "1.2.3", true},
		{"1.2.3beta", "1.2.3-beta", true},
		{"1.2.3-01", "1.2.3-1", true},
		{"1.2.3-alpha.01.x", "1.2.3-alpha.1.x", true},
		{"1.2.3-0", "1.2.3-0", true},
		{"1.0.0-rc.1", "1.0.0-rc.1", true},
		{"1.2.3-", "1.2.3--", true},
		{"1.2.3--", "1.2.3--", true},
		{"1.2.3-1.-.a", "1.2.3-1.-.a", true},
		{"9007199254740991.0.0", "9007199254740991.0.0", true},
		{"1.2.3-9007199254740993", "1.2.3-9007199254740993", true},
		{"1", "", false},
		{"1.2", "", false},
		{"V1.2.3", "", false},
		{"a.b.c", "", false},
		{"", "", false},
		{" ", "", false},
		{"1.2.3.4", "", false},
		{"9007199254740992.0.0", "", false},
		{"1.2.3-a_b", "", false},
		{"x1.2.3", "", false},
		{"1.2.3+", "", false},
	}
	for _, c := range cases {
		t.Run("R-NPM-007 "+c.in, func(t *testing.T) {
			got, ok := CleanVersion(c.in)
			if ok != c.ok || got != c.want {
				t.Fatalf("CleanVersion(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
			}
		})
	}
	t.Run("R-NPM-007 a version of more than 256 characters is not a version", func(t *testing.T) {
		long := "1.2.3-"
		for len(long) < 300 {
			long += "a"
		}
		if _, ok := CleanVersion(long); ok {
			t.Fatal("accepted")
		}
	})
}
