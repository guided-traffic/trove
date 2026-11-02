package config

import "time"

// NuGet is the nuget section (R-NUGET-005, R-NUGET-009, R-NUGET-010, R-NUGET-016): the
// bounds of the archive a package is read from, the paging of the registration
// documents and the limits of a search. The per-repository settings of the format
// (deleteMode, forwardSearch) are in the repository document, not here. The keys of
// the symbol server and of the vulnerability information arrive with their
// requirements (R-NUGET-021, R-NUGET-023, both P1).
type NuGet struct {
	Archive      NuGetArchive      `yaml:"archive"`
	Registration NuGetRegistration `yaml:"registration"`
	Search       NuGetSearch       `yaml:"search"`
}

// NuGetArchive bounds what a .nupkg may declare and what is read of it into memory
// (R-NUGET-005 step 1, Q-NUGET-23). The two bounds on the central directory may be
// raised or lowered; the two on what is read into memory can only be tightened, as
// R-ARCH-028 says of every format.
type NuGetArchive struct {
	MaxNuspecBytes       ByteSize `yaml:"max_nuspec_bytes" default:"1MiB" validate:"min=1KiB,max=1MiB" doc:"Largest .nuspec of a package; at most the 1 MiB of R-ARCH-028 (Q-NUGET-25)."`
	MaxEntries           int      `yaml:"max_entries" default:"65535" validate:"min=1,max=1000000" doc:"Most entries the central directory of a package may declare (Q-NUGET-23)."`
	MaxUncompressedBytes ByteSize `yaml:"max_uncompressed_bytes" default:"4GiB" validate:"min=1MiB,max=1TiB" doc:"Largest sum of the declared sizes of all entries of a package (Q-NUGET-23)."`
	MaxCompressionRatio  int      `yaml:"max_compression_ratio" default:"100" validate:"min=1,max=100" doc:"Largest compression ratio of an entry read into memory; at most the 100:1 of R-ARCH-028."`
}

// NuGetRegistration is the paging of the registration documents (R-NUGET-009).
type NuGetRegistration struct {
	PageSize    int `yaml:"page_size" default:"64" validate:"min=1,max=1000" doc:"Versions per registration page."`
	InlineBelow int `yaml:"inline_below" default:"128" validate:"min=1,max=100000" doc:"A package with fewer visible versions has every page inlined in its registration index; with more, the pages are fetched from their own URL."`
}

// NuGetSearch bounds a search (R-NUGET-010) and sets the period of a forwarded one
// (R-NUGET-016, Q-NUGET-16).
type NuGetSearch struct {
	DefaultTake        int           `yaml:"default_take" default:"20" validate:"min=1,max=1000" doc:"Results of a search that names no take."`
	MaxTake            int           `yaml:"max_take" default:"1000" validate:"min=1,max=10000" doc:"Largest take; a larger one is clamped."`
	MaxSkip            int           `yaml:"max_skip" default:"3000" validate:"min=0,max=1000000" doc:"Largest skip; a larger one answers 400."`
	VirtualMergeWindow int           `yaml:"virtual_merge_window" default:"1000" validate:"min=1,max=10000" doc:"How many results of each member a search through a virtual repository merges; positions beyond it answer an empty data (R-NUGET-018)."`
	RemoteResultTTL    time.Duration `yaml:"remote_result_ttl" default:"60s" validate:"min=0s,max=1h" doc:"How long the answer of a forwarded search is kept; 0s keeps none. The engine cuts it to one hour (Q-NUGET-16)."`
}

// validateNuGetCoupling checks the NuGet limits against each other: a default
// take above the largest take could never be served.
func (c *Config) validateNuGetCoupling(r *Report) {
	if c.NuGet.Search.DefaultTake > c.NuGet.Search.MaxTake {
		r.Add("nuget.search.default_take", "(%d) must not exceed nuget.search.max_take (%d)", c.NuGet.Search.DefaultTake, c.NuGet.Search.MaxTake)
	}
}
