package version

import (
	"slices"
	"testing"
)

// The order of R-DEB-002: a chain of versions, each older than the next.
func TestOrderChain(t *testing.T) {
	chain := []string{"1.0~rc1", "1.0", "1.0-1", "1.0+b1", "1.1~", "1.1", "1:0.1"}
	for i, a := range chain {
		for j, b := range chain {
			want := 0
			switch {
			case i < j:
				want = -1
			case i > j:
				want = 1
			}
			if got := Compare(a, b); got != want {
				t.Errorf("Compare(%q, %q) = %d, want %d", a, b, got, want)
			}
		}
	}
}

func TestEqualVersions(t *testing.T) {
	for _, p := range [][2]string{{"2.10-3", "0:2.10-3"}, {"1.0", "1.00"}, {"1.0-1", "1.0-01"}, {"1:1", "01:1"}, {"1.0", "1.0-0"}} {
		if !Equal(p[0], p[1]) || !Equal(p[1], p[0]) {
			t.Errorf("%q and %q are not equal", p[0], p[1])
		}
	}
}

// Pairs of dpkg's own test suite and of deb-version(7).
func TestDpkgPairs(t *testing.T) {
	older := [][2]string{
		{"1.0", "1.1"},
		{"1.9", "1.10"},
		{"2.10-3", "2.10-4"},
		{"2.9", "2.10"},
		{"1.0a", "1.0b"},
		{"1.0", "1.0a"},
		{"1.0~", "1.0"},
		{"1.0~~", "1.0~"},
		{"1.0~a", "1.0~b"},
		{"1.0", "1.0.1"},
		{"1.0+1", "1.0.1"},
		{"a", "b"},
		{"1.2.3", "1:0"},
		{"9:1", "10:0"},
		{"0.9-1", "0.9-1.1"},
		{"1~1", "1~1a"},
		{"1a", "1+"},
		{"1.0-1~bpo1", "1.0-1"},
		{"2.9.14+dfsg-1.3", "2.9.14+dfsg-1.3+b1"},
		{"1.0-9", "1.0-10"},
		{"007", "8"},
	}
	for _, p := range older {
		if Compare(p[0], p[1]) >= 0 || Compare(p[1], p[0]) <= 0 {
			t.Errorf("%q is not older than %q (%d, %d)", p[0], p[1], Compare(p[0], p[1]), Compare(p[1], p[0]))
		}
	}
}

func TestSort(t *testing.T) {
	vs := []string{"1:0.1", "1.1", "1.0+b1", "1.0", "1.0~rc1", "1.1~", "1.0-1"}
	slices.SortFunc(vs, Compare)
	want := []string{"1.0~rc1", "1.0", "1.0-1", "1.0+b1", "1.1~", "1.1", "1:0.1"}
	if !slices.Equal(vs, want) {
		t.Fatalf("sorted = %v", vs)
	}
}

func TestParse(t *testing.T) {
	cases := map[string]Version{
		"2.10-3":     {Upstream: "2.10", Revision: "3"},
		"1:2.0-1-2":  {Epoch: "1", Upstream: "2.0-1", Revision: "2"},
		"1.0":        {Upstream: "1.0"},
		"3:4.5":      {Epoch: "3", Upstream: "4.5"},
		"1.0+dfsg-1": {Upstream: "1.0+dfsg", Revision: "1"},
	}
	for in, want := range cases {
		if got := Parse(in); got != want {
			t.Errorf("Parse(%q) = %+v, want %+v", in, got, want)
		}
	}
}

func TestLessEmptyAndOddStringsDoNotPanic(t *testing.T) {
	for _, s := range []string{"", ":", "-", "a:b-c", "~", "99999999999999999999999:1"} {
		_ = Compare(s, "1.0")
		_ = Compare("1.0", s)
	}
	if !Less("", "1.0") {
		t.Error("the empty string is not the oldest version")
	}
}

func FuzzCompare(f *testing.F) {
	for _, s := range []string{"1.0~rc1", "1:2.3-4", "", "~~", "a1b2"} {
		f.Add(s, "1.0")
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		ab, ba := Compare(a, b), Compare(b, a)
		if (ab < 0) != (ba > 0) || (ab == 0) != (ba == 0) {
			t.Fatalf("Compare(%q, %q) = %d but the reverse is %d", a, b, ab, ba)
		}
		if Compare(a, a) != 0 {
			t.Fatalf("Compare(%q, %q) != 0", a, a)
		}
	})
}
