package version

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/guided-traffic/trove/internal/errs"
)

// TestR_NUGET_003_Forms covers the three forms of R-NUGET-003 with the examples of the spec.
func TestR_NUGET_003_Forms(t *testing.T) {
	tests := []struct {
		in, normalized, full, url string
	}{
		{"01.2.0.0-RC.1+sha.9f2c", "1.2.0-RC.1", "1.2.0-RC.1+sha.9f2c", "1.2.0-rc.1"},
		{"01.02.03.0", "1.2.3", "1.2.3", "1.2.3"},
		{"2.5", "2.5.0", "2.5.0", "2.5.0"},
		{"2.5.1.3", "2.5.1.3", "2.5.1.3", "2.5.1.3"},
		{"2.5.1.0+sha.9f2c", "2.5.1", "2.5.1+sha.9f2c", "2.5.1"},
		{"2.6.1.0+sha.9f2c", "2.6.1", "2.6.1+sha.9f2c", "2.6.1"},
		{"1.0.0-Beta.2", "1.0.0-Beta.2", "1.0.0-Beta.2", "1.0.0-beta.2"},
		{"1", "1.0.0", "1.0.0", "1.0.0"},
		{" 1.0.0.0 ", "1.0.0", "1.0.0", "1.0.0"},
		{"0.0.0.1", "0.0.0.1", "0.0.0.1", "0.0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			v, err := Parse(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if v.Normalized() != tt.normalized || v.FullNormalized() != tt.full || v.URLForm() != tt.url || v.String() != tt.full {
				t.Fatalf("got %q %q %q, want %q %q %q", v.Normalized(), v.FullNormalized(), v.URLForm(), tt.normalized, tt.full, tt.url)
			}
			if v.Original() != tt.in {
				t.Fatalf("Original() = %q", v.Original())
			}
		})
	}
}

// TestR_NUGET_003_DuplicateDetection: the pairs of the acceptance table that must collide
// (second push 409) have one URL form, the ones that must not (201) have two.
func TestR_NUGET_003_DuplicateDetection(t *testing.T) {
	tests := []struct {
		first, second string
		same          bool
	}{
		{"01.02.03.0", "1.2.3+other", true},
		{"2.5", "2.5.0.0", true},
		{"2.5.1.3", "2.5.1.3+x", true},
		{"2.6.1.0+sha.9f2c", "2.6.1", true},
		{"1.0.0-Beta.2", "1.0.0-BETA.2", true},
		{"1.0.0-beta", "1.0.0-beta.1", false},
		{"1", "1.0.0.0", true},
		{"3.0.0+a1", "3.0.0+b2", true},
		{"1.0.0", "1.0.0.1", false},
	}
	for _, tt := range tests {
		a, b := MustParse(tt.first), MustParse(tt.second)
		if got := a.URLForm() == b.URLForm(); got != tt.same {
			t.Errorf("%q and %q: same URL form = %v, want %v", tt.first, tt.second, got, tt.same)
		}
		if got := a.Equal(b); got != tt.same {
			t.Errorf("%q and %q: Equal = %v, want %v", tt.first, tt.second, got, tt.same)
		}
	}
}

// TestR_NUGET_003_FlatContainerOrder is the list of the spec: pre-release identifiers compare
// numerically and case-insensitively, a release sits above its pre-releases and the fourth
// part above the release. The sort keys give the same order.
func TestR_NUGET_003_FlatContainerOrder(t *testing.T) {
	want := []string{"0.9.0", "1.0.0-alpha", "1.0.0-rc.2", "1.0.0-rc.10", "1.0.0", "1.0.0.1"}
	in := []string{"1.0.0", "1.0.0-rc.10", "1.0.0-rc.2", "1.0.0-alpha", "1.0.0.1", "0.9.0"}

	byCompare := slices.Clone(in)
	slices.SortFunc(byCompare, func(a, b string) int { return Compare(MustParse(a), MustParse(b)) })
	if !slices.Equal(byCompare, want) {
		t.Fatalf("by Compare: %v", byCompare)
	}
	byKey := slices.Clone(in)
	slices.SortFunc(byKey, func(a, b string) int { return bytes.Compare(MustParse(a).SortKey(), MustParse(b).SortKey()) })
	if !slices.Equal(byKey, want) {
		t.Fatalf("by SortKey: %v", byKey)
	}
}

func TestR_NUGET_003_Precedence(t *testing.T) {
	// Each line is ascending; all the versions of a line are strictly ordered.
	chains := []string{
		"1.0.0-alpha < 1.0.0-alpha.1 < 1.0.0-alpha.beta < 1.0.0-beta < 1.0.0-beta.2 < 1.0.0-beta.11 < 1.0.0-rc.1 < 1.0.0",
		"1.0.0-1 < 1.0.0-2 < 1.0.0-10 < 1.0.0-2147483647 < 1.0.0-2147483648 < 1.0.0-a",
		"1.0.0-a < 1.0.0-a.0 < 1.0.0-a.1 < 1.0.0-a.1.0 < 1.0.0-a.a < 1.0.0-aa < 1.0.0-b",
		"1.0.0-ALPHA < 1.0.0-beta < 1.0.0-GAMMA",
		"1.0.0-1 < 1.0.0-1a < 1.0.0-a",
		"1.0.0-a.b < 1.0.0-a-b < 1.0.0-ab",
		"0.0.0.1 < 0.0.1 < 0.1.0 < 1.0.0 < 1.0.0.1 < 1.0.1 < 1.1.0 < 2.0.0 < 10.0.0 < 2147483647.0.0.0",
		"1.0.0-rc.1 < 1.0.0 < 1.0.0.1-alpha < 1.0.0.1",
	}
	for _, chain := range chains {
		versions := strings.Split(chain, " < ")
		for i := range versions {
			for j := range versions {
				a, b := MustParse(versions[i]), MustParse(versions[j])
				want := cmpInt(i, j)
				if got := Compare(a, b); got != want {
					t.Errorf("Compare(%q, %q) = %d, want %d", versions[i], versions[j], got, want)
				}
				if got := bytes.Compare(a.SortKey(), b.SortKey()); got != want {
					t.Errorf("SortKey order (%q, %q) = %d, want %d", versions[i], versions[j], got, want)
				}
			}
		}
	}
}

func TestR_NUGET_003_BuildMetadataDoesNotOrder(t *testing.T) {
	a, b := MustParse("3.0.0+a1"), MustParse("3.0.0+b2")
	if Compare(a, b) != 0 || !bytes.Equal(a.SortKey(), b.SortKey()) {
		t.Fatal("build metadata took part in the order")
	}
}

func TestR_NUGET_003_SortKeyBytes(t *testing.T) {
	tests := []struct {
		in   string
		want []byte
	}{
		{"1.2.3", []byte{0, 0, 0, 1, 0, 0, 0, 2, 0, 0, 0, 3, 0, 0, 0, 0, 0xFF}},
		{"1.2.3.4", []byte{0, 0, 0, 1, 0, 0, 0, 2, 0, 0, 0, 3, 0, 0, 0, 4, 0xFF}},
		{"1.0.0-RC.10", append([]byte{0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x02, 'r', 'c', 0x00, 0x01, 2, '1', '0'}, 0x00)},
		{"0.0.0-a-1.7+x", append([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x02, 'a', '-', '1', 0x00, 0x01, 1, '7'}, 0x00)},
		{"2147483647.0.0", []byte{0x7F, 0xFF, 0xFF, 0xFF, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF}},
	}
	for _, tt := range tests {
		if got := MustParse(tt.in).SortKey(); !bytes.Equal(got, tt.want) {
			t.Errorf("SortKey(%q) = %x, want %x", tt.in, got, tt.want)
		}
	}
}

// TestR_NUGET_004_Semver2 covers the classification of R-NUGET-004: a dot in the original
// pre-release label, or build metadata.
func TestR_NUGET_004_Semver2(t *testing.T) {
	tests := map[string]bool{
		"1.0.0":              false,
		"1.0.0-beta":         false,
		"1.0.0-beta1":        false,
		"1.0.0-beta-1":       false,
		"1.0.0-beta.1":       true,
		"1.0.0-ci.5+sha.abc": true,
		"1.0.0+sha":          true,
		"1.0.0.0+sha":        true,
		"2.0.0-RC.1":         true,
	}
	for in, want := range tests {
		if got := MustParse(in).IsSemver2(); got != want {
			t.Errorf("IsSemver2(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestR_NUGET_004_RangeSemver2(t *testing.T) {
	tests := map[string]bool{
		"1.0":                    false,
		"[1.0,2.0)":              false,
		"1.0.0-beta.1":           true,
		"[1.0,2.0.0-rc.1)":       true,
		"(,2.0.0+meta]":          true,
		"[1.0.0-beta,2.0.0-rc)":  false,
		"[1.0.0+sha]":            true,
		"(1.0.0-ci.5,)":          true,
		"[1.0.0-beta-1,2.0-x-y)": false,
	}
	for in, want := range tests {
		r, err := ParseRange(in)
		if err != nil {
			t.Fatalf("ParseRange(%q): %v", in, err)
		}
		if got := r.IsSemver2(); got != want {
			t.Errorf("Range(%q).IsSemver2() = %v, want %v", in, got, want)
		}
	}
}

// dependencyCase is one row of testdata/dependency-semver2.json, the table the push and the
// recomputation of stored rows decide alike (Q-NUGET-93).
type dependencyCase struct {
	Name    string `json:"name"`
	Range   string `json:"range"`
	Semver2 bool   `json:"semver2"`
}

func loadDependencyCases(t *testing.T) []dependencyCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/dependency-semver2.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Cases []dependencyCase `json:"cases"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if len(f.Cases) < 6 {
		t.Fatalf("%d cases in the table", len(f.Cases))
	}
	return f.Cases
}

// TestR_NUGET_004_PackageSemver2 covers the classification of a package: its version, or a bound
// of one of its dependency ranges. A range that is empty or does not parse does not count.
func TestR_NUGET_004_PackageSemver2(t *testing.T) {
	sv1, sv2 := MustParse("1.0.0"), MustParse("1.0.0-ci.5")
	for _, c := range loadDependencyCases(t) {
		t.Run(c.Name, func(t *testing.T) {
			if got := PackageIsSemver2(sv1, "[13.0.1,)", c.Range); got != c.Semver2 {
				t.Errorf("PackageIsSemver2(1.0.0, %q) = %v, want %v", c.Range, got, c.Semver2)
			}
			if !PackageIsSemver2(sv2, c.Range) {
				t.Errorf("PackageIsSemver2(1.0.0-ci.5, %q) = false: the version alone decides", c.Range)
			}
		})
	}
	if PackageIsSemver2(sv1) || PackageIsSemver2(sv1, "") || PackageIsSemver2(sv1, "[1.0.0-ci.5") {
		t.Error("no range, an empty one or one that does not parse made a SemVer 1 version SemVer 2")
	}
}

func TestR_NUGET_003_RangeNotation(t *testing.T) {
	tests := []struct {
		in, str string
		probes  [3]string
		want    [3]bool
	}{
		{"2.0", "[2.0.0, )", [3]string{"2.0", "99.0", "1.9.9"}, [3]bool{true, true, false}},
		{"[1.0,2.0)", "[1.0.0, 2.0.0)", [3]string{"1.0", "2.0", "0.9"}, [3]bool{true, false, false}},
		{"(,1.0]", "(, 1.0.0]", [3]string{"0.1", "1.0", "1.0.0.1"}, [3]bool{true, true, false}},
		{"[1.0]", "[1.0.0, 1.0.0]", [3]string{"1.0.0.0", "1.0.0+x", "1.0.1"}, [3]bool{true, true, false}},
		{"(1.0,)", "(1.0.0, )", [3]string{"1.0.0.1", "5.0", "1.0"}, [3]bool{true, true, false}},
	}
	for _, tt := range tests {
		r, err := ParseRange(tt.in)
		if err != nil {
			t.Fatalf("ParseRange(%q): %v", tt.in, err)
		}
		if r.String() != tt.str {
			t.Errorf("ParseRange(%q).String() = %q, want %q", tt.in, r.String(), tt.str)
		}
		for i, p := range tt.probes {
			if got := r.Satisfies(MustParse(p)); got != tt.want[i] {
				t.Errorf("Range(%q).Satisfies(%q) = %v, want %v", tt.in, p, got, tt.want[i])
			}
		}
	}
}

func TestR_NUGET_003_RefusedRanges(t *testing.T) {
	for _, in := range []string{
		"", " ", "[", "()", "[]", "(,)", "[,]", "[1.0,2.0", "1.0,2.0)", "(1.0)", "[1.0)", "[2.0,1.0]",
		"[1.0,1.0)", "(1.0,1.0]", "[1.0,2.0,3.0]", "1.0.*", "*", "[1.*,)", "[a,)", "~1.0", ">=1.0", strings.Repeat("1", MaxRangeLength+1),
	} {
		_, err := ParseRange(in)
		var e *errs.Error
		if !errors.As(err, &e) || e.Kind != errs.Invalid || e.Code != CodeInvalidRange {
			t.Errorf("ParseRange(%q) = %v, want an invalid-range error", in, err)
		}
	}
}

func TestR_NUGET_003_RefusedVersions(t *testing.T) {
	for _, in := range []string{
		"", " ", "a", "v1.0", "1.", ".1", "1..0", "1.0.0.0.0", "-1", "1.-1", "2147483648", "1.0.0-", "1.0.0-01",
		"1.0.0-a..b", "1.0.0-a_b", "1.0.0-é", "1.0.0+", "1.0.0+a..b", "1.0.0+a+b", "1.0.0+é", "1 0", "1.0.0\x00", "\u001f1.0.0",
		"1.0.0-" + strings.Repeat("a", MaxLength),
	} {
		_, err := Parse(in)
		var e *errs.Error
		if !errors.As(err, &e) || e.Kind != errs.Invalid || e.Code != CodeInvalid {
			t.Errorf("Parse(%q) = %v, want an invalid-version error", in, err)
		}
	}
}

// TestR_NUGET_003_ErrorDetailIsSafe: the detail of an error neither echoes control characters
// nor grows with the input.
func TestR_NUGET_003_ErrorDetailIsSafe(t *testing.T) {
	_, err := Parse("\x1b[31m" + strings.Repeat("x", 100))
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatal(err)
	}
	if len(e.Detail) > 200 || strings.ContainsAny(e.Detail, "\x1b\x00") {
		t.Fatalf("unsafe detail %q", e.Detail)
	}
}

// TestR_NUGET_003_MaxLength pins Q-NUGET-21: 255 bytes are accepted, 256 are not.
func TestR_NUGET_003_MaxLength(t *testing.T) {
	ok := "1.0.0-" + strings.Repeat("a", MaxLength-6)
	if _, err := Parse(ok); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(ok + "a"); err == nil {
		t.Fatal("a version above MaxLength was accepted")
	}
}

var fuzzSeeds = []string{
	"1", "1.0.0", "01.2.0.0-RC.1+sha.9f2c", "1.0.0-beta.11", "1.0.0-beta.2", "1.0.0-alpha", "1.0.0--", "1.0.0--5", "1.0.0-2147483648",
	"2147483647.2147483647.2147483647.2147483647", "1.0.0.1", " 1. 0 ", "1.0.0+a", "1.0.0-A.b-C+m", "0", "1.0.0-a.1", "1.0.0-A.1",
}

// FuzzNormalize: normalizing is idempotent, and the forms agree with each other.
func FuzzNormalize(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		v, err := Parse(s)
		if err != nil {
			return
		}
		again, err := Parse(v.FullNormalized())
		if err != nil {
			t.Fatalf("FullNormalized %q of %q does not parse: %v", v.FullNormalized(), s, err)
		}
		if again.FullNormalized() != v.FullNormalized() || again.Normalized() != v.Normalized() || again.URLForm() != v.URLForm() {
			t.Fatalf("normalizing %q is not idempotent: %q then %q", s, v.FullNormalized(), again.FullNormalized())
		}
		if !bytes.Equal(again.SortKey(), v.SortKey()) || !again.Equal(v) {
			t.Fatalf("%q and its normalized form %q are different versions", s, v.FullNormalized())
		}
		if v.URLForm() != strings.ToLower(v.Normalized()) || strings.Contains(v.URLForm(), "+") {
			t.Fatalf("URL form %q of %q is not the lowercased normalized form without metadata", v.URLForm(), s)
		}
		if !strings.HasPrefix(v.FullNormalized(), v.Normalized()) {
			t.Fatalf("%q does not extend %q", v.FullNormalized(), v.Normalized())
		}
		if v.IsSemver2() != (strings.Contains(v.Release(), ".") || v.Metadata() != "") {
			t.Fatalf("IsSemver2 of %q", s)
		}
	})
}

// FuzzCompare: Compare is a total order (antisymmetric, transitive, equal exactly for equal
// URL forms) and agrees with SortKey.
func FuzzCompare(f *testing.F) {
	for i, s := range fuzzSeeds {
		f.Add(s, fuzzSeeds[(i+3)%len(fuzzSeeds)], fuzzSeeds[(i+7)%len(fuzzSeeds)])
	}
	f.Fuzz(func(t *testing.T, a, b, c string) {
		va, errA := Parse(a)
		vb, errB := Parse(b)
		vc, errC := Parse(c)
		if errA != nil || errB != nil {
			return
		}
		ab := Compare(va, vb)
		if ab != -Compare(vb, va) {
			t.Fatalf("Compare is not antisymmetric on %q, %q", a, b)
		}
		if k := bytes.Compare(va.SortKey(), vb.SortKey()); k != ab {
			t.Fatalf("SortKey order %d != Compare %d on %q, %q (%x, %x)", k, ab, a, b, va.SortKey(), vb.SortKey())
		}
		if (ab == 0) != (va.URLForm() == vb.URLForm()) {
			t.Fatalf("%q and %q: compare %d but URL forms %q and %q", a, b, ab, va.URLForm(), vb.URLForm())
		}
		if errC != nil {
			return
		}
		bc, ac := Compare(vb, vc), Compare(va, vc)
		if ab <= 0 && bc <= 0 && ac > 0 || ab >= 0 && bc >= 0 && ac < 0 {
			t.Fatalf("Compare is not transitive on %q, %q, %q", a, b, c)
		}
		if ab == 0 && bc != ac {
			t.Fatalf("equal versions %q, %q compare differently to %q", a, b, c)
		}
	})
}

// FuzzRange: a parsed range prints in a form that parses to the same range, and Satisfies
// agrees between the two.
func FuzzRange(f *testing.F) {
	for _, s := range []string{"1.0", "[1.0]", "[1.0,2.0)", "(,1.0]", "(1.0.0-a.1,2.0+m]", "[ 1 , )", "(1.0,1.0)"} {
		f.Add(s, "1.5")
	}
	f.Fuzz(func(t *testing.T, s, probe string) {
		r, err := ParseRange(s)
		if err != nil {
			return
		}
		again, err := ParseRange(r.String())
		if err != nil {
			t.Fatalf("String %q of %q does not parse: %v", r.String(), s, err)
		}
		if again.String() != r.String() || again.MinInclusive != r.MinInclusive || again.MaxInclusive != r.MaxInclusive {
			t.Fatalf("range %q is not stable: %q then %q", s, r.String(), again.String())
		}
		v, err := Parse(probe)
		if err != nil {
			return
		}
		if r.Satisfies(v) != again.Satisfies(v) {
			t.Fatalf("range %q and its printed form disagree on %q", s, probe)
		}
	})
}
