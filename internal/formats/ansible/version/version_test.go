package version

import (
	"bytes"
	"math/rand"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// chain is the precedence chain of https://semver.org/spec/v2.0.0.html#spec-item-11, lowest first.
var chain = []string{
	"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0",
	"1.0.1", "1.1.0", "1.10.0", "2.0.0", "10.0.0", "10.0.1",
}

func TestR_ANS_002_PrecedenceFollowsSemver(t *testing.T) {
	for i, a := range chain {
		for j, b := range chain {
			va, err := Parse(a)
			if err != nil {
				t.Fatalf("Parse(%q): %v", a, err)
			}
			vb, err := Parse(b)
			if err != nil {
				t.Fatalf("Parse(%q): %v", b, err)
			}
			want := 0
			switch {
			case i < j:
				want = -1
			case i > j:
				want = 1
			}
			if got := Compare(va, vb); got != want {
				t.Errorf("Compare(%q, %q) = %d, want %d", a, b, got, want)
			}
			if got := bytes.Compare(SortKey(va), SortKey(vb)); got != want {
				t.Errorf("SortKey order of (%q, %q) = %d, want %d", a, b, got, want)
			}
		}
	}
}

func TestR_ANS_002_BuildMetadataTakesNoPartInPrecedence(t *testing.T) {
	for _, tc := range []struct{ a, b string }{
		{"1.0.0", "1.0.0+build.5"}, {"1.0.0+a", "1.0.0+b"}, {"1.0.0-rc.1+x", "1.0.0-rc.1"},
	} {
		if !Same(tc.a, tc.b) {
			t.Errorf("Same(%q, %q) = false, want true", tc.a, tc.b)
		}
	}
	if Same("1.0.0", "1.0.1") || Same("1.0.0-rc.1", "1.0.0") {
		t.Error("versions of different precedence are the same version")
	}
	if !Same("not-a-version", "not-a-version") || Same("not-a-version", "other") {
		t.Error("a string that is no version is the same as itself and nothing else")
	}
	v, err := Parse("2.0.0+build.5")
	if err != nil || v.Build != "build.5" || v.Original != "2.0.0+build.5" {
		t.Errorf("Parse = %+v, %v", v, err)
	}
}

func TestR_ANS_002_ParseRefusesWhatSemverRefuses(t *testing.T) {
	valid := []string{
		"0.0.0", "1.2.3", "1.0.0-rc.1", "2.0.0+build.5", "1.0.0-0.3.7", "1.0.0-x-y-z.--", "1.0.0+001", "1.0.0-alpha+001", "1.0.0-rc.1-", "1.0.0--",
		"123456789012345678901234567890.1.1", "1.0.0-123456789012345678901234567890",
	}
	for _, s := range valid {
		if !Valid(s) {
			t.Errorf("Valid(%q) = false", s)
		}
	}
	invalid := []string{
		"", "1", "1.0", "1.0.0.0", "v1.0.0", "01.0.0", "1.01.0", "1.0.01", "1.0.0-", "1.0.0+", "1.0.0-01", "1.0.0-a..b",
		"1.0.0-a.", "1.0.0+a..b", "1.0.0-é", "1.0.0 ", " 1.0.0", "1.0.0-a_b", "a.b.c", "-1.0.0", strings.Repeat("1", MaxLength) + ".0.0",
	}
	for _, s := range invalid {
		if Valid(s) {
			t.Errorf("Valid(%q) = true", s)
		}
	}
}

// reference is a direct implementation of the precedence rules, independent of the sort key.
func reference(a, b Semver) int {
	for _, p := range [][2]string{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if c := compareNumbers(p[0], p[1]); c != 0 {
			return c
		}
	}
	switch {
	case a.Pre == nil && b.Pre == nil:
		return 0
	case a.Pre == nil:
		return 1
	case b.Pre == nil:
		return -1
	}
	for i := 0; i < len(a.Pre) && i < len(b.Pre); i++ {
		x, y := a.Pre[i], b.Pre[i]
		xn, yn := numeric(x), numeric(y)
		var c int
		switch {
		case xn && yn:
			c = compareNumbers(x, y)
		case xn:
			c = -1
		case yn:
			c = 1
		default:
			c = strings.Compare(x, y)
		}
		if c != 0 {
			return c
		}
	}
	return len(a.Pre) - len(b.Pre)
}

func compareNumbers(a, b string) int {
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

func TestR_ANS_002_SortKeyOrderEqualsTheReferenceOrder(t *testing.T) {
	rnd := rand.New(rand.NewSource(7))
	ids := []string{"0", "1", "2", "10", "alpha", "beta", "a", "ab", "a-b", "-", "rc", "99999999999"}
	gen := func() Semver {
		s := strconv.Itoa(rnd.Intn(3)) + "." + strconv.Itoa(rnd.Intn(12)) + "." + strconv.Itoa(rnd.Intn(3))
		if rnd.Intn(2) == 0 {
			n := 1 + rnd.Intn(3)
			parts := make([]string, n)
			for i := range parts {
				parts[i] = ids[rnd.Intn(len(ids))]
			}
			s += "-" + strings.Join(parts, ".")
		}
		v, err := Parse(s)
		if err != nil {
			t.Fatalf("generated %q: %v", s, err)
		}
		return v
	}
	for i := 0; i < 20000; i++ {
		a, b := gen(), gen()
		if got, want := sign(bytes.Compare(SortKey(a), SortKey(b))), sign(reference(a, b)); got != want {
			t.Fatalf("%q vs %q: key order %d, reference %d", a.Original, b.Original, got, want)
		}
	}
}

func TestR_ANS_002_ASortedListIsNewestFirstByKey(t *testing.T) {
	in := []string{"1.10.0", "1.2.0", "1.2.0-rc.1", "1.9.0", "2.0.0+b", "1.2.0+a"}
	type kv struct {
		s string
		k []byte
	}
	var all []kv
	for _, s := range in {
		v, _ := Parse(s)
		all = append(all, kv{s, SortKey(v)})
	}
	slices.SortStableFunc(all, func(a, b kv) int { return bytes.Compare(b.k, a.k) })
	var got []string
	for _, e := range all {
		got = append(got, e.s)
	}
	want := []string{"2.0.0+b", "1.10.0", "1.9.0", "1.2.0", "1.2.0+a", "1.2.0-rc.1"}
	if !slices.Equal(got, want) {
		t.Errorf("descending order = %v, want %v", got, want)
	}
}

func FuzzR_ANS_002_ParseAndOrder(f *testing.F) {
	for _, s := range chain {
		f.Add(s, "1.0.0")
	}
	f.Add("1.0.0+x", "")
	f.Fuzz(func(t *testing.T, a, b string) {
		va, ea := Parse(a)
		vb, eb := Parse(b)
		if ea == nil && (va.Original != a || len(a) > MaxLength) {
			t.Fatalf("Parse(%q) = %+v", a, va)
		}
		if ea != nil || eb != nil {
			return
		}
		if got, want := sign(Compare(va, vb)), sign(reference(va, vb)); got != want {
			t.Fatalf("Compare(%q, %q) = %d, reference %d", a, b, got, want)
		}
		if sign(Compare(va, vb)) != -sign(Compare(vb, va)) {
			t.Fatalf("Compare is not antisymmetric for %q, %q", a, b)
		}
	})
}
