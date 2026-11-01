package version

import (
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The golden vectors are generated from the NuGet.Versioning library by tools/nugetvectors
// (R-NUGET-003) and committed; CI regenerates them and fails on drift.

type vectorFile struct {
	Generator struct {
		Library       string `json:"library"`
		Informational string `json:"informational"`
	} `json:"generator"`
	Probes   []string          `json:"probes"`
	Versions []versionVec      `json:"versions"`
	Ranges   []rangeVec        `json:"ranges"`
	IDs      []json.RawMessage `json:"ids"`
}

type versionVec struct {
	Input      string `json:"input"`
	Valid      bool   `json:"valid"`
	Normalized string `json:"normalized"`
	Full       string `json:"full"`
	URL        string `json:"url"`
	Metadata   string `json:"metadata"`
	Prerelease bool   `json:"prerelease"`
	Semver2    bool   `json:"semver2"`
	Rank       int    `json:"rank"`
}

type rangeVec struct {
	Input        string   `json:"input"`
	Valid        bool     `json:"valid"`
	Normalized   string   `json:"normalized"`
	Min          *string  `json:"min"`
	MinInclusive bool     `json:"minInclusive"`
	Max          *string  `json:"max"`
	MaxInclusive bool     `json:"maxInclusive"`
	Semver2      bool     `json:"semver2"`
	Satisfies    []string `json:"satisfies"`
}

func loadVectors(t testing.TB) vectorFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var f vectorFile
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if f.Generator.Library != "NuGet.Versioning" || len(f.Versions) < 1000 || len(f.Ranges) < 200 {
		t.Fatalf("vector file is not the generator's output: %+v, %d versions, %d ranges", f.Generator, len(f.Versions), len(f.Ranges))
	}
	return f
}

// TestR_NUGET_003_GoldenVersions checks accept or reject, the normalized forms, the URL form and
// the SemVer 2 classification of every vector. A version string above MaxLength is the one
// deliberate difference (Q-NUGET-21) and is checked to be refused.
func TestR_NUGET_003_GoldenVersions(t *testing.T) {
	f := loadVectors(t)
	checked := 0
	for _, vec := range f.Versions {
		v, err := Parse(vec.Input)
		if len(vec.Input) > MaxLength {
			if err == nil {
				t.Errorf("Parse(%d bytes) accepted a version above MaxLength", len(vec.Input))
			}
			continue
		}
		if !vec.Valid {
			if err == nil {
				t.Errorf("Parse(%q) = %q, the library refuses it", vec.Input, v.FullNormalized())
			}
			continue
		}
		if err != nil {
			t.Errorf("Parse(%q): %v, the library accepts it", vec.Input, err)
			continue
		}
		checked++
		if got := v.Normalized(); got != vec.Normalized {
			t.Errorf("Parse(%q).Normalized() = %q, want %q", vec.Input, got, vec.Normalized)
		}
		if got := v.FullNormalized(); got != vec.Full {
			t.Errorf("Parse(%q).FullNormalized() = %q, want %q", vec.Input, got, vec.Full)
		}
		if got := v.URLForm(); got != vec.URL {
			t.Errorf("Parse(%q).URLForm() = %q, want %q", vec.Input, got, vec.URL)
		}
		if got := v.Metadata(); got != vec.Metadata {
			t.Errorf("Parse(%q).Metadata() = %q, want %q", vec.Input, got, vec.Metadata)
		}
		if got := v.IsPrerelease(); got != vec.Prerelease {
			t.Errorf("Parse(%q).IsPrerelease() = %v, want %v", vec.Input, got, vec.Prerelease)
		}
		if got := v.IsSemver2(); got != vec.Semver2 { // R-NUGET-004
			t.Errorf("Parse(%q).IsSemver2() = %v, want %v", vec.Input, got, vec.Semver2)
		}
		if got := v.Original(); got != vec.Input {
			t.Errorf("Parse(%q).Original() = %q", vec.Input, got)
		}
	}
	if checked < 500 {
		t.Fatalf("only %d valid vectors were checked", checked)
	}
}

// TestR_NUGET_003_GoldenOrdering checks the order of every pair of valid vectors against the
// library's dense rank, and SortKey against the same rank: the key order is the version order.
func TestR_NUGET_003_GoldenOrdering(t *testing.T) {
	f := loadVectors(t)
	type ranked struct {
		in   string
		v    Version
		rank int
		key  []byte
	}
	var all []ranked
	for _, vec := range f.Versions {
		if !vec.Valid || len(vec.Input) > MaxLength || readsSignedIdentifier(vec.Input) {
			continue
		}
		v := MustParse(vec.Input)
		all = append(all, ranked{vec.Input, v, vec.Rank, v.SortKey()})
	}
	for i := range all {
		for j := range all {
			a, b := all[i], all[j]
			want := cmpInt(a.rank, b.rank)
			if got := Compare(a.v, b.v); got != want {
				t.Fatalf("Compare(%q, %q) = %d, the library says %d", a.in, b.in, got, want)
			}
			if got := bytes.Compare(a.key, b.key); got != want {
				t.Fatalf("SortKey order of (%q, %q) = %d, the library says %d (%x, %x)", a.in, b.in, got, want, a.key, b.key)
			}
			if got := a.v.URLForm() == b.v.URLForm(); got && want != 0 {
				t.Fatalf("%q and %q have one URL form but order %d", a.in, b.in, want)
			}
		}
	}
}

// TestR_NUGET_003_SameVersionSameURLForm: equal precedence is exactly equal URL form, so the
// duplicate detection by URL form and the order cannot disagree.
func TestR_NUGET_003_SameVersionSameURLForm(t *testing.T) {
	f := loadVectors(t)
	byRank := map[int]string{}
	for _, vec := range f.Versions {
		if !vec.Valid || len(vec.Input) > MaxLength || readsSignedIdentifier(vec.Input) {
			continue
		}
		if url, ok := byRank[vec.Rank]; ok && url != vec.URL {
			t.Errorf("rank %d holds URL forms %q and %q (input %q)", vec.Rank, url, vec.URL, vec.Input)
		}
		byRank[vec.Rank] = vec.URL
	}
}

func TestR_NUGET_003_GoldenRanges(t *testing.T) {
	f := loadVectors(t)
	probes := make([]Version, len(f.Probes))
	for i, p := range f.Probes {
		probes[i] = MustParse(p)
	}
	valid := 0
	for _, vec := range f.Ranges {
		r, err := ParseRange(vec.Input)
		if len(vec.Input) > MaxRangeLength {
			continue
		}
		if !vec.Valid {
			if err == nil {
				t.Errorf("ParseRange(%q) = %q, the library refuses it", vec.Input, r)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseRange(%q): %v, the library accepts it", vec.Input, err)
			continue
		}
		valid++
		if got := r.String(); got != vec.Normalized {
			t.Errorf("ParseRange(%q).String() = %q, want %q", vec.Input, got, vec.Normalized)
		}
		checkBound(t, vec.Input, "min", r.Min, vec.Min)
		checkBound(t, vec.Input, "max", r.Max, vec.Max)
		if r.MinInclusive != vec.MinInclusive || r.MaxInclusive != vec.MaxInclusive {
			t.Errorf("ParseRange(%q) inclusive = %v, %v; want %v, %v", vec.Input, r.MinInclusive, r.MaxInclusive, vec.MinInclusive, vec.MaxInclusive)
		}
		if got := r.IsSemver2(); got != vec.Semver2 { // R-NUGET-004
			t.Errorf("ParseRange(%q).IsSemver2() = %v, want %v", vec.Input, got, vec.Semver2)
		}
		var got []string
		for i, p := range probes {
			if r.Satisfies(p) {
				got = append(got, f.Probes[i])
			}
		}
		if len(got) != len(vec.Satisfies) || (len(got) > 0 && !slices.Equal(got, vec.Satisfies)) {
			t.Errorf("ParseRange(%q) satisfies %v, want %v", vec.Input, got, vec.Satisfies)
		}
	}
	if valid < 100 {
		t.Fatalf("only %d valid range vectors were checked", valid)
	}
}

func checkBound(t *testing.T, input, name string, got *Version, want *string) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil || want == nil:
		t.Errorf("ParseRange(%q).%s = %v, want %v", input, name, got, want)
	case got.FullNormalized() != *want:
		t.Errorf("ParseRange(%q).%s = %q, want %q", input, name, got.FullNormalized(), *want)
	}
}

// readsSignedIdentifier reports whether the library reads a pre-release identifier of the
// version as a negative number: "-" and digits, as in 1.0.0--5. The library hands such an
// identifier to Int32.TryParse, so "1.0.0--0" and "1.0.0-0" have equal precedence but two
// URL forms. trove orders the identifier as text instead, which keeps equal precedence and
// equal URL form the same thing (Q-NUGET-22); the ordering vectors of such versions are
// therefore not applicable.
func readsSignedIdentifier(input string) bool {
	_, rest, ok := strings.Cut(strings.TrimSpace(input), "-")
	if !ok {
		return false
	}
	rest, _, _ = strings.Cut(rest, "+")
	for id := range strings.SplitSeq(rest, ".") {
		if len(id) > 1 && id[0] == '-' && strings.Trim(id[1:], "0123456789") == "" {
			return true
		}
	}
	return false
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// TestR_NUGET_003_RanksAreDense guards the vector file itself: ranks start at zero and have no
// gap, so the pairwise check above compares real positions.
func TestR_NUGET_003_RanksAreDense(t *testing.T) {
	f := loadVectors(t)
	seen := map[int]bool{}
	for _, vec := range f.Versions {
		if vec.Valid {
			seen[vec.Rank] = true
		}
	}
	ranks := make([]int, 0, len(seen))
	for r := range seen {
		ranks = append(ranks, r)
	}
	sort.Ints(ranks)
	for i, r := range ranks {
		if r != i {
			t.Fatalf("rank %d is missing", i)
		}
	}
}

// TestR_NUGET_003_SignedIdentifierDeviation pins Q-NUGET-22: versions the library gives equal
// precedence only by reading "-0" as a number are different versions here, and the deviation
// stays confined to the few vectors that have such an identifier.
func TestR_NUGET_003_SignedIdentifierDeviation(t *testing.T) {
	a, b := MustParse("1.0.0-0"), MustParse("1.0.0--0")
	if Compare(a, b) == 0 || a.URLForm() == b.URLForm() || bytes.Equal(a.SortKey(), b.SortKey()) {
		t.Fatalf("1.0.0-0 and 1.0.0--0 must be two versions: compare %d, keys %x %x", Compare(a, b), a.SortKey(), b.SortKey())
	}
	f := loadVectors(t)
	n := 0
	for _, vec := range f.Versions {
		if vec.Valid && readsSignedIdentifier(vec.Input) {
			n++
		}
	}
	if n == 0 || n > 25 {
		t.Fatalf("%d vectors are exempt from the ordering check, want a handful", n)
	}
}
