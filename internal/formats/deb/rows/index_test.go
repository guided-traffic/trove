package rows_test

import (
	"testing"

	"github.com/guided-traffic/trove/internal/formats/deb/rows"
)

func TestIndexBaseCutsTheCompressionOfPackagesAndSourcesOnly(t *testing.T) {
	for in, want := range map[string][2]string{
		"main/binary-amd64/Packages.xz": {"main/binary-amd64/Packages", ".xz"},
		"main/binary-amd64/Packages":    {"main/binary-amd64/Packages", ""},
		"main/source/Sources.gz":        {"main/source/Sources", ".gz"},
		"Packages.zst":                  {"Packages", ".zst"},
	} {
		base, suffix, ok := rows.IndexBase(in)
		if !ok || base != want[0] || suffix != want[1] {
			t.Errorf("%s = %q %q %v", in, base, suffix, ok)
		}
	}
	for _, in := range []string{"main/Contents-amd64.gz", "main/i18n/Translation-en.xz", "Release", "main/binary-amd64/Packages.lzma", "NotPackages.gz"} {
		if _, _, ok := rows.IndexBase(in); ok {
			t.Errorf("%s is an index", in)
		}
	}
}

func TestPreferredIndexesPicksTheSmallestVariantOnePerIndex(t *testing.T) {
	f := func(p string, b byte) rows.ReleaseFile { return rows.ReleaseFile{Path: p, SHA256: [32]byte{b}} }
	files := []rows.ReleaseFile{
		f("main/binary-amd64/Packages", 1), f("main/binary-amd64/Packages.gz", 2), f("main/binary-amd64/Packages.xz", 3),
		f("main/binary-arm64/Packages.gz", 4), f("main/binary-arm64/Packages.bz2", 5),
		f("main/source/Sources.xz", 6), f("main/binary-amd64/Packages.diff/Index", 7), f("main/Contents-amd64.gz", 8),
		f("main/binary-amd64/by-hash/SHA256/aa", 9),
	}
	got := rows.PreferredIndexes(files, false)
	if len(got) != 2 || got[0].Path != "main/binary-amd64/Packages" || got[0].File != "main/binary-amd64/Packages.xz" || got[0].SHA256 != [32]byte{3} ||
		got[1].Path != "main/binary-arm64/Packages" || got[1].File != "main/binary-arm64/Packages.gz" {
		t.Errorf("packages = %+v", got)
	}
	got = rows.PreferredIndexes(files, true)
	if len(got) != 3 || got[2].Path != "main/source/Sources" {
		t.Errorf("with sources = %+v", got)
	}
}

func TestInScopeFollowsComponentAndArchitecture(t *testing.T) {
	comps, archs := []string{"main"}, []string{"amd64"}
	for path, want := range map[string]bool{
		"main/binary-amd64/Packages":    true,
		"main/binary-all/Packages":      true,
		"main/binary-arm64/Packages":    false,
		"contrib/binary-amd64/Packages": false,
		"main/source/Sources":           true,
		"contrib/source/Sources":        false,
		"Packages":                      true,
		"main/dep11/Components":         false,
	} {
		if got := rows.InScope(path, comps, archs); got != want {
			t.Errorf("%s = %v, want %v", path, got, want)
		}
	}
	if _, ok := rows.FileOf([]rows.ReleaseFile{{Path: "a"}}, "a"); !ok {
		t.Error("FileOf does not find a listed path")
	}
}
