package tagorder

import (
	"slices"
	"testing"
)

func TestR_OCI_031_NewestFirstOverATableOfTagSets(t *testing.T) {
	// Each row lists tags newest first, as the package screen shows them.
	for _, want := range [][]string{
		{"1.29.3", "1.29.3-perl", "1.29.3-alpine", "1.29.3-rc1", "1.29.2", "1.9.15", "latest", "edge", "alpine"},
		{"v2.10.0", "v2.9.1", "v2.9.0", "v2.9.0-rc2", "v2.9.0-rc1", "v2.9.0-beta.1", "v2.9.0-alpha.3", "v1.99", "main", "dev"},
		{"3.20", "3.19.1", "3.19", "3.2", "3.1", "latest", "edge"},
		{"20240315", "20240101", "9", "latest"},
		{"10.0", "9.9", "9.0"},
	} {
		got := slices.Clone(want)
		slices.Reverse(got)
		slices.SortFunc(got, Compare)
		slices.Reverse(got)
		if !slices.Equal(got, want) {
			t.Errorf("got  %v\nwant %v", got, want)
		}
	}
}

func TestR_OCI_031_ParseAndPrerelease(t *testing.T) {
	for tag, ok := range map[string]bool{
		"1": true, "v1": true, "1.2.3": true, "v1.2.3-rc1": true, "1.2-alpine": true, "01.02": true,
		"": false, "v": false, "latest": false, "1.": false, ".1": false, "1..2": false, "1-": false, "1.2.x": false, "vv1": false, "V1": false, "-1": false,
	} {
		if _, got := Parse(tag); got != ok {
			t.Errorf("Parse(%q) = %v, want %v", tag, got, ok)
		}
	}
	for tag, want := range map[string]bool{
		"1.0-rc1": true, "1.0-RC.2": true, "1.0-alpha": true, "1.0-beta.3": true, "1.0-pre": true, "1.0-dev12": true,
		"1.0-alpine": false, "1.0-rcx": false, "1.0-rc1-x": false, "1.0": false, "rc1": false, "1.0-develop": false, "1.0-prerelease": false,
	} {
		if got := Prerelease(tag); got != want {
			t.Errorf("Prerelease(%q) = %v, want %v", tag, got, want)
		}
	}
}

func TestR_OCI_031_CompareIsATotalOrder(t *testing.T) {
	tags := []string{"1", "v1", "01", "1.0", "1.0.0", "v1.0", "1-a", "1-rc1", "2", "latest", "Latest", "_x", "0", "1.2.3", "1.10", "1.9", "99999999999999999999999999", "1-alpine"}
	for _, a := range tags {
		if Compare(a, a) != 0 {
			t.Errorf("Compare(%q, %q) != 0", a, a)
		}
		for _, b := range tags {
			ab, ba := Compare(a, b), Compare(b, a)
			if (ab < 0) != (ba > 0) || (ab == 0) != (a == b) {
				t.Errorf("Compare(%q, %q) = %d, Compare(%q, %q) = %d", a, b, ab, b, a, ba)
			}
			for _, c := range tags {
				if Compare(a, b) < 0 && Compare(b, c) < 0 && Compare(a, c) >= 0 {
					t.Errorf("%q < %q < %q is not transitive", a, b, c)
				}
			}
		}
	}
}

func FuzzR_OCI_031_Compare(f *testing.F) {
	for _, s := range []string{"1.2.3", "v1", "latest", "1-rc1", "", "0", "99999999999999999999999999"} {
		f.Add(s, "1.2.4")
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		ab, ba := Compare(a, b), Compare(b, a)
		if (ab < 0) != (ba > 0) || (ab == 0) != (a == b) {
			t.Fatalf("Compare(%q, %q) = %d, reverse %d", a, b, ab, ba)
		}
	})
}
