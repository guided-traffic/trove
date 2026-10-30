package semver

import (
	"reflect"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

func optName(o Options) string {
	switch {
	case o.Loose:
		return "loose"
	case o.IncludePrerelease:
		return "includePrerelease"
	}
	return "strict"
}

// TestCorpus runs the node-semver corpus (corpus_test.go): which texts are
// valid ranges, the formatted range, and which of corpusVersions satisfy it.
func TestCorpus(t *testing.T) {
	versions := strings.Fields(corpusVersions)
	for _, row := range corpus() {
		t.Run("R-NPM-048 "+optName(row.opts)+" "+strings.TrimSpace(row.rng), func(t *testing.T) {
			r, err := ParseRange(row.rng, row.opts)
			if (err == nil) != row.valid {
				t.Fatalf("ParseRange(%q, %v) error = %v, want valid = %v", row.rng, row.opts, err, row.valid)
			}
			if err != nil {
				if e, ok := errs.As(err); !ok || e.Kind != errs.Invalid || e.Code != CodeInvalidRange {
					t.Fatalf("error = %#v, want an Invalid %s", err, CodeInvalidRange)
				}
				return
			}
			if got := r.String(); got != row.format {
				t.Fatalf("String() = %q, want %q", got, row.format)
			}
			var got []string
			for _, v := range versions {
				if r.SatisfiesString(v) {
					got = append(got, v)
				}
			}
			if joined := strings.Join(got, " "); joined != row.matches {
				t.Fatalf("satisfied by %q, want %q", joined, row.matches)
			}
			again, err := ParseRange(r.String(), row.opts)
			if err != nil || again.String() != r.String() {
				t.Fatalf("re-parsing %q = %v, %v: the formatted range must settle", r.String(), again, err)
			}
		})
	}
}

func TestPrereleaseRule(t *testing.T) {
	cases := []struct {
		name, rng, version string
		opts               Options
		want               bool
	}{
		// node-semver README: a prerelease satisfies only when a comparator of the
		// same [major, minor, patch] carries a prerelease.
		{"caret with prerelease admits a later prerelease of the same tuple", "^1.2.3-beta.2", "1.2.3-beta.4", Options{}, true},
		{"caret with prerelease refuses a prerelease of another tuple", "^1.2.3-beta.2", "1.2.4-beta.2", Options{}, false},
		{"caret with prerelease admits the release above", "^1.2.3-beta.2", "1.2.3", Options{}, true},
		{"caret with prerelease refuses a lower prerelease", "^1.2.3-beta.2", "1.2.3-beta.1", Options{}, false},
		{"a plain range refuses a prerelease above its floor", ">=1.2.3", "1.3.0-beta", Options{}, false},
		{"a plain range refuses a prerelease below its ceiling", "<2.0.0", "1.9.0-rc.1", Options{}, false},
		{"includePrerelease admits it", ">=1.2.3", "1.3.0-beta", Options{IncludePrerelease: true}, true},
		{"includePrerelease still honors the ceiling's lowest prerelease", "<2.0.0", "2.0.0-rc.1", Options{IncludePrerelease: true}, true},
		{"an x-range ceiling excludes the prerelease of the next major", "1.x", "2.0.0-rc.1", Options{IncludePrerelease: true}, false},
		{"an x-range ceiling excludes the prerelease of the next major without the option", "1.x", "2.0.0-rc.1", Options{}, false},
		{"the star admits releases only", "*", "1.0.0-alpha", Options{}, false},
		{"the star admits prereleases with the option", "*", "1.0.0-alpha", Options{IncludePrerelease: true}, true},
		{"the empty range admits releases only", "", "1.0.0-alpha", Options{}, false},
		{"the rule is per comparator set", "1.0.0-beta.1 || >=2.0.0", "2.5.0-rc.1", Options{}, false},
		{"another set's prerelease does not help", "1.0.0-beta.1 || >=2.0.0", "1.0.0-beta.1", Options{}, true},
		{"a bound without prerelease does not help", ">=1.2.3 <1.3.0", "1.2.4-beta", Options{}, false},
		{"hyphen range with a prerelease bound", "1.0.0-beta - 2.0.0", "1.0.0-rc.1", Options{}, true},
		{"hyphen range does not admit another tuple's prerelease", "1.0.0-beta - 2.0.0", "1.5.0-rc.1", Options{}, false},
		{"a version with build metadata satisfies like its core", "1.2.3", "1.2.3+build", Options{}, true},
		{"a text that is not a version satisfies nothing", "*", "latest", Options{IncludePrerelease: true}, false},
	}
	for _, c := range cases {
		t.Run("R-NPM-048 "+c.name, func(t *testing.T) {
			r, err := ParseRange(c.rng, c.opts)
			if err != nil {
				t.Fatalf("ParseRange(%q): %v", c.rng, err)
			}
			if got := r.SatisfiesString(c.version); got != c.want {
				t.Fatalf("%q satisfies %q = %v, want %v", c.version, c.rng, got, c.want)
			}
		})
	}
}

func TestStrictAndLoose(t *testing.T) {
	cases := []struct {
		name, rng       string
		strict, loose   bool
		looseMatches    []string
		looseNotMatches []string
	}{
		{"garbage comparator", ">=1.2.3 garbage", false, true, []string{"1.2.3", "2.0.0"}, nil},
		{"only garbage", "garbage", false, false, nil, nil},
		{"leading zeros", "01.2.3", false, true, []string{"1.2.3"}, []string{"1.2.4"}},
		{"equals spaced", "= 1.2.3", true, true, []string{"1.2.3"}, nil},
		{"prerelease without hyphen", "1.2.3beta1", false, true, []string{"1.2.3-beta1"}, nil},
		{"v prefix is strict-valid", "v1.2.3", true, true, []string{"1.2.3"}, nil},
	}
	for _, c := range cases {
		t.Run("R-NPM-048 "+c.name, func(t *testing.T) {
			if got := Validate(c.rng) == nil; got != c.strict {
				t.Fatalf("Validate(%q) valid = %v, want %v", c.rng, got, c.strict)
			}
			r, err := ParseRange(c.rng, Options{Loose: true})
			if (err == nil) != c.loose {
				t.Fatalf("loose ParseRange(%q) error = %v, want valid = %v", c.rng, err, c.loose)
			}
			if err != nil {
				return
			}
			for _, v := range c.looseMatches {
				if !r.SatisfiesString(v) {
					t.Errorf("loose %q does not admit %q", c.rng, v)
				}
			}
			for _, v := range c.looseNotMatches {
				if r.SatisfiesString(v) {
					t.Errorf("loose %q admits %q", c.rng, v)
				}
			}
		})
	}
}

func TestWhitespace(t *testing.T) {
	cases := []struct {
		name, rng string
		want      string
	}{
		{"no-break space separates", ">=1.2.3\u00a0<2.0.0", ">=1.2.3 <2.0.0"},
		{"ideographic space trims", "\u3000^1.2.3\u3000", ">=1.2.3 <2.0.0-0"},
		{"byte order mark trims", "\ufeff1.2.3", "1.2.3"},
		{"tabs and newlines collapse", ">=1.2.3\t\n<2", ">=1.2.3 <2.0.0-0"},
		{"line separator collapses", ">=1.2.3\u2028<2", ">=1.2.3 <2.0.0-0"},
		{"operator separated from its version", ">=   1.2.3", ">=1.2.3"},
		{"next-line is not whitespace", "", ""},
	}
	for _, c := range cases {
		t.Run("R-NPM-048 "+c.name, func(t *testing.T) {
			if c.name == "next-line is not whitespace" {
				if err := Validate("1.2.3\u0085"); err == nil {
					t.Fatal("U+0085 must not be whitespace, as in ECMAScript")
				}
				return
			}
			r, err := ParseRange(c.rng, Options{})
			if err != nil {
				t.Fatalf("ParseRange(%q): %v", c.rng, err)
			}
			if r.String() != c.want {
				t.Fatalf("String() = %q, want %q", r.String(), c.want)
			}
		})
	}
}

func TestRangeLength(t *testing.T) {
	t.Run("R-NPM-048 a range at the bound parses and one byte above is refused", func(t *testing.T) {
		pad := strings.Repeat(" ", MaxRangeLength-len("1.2.3"))
		if err := Validate("1.2.3" + pad); err != nil {
			t.Fatalf("range of %d bytes: %v", MaxRangeLength, err)
		}
		if err := Validate("1.2.3" + pad + " "); err == nil {
			t.Fatal("range above the bound accepted")
		}
	})
	t.Run("R-NPM-048 a long run of alternatives stays fast and valid", func(t *testing.T) {
		rng := strings.Repeat("1.x || ", 500) + "2.x"
		if len(rng) > MaxRangeLength {
			t.Fatalf("test range is %d bytes", len(rng))
		}
		if _, err := ParseRange(rng, Options{}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestExposureRange(t *testing.T) {
	t.Run("R-NPM-048 ParseForExposure refuses what npm refuses", func(t *testing.T) {
		for _, bad := range []string{"garbage", ">=1.2.3 junk", "1.2.3.4", ">=", "^"} {
			if _, err := ParseForExposure(bad); err == nil {
				t.Errorf("ParseForExposure(%q) accepted", bad)
			}
		}
	})
	t.Run("R-NPM-048 the rule's range ignores the pre-release rule and keeps the numeric bounds", func(t *testing.T) {
		r, err := ParseForExposure(">=2.0.0 <4")
		if err != nil {
			t.Fatal(err)
		}
		for v, want := range map[string]bool{
			"1.9.9":         false,
			"2.0.0-rc.1":    false, // below the floor, as a number
			"2.0.0":         true,
			"2.1.0-beta":    true, // a pre-release inside the range; the rule's prereleases flag decides
			"3.9.9":         true,
			"4.0.0-rc.1":    false, // `<4` excludes every 4.0.0 pre-release
			"4.0.0":         false,
			"3.0.0-alpha.1": true,
		} {
			if got := r.SatisfiesString(v); got != want {
				t.Errorf("%s in >=2.0.0 <4 = %v, want %v", v, got, want)
			}
		}
	})
	t.Run("R-NPM-048 a caret keeps its ceiling for pre-releases", func(t *testing.T) {
		r, err := ParseForExposure("^2.1.0")
		if err != nil {
			t.Fatal(err)
		}
		if r.SatisfiesString("3.0.0-rc.1") || !r.SatisfiesString("2.2.0-rc.1") {
			t.Fatal("^2.1.0 must exclude 3.0.0-rc.1 and include 2.2.0-rc.1 when pre-releases are exposed")
		}
	})
	t.Run("R-NPM-048 acceptance holds in both option sets at the largest component", func(t *testing.T) {
		// The upper bound of a hyphen range adds one to the patch only when
		// pre-releases are included; 2^53 is not a version component.
		const rng = "1.0.0 - 1.0.9007199254740991"
		if err := Validate(rng); err != nil {
			t.Fatalf("strict: %v", err)
		}
		if _, err := ParseRange(rng, Options{IncludePrerelease: true}); err == nil {
			t.Fatal("includePrerelease should refuse the bound that needs 2^53")
		}
		if _, err := ParseForExposure(rng); err == nil {
			t.Fatal("ParseForExposure must refuse a range its evaluation cannot represent")
		}
	})
}

func TestMaxSatisfying(t *testing.T) {
	versions := []string{"1.0.0", "1.2.0", "1.2.1-beta.1", "1.3.0-rc.1", "2.0.0", "2.1.0", "not-a-version", "v1.2.5"}
	cases := []struct {
		name, rng string
		opts      Options
		want      string
		ok        bool
	}{
		{"highest release in a caret", "^1.0.0", Options{}, "v1.2.5", true},
		{"prereleases excluded by the rule", "^1.2.0", Options{}, "v1.2.5", true},
		{"prereleases included on request", ">=1.3.0-0 <2", Options{IncludePrerelease: true}, "1.3.0-rc.1", true},
		{"nothing satisfies", ">=3", Options{}, "", false},
		{"tilde", "~2.0.0", Options{}, "2.0.0", true},
		{"any", "*", Options{}, "2.1.0", true},
	}
	for _, c := range cases {
		t.Run("R-NPM-016 R-NPM-048 "+c.name, func(t *testing.T) {
			r, err := ParseRange(c.rng, c.opts)
			if err != nil {
				t.Fatal(err)
			}
			got, ok := MaxSatisfyingStrings(versions, r)
			if ok != c.ok || got != c.want {
				t.Fatalf("MaxSatisfyingStrings = %q, %v; want %q, %v", got, ok, c.want, c.ok)
			}
		})
	}
	t.Run("R-NPM-016 the first of equal-precedence versions wins", func(t *testing.T) {
		r, _ := ParseRange("*", Options{})
		vs := []Version{mustParse(t, "1.0.0+a"), mustParse(t, "1.0.0+b")}
		got, ok := MaxSatisfying(vs, r)
		if !ok || got.String() != "1.0.0+a" {
			t.Fatalf("MaxSatisfying = %v, %v", got, ok)
		}
	})
	t.Run("R-NPM-016 an empty list has no maximum", func(t *testing.T) {
		r, _ := ParseRange("*", Options{})
		if _, ok := MaxSatisfying(nil, r); ok {
			t.Fatal("found a maximum in an empty list")
		}
	})
}

func TestRangeIsReusable(t *testing.T) {
	t.Run("a parsed range does not change between uses", func(t *testing.T) {
		r, err := ParseRange("^1.2.3 || >=3 <4", Options{})
		if err != nil {
			t.Fatal(err)
		}
		before := r.String()
		for i := 0; i < 3; i++ {
			r.SatisfiesString("1.5.0")
		}
		if r.String() != before || !reflect.DeepEqual(r.Options(), Options{}) {
			t.Fatal("range changed")
		}
	})
}

func FuzzRange(f *testing.F) {
	for _, row := range corpus() {
		f.Add(row.rng, "1.2.3")
	}
	for _, v := range strings.Fields(corpusVersions) {
		f.Add("^1.0.0", v)
	}
	f.Add("", "")
	f.Add("1.2.3 - 2.3.4 || >=5 <6", "v1.0.0-0+b")
	f.Add("\u00a0>=\u30001.2.3", "01.02.03")
	f.Fuzz(func(t *testing.T, rng, ver string) {
		// A version: parse(format(v)) == v, and the forms agree.
		for _, loose := range []bool{false, true} {
			v, err := ParseNode(ver, loose)
			if err != nil {
				continue
			}
			back, err := Parse(v.String())
			if err != nil || !reflect.DeepEqual(back, v) {
				// Loose parsing keeps the zeros of a numeric identifier it cannot
				// turn into a number (Q-NPM-15); the canonical text then fails
				// the strict grammar, and loose parsing must restore it.
				back2, err2 := ParseNode(v.String(), true)
				if err2 != nil || !reflect.DeepEqual(back2, v) {
					t.Fatalf("parse(format(%#v)) = %#v, %v / %#v, %v", v, back, err, back2, err2)
				}
			}
			if c, ok := Clean(ver); ok {
				cv, err := Parse(c)
				if err != nil || cv.Canonical() != c {
					t.Fatalf("Clean(%q) = %q is not canonical", ver, c)
				}
			}
			if Compare(v, v) != 0 || CompareBuild(v, v) != 0 {
				t.Fatalf("%v does not equal itself", v)
			}
		}
		// A range, in each option set: no panic, a formatted form that settles
		// after one more round, and the same answers from the formatted form.
		sample := []string{ver, "0.0.0", "1.2.3", "1.2.3-beta.1", "2.0.0", "2.0.0-0", "10.20.30"}
		for _, opts := range []Options{{}, {Loose: true}, {IncludePrerelease: true}, {Loose: true, IncludePrerelease: true}} {
			r, err := ParseRange(rng, opts)
			if err != nil {
				if e, ok := errs.As(err); !ok || e.Code != CodeInvalidRange {
					t.Fatalf("ParseRange(%q) error %v is not a %s", rng, err, CodeInvalidRange)
				}
				continue
			}
			s := r.String()
			again, err := ParseRange(s, opts)
			if err != nil {
				t.Fatalf("formatted range %q of %q does not parse: %v", s, rng, err)
			}
			// node-semver drops a ">=0.0.0" it finds in a range, including one it
			// wrote itself, so the form can shrink once; after that it is fixed.
			third, err := ParseRange(again.String(), opts)
			if err != nil || third.String() != again.String() {
				t.Fatalf("format is not stable: %q -> %q -> %q -> %v", rng, s, again.String(), third)
			}
			for _, v := range sample {
				if r.SatisfiesString(v) != again.SatisfiesString(v) {
					t.Fatalf("%q and its form %q disagree on %q", rng, s, v)
				}
			}
		}
	})
}
