package config

import (
	"testing"
	"time"
)

func TestNuGetDefaults(t *testing.T) {
	n := mustLoad(t, Options{Environ: servableEnv(), Command: CommandServe}).Config.NuGet
	t.Run("R-NUGET-005 nuget.archive defaults", func(t *testing.T) {
		a := n.Archive
		if a.MaxNuspecBytes != MiB || a.MaxEntries != 65535 || a.MaxUncompressedBytes != 4*GiB || a.MaxCompressionRatio != 100 {
			t.Errorf("archive = %+v", a)
		}
	})
	t.Run("R-NUGET-009 nuget.registration defaults", func(t *testing.T) {
		if r := n.Registration; r.PageSize != 64 || r.InlineBelow != 128 {
			t.Errorf("registration = %+v", r)
		}
	})
	t.Run("R-NUGET-010 nuget.search defaults, R-NUGET-016 the forwarded result period", func(t *testing.T) {
		s := n.Search
		if s.DefaultTake != 20 || s.MaxTake != 1000 || s.MaxSkip != 3000 || s.RemoteResultTTL != 60*time.Second {
			t.Errorf("search = %+v", s)
		}
	})
}

func TestNuGetRanges(t *testing.T) {
	cases := []struct {
		req, name, yaml, key string
		bad                  bool
	}{
		{"R-NUGET-005", "a .nuspec limit above the 1 MiB of R-ARCH-028", "nuget:\n  archive:\n    max_nuspec_bytes: 2MiB\n", "nuget.archive.max_nuspec_bytes", true},
		{"R-NUGET-005", "a .nuspec limit below 1 KiB", "nuget:\n  archive:\n    max_nuspec_bytes: 100\n", "nuget.archive.max_nuspec_bytes", true},
		{"R-NUGET-005", "a .nuspec limit of 64 KiB", "nuget:\n  archive:\n    max_nuspec_bytes: 64KiB\n", "nuget.archive.max_nuspec_bytes", false},
		{"R-NUGET-005", "no entries", "nuget:\n  archive:\n    max_entries: 0\n", "nuget.archive.max_entries", true},
		{"R-NUGET-005", "more entries than the default", "nuget:\n  archive:\n    max_entries: 100000\n", "nuget.archive.max_entries", false},
		{"R-NUGET-005", "uncompressed bytes below 1 MiB", "nuget:\n  archive:\n    max_uncompressed_bytes: 1KiB\n", "nuget.archive.max_uncompressed_bytes", true},
		{"R-NUGET-005", "a ratio above the 100:1 of R-ARCH-028", "nuget:\n  archive:\n    max_compression_ratio: 101\n", "nuget.archive.max_compression_ratio", true},
		{"R-NUGET-005", "a ratio of 20", "nuget:\n  archive:\n    max_compression_ratio: 20\n", "nuget.archive.max_compression_ratio", false},
		{"R-NUGET-009", "a page size of 0", "nuget:\n  registration:\n    page_size: 0\n", "nuget.registration.page_size", true},
		{"R-NUGET-009", "inline_below 0", "nuget:\n  registration:\n    inline_below: 0\n", "nuget.registration.inline_below", true},
		{"R-NUGET-010", "default take above the largest take", "nuget:\n  search:\n    default_take: 50\n    max_take: 10\n", "nuget.search.default_take", true},
		{"R-NUGET-010", "default take at the largest take", "nuget:\n  search:\n    default_take: 10\n    max_take: 10\n", "nuget.search.default_take", false},
		{"R-NUGET-010", "a skip limit of 0", "nuget:\n  search:\n    max_skip: 0\n", "nuget.search.max_skip", false},
		{"R-NUGET-016", "a result period above one hour", "nuget:\n  search:\n    remote_result_ttl: 2h\n", "nuget.search.remote_result_ttl", true},
		{"R-NUGET-016", "a result period of 0s keeps none", "nuget:\n  search:\n    remote_result_ttl: 0s\n", "nuget.search.remote_result_ttl", false},
	}
	for _, c := range cases {
		t.Run(c.req+" "+c.name, func(t *testing.T) {
			_, err := loadM2(t, c.yaml)
			if got := hasIssue(err, c.key); got != c.bad {
				t.Fatalf("issue on %s = %v, want %v (%v)", c.key, got, c.bad, err)
			}
			if !c.bad && err != nil {
				t.Fatalf("a valid value is refused: %v", err)
			}
		})
	}
}

func TestNuGetEnvironment(t *testing.T) {
	t.Run("R-NUGET-010 an environment value reaches nuget.search.max_take", func(t *testing.T) {
		l := mustLoad(t, Options{Environ: servableEnv("TROVE_NUGET_SEARCH_MAX_TAKE=50"), Command: CommandServe})
		if l.Config.NuGet.Search.MaxTake != 50 || l.Sources["nuget.search.max_take"].Kind != "env" {
			t.Errorf("max_take = %d, source = %+v", l.Config.NuGet.Search.MaxTake, l.Sources["nuget.search.max_take"])
		}
	})
}
