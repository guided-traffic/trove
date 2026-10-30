package semver

import (
	"reflect"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

func TestParse(t *testing.T) {
	cases := []struct {
		name, in string
		valid    bool
		canon    string // Canonical form, without build metadata
		full     string // String form, with build metadata
	}{
		{"core", "1.2.3", true, "1.2.3", "1.2.3"},
		{"zero", "0.0.0", true, "0.0.0", "0.0.0"},
		{"prerelease", "1.0.0-alpha.1", true, "1.0.0-alpha.1", "1.0.0-alpha.1"},
		{"build", "1.0.0+20130313144700", true, "1.0.0", "1.0.0+20130313144700"},
		{"prerelease and build", "1.0.0-beta+exp.sha.5114f85", true, "1.0.0-beta", "1.0.0-beta+exp.sha.5114f85"},
		{"hyphens in identifiers", "1.0.0-a-b.c-d+e-f", true, "1.0.0-a-b.c-d", "1.0.0-a-b.c-d+e-f"},
		{"alphanumeric starting with a digit", "1.0.0-0a", true, "1.0.0-0a", "1.0.0-0a"},
		{"numeric zero identifier", "1.0.0-0", true, "1.0.0-0", "1.0.0-0"},
		{"build with leading zeros is kept", "1.0.0+001", true, "1.0.0", "1.0.0+001"},
		{"largest safe component", "9007199254740991.0.0", true, "9007199254740991.0.0", "9007199254740991.0.0"},

		{"empty", "", false, "", ""},
		{"v prefix", "v1.2.3", false, "", ""},
		{"equals prefix", "=1.2.3", false, "", ""},
		{"leading space", " 1.2.3", false, "", ""},
		{"trailing space", "1.2.3 ", false, "", ""},
		{"trailing newline", "1.2.3\n", false, "", ""},
		{"two components", "1.2", false, "", ""},
		{"four components", "1.2.3.4", false, "", ""},
		{"leading zero major", "01.2.3", false, "", ""},
		{"leading zero minor", "1.02.3", false, "", ""},
		{"leading zero patch", "1.2.03", false, "", ""},
		{"leading zero numeric prerelease", "1.2.3-01", false, "", ""},
		{"empty prerelease", "1.2.3-", false, "", ""},
		{"empty prerelease identifier", "1.2.3-a..b", false, "", ""},
		{"empty build", "1.2.3+", false, "", ""},
		{"empty build identifier", "1.2.3+a..b", false, "", ""},
		{"underscore in prerelease", "1.2.3-a_b", false, "", ""},
		{"prerelease without hyphen", "1.2.3alpha", false, "", ""},
		{"component beyond 2^53-1", "9007199254740992.0.0", false, "", ""},
		{"negative", "-1.2.3", false, "", ""},
		{"wildcard", "1.2.x", false, "", ""},
		{"non-ASCII digit", "1.2.٣", false, "", ""},
		{"longer than 256 characters", "1.2.3-" + strings.Repeat("a", 251), false, "", ""},
		{"256 characters", "1.2.3-" + strings.Repeat("a", 250), true, "1.2.3-" + strings.Repeat("a", 250), "1.2.3-" + strings.Repeat("a", 250)},
	}
	for _, c := range cases {
		t.Run("R-NPM-007 Parse "+c.name, func(t *testing.T) {
			v, err := Parse(c.in)
			if (err == nil) != c.valid {
				t.Fatalf("Parse(%q) error = %v, want valid = %v", c.in, err, c.valid)
			}
			if err != nil {
				e, ok := errs.As(err)
				if !ok || e.Kind != errs.Invalid || e.Code != CodeInvalidVersion {
					t.Fatalf("error = %#v, want an Invalid %s", err, CodeInvalidVersion)
				}
				return
			}
			if v.Canonical() != c.canon || v.String() != c.full {
				t.Fatalf("Canonical/String = %q/%q, want %q/%q", v.Canonical(), v.String(), c.canon, c.full)
			}
			back, err := Parse(v.String())
			if err != nil || !reflect.DeepEqual(back, v) {
				t.Fatalf("Parse(String()) = %+v, %v; want %+v", back, err, v)
			}
		})
	}
}

func TestParseNode(t *testing.T) {
	cases := []struct {
		name, in string
		loose    bool
		valid    bool
		canon    string
	}{
		{"strict allows v", "v1.2.3", false, true, "1.2.3"},
		{"strict trims", "  1.2.3\n", false, true, "1.2.3"},
		{"strict refuses equals", "=1.2.3", false, false, ""},
		{"strict refuses leading zero", "1.02.3", false, false, ""},
		{"loose allows equals", "=1.2.3", true, true, "1.2.3"},
		{"loose allows v and equals and spaces", "= v 1.2.3", true, true, "1.2.3"},
		{"loose allows leading zeros", "01.002.0003", true, true, "1.2.3"},
		{"loose numeric prerelease loses leading zeros", "1.2.3-007", true, true, "1.2.3-7"},
		{"loose prerelease without hyphen", "1.2.3alpha1", true, true, "1.2.3-alpha1"},
		{"loose still needs three components", "1.2", true, false, ""},
		{"large numeric prerelease stays text", "1.2.3-9007199254740993", false, true, "1.2.3-9007199254740993"},
		{"numeric prerelease below 2^53-1 is a number", "1.2.3-9007199254740990", false, true, "1.2.3-9007199254740990"},
		{"loose keeps zeros of a large numeric prerelease", "1.2.3-009007199254740993", true, true, "1.2.3-009007199254740993"},
		{"component beyond 2^53-1", "1.2.9007199254740992", true, false, ""},
	}
	for _, c := range cases {
		t.Run("R-NPM-007 ParseNode "+c.name, func(t *testing.T) {
			v, err := ParseNode(c.in, c.loose)
			if (err == nil) != c.valid {
				t.Fatalf("ParseNode(%q, %v) error = %v, want valid = %v", c.in, c.loose, err, c.valid)
			}
			if err == nil && v.Canonical() != c.canon {
				t.Fatalf("Canonical = %q, want %q", v.Canonical(), c.canon)
			}
		})
	}
}

func TestClean(t *testing.T) {
	cases := []struct {
		in    string
		want  string
		valid bool
	}{
		{"1.2.3", "1.2.3", true},
		{"v1.2.3", "1.2.3", true},
		{"=v1.2.3", "1.2.3", true},
		{"  =1.2.3  ", "1.2.3", true},
		{"vv1.2.3", "1.2.3", true},
		{"1.2.3+build", "1.2.3", true},
		{"v1.2.3-beta.1+b", "1.2.3-beta.1", true},
		{"1.2", "", false},
		{"", "", false},
		{"1.2.3 4", "", false},
		{"x1.2.3", "", false},
	}
	for _, c := range cases {
		t.Run("R-NPM-007 Clean "+c.in, func(t *testing.T) {
			got, ok := Clean(c.in)
			if ok != c.valid || got != c.want {
				t.Fatalf("Clean(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.valid)
			}
		})
	}
}

func mustParse(t testing.TB, s string) Version {
	t.Helper()
	v, err := Parse(s)
	if err != nil {
		t.Fatalf("Parse(%q): %v", s, err)
	}
	return v
}

func TestCompare(t *testing.T) {
	t.Run("R-NPM-016 the precedence chain of SemVer 2.0.0 item 11", func(t *testing.T) {
		chain := []string{
			"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2",
			"1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "2.0.0", "2.1.0", "2.1.1",
		}
		for i := range chain {
			for j := range chain {
				want := 0
				if i < j {
					want = -1
				} else if i > j {
					want = 1
				}
				if got := Compare(mustParse(t, chain[i]), mustParse(t, chain[j])); got != want {
					t.Errorf("Compare(%s, %s) = %d, want %d", chain[i], chain[j], got, want)
				}
			}
		}
	})
	cases := []struct {
		name, a, b string
		want       int
	}{
		{"numeric identifiers compare by value", "1.0.0-2", "1.0.0-10", -1},
		{"numeric before alphanumeric", "1.0.0-1", "1.0.0-a", -1},
		{"alphanumeric compares in ASCII order", "1.0.0-B", "1.0.0-a", -1},
		{"a prefix sorts first", "1.0.0-a", "1.0.0-a.0", -1},
		{"build metadata is ignored", "1.0.0+a", "1.0.0+b", 0},
		{"prerelease below release", "1.0.0-rc.1", "1.0.0", -1},
		{"major wins over minor", "2.0.0", "1.99.99", 1},
		{"numeric identifiers beyond 2^53 compare exactly", "1.0.0-9007199254740993", "1.0.0-9007199254740992", 1},
		{"long numeric identifiers compare by length first", "1.0.0-99999999999999999999", "1.0.0-100000000000000000000", -1},
	}
	for _, c := range cases {
		t.Run("R-NPM-016 "+c.name, func(t *testing.T) {
			a, b := mustParse(t, c.a), mustParse(t, c.b)
			if got := Compare(a, b); got != c.want {
				t.Fatalf("Compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
			}
			if got := Compare(b, a); got != -c.want {
				t.Fatalf("Compare(%s, %s) = %d, want %d", c.b, c.a, got, -c.want)
			}
		})
	}
}

func TestSort(t *testing.T) {
	in := []string{"1.0.0+b", "1.0.0", "1.0.0-rc.1", "0.9.0", "1.0.0+a", "1.0.0+a.1", "1.0.0+2", "1.0.0+10", "2.0.0-0", "10.0.0", "2.0.0"}
	asc := []string{"0.9.0", "1.0.0-rc.1", "1.0.0", "1.0.0+2", "1.0.0+10", "1.0.0+a", "1.0.0+a.1", "1.0.0+b", "2.0.0-0", "2.0.0", "10.0.0"}
	parse := func(ss []string) []Version {
		vs := make([]Version, len(ss))
		for i, s := range ss {
			vs[i] = mustParse(t, s)
		}
		return vs
	}
	names := func(vs []Version) []string {
		out := make([]string, len(vs))
		for i, v := range vs {
			out[i] = v.String()
		}
		return out
	}
	t.Run("R-NPM-016 Sort orders by precedence, then by build metadata like node-semver sort", func(t *testing.T) {
		vs := parse(in)
		Sort(vs)
		if got := names(vs); !reflect.DeepEqual(got, asc) {
			t.Fatalf("Sort = %v, want %v", got, asc)
		}
	})
	t.Run("R-NPM-016 Rsort is the reverse order", func(t *testing.T) {
		vs := parse(in)
		Rsort(vs)
		want := make([]string, len(asc))
		for i, s := range asc {
			want[len(asc)-1-i] = s
		}
		if got := names(vs); !reflect.DeepEqual(got, want) {
			t.Fatalf("Rsort = %v, want %v", got, want)
		}
	})
}

func TestValidateTag(t *testing.T) {
	cases := []struct {
		tag   string
		valid bool
	}{
		{"latest", true},
		{"next", true},
		{"beta", true},
		{"rc-1", true},
		{"v1-legacy", true},
		{"a.b_c-d", true},
		{"1.2.3-foo", false}, // starts as a version, "1.2.3-foo" is a valid range
		{"canary-2024", true},
		{"0", false}, // a range
		{"1", false},
		{"1.x", false},
		{"v1.4", false},
		{"1.2.3", false},
		{"x", false},
		{"X", false},
		{">1", false}, // outside the character set too
		{"*", false},
		{"", false},
		{"-next", false},
		{".next", false},
		{"_next", false},
		{"ne xt", false},
		{"ne/xt", false},
		{"nëxt", false},
		{strings.Repeat("a", 128), true},
		{strings.Repeat("a", 129), false},
	}
	for _, c := range cases {
		t.Run("R-NPM-007 R-NPM-016 tag "+c.tag, func(t *testing.T) {
			err := ValidateTag(c.tag)
			if (err == nil) != c.valid {
				t.Fatalf("ValidateTag(%q) = %v, want valid = %v", c.tag, err, c.valid)
			}
			if err != nil {
				if e, ok := errs.As(err); !ok || e.Kind != errs.Invalid || e.Code != CodeInvalidTag {
					t.Fatalf("error = %#v, want an Invalid %s", err, CodeInvalidTag)
				}
			}
		})
	}
}

func TestErrorDetailIsSafe(t *testing.T) {
	t.Run("R-NPM-005 an error detail carries no control characters and stays short", func(t *testing.T) {
		in := "1.2.3\x00\n" + strings.Repeat("é", 500) + "\x1b[31m"
		_, err := Parse(in)
		e, ok := errs.As(err)
		if !ok {
			t.Fatalf("error = %v", err)
		}
		if len(e.Detail) > 128 {
			t.Fatalf("detail has %d bytes", len(e.Detail))
		}
		for i := 0; i < len(e.Detail); i++ {
			if e.Detail[i] < 0x20 || e.Detail[i] >= 0x7f {
				t.Fatalf("detail %q holds byte %#x", e.Detail, e.Detail[i])
			}
		}
	})
}
